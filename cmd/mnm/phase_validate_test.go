package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatePromptIncludesRequiredLedgerCommands(t *testing.T) {
	runDir := newLedgerTestRun(t)
	finding := addCanonicalFindingForTest(t, runDir, "finding_auth", "evidence/finding-auth.md", "Admin mutation is reachable without authorization.")

	prompt, err := validatePrompt(runDir, "/workspace", Config{
		Instructions: InstructionConfig{Scope: "Security and correctness only."},
	}, finding)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# makenomistakes Validate",
		"Finding ID: finding_auth",
		"Security and correctness only.",
		"Admin mutation is reachable without authorization.",
		"Docker/Compose/minikube",
		"mnm evidence add --kind markdown",
		"mnm verdict record --finding finding_auth --phase validate --value proven",
		"mnm verdict record --finding finding_auth --phase validate --value failed",
		"mnm verdict record --finding finding_auth --phase validate --value inconclusive",
		"mnm task complete --status completed",
		filepath.ToSlash(filepath.Join(runDir, "evidence", "validate-finding_auth-notes.md")),
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestRunValidatePhaseRecordsVerdict(t *testing.T) {
	runDir := newLedgerTestRun(t)
	addCanonicalFindingForTest(t, runDir, "finding_auth", "evidence/finding-auth.md", "Candidate auth defect.")

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
: "${MNM_FINDING_ID:?MNM_FINDING_ID is required}"
cat > "$MNM_RUN_DIR/evidence/validate-$MNM_FINDING_ID-notes.md" <<'EOF'
# Validation notes

Proof observed by fake opencode.
EOF
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_validate_evidence_$MNM_FINDING_ID","run_id":"run_validate","type":"evidence.added","object":"evidence","object_id":"evidence_validate_$MNM_FINDING_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Validation notes","path":"evidence/validate-$MNM_FINDING_ID-notes.md","content_sha256":"e17c0f25a13f204cc60aeca367b6ba453e176189f24810b938164b9b46aaac6d","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_validate_$MNM_FINDING_ID","run_id":"run_validate","type":"verdict.recorded","object":"verdict","object_id":"verdict_validate_$MNM_FINDING_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"finding_id":"$MNM_FINDING_ID","phase":"validate","value":"proven","reason":"proven by fake opencode","canonical_finding_id":""}}
{"id":"event_done_$MNM_FINDING_ID","run_id":"run_validate","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
	}
	if err := runValidatePhase(runDir, "run_validate", t.TempDir(), cfg, opencodePath); err != nil {
		t.Fatal(err)
	}
	pending, err := unvalidatedCanonicalFindings(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending validation findings, got %#v", pending)
	}
	verdict, ok, err := ledgerFindingVerdict(runDir, "finding_auth", "validate")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || verdict.Value != "proven" {
		t.Fatalf("unexpected validation verdict: %#v", verdict)
	}
}

func TestRunValidatePhaseRequiresValidationEvidence(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	addCanonicalFindingForTest(t, runDir, "finding_auth", "evidence/finding-auth.md", "Candidate auth defect.")

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
: "${MNM_FINDING_ID:?MNM_FINDING_ID is required}"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_validate_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"verdict.recorded","object":"verdict","object_id":"verdict_validate_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"finding_id":"$MNM_FINDING_ID","phase":"validate","value":"proven","reason":"proven by fake opencode","canonical_finding_id":""}}
{"id":"event_done_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
	}
	err := runValidatePhase(runDir, "run_validate", t.TempDir(), cfg, opencodePath)
	if err == nil {
		t.Fatal("expected validation evidence error")
	}
	if !strings.Contains(err.Error(), "did not register validation evidence evidence/validate-finding_auth-notes.md for finding finding_auth") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunValidatePhaseRequiresValidationEvidenceFile(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	addCanonicalFindingForTest(t, runDir, "finding_auth", "evidence/finding-auth.md", "Candidate auth defect.")

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
: "${MNM_FINDING_ID:?MNM_FINDING_ID is required}"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_validate_evidence_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"evidence.added","object":"evidence","object_id":"evidence_validate_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Validation notes","path":"evidence/validate-$MNM_FINDING_ID-notes.md","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_validate_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"verdict.recorded","object":"verdict","object_id":"verdict_validate_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"finding_id":"$MNM_FINDING_ID","phase":"validate","value":"proven","reason":"proven by fake opencode","canonical_finding_id":""}}
{"id":"event_done_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
	}
	err := runValidatePhase(runDir, "run_validate", t.TempDir(), cfg, opencodePath)
	if err == nil {
		t.Fatal("expected missing validation evidence file error")
	}
	if !strings.Contains(err.Error(), "read validation evidence evidence/validate-finding_auth-notes.md") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunValidatePhaseRejectsBlankValidationEvidence(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	addCanonicalFindingForTest(t, runDir, "finding_auth", "evidence/finding-auth.md", "Candidate auth defect.")

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
: "${MNM_FINDING_ID:?MNM_FINDING_ID is required}"
printf ' \n\t\n' > "$MNM_RUN_DIR/evidence/validate-$MNM_FINDING_ID-notes.md"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_validate_evidence_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"evidence.added","object":"evidence","object_id":"evidence_validate_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Validation notes","path":"evidence/validate-$MNM_FINDING_ID-notes.md","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_validate_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"verdict.recorded","object":"verdict","object_id":"verdict_validate_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"finding_id":"$MNM_FINDING_ID","phase":"validate","value":"proven","reason":"proven by fake opencode","canonical_finding_id":""}}
{"id":"event_done_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
	}
	err := runValidatePhase(runDir, "run_validate", t.TempDir(), cfg, opencodePath)
	if err == nil {
		t.Fatal("expected blank validation evidence error")
	}
	if !strings.Contains(err.Error(), "validation evidence evidence/validate-finding_auth-notes.md must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func addCanonicalFindingForTest(t *testing.T, runDir, id, bodyRel, body string) FindingRecord {
	t.Helper()
	finding := addReviewedFindingForTest(t, runDir, id, bodyRel, body)
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_validate",
		Type:     "verdict.recorded",
		Object:   "verdict",
		ObjectID: "verdict_deduplicate_" + id,
		TaskID:   "task_deduplicate",
		Data: map[string]any{
			"finding_id":           id,
			"phase":                "deduplicate",
			"value":                "canonical",
			"reason":               "Unique issue.",
			"canonical_finding_id": "",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_validate",
		Type:     "task.completed",
		Object:   "task",
		ObjectID: "task_deduplicate",
		TaskID:   "task_deduplicate",
		Data: map[string]any{
			"status":  "completed",
			"summary": "Deduplicated for test.",
		},
	}); err != nil {
		t.Fatal(err)
	}
	return finding
}
