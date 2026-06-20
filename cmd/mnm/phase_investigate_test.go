package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInvestigatePromptIncludesRequiredLedgerCommands(t *testing.T) {
	runDir := newLedgerTestRun(t)
	writeRunFile(t, runDir, "evidence/lead-auth.md", "Check whether admin routes miss authorization.")
	cfg := Config{
		Instructions: InstructionConfig{Scope: "Security and correctness only."},
	}
	lead := LeadRecord{
		ID:       "lead_auth",
		Title:    "Authorization boundary",
		Category: "security",
		Priority: "high",
		BodyPath: "evidence/lead-auth.md",
		Status:   "open",
	}

	prompt, err := investigatePrompt(runDir, "/workspace", cfg, lead)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# makenomistakes Investigate",
		"Lead ID: lead_auth",
		"Security and correctness only.",
		"Check whether admin routes miss authorization.",
		"mnm finding create --lead lead_auth",
		"mnm lead close --id lead_auth",
		"mnm task complete --status completed",
		filepath.ToSlash(filepath.Join(runDir, "evidence", "investigate-lead_auth-notes.md")),
		filepath.ToSlash(filepath.Join(runDir, "evidence", "finding-lead_auth.md")),
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestInvestigateConfigDefaults(t *testing.T) {
	cfg := Config{Runner: RunnerConfig{CPUs: 1, MaxLeads: 7}}
	if got := maxInvestigations(cfg); got != 7 {
		t.Fatalf("maxInvestigations = %d, want 7", got)
	}
	if got := taskParallelism(cfg); got != 1 {
		t.Fatalf("taskParallelism = %d, want 1", got)
	}

	cfg.Runner.CPUs = 4
	if got := taskParallelism(cfg); got != 2 {
		t.Fatalf("taskParallelism = %d, want 2", got)
	}

	cfg.Runner.MaxInvestigations = 11
	cfg.Runner.ParallelTasks = 3
	if got := maxInvestigations(cfg); got != 11 {
		t.Fatalf("maxInvestigations = %d, want 11", got)
	}
	if got := taskParallelism(cfg); got != 3 {
		t.Fatalf("taskParallelism = %d, want 3", got)
	}
}

func TestRunInvestigatePhaseRecordsLimitReached(t *testing.T) {
	runDir := newLedgerTestRun(t)
	writeRunFile(t, runDir, "evidence/lead-one.md", "Investigate first lead.")
	writeRunFile(t, runDir, "evidence/lead-two.md", "Investigate second lead.")
	for _, lead := range []struct {
		id   string
		body string
	}{
		{id: "lead_one", body: "evidence/lead-one.md"},
		{id: "lead_two", body: "evidence/lead-two.md"},
	} {
		if err := appendLedgerEvent(runDir, LedgerEvent{
			RunID:    "run_limit",
			Type:     "lead.created",
			Object:   "lead",
			ObjectID: lead.id,
			TaskID:   "task_recon",
			Data: map[string]any{
				"title":     "Lead " + lead.id,
				"category":  "security",
				"priority":  "medium",
				"body_path": lead.body,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
: "${MNM_LEAD_ID:?MNM_LEAD_ID is required}"
cat > "$MNM_RUN_DIR/evidence/investigate-$MNM_LEAD_ID-notes.md" <<'EOF'
# Investigation notes

Lead closed by fake opencode.
EOF
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_investigate_evidence_$MNM_LEAD_ID","run_id":"run_limit","type":"evidence.added","object":"evidence","object_id":"evidence_investigate_$MNM_LEAD_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Investigation notes","path":"evidence/investigate-$MNM_LEAD_ID-notes.md","content_sha256":"d1bbbd1e106e0c495f8dccdf753163e3ae58f2a3428ce125bc1c520bd697caa6","lead_id":"$MNM_LEAD_ID","finding_id":""}}
{"id":"event_close_$MNM_LEAD_ID","run_id":"run_limit","type":"lead.closed","object":"lead","object_id":"$MNM_LEAD_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"status":"closed_no_finding","reason":"closed by fake opencode"}}
{"id":"event_done_$MNM_LEAD_ID","run_id":"run_limit","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
		Runner: RunnerConfig{
			MaxLeads:          2,
			MaxInvestigations: 1,
			ParallelTasks:     1,
		},
	}
	if err := runInvestigatePhase(runDir, "run_limit", t.TempDir(), cfg, opencodePath); err != nil {
		t.Fatal(err)
	}
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(eventTypes(events), "investigate.limit_reached") {
		t.Fatalf("missing investigate.limit_reached event in %#v", eventTypes(events))
	}
	open, err := openLedgerLeads(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].ID != "lead_two" {
		t.Fatalf("unexpected open leads after limit: %#v", open)
	}
}

func TestRunInvestigatePhaseCountsCompletedInvestigationsOnResume(t *testing.T) {
	runDir := newLedgerTestRun(t)
	writeRunFile(t, runDir, "evidence/lead-one.md", "Investigate first lead.")
	writeRunFile(t, runDir, "evidence/lead-two.md", "Investigate second lead.")
	for _, lead := range []struct {
		id   string
		body string
	}{
		{id: "lead_one", body: "evidence/lead-one.md"},
		{id: "lead_two", body: "evidence/lead-two.md"},
	} {
		if err := appendLedgerEvent(runDir, LedgerEvent{
			RunID:    "run_resume_limit",
			Type:     "lead.created",
			Object:   "lead",
			ObjectID: lead.id,
			TaskID:   "task_recon",
			Data: map[string]any{
				"title":     "Lead " + lead.id,
				"category":  "security",
				"priority":  "medium",
				"body_path": lead.body,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_resume_limit",
		Type:     "task.started",
		Object:   "task",
		ObjectID: "task_investigate_lead_one",
		TaskID:   "task_investigate_lead_one",
		Data: map[string]any{
			"phase":   "investigate",
			"title":   "Investigate: Lead lead_one",
			"lead_id": "lead_one",
		},
	}); err != nil {
		t.Fatal(err)
	}
	notesRel := investigationNotesRelPath("lead_one")
	writeRunFile(t, runDir, notesRel, "Resume investigation notes.\n")
	appendInvestigationEvidenceForTest(t, runDir, "run_resume_limit", "task_investigate_lead_one", "lead_one", notesRel)
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_resume_limit",
		Type:     "lead.closed",
		Object:   "lead",
		ObjectID: "lead_one",
		TaskID:   "task_investigate_lead_one",
		Data: map[string]any{
			"status": "closed_no_finding",
			"reason": "already investigated before resume",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_resume_limit",
		Type:     "task.completed",
		Object:   "task",
		ObjectID: "task_investigate_lead_one",
		TaskID:   "task_investigate_lead_one",
		Data: map[string]any{
			"status":  "completed",
			"summary": "already investigated before resume",
		},
	}); err != nil {
		t.Fatal(err)
	}

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(opencodePath, []byte("#!/bin/sh\nexit 42\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
		Runner: RunnerConfig{
			MaxLeads:          2,
			MaxInvestigations: 1,
			ParallelTasks:     1,
		},
	}
	if err := runInvestigatePhase(runDir, "run_resume_limit", t.TempDir(), cfg, opencodePath); err != nil {
		t.Fatal(err)
	}
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(eventTypes(events), "investigate.limit_reached") {
		t.Fatalf("missing investigate.limit_reached event in %#v", eventTypes(events))
	}
	open, err := openLedgerLeads(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].ID != "lead_two" {
		t.Fatalf("unexpected open leads after resumed limit: %#v", open)
	}
}

func TestRunInvestigatePhaseFailsOnIncompleteClosedInvestigationOnResume(t *testing.T) {
	runDir := newLedgerTestRun(t)
	writeRunFile(t, runDir, "evidence/lead-one.md", "Investigate first lead.")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_resume_incomplete",
		Type:     "lead.created",
		Object:   "lead",
		ObjectID: "lead_one",
		TaskID:   "task_recon",
		Data: map[string]any{
			"title":     "Lead lead_one",
			"category":  "security",
			"priority":  "medium",
			"body_path": "evidence/lead-one.md",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_resume_incomplete",
		Type:     "task.started",
		Object:   "task",
		ObjectID: "task_investigate_lead_one",
		TaskID:   "task_investigate_lead_one",
		Data: map[string]any{
			"phase":   "investigate",
			"title":   "Investigate: Lead lead_one",
			"lead_id": "lead_one",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_resume_incomplete",
		Type:     "lead.closed",
		Object:   "lead",
		ObjectID: "lead_one",
		TaskID:   "task_investigate_lead_one",
		Data: map[string]any{
			"status": "closed_no_finding",
			"reason": "closed without evidence before resume",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_resume_incomplete",
		Type:     "task.completed",
		Object:   "task",
		ObjectID: "task_investigate_lead_one",
		TaskID:   "task_investigate_lead_one",
		Data: map[string]any{
			"status":  "completed",
			"summary": "closed without evidence before resume",
		},
	}); err != nil {
		t.Fatal(err)
	}

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(opencodePath, []byte("#!/bin/sh\nexit 42\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
		Runner: RunnerConfig{
			MaxLeads:          2,
			MaxInvestigations: 1,
			ParallelTasks:     1,
		},
	}
	err := runInvestigatePhase(runDir, "run_resume_incomplete", t.TempDir(), cfg, opencodePath)
	if err == nil {
		t.Fatal("expected incomplete closed investigation resume error")
	}
	if !strings.Contains(err.Error(), "closed leads with incomplete investigation tasks: lead_one") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLedgerTaskCompletedUsesLatestCompletionStatus(t *testing.T) {
	runDir := newLedgerTestRun(t)
	for _, event := range []LedgerEvent{
		{
			RunID:    "run_latest",
			Type:     "task.completed",
			Object:   "task",
			ObjectID: "task_flaky",
			TaskID:   "task_flaky",
			Data:     map[string]any{"status": "failed"},
		},
		{
			RunID:    "run_latest",
			Type:     "task.completed",
			Object:   "task",
			ObjectID: "task_flaky",
			TaskID:   "task_flaky",
			Data:     map[string]any{"status": "completed"},
		},
		{
			RunID:    "run_latest",
			Type:     "task.completed",
			Object:   "task",
			ObjectID: "task_regressed",
			TaskID:   "task_regressed",
			Data:     map[string]any{"status": "completed"},
		},
		{
			RunID:    "run_latest",
			Type:     "task.completed",
			Object:   "task",
			ObjectID: "task_regressed",
			TaskID:   "task_regressed",
			Data:     map[string]any{"status": "failed"},
		},
	} {
		if err := appendLedgerEvent(runDir, event); err != nil {
			t.Fatal(err)
		}
	}

	if !ledgerTaskCompleted(runDir, "task_flaky") {
		t.Fatal("expected task with latest completed status to be complete")
	}
	if ledgerTaskCompleted(runDir, "task_regressed") {
		t.Fatal("expected task with latest failed status to be incomplete")
	}
}

func TestRunInvestigatePhaseRejectsBlankInvestigationEvidence(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	addOpenLeadForInvestigateTest(t, runDir, "lead_auth", "evidence/lead-auth.md")

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
: "${MNM_LEAD_ID:?MNM_LEAD_ID is required}"
printf ' \n' > "$MNM_RUN_DIR/evidence/investigate-$MNM_LEAD_ID-notes.md"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_investigate_evidence_${MNM_LEAD_ID}_$$","run_id":"run_investigate","type":"evidence.added","object":"evidence","object_id":"evidence_investigate_${MNM_LEAD_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Investigation notes","path":"evidence/investigate-$MNM_LEAD_ID-notes.md","content_sha256":"e16f1596201850fd4a63680b27f603cb64e67176159be3d8ed78a4403fdb1700","lead_id":"$MNM_LEAD_ID","finding_id":""}}
{"id":"event_close_${MNM_LEAD_ID}_$$","run_id":"run_investigate","type":"lead.closed","object":"lead","object_id":"$MNM_LEAD_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"status":"closed_no_finding","reason":"closed by fake opencode"}}
{"id":"event_done_${MNM_LEAD_ID}_$$","run_id":"run_investigate","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
		Runner: RunnerConfig{ParallelTasks: 1, MaxLeads: 1},
	}
	err := runInvestigatePhase(runDir, "run_investigate", t.TempDir(), cfg, opencodePath)
	if err == nil {
		t.Fatal("expected blank investigation evidence error")
	}
	if !strings.Contains(err.Error(), "evidence file evidence/investigate-lead_auth-notes.md must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunInvestigatePhaseRequiresRegisteredInvestigationEvidenceHash(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	addOpenLeadForInvestigateTest(t, runDir, "lead_auth", "evidence/lead-auth.md")

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
: "${MNM_LEAD_ID:?MNM_LEAD_ID is required}"
cat > "$MNM_RUN_DIR/evidence/investigate-$MNM_LEAD_ID-notes.md" <<'EOF'
# Investigation notes

Lead closed by fake opencode.
EOF
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_investigate_evidence_${MNM_LEAD_ID}_$$","run_id":"run_investigate","type":"evidence.added","object":"evidence","object_id":"evidence_investigate_${MNM_LEAD_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Investigation notes","path":"evidence/investigate-$MNM_LEAD_ID-notes.md","lead_id":"$MNM_LEAD_ID","finding_id":""}}
{"id":"event_close_${MNM_LEAD_ID}_$$","run_id":"run_investigate","type":"lead.closed","object":"lead","object_id":"$MNM_LEAD_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"status":"closed_no_finding","reason":"closed by fake opencode"}}
{"id":"event_done_${MNM_LEAD_ID}_$$","run_id":"run_investigate","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
		Runner: RunnerConfig{ParallelTasks: 1, MaxLeads: 1},
	}
	err := runInvestigatePhase(runDir, "run_investigate", t.TempDir(), cfg, opencodePath)
	if err == nil {
		t.Fatal("expected missing investigation evidence hash error")
	}
	if !strings.Contains(err.Error(), "evidence file evidence/investigate-lead_auth-notes.md is missing registered content hash") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunInvestigatePhaseRequiresInvestigationEvidence(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	addOpenLeadForInvestigateTest(t, runDir, "lead_auth", "evidence/lead-auth.md")

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
: "${MNM_LEAD_ID:?MNM_LEAD_ID is required}"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_close_${MNM_LEAD_ID}_$$","run_id":"run_investigate","type":"lead.closed","object":"lead","object_id":"$MNM_LEAD_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"status":"closed_no_finding","reason":"closed by fake opencode"}}
{"id":"event_done_${MNM_LEAD_ID}_$$","run_id":"run_investigate","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
		Runner: RunnerConfig{ParallelTasks: 1, MaxLeads: 1},
	}
	err := runInvestigatePhase(runDir, "run_investigate", t.TempDir(), cfg, opencodePath)
	if err == nil {
		t.Fatal("expected investigation evidence error")
	}
	if !strings.Contains(err.Error(), "did not register investigation evidence evidence/investigate-lead_auth-notes.md for lead lead_auth") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunInvestigatePhaseRequiresInvestigationEvidenceFile(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	addOpenLeadForInvestigateTest(t, runDir, "lead_auth", "evidence/lead-auth.md")

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
: "${MNM_LEAD_ID:?MNM_LEAD_ID is required}"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_investigate_evidence_${MNM_LEAD_ID}_$$","run_id":"run_investigate","type":"evidence.added","object":"evidence","object_id":"evidence_investigate_${MNM_LEAD_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Investigation notes","path":"evidence/investigate-$MNM_LEAD_ID-notes.md","lead_id":"$MNM_LEAD_ID","finding_id":""}}
{"id":"event_close_${MNM_LEAD_ID}_$$","run_id":"run_investigate","type":"lead.closed","object":"lead","object_id":"$MNM_LEAD_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"status":"closed_no_finding","reason":"closed by fake opencode"}}
{"id":"event_done_${MNM_LEAD_ID}_$$","run_id":"run_investigate","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
		Runner: RunnerConfig{ParallelTasks: 1, MaxLeads: 1},
	}
	err := runInvestigatePhase(runDir, "run_investigate", t.TempDir(), cfg, opencodePath)
	if err == nil {
		t.Fatal("expected missing investigation evidence file error")
	}
	if !strings.Contains(err.Error(), "read evidence file evidence/investigate-lead_auth-notes.md") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunInvestigatePhaseRequiresFindingForPromotedLead(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	addOpenLeadForInvestigateTest(t, runDir, "lead_auth", "evidence/lead-auth.md")

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
: "${MNM_LEAD_ID:?MNM_LEAD_ID is required}"
cat > "$MNM_RUN_DIR/evidence/investigate-$MNM_LEAD_ID-notes.md" <<'EOF'
# Investigation notes

Lead was promoted without a finding by fake opencode.
EOF
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_investigate_evidence_${MNM_LEAD_ID}_$$","run_id":"run_investigate","type":"evidence.added","object":"evidence","object_id":"evidence_investigate_${MNM_LEAD_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Investigation notes","path":"evidence/investigate-$MNM_LEAD_ID-notes.md","content_sha256":"3e00c34bcb1b3ea7036a1ca3a44e550bf36bdd52b786872a2b95a12b9280a25d","lead_id":"$MNM_LEAD_ID","finding_id":""}}
{"id":"event_close_${MNM_LEAD_ID}_$$","run_id":"run_investigate","type":"lead.closed","object":"lead","object_id":"$MNM_LEAD_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"status":"promoted_to_finding","reason":"promoted by fake opencode"}}
{"id":"event_done_${MNM_LEAD_ID}_$$","run_id":"run_investigate","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
		Runner: RunnerConfig{ParallelTasks: 1, MaxLeads: 1},
	}
	err := runInvestigatePhase(runDir, "run_investigate", t.TempDir(), cfg, opencodePath)
	if err == nil {
		t.Fatal("expected promoted lead finding error")
	}
	if !strings.Contains(err.Error(), "closed lead lead_auth as promoted_to_finding without creating a finding") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func addOpenLeadForInvestigateTest(t *testing.T, runDir, leadID, bodyRel string) {
	t.Helper()
	writeRunFile(t, runDir, bodyRel, "Investigate this lead.")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_investigate",
		Type:     "lead.created",
		Object:   "lead",
		ObjectID: leadID,
		TaskID:   "task_recon",
		Data: map[string]any{
			"title":     "Authorization boundary",
			"category":  "security",
			"priority":  "high",
			"body_path": bodyRel,
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func appendInvestigationEvidenceForTest(t *testing.T, runDir, runID, taskID, leadID, notesRel string) {
	t.Helper()
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    runID,
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_investigate_" + safeFileID(leadID),
		TaskID:   taskID,
		Data: map[string]any{
			"kind":           "markdown",
			"title":          "Investigation notes",
			"path":           notesRel,
			"content_sha256": runFileSHA256ForTest(t, runDir, notesRel),
			"lead_id":        leadID,
			"finding_id":     "",
		},
	}); err != nil {
		t.Fatal(err)
	}
}
