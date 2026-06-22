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

	handoffRel := "evidence/phase-handoff-task_deduplicate.json"
	prompt, err := deduplicatePrompt(runDir, "/workspace", Config{
		Instructions: InstructionConfig{Scope: "Security and correctness only."},
	}, []FindingRecord{first, second}, []FindingRecord{first, second}, handoffRel)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# makenomistakes Deduplicate",
		"Review-accepted finding count: 2",
		"Findings requiring deduplicate verdicts: finding_one, finding_two",
		"Security and correctness only.",
		filepath.ToSlash(filepath.Join(runDir, handoffRel)),
		"## finding_one",
		"Deduplicate status: Pending deduplicate verdict",
		"First candidate body.",
		"mnm evidence write --kind markdown --title \"Deduplication notes\"",
		"read artifact content from stdin unless you pass --input /tmp/file",
		"mnm handoff write --path",
		filepath.ToSlash(filepath.Join(runDir, "evidence", "deduplicate-notes.md")),
		filepath.ToSlash(filepath.Join(runDir, "evidence", "handoff-deduplicate.json")),
		"attempted_commands",
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

	prompt, err := deduplicatePrompt(runDir, "/workspace", Config{}, []FindingRecord{first, second}, []FindingRecord{second}, "evidence/phase-handoff-task_deduplicate.json")
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

func TestDeduplicatePromptTruncatesLargeFindingBody(t *testing.T) {
	runDir := newLedgerTestRun(t)
	longBody := "body-start\n" + strings.Repeat("A", deduplicateFindingBodyPreviewRunes+500) + "\nbody-tail"
	finding := addReviewedFindingForTest(t, runDir, "finding_one", "evidence/finding-one.md", longBody)

	prompt, err := deduplicatePrompt(runDir, "/workspace", Config{}, []FindingRecord{finding}, []FindingRecord{finding}, "evidence/phase-handoff-task_deduplicate.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"body-start",
		"[truncated; read the full finding body at ",
		filepath.Join(runDir, "evidence", "finding-one.md"),
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "body-tail") {
		t.Fatalf("prompt included unbounded finding body tail:\n%s", prompt)
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
cat > "$MNM_RUN_DIR/evidence/deduplicate-notes.md" <<'EOF'
# Deduplication notes

Finding two duplicates finding one.
EOF
cat > "$MNM_RUN_DIR/evidence/handoff-deduplicate.json" <<EOF
{"version":1,"phase":"deduplicate","task_id":"$MNM_TASK_ID","attempted_commands":["fake deduplicate"],"setup_discoveries":[],"blockers":[],"likely_leads":[],"confirmed_dead_ends":[{"summary":"finding_two duplicates finding_one","negative_proof_boundary":"same reviewed finding cluster","negative_proof_enforcement":"deduplicate canonical mapping","negative_proof_exposure":"same affected deployment path","negative_proof_edge_cases":"compared root cause, affected file, and fix scope"}]}
EOF
handoff_sha="$( (sha256sum "$MNM_RUN_DIR/evidence/handoff-deduplicate.json" 2>/dev/null || shasum -a 256 "$MNM_RUN_DIR/evidence/handoff-deduplicate.json") | awk '{print $1}')"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_dedup_evidence","run_id":"run_dedup","type":"evidence.added","object":"evidence","object_id":"evidence_dedup_notes","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Deduplication notes","path":"evidence/deduplicate-notes.md","content_sha256":"4315de7bd86fbd899b39ee7a407da54e55722cf1fc5af0c3d3de426d40c791d6"}}
{"id":"event_dedup_handoff","run_id":"run_dedup","type":"evidence.added","object":"evidence","object_id":"evidence_dedup_handoff","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"kind":"json","title":"Task handoff: Deduplication","path":"evidence/handoff-deduplicate.json","content_sha256":"$handoff_sha"}}
{"id":"event_dedup_one","run_id":"run_dedup","type":"verdict.recorded","object":"verdict","object_id":"verdict_dedup_one","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"finding_id":"finding_one","phase":"deduplicate","value":"canonical","reason":"unique issue","canonical_finding_id":""}}
{"id":"event_dedup_two","run_id":"run_dedup","type":"verdict.recorded","object":"verdict","object_id":"verdict_dedup_two","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"finding_id":"finding_two","phase":"deduplicate","value":"duplicate","reason":"same root issue","canonical_finding_id":"finding_one"}}
{"id":"event_done_dedup","run_id":"run_dedup","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:03Z","data":{"status":"completed","summary":"done"}}
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

func TestRunDeduplicatePhaseRequiresDeduplicationEvidence(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	addReviewedFindingForTest(t, runDir, "finding_one", "evidence/finding-one.md", "First candidate body.")

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_dedup_one_$$","run_id":"run_dedup","type":"verdict.recorded","object":"verdict","object_id":"verdict_dedup_one_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"finding_id":"finding_one","phase":"deduplicate","value":"canonical","reason":"unique issue","canonical_finding_id":""}}
{"id":"event_done_dedup_$$","run_id":"run_dedup","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
	}
	err := runDeduplicatePhase(runDir, "run_dedup", t.TempDir(), cfg, opencodePath)
	if err == nil {
		t.Fatal("expected deduplication evidence error")
	}
	if !strings.Contains(err.Error(), "did not register deduplication evidence evidence/deduplicate-notes.md") {
		t.Fatalf("unexpected error: %v", err)
	}
	pending, pendingErr := undeduplicatedLedgerFindings(runDir)
	if pendingErr != nil {
		t.Fatal(pendingErr)
	}
	if len(pending) != 1 || pending[0].ID != "finding_one" {
		t.Fatalf("expected finding_one to remain pending after missing evidence, got %#v", pending)
	}
	if ledgerFindingHasVerdict(runDir, "finding_one", "deduplicate") {
		t.Fatal("dedup verdict without deduplication evidence should not be complete")
	}
	err = runDeduplicatePhase(runDir, "run_dedup", t.TempDir(), cfg, opencodePath)
	if err == nil {
		t.Fatal("expected rerun to reject incomplete deduplication evidence again")
	}
}

func TestRunDeduplicatePhaseRetriesAfterPartialBundleWithoutIngesting(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	addReviewedFindingForTest(t, runDir, "finding_one", "evidence/finding-one.md", "First candidate body.")
	addReviewedFindingForTest(t, runDir, "finding_two", "evidence/finding-two.md", "Second candidate body.")

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	countFile := filepath.Join(t.TempDir(), "dedup-attempt-count")
	body := strings.ReplaceAll(`#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
count_file="__COUNT_FILE__"
count=0
if [ -f "$count_file" ]; then
  count="$(cat "$count_file")"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$count_file"
if [ "$count" -eq 1 ]; then
  cat > "$MNM_RUN_DIR/evidence/deduplicate-notes.md" <<'EOF'
# Deduplication notes

First pass.
EOF
  cat > "$MNM_RUN_DIR/evidence/handoff-deduplicate.json" <<EOF
{"version":1,"phase":"deduplicate","task_id":"$MNM_TASK_ID","attempted_commands":["fake deduplicate attempt 1"],"setup_discoveries":[],"blockers":[],"likely_leads":[],"confirmed_dead_ends":[]}
EOF
  handoff_sha="$( (sha256sum "$MNM_RUN_DIR/evidence/handoff-deduplicate.json" 2>/dev/null || shasum -a 256 "$MNM_RUN_DIR/evidence/handoff-deduplicate.json") | awk '{print $1}')"
  cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_dedup_evidence_1","run_id":"run_dedup","type":"evidence.added","object":"evidence","object_id":"evidence_dedup_notes_1","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Deduplication notes","path":"evidence/deduplicate-notes.md","content_sha256":"b0b3b30141ac6d1c99517813a66f0cda5fbf07332cb6ebfdd173209a525dd9a8"}}
{"id":"event_dedup_handoff_1","run_id":"run_dedup","type":"evidence.added","object":"evidence","object_id":"evidence_dedup_handoff_1","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"kind":"json","title":"Task handoff: Deduplication","path":"evidence/handoff-deduplicate.json","content_sha256":"$handoff_sha"}}
{"id":"event_dedup_one_partial","run_id":"run_dedup","type":"verdict.recorded","object":"verdict","object_id":"verdict_dedup_one_partial","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"finding_id":"finding_one","phase":"deduplicate","value":"canonical","reason":"unique issue","canonical_finding_id":""}}
{"id":"event_done_dedup_1","run_id":"run_dedup","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"partial dedup"}}
EOF
  printf '{"type":"done","attempt":1}\n'
  exit 0
fi
cat > "$MNM_RUN_DIR/evidence/deduplicate-notes.md" <<'EOF'
# Deduplication notes

First pass.
EOF
cat > "$MNM_RUN_DIR/evidence/handoff-deduplicate.json" <<EOF
{"version":1,"phase":"deduplicate","task_id":"$MNM_TASK_ID","attempted_commands":["fake deduplicate attempt 2"],"setup_discoveries":[],"blockers":[],"likely_leads":[],"confirmed_dead_ends":[]}
EOF
handoff_sha="$( (sha256sum "$MNM_RUN_DIR/evidence/handoff-deduplicate.json" 2>/dev/null || shasum -a 256 "$MNM_RUN_DIR/evidence/handoff-deduplicate.json") | awk '{print $1}')"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_dedup_evidence_2","run_id":"run_dedup","type":"evidence.added","object":"evidence","object_id":"evidence_dedup_notes_2","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:03Z","data":{"kind":"markdown","title":"Deduplication notes","path":"evidence/deduplicate-notes.md","content_sha256":"b0b3b30141ac6d1c99517813a66f0cda5fbf07332cb6ebfdd173209a525dd9a8"}}
{"id":"event_dedup_handoff_2","run_id":"run_dedup","type":"evidence.added","object":"evidence","object_id":"evidence_dedup_handoff_2","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:04Z","data":{"kind":"json","title":"Task handoff: Deduplication","path":"evidence/handoff-deduplicate.json","content_sha256":"$handoff_sha"}}
{"id":"event_dedup_one_complete","run_id":"run_dedup","type":"verdict.recorded","object":"verdict","object_id":"verdict_dedup_one_complete","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:04Z","data":{"finding_id":"finding_one","phase":"deduplicate","value":"canonical","reason":"unique issue","canonical_finding_id":""}}
{"id":"event_dedup_two_complete","run_id":"run_dedup","type":"verdict.recorded","object":"verdict","object_id":"verdict_dedup_two_complete","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:05Z","data":{"finding_id":"finding_two","phase":"deduplicate","value":"canonical","reason":"unique issue","canonical_finding_id":""}}
{"id":"event_done_dedup_2","run_id":"run_dedup","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:05Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done","attempt":2}\n'
`, "__COUNT_FILE__", countFile)
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Models: ModelConfig{Default: "fake/model"}}
	if err := runDeduplicatePhase(runDir, "run_dedup", t.TempDir(), cfg, opencodePath); err != nil {
		t.Fatalf("expected partial bundle retry to recover, got: %v", err)
	}
	if count := strings.TrimSpace(readFile(t, countFile)); count != "2" {
		t.Fatalf("attempt count = %s, want 2", count)
	}
	pending, err := undeduplicatedLedgerFindings(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending findings after retry = %#v, want none", pending)
	}
	assertTaskStartedEventCount(t, runDir, "task_deduplicate", 1)
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	var sawRetry bool
	for _, event := range events {
		if event.ID == "event_dedup_one_partial" {
			t.Fatalf("partial bundle event was ingested: %#v", event)
		}
		if event.Type == "task.retrying" && event.ObjectID == "task_deduplicate" {
			sawRetry = true
		}
	}
	if !sawRetry {
		t.Fatalf("expected task.retrying event, got %#v", eventTypes(events))
	}
}

func TestRunDeduplicatePhaseRequiresDeduplicationEvidenceFile(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	addReviewedFindingForTest(t, runDir, "finding_one", "evidence/finding-one.md", "First candidate body.")

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_dedup_evidence_$$","run_id":"run_dedup","type":"evidence.added","object":"evidence","object_id":"evidence_dedup_notes_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Deduplication notes","path":"evidence/deduplicate-notes.md"}}
{"id":"event_dedup_one_$$","run_id":"run_dedup","type":"verdict.recorded","object":"verdict","object_id":"verdict_dedup_one_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"finding_id":"finding_one","phase":"deduplicate","value":"canonical","reason":"unique issue","canonical_finding_id":""}}
{"id":"event_done_dedup_$$","run_id":"run_dedup","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
	}
	err := runDeduplicatePhase(runDir, "run_dedup", t.TempDir(), cfg, opencodePath)
	if err == nil {
		t.Fatal("expected missing deduplication evidence file error")
	}
	if !strings.Contains(err.Error(), "artifact evidence/deduplicate-notes.md") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunDeduplicatePhaseRequiresRegisteredDeduplicationEvidenceHash(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	addReviewedFindingForTest(t, runDir, "finding_one", "evidence/finding-one.md", "First candidate body.")

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
cat > "$MNM_RUN_DIR/evidence/deduplicate-notes.md" <<'EOF'
# Deduplication notes

Clustered by fake opencode.
EOF
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_dedup_evidence_$$","run_id":"run_dedup","type":"evidence.added","object":"evidence","object_id":"evidence_dedup_notes_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Deduplication notes","path":"evidence/deduplicate-notes.md"}}
{"id":"event_dedup_one_$$","run_id":"run_dedup","type":"verdict.recorded","object":"verdict","object_id":"verdict_dedup_one_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"finding_id":"finding_one","phase":"deduplicate","value":"canonical","reason":"unique issue","canonical_finding_id":""}}
{"id":"event_done_dedup_$$","run_id":"run_dedup","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	err := runDeduplicatePhase(runDir, "run_dedup", t.TempDir(), Config{Models: ModelConfig{Default: "fake/model"}}, opencodePath)
	if err == nil {
		t.Fatal("expected missing deduplication evidence hash error")
	}
	if !strings.Contains(err.Error(), "data.content_sha256 is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunDeduplicatePhaseRejectsRerunThatInvalidatesExistingVerdict(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	addReviewedFindingForTest(t, runDir, "finding_one", "evidence/finding-one.md", "First candidate body.")
	addReviewedFindingForTest(t, runDir, "finding_two", "evidence/finding-two.md", "Second candidate body.")
	addCompletedDeduplicateVerdictForTest(t, runDir, "finding_one", "canonical", "")

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
cat > "$MNM_RUN_DIR/evidence/deduplicate-notes.md" <<'EOF'
# Deduplication notes

New notes for finding two.
EOF
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_dedup_evidence_$$","run_id":"run_dedup","type":"evidence.added","object":"evidence","object_id":"evidence_dedup_notes_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Deduplication notes","path":"evidence/deduplicate-notes.md","content_sha256":"716810a70416fa43be4a0700e9ea1eca5195707090391c11fe8087efb5ffca9a"}}
{"id":"event_dedup_two_$$","run_id":"run_dedup","type":"verdict.recorded","object":"verdict","object_id":"verdict_dedup_two_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"finding_id":"finding_two","phase":"deduplicate","value":"canonical","reason":"unique issue","canonical_finding_id":""}}
{"id":"event_done_dedup_$$","run_id":"run_dedup","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	err := runDeduplicatePhase(runDir, "run_dedup", t.TempDir(), Config{Models: ModelConfig{Default: "fake/model"}}, opencodePath)
	if err == nil {
		t.Fatal("expected rerun invalidation error")
	}
	if !strings.Contains(err.Error(), "target artifact evidence/deduplicate-notes.md already exists with different contents") {
		t.Fatalf("unexpected error: %v", err)
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
cat > "$MNM_RUN_DIR/evidence/deduplicate-notes.md" <<'EOF'
# Deduplication notes

Duplicate graph is invalid.
EOF
cat > "$MNM_RUN_DIR/evidence/handoff-deduplicate.json" <<EOF
{"version":1,"phase":"deduplicate","task_id":"$MNM_TASK_ID","attempted_commands":["fake deduplicate"],"setup_discoveries":[],"blockers":[],"likely_leads":[],"confirmed_dead_ends":[]}
EOF
handoff_sha="$( (sha256sum "$MNM_RUN_DIR/evidence/handoff-deduplicate.json" 2>/dev/null || shasum -a 256 "$MNM_RUN_DIR/evidence/handoff-deduplicate.json") | awk '{print $1}')"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_dedup_evidence","run_id":"run_dedup","type":"evidence.added","object":"evidence","object_id":"evidence_dedup_notes","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Deduplication notes","path":"evidence/deduplicate-notes.md","content_sha256":"a70e55557e64a822f69d8781af3e6849c9d4901cab9d9f0f43fdb3ffb7429a9a"}}
{"id":"event_dedup_handoff","run_id":"run_dedup","type":"evidence.added","object":"evidence","object_id":"evidence_dedup_handoff","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"kind":"json","title":"Task handoff: Deduplication","path":"evidence/handoff-deduplicate.json","content_sha256":"$handoff_sha"}}
{"id":"event_dedup_one","run_id":"run_dedup","type":"verdict.recorded","object":"verdict","object_id":"verdict_dedup_one","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"finding_id":"finding_one","phase":"deduplicate","value":"duplicate","reason":"same root issue","canonical_finding_id":"finding_two"}}
{"id":"event_dedup_two","run_id":"run_dedup","type":"verdict.recorded","object":"verdict","object_id":"verdict_dedup_two","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"finding_id":"finding_two","phase":"deduplicate","value":"duplicate","reason":"same root issue","canonical_finding_id":"finding_one"}}
{"id":"event_done_dedup","run_id":"run_dedup","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:03Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	err := runDeduplicatePhase(runDir, "run_dedup", t.TempDir(), Config{Models: ModelConfig{Default: "fake/model"}}, opencodePath)
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
	notesRel := reviewNotesRelPath(id)
	writeRunFile(t, runDir, notesRel, "Review evidence for test.")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_dedup",
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_review_" + id,
		TaskID:   "task_review_" + id,
		Data: map[string]any{
			"kind":           "markdown",
			"title":          "Review notes",
			"path":           notesRel,
			"content_sha256": runFileSHA256ForTest(t, runDir, notesRel),
			"finding_id":     id,
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
	notesRel := deduplicateNotesRelPath()
	writeRunFile(t, runDir, notesRel, "Existing deduplication evidence for test.")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_dedup",
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_dedup_" + findingID,
		TaskID:   taskID,
		Data: map[string]any{
			"kind":           "markdown",
			"title":          "Deduplication notes",
			"path":           notesRel,
			"content_sha256": runFileSHA256ForTest(t, runDir, notesRel),
		},
	}); err != nil {
		t.Fatal(err)
	}
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
