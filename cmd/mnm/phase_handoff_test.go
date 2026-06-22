package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildPhaseHandoffContextAggregatesPriorLearning(t *testing.T) {
	runDir := newLedgerTestRun(t)
	writeRunFile(t, runDir, "evidence/lead-open.md", "Investigate open lead.")
	writeRunFile(t, runDir, "evidence/lead-dead.md", "Investigate dead lead.")
	openLeadID := "lead_open"
	deadLeadID := "lead_dead"
	for _, lead := range []struct {
		id    string
		title string
		body  string
	}{
		{id: openLeadID, title: "Open authorization boundary", body: "evidence/lead-open.md"},
		{id: deadLeadID, title: "Dead static asset lead", body: "evidence/lead-dead.md"},
	} {
		if err := appendLedgerEvent(runDir, LedgerEvent{
			RunID:    "run_test",
			Type:     "lead.created",
			Object:   "lead",
			ObjectID: lead.id,
			TaskID:   "task_recon",
			Data: map[string]any{
				"title":     lead.title,
				"category":  "security",
				"priority":  "medium",
				"body_path": lead.body,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "lead.closed",
		Object:   "lead",
		ObjectID: deadLeadID,
		TaskID:   "task_investigate_" + safeFileID(deadLeadID),
		Data: map[string]any{
			"status": "closed_no_finding",
			"reason": "route is guarded by middleware in server/auth.go",
		},
	}); err != nil {
		t.Fatal(err)
	}
	findingID := createFindingForTest(t, runDir, openLeadID)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")

	setupRel := "evidence/setup-task_investigate_" + safeFileID(openLeadID) + "-attempt-1.log"
	writeRunFile(t, runDir, setupRel, "installed protoc and generated service stubs\n")
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:  "run_test",
		TaskID: "task_investigate_" + safeFileID(openLeadID),
		Kind:   "log",
		Title:  "Setup hook log: Investigate",
		Path:   setupRel,
		LeadID: openLeadID,
	}); err != nil {
		t.Fatal(err)
	}

	handoffRel := "evidence/handoff-investigate-" + safeFileID(openLeadID) + ".json"
	if err := writeJSON(filepath.Join(runDir, filepath.FromSlash(handoffRel)), taskHandoffFile{
		Version:           phaseHandoffVersion,
		Phase:             "investigate",
		TaskID:            "task_investigate_" + safeFileID(openLeadID),
		LeadID:            openLeadID,
		AttemptedCommands: []string{"go test ./...: passed"},
		SetupDiscoveries:  []string{"use make smoke before e2e"},
		Blockers: []taskHandoffBlocker{{
			Summary:         "database service was absent",
			RequiredService: "postgres",
			NextCommand:     "docker compose up -d postgres",
		}},
		LikelyLeads:       []string{"authorization boundary still plausible"},
		ConfirmedDeadEnds: []string{"static assets are not served by the API process"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:  "run_test",
		TaskID: "task_investigate_" + safeFileID(openLeadID),
		Kind:   "json",
		Title:  "Task handoff: Investigate something",
		Path:   handoffRel,
		LeadID: openLeadID,
	}); err != nil {
		t.Fatal(err)
	}

	context, err := buildPhaseHandoffContext(runDir, "run_test", "validate")
	if err != nil {
		t.Fatal(err)
	}
	if context.Version != phaseHandoffVersion || context.RunID != "run_test" || context.TargetPhase != "validate" {
		t.Fatalf("unexpected context header: %#v", context)
	}
	if len(context.OpenLeads) != 1 || context.OpenLeads[0].ID != openLeadID {
		t.Fatalf("unexpected open leads: %#v", context.OpenLeads)
	}
	if len(context.ConfirmedDeadEnds) != 1 || context.ConfirmedDeadEnds[0].ID != deadLeadID || !strings.Contains(context.ConfirmedDeadEnds[0].Reason, "middleware") {
		t.Fatalf("unexpected confirmed dead ends: %#v", context.ConfirmedDeadEnds)
	}
	if len(context.SetupLogs) != 1 || context.SetupLogs[0].Path != setupRel {
		t.Fatalf("unexpected setup logs: %#v", context.SetupLogs)
	}
	if len(context.Findings) != 1 || context.Findings[0].ID != findingID || !containsString(context.Findings[0].Verdicts, "review accepted") {
		t.Fatalf("unexpected findings: %#v", context.Findings)
	}
	if len(context.TaskHandoffs) != 1 || context.TaskHandoffs[0].SourcePath != handoffRel {
		t.Fatalf("unexpected task handoffs: %#v", context.TaskHandoffs)
	}
	if got := context.TaskHandoffs[0].Blockers[0].NextCommand; got != "docker compose up -d postgres" {
		t.Fatalf("next command = %q", got)
	}
	data, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "attempted_commands") {
		t.Fatalf("context JSON missing structured fields: %s", data)
	}
}

func TestBuildPhaseHandoffContextRejectsMalformedTaskHandoff(t *testing.T) {
	runDir := newLedgerTestRun(t)
	handoffRel := "evidence/handoff-investigate-bad.json"
	writeRunFile(t, runDir, handoffRel, `{"version":1,"phase":"investigate","task_id":"wrong_task"}`)
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:  "run_test",
		TaskID: "task_investigate_lead_auth",
		Kind:   "json",
		Title:  "Task handoff: Bad",
		Path:   handoffRel,
		LeadID: "lead_auth",
	}); err != nil {
		t.Fatal(err)
	}

	_, err := buildPhaseHandoffContext(runDir, "run_test", "validate")
	if err == nil {
		t.Fatal("expected malformed task handoff error")
	}
	if !strings.Contains(err.Error(), "task_id") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildPhaseHandoffContextRejectsChangedTaskHandoff(t *testing.T) {
	runDir := newLedgerTestRun(t)
	handoffRel := "evidence/handoff-investigate-lead_auth.json"
	if err := writeJSON(filepath.Join(runDir, filepath.FromSlash(handoffRel)), taskHandoffFile{
		Version: phaseHandoffVersion,
		Phase:   "investigate",
		TaskID:  "task_investigate_lead_auth",
		LeadID:  "lead_auth",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:  "run_test",
		TaskID: "task_investigate_lead_auth",
		Kind:   "json",
		Title:  "Task handoff: Investigate auth",
		Path:   handoffRel,
		LeadID: "lead_auth",
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(runDir, filepath.FromSlash(handoffRel)), taskHandoffFile{
		Version:          phaseHandoffVersion,
		Phase:            "investigate",
		TaskID:           "task_investigate_lead_auth",
		LeadID:           "lead_auth",
		SetupDiscoveries: []string{"changed after registration"},
	}); err != nil {
		t.Fatal(err)
	}

	_, err := buildPhaseHandoffContext(runDir, "run_test", "validate")
	if err == nil {
		t.Fatal("expected changed task handoff error")
	}
	if !strings.Contains(err.Error(), "changed after registration") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRequiredTaskHandoffRejectsMissingOrMalformedProducerArtifact(t *testing.T) {
	runDir := newLedgerTestRun(t)
	if err := validateRequiredTaskHandoff(runDir, "validate", "task_validate_finding_auth", "", "finding_auth"); err == nil {
		t.Fatal("expected missing task handoff error")
	}

	handoffRel := taskHandoffRelPath("validate", "finding_auth")
	if err := writeJSON(filepath.Join(runDir, filepath.FromSlash(handoffRel)), taskHandoffFile{
		Version:           phaseHandoffVersion,
		Phase:             "review",
		TaskID:            "task_validate_finding_auth",
		FindingID:         "finding_auth",
		AttemptedCommands: []string{"fake validate"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:     "run_test",
		TaskID:    "task_validate_finding_auth",
		Kind:      "json",
		Title:     "Task handoff: finding_auth",
		Path:      handoffRel,
		FindingID: "finding_auth",
	}); err != nil {
		t.Fatal(err)
	}

	err := validateRequiredTaskHandoff(runDir, "validate", "task_validate_finding_auth", "", "finding_auth")
	if err == nil {
		t.Fatal("expected malformed task handoff error")
	}
	if !strings.Contains(err.Error(), `phase = "review", want "validate"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRequiredTaskHandoffRejectsChangedProducerArtifact(t *testing.T) {
	runDir := newLedgerTestRun(t)
	handoffRel := taskHandoffRelPath("validate", "finding_auth")
	if err := writeJSON(filepath.Join(runDir, filepath.FromSlash(handoffRel)), taskHandoffFile{
		Version:   phaseHandoffVersion,
		Phase:     "validate",
		TaskID:    "task_validate_finding_auth",
		FindingID: "finding_auth",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:     "run_test",
		TaskID:    "task_validate_finding_auth",
		Kind:      "json",
		Title:     "Task handoff: finding_auth",
		Path:      handoffRel,
		FindingID: "finding_auth",
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(filepath.Join(runDir, filepath.FromSlash(handoffRel)), taskHandoffFile{
		Version:           phaseHandoffVersion,
		Phase:             "validate",
		TaskID:            "task_validate_finding_auth",
		FindingID:         "finding_auth",
		AttemptedCommands: []string{"changed after registration"},
	}); err != nil {
		t.Fatal(err)
	}

	err := validateRequiredTaskHandoff(runDir, "validate", "task_validate_finding_auth", "", "finding_auth")
	if err == nil {
		t.Fatal("expected changed task handoff error")
	}
	if !strings.Contains(err.Error(), "changed after registration") {
		t.Fatalf("unexpected error: %v", err)
	}
}
