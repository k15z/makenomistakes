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
			"status":                     "closed_no_finding",
			"reason":                     "route is guarded by middleware in server/auth.go",
			"negative_proof_boundary":    "admin route is behind the internal listener",
			"negative_proof_enforcement": "server/auth.go RequireAdmin middleware",
			"negative_proof_exposure":    "not exposed on the public deployment",
			"negative_proof_edge_cases":  "checked anonymous and non-admin roles",
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
		LikelyLeads: []string{"authorization boundary still plausible"},
		ConfirmedDeadEnds: []taskHandoffDeadEnd{{
			Summary:                  "static assets are not served by the API process",
			NegativeProofBoundary:    "static file path is outside the API router",
			NegativeProofEnforcement: "router only mounts /api handlers",
			NegativeProofExposure:    "assets are served by a separate CDN",
			NegativeProofEdgeCases:   "checked direct /api/static and CDN fallback routes",
		}},
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
	if got := context.ConfirmedDeadEnds[0].NegativeProofEnforcement; !strings.Contains(got, "RequireAdmin") {
		t.Fatalf("negative proof enforcement = %q", got)
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
		Version:           phaseHandoffVersion,
		Phase:             "investigate",
		TaskID:            "task_investigate_lead_auth",
		LeadID:            "lead_auth",
		AttemptedCommands: []string{"go test ./..."},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:  "run_test",
		TaskID: "task_investigate_lead_auth",
		Kind:   "json",
		Title:  "Task handoff: lead_auth",
		Path:   handoffRel,
		LeadID: "lead_auth",
	}); err != nil {
		t.Fatal(err)
	}
	writeRunFile(t, runDir, handoffRel, `{"version":1,"phase":"investigate","task_id":"task_investigate_lead_auth","lead_id":"lead_auth","blockers":[{"summary":"late edit"}]}`)

	_, err := buildPhaseHandoffContext(runDir, "run_test", "validate")
	if err == nil {
		t.Fatal("expected changed task handoff error")
	}
	if !strings.Contains(err.Error(), "changed after registration") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildPhaseHandoffContextSkipsUnprovenClosedAndCarriesInconclusiveLeads(t *testing.T) {
	runDir := newLedgerTestRun(t)
	for _, lead := range []struct {
		id    string
		title string
		data  map[string]any
	}{
		{
			id:    "lead_proofless",
			title: "Proofless no finding",
			data: map[string]any{
				"status": "closed_no_finding",
				"reason": "legacy close without structured proof",
			},
		},
		{
			id:    "lead_inconclusive",
			title: "Plausible but blocked",
			data: map[string]any{
				"status": "inconclusive",
				"reason": "missing deployment context",
			},
		},
		{
			id:    "lead_proven_dead",
			title: "Protected admin route",
			data: map[string]any{
				"status":                     "closed_no_finding",
				"reason":                     "admin route is protected",
				"negative_proof_boundary":    "admin listener only",
				"negative_proof_enforcement": "RequireAdmin middleware",
				"negative_proof_exposure":    "not mounted on public router",
				"negative_proof_edge_cases":  "checked anonymous and user roles",
			},
		},
	} {
		bodyRel := "evidence/" + lead.id + ".md"
		writeRunFile(t, runDir, bodyRel, "Investigate "+lead.title)
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
				"body_path": bodyRel,
			},
		}); err != nil {
			t.Fatal(err)
		}
		if err := appendLedgerEvent(runDir, LedgerEvent{
			RunID:    "run_test",
			Type:     "lead.closed",
			Object:   "lead",
			ObjectID: lead.id,
			TaskID:   "task_investigate_" + lead.id,
			Data:     lead.data,
		}); err != nil {
			t.Fatal(err)
		}
	}

	context, err := buildPhaseHandoffContext(runDir, "run_test", "validate")
	if err != nil {
		t.Fatal(err)
	}
	if len(context.ConfirmedDeadEnds) != 1 || context.ConfirmedDeadEnds[0].ID != "lead_proven_dead" {
		t.Fatalf("unexpected confirmed dead ends: %#v", context.ConfirmedDeadEnds)
	}
	if len(context.InconclusiveLeads) != 1 || context.InconclusiveLeads[0].ID != "lead_inconclusive" {
		t.Fatalf("unexpected inconclusive leads: %#v", context.InconclusiveLeads)
	}
	if !strings.Contains(context.InconclusiveLeads[0].Reason, "missing deployment") {
		t.Fatalf("inconclusive reason = %q", context.InconclusiveLeads[0].Reason)
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
		Version:           phaseHandoffVersion,
		Phase:             "validate",
		TaskID:            "task_validate_finding_auth",
		FindingID:         "finding_auth",
		AttemptedCommands: []string{"go test ./..."},
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
	writeRunFile(t, runDir, handoffRel, `{"version":1,"phase":"validate","task_id":"task_validate_finding_auth","finding_id":"finding_auth","blockers":[{"summary":"late edit"}]}`)

	err := validateRequiredTaskHandoff(runDir, "validate", "task_validate_finding_auth", "", "finding_auth")
	if err == nil {
		t.Fatal("expected changed task handoff error")
	}
	if !strings.Contains(err.Error(), "changed after registration") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateBlockedValidationHandoffRequiresActionableBlocker(t *testing.T) {
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

	err := validateBlockedValidationHandoff(runDir, "finding_auth")
	if err == nil {
		t.Fatal("expected missing blocker error")
	}
	if !strings.Contains(err.Error(), "requires at least one task handoff blocker") {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := writeJSON(filepath.Join(runDir, filepath.FromSlash(handoffRel)), taskHandoffFile{
		Version:   phaseHandoffVersion,
		Phase:     "validate",
		TaskID:    "task_validate_finding_auth",
		FindingID: "finding_auth",
		Blockers: []taskHandoffBlocker{{
			Summary:         "database service was absent",
			RequiredService: "postgres",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	err = validateBlockedValidationHandoff(runDir, "finding_auth")
	if err == nil {
		t.Fatal("expected missing next command error")
	}
	if !strings.Contains(err.Error(), "next_command is required") {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := writeJSON(filepath.Join(runDir, filepath.FromSlash(handoffRel)), taskHandoffFile{
		Version:   phaseHandoffVersion,
		Phase:     "validate",
		TaskID:    "task_validate_finding_auth",
		FindingID: "finding_auth",
		Blockers: []taskHandoffBlocker{{
			Summary:     "database service was absent",
			NextCommand: "docker compose up -d db && go test ./...",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	err = validateBlockedValidationHandoff(runDir, "finding_auth")
	if err == nil {
		t.Fatal("expected missing blocker cause error")
	}
	if !strings.Contains(err.Error(), "must include a missing dependency, failed command, required service, or suspected config gap") {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := writeJSON(filepath.Join(runDir, filepath.FromSlash(handoffRel)), taskHandoffFile{
		Version:   phaseHandoffVersion,
		Phase:     "validate",
		TaskID:    "task_validate_finding_auth",
		FindingID: "finding_auth",
		Blockers: []taskHandoffBlocker{{
			Summary:         "database service was absent",
			RequiredService: "postgres",
			NextCommand:     "docker compose up -d db && go test ./...",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := validateBlockedValidationHandoff(runDir, "finding_auth"); err != nil {
		t.Fatalf("expected actionable blocker to pass: %v", err)
	}
}

func TestValidateTaskHandoffRequiresNegativeProofForConfirmedDeadEnds(t *testing.T) {
	err := validateTaskHandoffFile(taskHandoffFile{
		Version: phaseHandoffVersion,
		Phase:   "investigate",
		TaskID:  "task_investigate_lead_auth",
		LeadID:  "lead_auth",
		ConfirmedDeadEnds: []taskHandoffDeadEnd{{
			Summary: "auth route appears protected",
		}},
	}, EvidenceRecord{
		TaskID: "task_investigate_lead_auth",
		LeadID: "lead_auth",
	}, false)
	if err == nil {
		t.Fatal("expected missing negative proof error")
	}
	if !strings.Contains(err.Error(), "confirmed_dead_ends[0].negative_proof_boundary is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildPhaseHandoffContextAcceptsLegacyStringDeadEnds(t *testing.T) {
	runDir := newLedgerTestRun(t)
	handoffRel := "evidence/handoff-investigate-legacy.json"
	writeRunFile(t, runDir, handoffRel, `{"version":1,"phase":"investigate","task_id":"task_investigate_legacy","lead_id":"lead_legacy","attempted_commands":["fake investigate"],"setup_discoveries":[],"blockers":[],"likely_leads":[],"confirmed_dead_ends":["legacy dead end without structured proof"]}`)
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:  "run_test",
		TaskID: "task_investigate_legacy",
		Kind:   "json",
		Title:  "Task handoff: legacy",
		Path:   handoffRel,
		LeadID: "lead_legacy",
	}); err != nil {
		t.Fatal(err)
	}

	context, err := buildPhaseHandoffContext(runDir, "run_test", "validate")
	if err != nil {
		t.Fatal(err)
	}
	if len(context.TaskHandoffs) != 1 || len(context.TaskHandoffs[0].ConfirmedDeadEnds) != 1 {
		t.Fatalf("unexpected task handoffs: %#v", context.TaskHandoffs)
	}
	deadEnd := context.TaskHandoffs[0].ConfirmedDeadEnds[0]
	if !deadEnd.Legacy || deadEnd.Summary != "legacy dead end without structured proof" {
		t.Fatalf("legacy dead end not normalized: %#v", deadEnd)
	}
}

func TestValidateRequiredTaskHandoffRejectsLegacyStringDeadEndsForCurrentTask(t *testing.T) {
	runDir := newLedgerTestRun(t)
	handoffRel := taskHandoffRelPath("investigate", "lead_auth")
	writeRunFile(t, runDir, handoffRel, `{"version":1,"phase":"investigate","task_id":"task_investigate_lead_auth","lead_id":"lead_auth","attempted_commands":["fake investigate"],"setup_discoveries":[],"blockers":[],"likely_leads":[],"confirmed_dead_ends":["legacy dead end without structured proof"]}`)
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:  "run_test",
		TaskID: "task_investigate_lead_auth",
		Kind:   "json",
		Title:  "Task handoff: lead_auth",
		Path:   handoffRel,
		LeadID: "lead_auth",
	}); err != nil {
		t.Fatal(err)
	}

	err := validateRequiredTaskHandoff(runDir, "investigate", "task_investigate_lead_auth", "lead_auth", "")
	if err == nil {
		t.Fatal("expected current task legacy dead end rejection")
	}
	if !strings.Contains(err.Error(), "confirmed_dead_ends[0].negative_proof_boundary is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTaskHandoffSchemaDescribesLikelyLeadFanout(t *testing.T) {
	schema := taskHandoffSchemaText()
	for _, want := range []string{
		"under-covered follow-up areas",
		"same-class sibling instances",
		"adjacent risk classes",
	} {
		if !strings.Contains(schema, want) {
			t.Fatalf("schema missing %q:\n%s", want, schema)
		}
	}
}
