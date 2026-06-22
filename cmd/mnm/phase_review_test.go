package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReviewPromptIncludesRequiredLedgerCommands(t *testing.T) {
	runDir := newLedgerTestRun(t)
	writeRunFile(t, runDir, "evidence/lead-auth.md", "Check whether admin routes miss authorization.")
	writeRunFile(t, runDir, "evidence/finding-auth.md", "Admin routes do not check user role before mutation.")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "lead.created",
		Object:   "lead",
		ObjectID: "lead_auth",
		TaskID:   "task_recon",
		Data: map[string]any{
			"title":     "Authorization boundary",
			"category":  "security",
			"priority":  "high",
			"body_path": "evidence/lead-auth.md",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_finding",
		TaskID:   "task_investigate_lead_auth",
		Data: map[string]any{
			"kind":       "markdown",
			"title":      "Investigation notes",
			"path":       "evidence/finding-auth.md",
			"finding_id": "finding_auth",
		},
	}); err != nil {
		t.Fatal(err)
	}
	finding := FindingRecord{
		ID:         "finding_auth",
		Title:      "Missing authorization check",
		LeadID:     "lead_auth",
		Category:   "security",
		Severity:   "high",
		Confidence: "medium",
		BodyPath:   "evidence/finding-auth.md",
	}

	handoffRel := "evidence/phase-handoff-task_review_finding_auth.json"
	prompt, err := reviewPrompt(runDir, "/workspace", Config{
		Instructions: InstructionConfig{Scope: "Security and correctness only."},
	}, finding, handoffRel)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# makenomistakes Review",
		"Finding ID: finding_auth",
		"Security and correctness only.",
		"Admin routes do not check user role",
		filepath.ToSlash(filepath.Join(runDir, handoffRel)),
		"mnm evidence add --kind markdown",
		filepath.ToSlash(filepath.Join(runDir, "evidence", "handoff-review-finding_auth.json")),
		"attempted_commands",
		"mnm verdict record --finding finding_auth --phase review --value accepted",
		"mnm verdict record --finding finding_auth --phase review --value rejected",
		"under-covered follow-up areas, sibling instances, adjacent risk classes",
		"bundles separable root causes",
		"bounded sibling-instance check",
		"mnm task complete --status completed",
		filepath.ToSlash(filepath.Join(runDir, "evidence", "review-finding_auth-notes.md")),
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestRunReviewPhaseRecordsVerdict(t *testing.T) {
	runDir := newLedgerTestRun(t)
	writeRunFile(t, runDir, "evidence/finding-auth.md", "Candidate auth defect.")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_review",
		Type:     "finding.created",
		Object:   "finding",
		ObjectID: "finding_auth",
		TaskID:   "task_investigate_lead_auth",
		Data: map[string]any{
			"title":      "Missing authorization check",
			"category":   "authz",
			"severity":   "high",
			"confidence": "medium",
			"body_path":  "evidence/finding-auth.md",
		},
	}); err != nil {
		t.Fatal(err)
	}

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
: "${MNM_FINDING_ID:?MNM_FINDING_ID is required}"
cat > "$MNM_RUN_DIR/evidence/review-$MNM_FINDING_ID-notes.md" <<'EOF'
# Review notes

Finding accepted by fake opencode.
EOF
cat > "$MNM_RUN_DIR/evidence/handoff-review-$MNM_FINDING_ID.json" <<EOF
{"version":1,"phase":"review","task_id":"$MNM_TASK_ID","finding_id":"$MNM_FINDING_ID","attempted_commands":["fake review"],"setup_discoveries":[],"blockers":[],"likely_leads":[],"confirmed_dead_ends":[]}
EOF
handoff_sha="$( (sha256sum "$MNM_RUN_DIR/evidence/handoff-review-$MNM_FINDING_ID.json" 2>/dev/null || shasum -a 256 "$MNM_RUN_DIR/evidence/handoff-review-$MNM_FINDING_ID.json") | awk '{print $1}')"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_review_evidence_$MNM_FINDING_ID","run_id":"run_review","type":"evidence.added","object":"evidence","object_id":"evidence_review_$MNM_FINDING_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Review notes","path":"evidence/review-$MNM_FINDING_ID-notes.md","content_sha256":"ddac59eafaa08bc99530d71bd9784951b3b1fee0973d0d75fde6a294b3b60e53","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_review_handoff_$MNM_FINDING_ID","run_id":"run_review","type":"evidence.added","object":"evidence","object_id":"evidence_review_handoff_$MNM_FINDING_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"kind":"json","title":"Task handoff: $MNM_FINDING_ID","path":"evidence/handoff-review-$MNM_FINDING_ID.json","content_sha256":"$handoff_sha","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_review_$MNM_FINDING_ID","run_id":"run_review","type":"verdict.recorded","object":"verdict","object_id":"verdict_$MNM_FINDING_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"finding_id":"$MNM_FINDING_ID","phase":"review","value":"accepted","reason":"accepted by fake opencode"}}
{"id":"event_done_$MNM_FINDING_ID","run_id":"run_review","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
		Runner: RunnerConfig{ParallelTasks: 1},
	}
	if err := runReviewPhase(runDir, "run_review", t.TempDir(), cfg, opencodePath); err != nil {
		t.Fatal(err)
	}
	if !ledgerFindingHasVerdict(runDir, "finding_auth", "review") {
		t.Fatal("expected review verdict")
	}
	pending, err := unreviewedLedgerFindings(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending findings, got %#v", pending)
	}
}

func TestRunReviewPhaseRequiresReviewEvidence(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	addReviewCandidateFindingForTest(t, runDir)

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
: "${MNM_FINDING_ID:?MNM_FINDING_ID is required}"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_review_${MNM_FINDING_ID}_$$","run_id":"run_review","type":"verdict.recorded","object":"verdict","object_id":"verdict_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"finding_id":"$MNM_FINDING_ID","phase":"review","value":"accepted","reason":"accepted by fake opencode"}}
{"id":"event_done_${MNM_FINDING_ID}_$$","run_id":"run_review","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
		Runner: RunnerConfig{ParallelTasks: 1},
	}
	err := runReviewPhase(runDir, "run_review", t.TempDir(), cfg, opencodePath)
	if err == nil {
		t.Fatal("expected review evidence error")
	}
	if !strings.Contains(err.Error(), "did not register review evidence evidence/review-finding_auth-notes.md for finding finding_auth") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunReviewPhaseRequiresReviewEvidenceFile(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	addReviewCandidateFindingForTest(t, runDir)

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
: "${MNM_FINDING_ID:?MNM_FINDING_ID is required}"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_review_evidence_${MNM_FINDING_ID}_$$","run_id":"run_review","type":"evidence.added","object":"evidence","object_id":"evidence_review_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Review notes","path":"evidence/review-$MNM_FINDING_ID-notes.md","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_review_${MNM_FINDING_ID}_$$","run_id":"run_review","type":"verdict.recorded","object":"verdict","object_id":"verdict_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"finding_id":"$MNM_FINDING_ID","phase":"review","value":"accepted","reason":"accepted by fake opencode"}}
{"id":"event_done_${MNM_FINDING_ID}_$$","run_id":"run_review","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
		Runner: RunnerConfig{ParallelTasks: 1},
	}
	err := runReviewPhase(runDir, "run_review", t.TempDir(), cfg, opencodePath)
	if err == nil {
		t.Fatal("expected missing review evidence file error")
	}
	if !strings.Contains(err.Error(), "artifact evidence/review-finding_auth-notes.md") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunReviewPhaseRejectsBlankReviewEvidence(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	addReviewCandidateFindingForTest(t, runDir)

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
: "${MNM_FINDING_ID:?MNM_FINDING_ID is required}"
printf ' \n\t\n' > "$MNM_RUN_DIR/evidence/review-$MNM_FINDING_ID-notes.md"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_review_evidence_${MNM_FINDING_ID}_$$","run_id":"run_review","type":"evidence.added","object":"evidence","object_id":"evidence_review_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Review notes","path":"evidence/review-$MNM_FINDING_ID-notes.md","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_review_${MNM_FINDING_ID}_$$","run_id":"run_review","type":"verdict.recorded","object":"verdict","object_id":"verdict_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"finding_id":"$MNM_FINDING_ID","phase":"review","value":"accepted","reason":"accepted by fake opencode"}}
{"id":"event_done_${MNM_FINDING_ID}_$$","run_id":"run_review","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
		Runner: RunnerConfig{ParallelTasks: 1},
	}
	err := runReviewPhase(runDir, "run_review", t.TempDir(), cfg, opencodePath)
	if err == nil {
		t.Fatal("expected blank review evidence error")
	}
	if !strings.Contains(err.Error(), "whitespace-only") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func addReviewCandidateFindingForTest(t *testing.T, runDir string) {
	t.Helper()
	writeRunFile(t, runDir, "evidence/finding-auth.md", "Candidate auth defect.")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_review",
		Type:     "finding.created",
		Object:   "finding",
		ObjectID: "finding_auth",
		TaskID:   "task_investigate_lead_auth",
		Data: map[string]any{
			"title":      "Missing authorization check",
			"category":   "authz",
			"severity":   "high",
			"confidence": "medium",
			"body_path":  "evidence/finding-auth.md",
		},
	}); err != nil {
		t.Fatal(err)
	}
}
