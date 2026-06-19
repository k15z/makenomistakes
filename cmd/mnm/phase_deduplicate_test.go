package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeduplicatePromptIncludesRequiredLedgerCommands(t *testing.T) {
	runDir := newLedgerTestRun(t)
	first := addReviewedFindingForTest(t, runDir, "finding_one", "evidence/finding-one.md", "First candidate body.")
	second := addReviewedFindingForTest(t, runDir, "finding_two", "evidence/finding-two.md", "Second candidate body.")

	prompt, err := deduplicatePrompt(runDir, "/workspace", Config{
		Instructions: InstructionConfig{Scope: "Security and correctness only."},
	}, []FindingRecord{first, second}, []FindingRecord{first, second})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# makenomistakes Deduplicate",
		"Review-accepted finding count: 2",
		"Findings requiring deduplicate verdicts: finding_one, finding_two",
		"Security and correctness only.",
		"## finding_one",
		"Deduplicate status: Pending deduplicate verdict",
		"First candidate body.",
		"mnm verdict record --finding FINDING_ID --phase deduplicate --value canonical",
		"mnm verdict record --finding FINDING_ID --phase deduplicate --value duplicate --canonical-finding CANONICAL_FINDING_ID",
		"mnm task complete --status completed",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestDeduplicatePromptIncludesExistingCanonicalContext(t *testing.T) {
	runDir := newLedgerTestRun(t)
	first := addReviewedFindingForTest(t, runDir, "finding_one", "evidence/finding-one.md", "First candidate body.")
	second := addReviewedFindingForTest(t, runDir, "finding_two", "evidence/finding-two.md", "Second candidate body.")
	addCompletedDeduplicateVerdictForTest(t, runDir, "finding_one", "canonical", "")

	prompt, err := deduplicatePrompt(runDir, "/workspace", Config{}, []FindingRecord{first, second}, []FindingRecord{second})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Review-accepted finding count: 2",
		"Findings requiring deduplicate verdicts: finding_two",
		"## finding_one",
		"Deduplicate status: Existing deduplicate verdict: canonical",
		"## finding_two",
		"Deduplicate status: Pending deduplicate verdict",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestRunDeduplicatePhaseRecordsVerdicts(t *testing.T) {
	runDir := newLedgerTestRun(t)
	addReviewedFindingForTest(t, runDir, "finding_one", "evidence/finding-one.md", "First candidate body.")
	addReviewedFindingForTest(t, runDir, "finding_two", "evidence/finding-two.md", "Second candidate body.")

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_dedup_one","run_id":"run_dedup","type":"verdict.recorded","object":"verdict","object_id":"verdict_dedup_one","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"finding_id":"finding_one","phase":"deduplicate","value":"canonical","reason":"unique issue","canonical_finding_id":""}}
{"id":"event_dedup_two","run_id":"run_dedup","type":"verdict.recorded","object":"verdict","object_id":"verdict_dedup_two","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"finding_id":"finding_two","phase":"deduplicate","value":"duplicate","reason":"same root issue","canonical_finding_id":"finding_one"}}
{"id":"event_done_dedup","run_id":"run_dedup","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
	}
	if err := runDeduplicatePhase(runDir, "run_dedup", t.TempDir(), cfg, opencodePath); err != nil {
		t.Fatal(err)
	}
	pending, err := undeduplicatedLedgerFindings(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending dedup findings, got %#v", pending)
	}
	verdict, ok, err := ledgerFindingVerdict(runDir, "finding_two", "deduplicate")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || verdict.Value != "duplicate" || verdict.CanonicalFindingID != "finding_one" {
		t.Fatalf("unexpected duplicate verdict: %#v", verdict)
	}
}

func TestRunDeduplicatePhaseRejectsDuplicateToNonCanonical(t *testing.T) {
	runDir := newLedgerTestRun(t)
	addReviewedFindingForTest(t, runDir, "finding_one", "evidence/finding-one.md", "First candidate body.")
	addReviewedFindingForTest(t, runDir, "finding_two", "evidence/finding-two.md", "Second candidate body.")

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_dedup_one","run_id":"run_dedup","type":"verdict.recorded","object":"verdict","object_id":"verdict_dedup_one","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"finding_id":"finding_one","phase":"deduplicate","value":"duplicate","reason":"same root issue","canonical_finding_id":"finding_two"}}
{"id":"event_dedup_two","run_id":"run_dedup","type":"verdict.recorded","object":"verdict","object_id":"verdict_dedup_two","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"finding_id":"finding_two","phase":"deduplicate","value":"duplicate","reason":"same root issue","canonical_finding_id":"finding_one"}}
{"id":"event_done_dedup","run_id":"run_dedup","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Models: ModelConfig{Default: "fake/model"}}
	err := runDeduplicatePhase(runDir, "run_dedup", t.TempDir(), cfg, opencodePath)
	if err == nil {
		t.Fatal("expected non-canonical duplicate target error")
	}
	if !strings.Contains(err.Error(), "non-canonical finding") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func addReviewedFindingForTest(t *testing.T, runDir, id, bodyRel, body string) FindingRecord {
	t.Helper()
	writeRunFile(t, runDir, bodyRel, body)
	finding := FindingRecord{
		ID:         id,
		Title:      "Finding " + id,
		Category:   "security",
		Severity:   "medium",
		Confidence: "medium",
		BodyPath:   bodyRel,
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_dedup",
		Type:     "finding.created",
		Object:   "finding",
		ObjectID: id,
		TaskID:   "task_investigate_" + id,
		Data: map[string]any{
			"title":      finding.Title,
			"category":   finding.Category,
			"severity":   finding.Severity,
			"confidence": finding.Confidence,
			"body_path":  finding.BodyPath,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_dedup",
		Type:     "verdict.recorded",
		Object:   "verdict",
		ObjectID: "verdict_review_" + id,
		TaskID:   "task_review_" + id,
		Data: map[string]any{
			"finding_id":           id,
			"phase":                "review",
			"value":                "accepted",
			"reason":               "Accepted for test.",
			"canonical_finding_id": "",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_dedup",
		Type:     "task.completed",
		Object:   "task",
		ObjectID: "task_review_" + id,
		TaskID:   "task_review_" + id,
		Data: map[string]any{
			"status":  "completed",
			"summary": "Reviewed for test.",
		},
	}); err != nil {
		t.Fatal(err)
	}
	return finding
}

func addCompletedDeduplicateVerdictForTest(t *testing.T, runDir, findingID, value, canonicalID string) {
	t.Helper()
	taskID := "task_deduplicate_existing_" + findingID
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_dedup",
		Type:     "verdict.recorded",
		Object:   "verdict",
		ObjectID: "verdict_dedup_" + findingID,
		TaskID:   taskID,
		Data: map[string]any{
			"finding_id":           findingID,
			"phase":                "deduplicate",
			"value":                value,
			"reason":               "Existing verdict.",
			"canonical_finding_id": canonicalID,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_dedup",
		Type:     "task.completed",
		Object:   "task",
		ObjectID: taskID,
		TaskID:   taskID,
		Data: map[string]any{
			"status":  "completed",
			"summary": "Deduplicated for test.",
		},
	}); err != nil {
		t.Fatal(err)
	}
}
