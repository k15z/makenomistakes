package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestValidatePromptIncludesRequiredLedgerCommands(t *testing.T) {
	runDir := newLedgerTestRun(t)
	finding := addCanonicalFindingForTest(t, runDir, "finding_auth", "evidence/finding-auth.md", "Admin mutation is reachable without authorization.")

	handoffRel := "evidence/phase-handoff-task_validate_finding_auth.json"
	prompt, err := validatePrompt(runDir, "/workspace", Config{
		Instructions: InstructionConfig{Scope: "Security and correctness only."},
	}, finding, handoffRel)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# makenomistakes Validate",
		"Finding ID: finding_auth",
		"Security and correctness only.",
		"Admin mutation is reachable without authorization.",
		"Docker/Compose/minikube",
		filepath.ToSlash(filepath.Join(runDir, handoffRel)),
		"missing dependency, failed command, required service, suspected config gap, and next command",
		"mnm evidence add --kind markdown",
		"Validation notes alone are not enough for a proven verdict.",
		"mnm verdict record --finding finding_auth --phase validate --value proven",
		"mnm verdict record --finding finding_auth --phase validate --value failed",
		"mnm verdict record --finding finding_auth --phase validate --value inconclusive",
		"bounded sibling-instance check",
		"under-covered follow-up areas, sibling instances, adjacent risk classes",
		"bundles separable root causes",
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
cat > "$MNM_RUN_DIR/evidence/validate-$MNM_FINDING_ID-proof.log" <<'EOF'
curl /admin returned 200 without authorization.
EOF
cat > "$MNM_RUN_DIR/evidence/handoff-validate-$MNM_FINDING_ID.json" <<EOF
{"version":1,"phase":"validate","task_id":"$MNM_TASK_ID","finding_id":"$MNM_FINDING_ID","attempted_commands":["fake validate"],"setup_discoveries":[],"blockers":[],"likely_leads":[],"confirmed_dead_ends":[]}
EOF
handoff_sha="$( (sha256sum "$MNM_RUN_DIR/evidence/handoff-validate-$MNM_FINDING_ID.json" 2>/dev/null || shasum -a 256 "$MNM_RUN_DIR/evidence/handoff-validate-$MNM_FINDING_ID.json") | awk '{print $1}')"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_validate_evidence_$MNM_FINDING_ID","run_id":"run_validate","type":"evidence.added","object":"evidence","object_id":"evidence_validate_$MNM_FINDING_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Validation notes","path":"evidence/validate-$MNM_FINDING_ID-notes.md","content_sha256":"e17c0f25a13f204cc60aeca367b6ba453e176189f24810b938164b9b46aaac6d","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_validate_proof_$MNM_FINDING_ID","run_id":"run_validate","type":"evidence.added","object":"evidence","object_id":"evidence_validate_proof_$MNM_FINDING_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"kind":"log","title":"Validation proof","path":"evidence/validate-$MNM_FINDING_ID-proof.log","content_sha256":"f96ec713c4678c5a741bca24d4af533e9f821a9b585ad94a5ac8170643001a40","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_validate_handoff_$MNM_FINDING_ID","run_id":"run_validate","type":"evidence.added","object":"evidence","object_id":"evidence_validate_handoff_$MNM_FINDING_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"kind":"json","title":"Task handoff: $MNM_FINDING_ID","path":"evidence/handoff-validate-$MNM_FINDING_ID.json","content_sha256":"$handoff_sha","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_validate_$MNM_FINDING_ID","run_id":"run_validate","type":"verdict.recorded","object":"verdict","object_id":"verdict_validate_$MNM_FINDING_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"finding_id":"$MNM_FINDING_ID","phase":"validate","value":"proven","reason":"proven by fake opencode","canonical_finding_id":""}}
{"id":"event_done_$MNM_FINDING_ID","run_id":"run_validate","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:03Z","data":{"status":"completed","summary":"done"}}
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

func TestRunValidatePhaseUsesConfiguredParallelism(t *testing.T) {
	runDir := newLedgerTestRun(t)
	addCanonicalFindingForTest(t, runDir, "finding_one", "evidence/finding-one.md", "Candidate issue one.")
	addCanonicalFindingForTest(t, runDir, "finding_two", "evidence/finding-two.md", "Candidate issue two.")
	addCanonicalFindingForTest(t, runDir, "finding_three", "evidence/finding-three.md", "Candidate issue three.")

	attemptRunner := &parallelValidationAttemptRunner{}
	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
		Runner: RunnerConfig{ParallelTasks: 2},
	}
	if err := runValidatePhaseWithAttemptRunner(runDir, "run_validate", t.TempDir(), cfg, attemptRunner); err != nil {
		t.Fatal(err)
	}
	if got := attemptRunner.maxInFlight(); got != 2 {
		t.Fatalf("max concurrent validation tasks = %d, want 2", got)
	}
	pending, err := unvalidatedCanonicalFindings(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending validation findings, got %#v", pending)
	}
}

func TestRunValidatePhaseRequiresProofEvidenceForProvenVerdict(t *testing.T) {
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
cat > "$MNM_RUN_DIR/evidence/validate-$MNM_FINDING_ID-notes.md" <<'EOF'
# Validation notes

Proof claimed without a separate artifact.
EOF
cat > "$MNM_RUN_DIR/evidence/handoff-validate-$MNM_FINDING_ID.json" <<EOF
{"version":1,"phase":"validate","task_id":"$MNM_TASK_ID","finding_id":"$MNM_FINDING_ID","attempted_commands":["fake validate"],"setup_discoveries":[],"blockers":[],"likely_leads":[],"confirmed_dead_ends":[]}
EOF
handoff_sha="$( (sha256sum "$MNM_RUN_DIR/evidence/handoff-validate-$MNM_FINDING_ID.json" 2>/dev/null || shasum -a 256 "$MNM_RUN_DIR/evidence/handoff-validate-$MNM_FINDING_ID.json") | awk '{print $1}')"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_validate_evidence_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"evidence.added","object":"evidence","object_id":"evidence_validate_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Validation notes","path":"evidence/validate-$MNM_FINDING_ID-notes.md","content_sha256":"0d4efc321ef76ae548d993d64630934853873aebbf327b3a3cac79690a19a710","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_validate_handoff_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"evidence.added","object":"evidence","object_id":"evidence_validate_handoff_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"kind":"json","title":"Task handoff: $MNM_FINDING_ID","path":"evidence/handoff-validate-$MNM_FINDING_ID.json","content_sha256":"$handoff_sha","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
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
		t.Fatal("expected missing proof evidence error")
	}
	if !strings.Contains(err.Error(), "recorded proven verdict for finding finding_auth without registering proof evidence beyond evidence/validate-finding_auth-notes.md") {
		t.Fatalf("unexpected error: %v", err)
	}
}

type parallelValidationAttemptRunner struct {
	mu       sync.Mutex
	inFlight int
	max      int
}

func (runner *parallelValidationAttemptRunner) RunOpenCodeTaskAttempt(ctx context.Context, _, runDir string, task opencodeTask, _ int) (openCodeAttemptResult, error) {
	runner.mu.Lock()
	runner.inFlight++
	if runner.inFlight > runner.max {
		runner.max = runner.inFlight
	}
	runner.mu.Unlock()

	defer func() {
		runner.mu.Lock()
		runner.inFlight--
		runner.mu.Unlock()
	}()

	select {
	case <-time.After(50 * time.Millisecond):
	case <-ctx.Done():
		return openCodeAttemptResult{TaskRunDir: runDir}, ctx.Err()
	}

	findingID := task.FindingID
	safeFindingID := safeFileID(findingID)
	notesRel := validationNotesRelPath(findingID)
	proofRel := filepath.ToSlash(filepath.Join("evidence", "validate-"+safeFindingID+"-proof.log"))
	handoffRel := filepath.ToSlash(filepath.Join("evidence", "handoff-validate-"+safeFindingID+".json"))
	files := map[string]string{
		notesRel:   "# Validation notes\n\nValidated " + findingID + ".\n",
		proofRel:   "proof for " + findingID + "\n",
		handoffRel: fmt.Sprintf(`{"version":1,"phase":"validate","task_id":%q,"finding_id":%q,"attempted_commands":["fake validate"],"setup_discoveries":[],"blockers":[],"likely_leads":[],"confirmed_dead_ends":[]}`+"\n", task.TaskID, findingID),
	}
	for rel, body := range files {
		path := filepath.Join(runDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
			return openCodeAttemptResult{TaskRunDir: runDir}, err
		}
		if err := os.WriteFile(path, []byte(body), filePerm); err != nil {
			return openCodeAttemptResult{TaskRunDir: runDir}, err
		}
	}
	notesSHA, err := evidenceFileSHA256(runDir, notesRel)
	if err != nil {
		return openCodeAttemptResult{TaskRunDir: runDir}, err
	}
	proofSHA, err := evidenceFileSHA256(runDir, proofRel)
	if err != nil {
		return openCodeAttemptResult{TaskRunDir: runDir}, err
	}
	handoffSHA, err := evidenceFileSHA256(runDir, handoffRel)
	if err != nil {
		return openCodeAttemptResult{TaskRunDir: runDir}, err
	}
	if err := os.MkdirAll(filepath.Dir(task.LogPath), dirPerm); err != nil {
		return openCodeAttemptResult{TaskRunDir: runDir}, err
	}
	if err := os.WriteFile(task.LogPath, []byte("{\"type\":\"done\"}\n"), filePerm); err != nil {
		return openCodeAttemptResult{TaskRunDir: runDir}, err
	}
	return openCodeAttemptResult{TaskRunDir: runDir}, appendLedgerEvents(runDir, []LedgerEvent{
		{
			RunID:    task.RunID,
			Type:     "evidence.added",
			Object:   "evidence",
			ObjectID: "evidence_validate_notes_" + safeFindingID,
			TaskID:   task.TaskID,
			Data: map[string]any{
				"kind":           "markdown",
				"title":          "Validation notes",
				"path":           notesRel,
				"content_sha256": notesSHA,
				"finding_id":     findingID,
			},
		},
		{
			RunID:    task.RunID,
			Type:     "evidence.added",
			Object:   "evidence",
			ObjectID: "evidence_validate_proof_" + safeFindingID,
			TaskID:   task.TaskID,
			Data: map[string]any{
				"kind":           "log",
				"title":          "Validation proof: " + findingID,
				"path":           proofRel,
				"content_sha256": proofSHA,
				"finding_id":     findingID,
			},
		},
		{
			RunID:    task.RunID,
			Type:     "evidence.added",
			Object:   "evidence",
			ObjectID: "evidence_validate_handoff_" + safeFindingID,
			TaskID:   task.TaskID,
			Data: map[string]any{
				"kind":           "json",
				"title":          "Task handoff: " + findingID,
				"path":           handoffRel,
				"content_sha256": handoffSHA,
				"finding_id":     findingID,
			},
		},
		{
			RunID:    task.RunID,
			Type:     "verdict.recorded",
			Object:   "verdict",
			ObjectID: "verdict_validate_" + safeFindingID,
			TaskID:   task.TaskID,
			Data: map[string]any{
				"finding_id":           findingID,
				"phase":                "validate",
				"value":                "proven",
				"reason":               "proven by fake validate runner",
				"canonical_finding_id": "",
			},
		},
		{
			RunID:    task.RunID,
			Type:     "task.completed",
			Object:   "task",
			ObjectID: task.TaskID,
			TaskID:   task.TaskID,
			Data: map[string]any{
				"status":  "completed",
				"summary": "validated by fake runner",
			},
		},
	})
}

func (runner *parallelValidationAttemptRunner) maxInFlight() int {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.max
}

func TestRunValidatePhaseRequiresProofEvidenceBeforeProvenVerdict(t *testing.T) {
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
cat > "$MNM_RUN_DIR/evidence/validate-$MNM_FINDING_ID-notes.md" <<'EOF'
# Validation notes

Proof claimed before the proof artifact was registered.
EOF
cat > "$MNM_RUN_DIR/evidence/validate-$MNM_FINDING_ID-proof.log" <<'EOF'
curl /admin returned 200 without authorization.
EOF
cat > "$MNM_RUN_DIR/evidence/handoff-validate-$MNM_FINDING_ID.json" <<EOF
{"version":1,"phase":"validate","task_id":"$MNM_TASK_ID","finding_id":"$MNM_FINDING_ID","attempted_commands":["fake validate"],"setup_discoveries":[],"blockers":[],"likely_leads":[],"confirmed_dead_ends":[]}
EOF
handoff_sha="$( (sha256sum "$MNM_RUN_DIR/evidence/handoff-validate-$MNM_FINDING_ID.json" 2>/dev/null || shasum -a 256 "$MNM_RUN_DIR/evidence/handoff-validate-$MNM_FINDING_ID.json") | awk '{print $1}')"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_validate_evidence_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"evidence.added","object":"evidence","object_id":"evidence_validate_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Validation notes","path":"evidence/validate-$MNM_FINDING_ID-notes.md","content_sha256":"eba091e23f96a3b32c27621b46977ef6fb7cd569e997c0f437324948a917535a","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_validate_handoff_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"evidence.added","object":"evidence","object_id":"evidence_validate_handoff_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"kind":"json","title":"Task handoff: $MNM_FINDING_ID","path":"evidence/handoff-validate-$MNM_FINDING_ID.json","content_sha256":"$handoff_sha","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_validate_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"verdict.recorded","object":"verdict","object_id":"verdict_validate_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"finding_id":"$MNM_FINDING_ID","phase":"validate","value":"proven","reason":"proven by fake opencode","canonical_finding_id":""}}
{"id":"event_validate_proof_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"evidence.added","object":"evidence","object_id":"evidence_validate_proof_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"kind":"log","title":"Validation proof","path":"evidence/validate-$MNM_FINDING_ID-proof.log","content_sha256":"f96ec713c4678c5a741bca24d4af533e9f821a9b585ad94a5ac8170643001a40","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_done_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:03Z","data":{"status":"completed","summary":"done"}}
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
		t.Fatal("expected late proof evidence error")
	}
	if !strings.Contains(err.Error(), "recorded proven verdict for finding finding_auth without registering proof evidence beyond evidence/validate-finding_auth-notes.md") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunValidatePhaseRejectsStaleProofEvidenceForProvenVerdict(t *testing.T) {
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
cat > "$MNM_RUN_DIR/evidence/validate-$MNM_FINDING_ID-notes.md" <<'EOF'
# Validation notes

Proof claimed with a stale proof hash.
EOF
cat > "$MNM_RUN_DIR/evidence/validate-$MNM_FINDING_ID-proof.log" <<'EOF'
curl /admin returned 200 without authorization.
EOF
cat > "$MNM_RUN_DIR/evidence/handoff-validate-$MNM_FINDING_ID.json" <<EOF
{"version":1,"phase":"validate","task_id":"$MNM_TASK_ID","finding_id":"$MNM_FINDING_ID","attempted_commands":["fake validate"],"setup_discoveries":[],"blockers":[],"likely_leads":[],"confirmed_dead_ends":[]}
EOF
handoff_sha="$( (sha256sum "$MNM_RUN_DIR/evidence/handoff-validate-$MNM_FINDING_ID.json" 2>/dev/null || shasum -a 256 "$MNM_RUN_DIR/evidence/handoff-validate-$MNM_FINDING_ID.json") | awk '{print $1}')"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_validate_evidence_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"evidence.added","object":"evidence","object_id":"evidence_validate_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Validation notes","path":"evidence/validate-$MNM_FINDING_ID-notes.md","content_sha256":"9a2441c6a6b8ad5178a70aa0b393ff643a9f2f792e907365328e5889401aa993","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_validate_proof_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"evidence.added","object":"evidence","object_id":"evidence_validate_proof_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"kind":"log","title":"Validation proof","path":"evidence/validate-$MNM_FINDING_ID-proof.log","content_sha256":"0000000000000000000000000000000000000000000000000000000000000000","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_validate_handoff_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"evidence.added","object":"evidence","object_id":"evidence_validate_handoff_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"kind":"json","title":"Task handoff: $MNM_FINDING_ID","path":"evidence/handoff-validate-$MNM_FINDING_ID.json","content_sha256":"$handoff_sha","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_validate_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"verdict.recorded","object":"verdict","object_id":"verdict_validate_${MNM_FINDING_ID}_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"finding_id":"$MNM_FINDING_ID","phase":"validate","value":"proven","reason":"proven by fake opencode","canonical_finding_id":""}}
{"id":"event_done_${MNM_FINDING_ID}_$$","run_id":"run_validate","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:03Z","data":{"status":"completed","summary":"done"}}
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
		t.Fatal("expected stale proof evidence hash error")
	}
	if !strings.Contains(err.Error(), "content_sha256") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidationProofEvidenceAllowsHandoffNamedProofAndExcludesGeneratedArtifacts(t *testing.T) {
	runDir := newLedgerTestRun(t)
	findingID := "finding_auth"
	taskID := "task_validate_finding_auth"
	for _, item := range []struct {
		objectID string
		title    string
		path     string
	}{
		{
			objectID: "evidence_handoff_capture",
			title:    "Validation proof: handoff request capture",
			path:     "evidence/handoff-request-capture.log",
		},
		{
			objectID: "evidence_task_handoff",
			title:    "Task handoff: validation",
			path:     "evidence/handoff-validate-finding_auth.json",
		},
		{
			objectID: "evidence_setup_log",
			title:    "Setup hook log: Validate",
			path:     "evidence/setup-task_validate_finding_auth-attempt-1.log",
		},
	} {
		writeRunFile(t, runDir, item.path, item.title+"\n")
		if err := appendLedgerEvent(runDir, LedgerEvent{
			RunID:    "run_validate",
			Type:     "evidence.added",
			Object:   "evidence",
			ObjectID: item.objectID,
			TaskID:   taskID,
			Data: map[string]any{
				"kind":       "log",
				"title":      item.title,
				"path":       item.path,
				"finding_id": findingID,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}

	proof, err := validationProofEvidence(runDir, findingID, taskID, maxInt)
	if err != nil {
		t.Fatal(err)
	}
	if len(proof) != 1 || proof[0].Path != "evidence/handoff-request-capture.log" {
		t.Fatalf("unexpected proof evidence: %#v", proof)
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
	if !strings.Contains(err.Error(), "artifact evidence/validate-finding_auth-notes.md") {
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
	if !strings.Contains(err.Error(), "whitespace-only") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func addCanonicalFindingForTest(t *testing.T, runDir, id, bodyRel, body string) FindingRecord {
	t.Helper()
	finding := addReviewedFindingForTest(t, runDir, id, bodyRel, body)
	addDeduplicationEvidenceForTest(t, runDir, "run_validate", "task_deduplicate")
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

func addDeduplicationEvidenceForTest(t *testing.T, runDir, runID, taskID string) {
	t.Helper()
	notesRel := deduplicateNotesRelPath()
	writeRunFile(t, runDir, notesRel, "Deduplication evidence for test.")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    runID,
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_deduplicate_" + taskID,
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
}
