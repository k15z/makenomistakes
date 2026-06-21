package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinalizePromptIncludesRequiredReportCommands(t *testing.T) {
	runDir := newLedgerTestRun(t)
	handoffRel := "evidence/phase-handoff-task_finalize.json"
	prompt, err := finalizePrompt(runDir, "/workspace", Config{
		Instructions: InstructionConfig{Scope: "Security and correctness only."},
	}, handoffRel)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# makenomistakes Finalize",
		filepath.ToSlash(filepath.Join(runDir, "evidence", "finalize-context.json")),
		"Ledger path: " + filepath.ToSlash(filepath.Join(runDir, "events.jsonl")),
		"Security and correctness only.",
		"Read " + filepath.ToSlash(filepath.Join(runDir, "evidence", "finalize-context.json")) + " first",
		filepath.ToSlash(filepath.Join(runDir, handoffRel)),
		"structured phase handoff context",
		"Do not read opencode-*.jsonl transcripts",
		"treat validation notes and verdict details as higher authority",
		"hardcoded session signing secret is not by itself proof of forged authenticated sessions",
		"mnm report finalize --markdown",
		filepath.ToSlash(filepath.Join(runDir, "report.md")),
		filepath.ToSlash(filepath.Join(runDir, "report.json")),
		"mnm task complete --status completed",
		"proven findings first",
		"The JSON must parse",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestBuildFinalizeContextIncludesCompactFindingData(t *testing.T) {
	runDir := newLedgerTestRun(t)
	manifestRel := "evidence/runner-manifest.json"
	if err := writeJSON(filepath.Join(runDir, filepath.FromSlash(manifestRel)), map[string]any{
		"workspace_files": []string{"NodeGoat/app/routes/profile.js"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registerRunnerEvidence(runDir, "run_test", "json", "Runner lifecycle manifest", manifestRel, false); err != nil {
		t.Fatal(err)
	}
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	findings, err := ledgerFindings(runDir)
	if err != nil {
		t.Fatal(err)
	}
	var finding FindingRecord
	for _, item := range findings {
		if item.ID == findingID {
			finding = item
			break
		}
	}
	if finding.ID == "" {
		t.Fatalf("missing finding %s", findingID)
	}
	writeRunFile(t, runDir, finding.BodyPath, "Profile route issue at app/routes/profile.js:64.")
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	proofRel := addValidationProofForFindingForTest(t, runDir, findingID)
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	data, err := buildFinalizeContext(runDir, "run_test")
	if err != nil {
		t.Fatal(err)
	}
	var context finalizeContextFile
	if err := json.Unmarshal(data, &context); err != nil {
		t.Fatalf("context JSON did not parse: %v\n%s", err, data)
	}
	if got := context.Counts["findings_proven"]; got != 1 {
		t.Fatalf("findings_proven = %d, want 1\n%s", got, data)
	}
	if len(context.Findings) != 1 {
		t.Fatalf("findings = %d, want 1\n%s", len(context.Findings), data)
	}
	got := context.Findings[0]
	if got.ID != findingID || got.Status != "validation_proven" || got.Bucket != "proven" {
		t.Fatalf("unexpected finding context: %#v", got)
	}
	if !containsString(got.ValidationProofEvidencePaths, proofRel) {
		t.Fatalf("validation proof paths missing %s: %#v", proofRel, got.ValidationProofEvidencePaths)
	}
	if !containsString(got.RecommendedEvidencePaths, proofRel) {
		t.Fatalf("recommended evidence paths missing %s: %#v", proofRel, got.RecommendedEvidencePaths)
	}
	if !containsString(got.AffectedPathCandidates, "NodeGoat/app/routes/profile.js") {
		t.Fatalf("affected path candidates missing manifest path: %#v", got.AffectedPathCandidates)
	}
	if got.Verdicts == nil || strings.Join(got.Verdicts, ",") != "review accepted,deduplicate canonical,validation proven" {
		t.Fatalf("unexpected verdict labels: %#v", got.Verdicts)
	}
}

func TestBuildFinalizeContextIncludesValidationBlockers(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	handoffRel := addValidationHandoffForFindingForTest(t, runDir, findingID, []taskHandoffBlocker{{
		Summary:            "database service was unavailable",
		MissingDependency:  "docker compose",
		FailedCommand:      "go test ./cmd/mnm",
		RequiredService:    "postgres",
		SuspectedConfigGap: "DATABASE_URL was unset",
		NextCommand:        "docker compose up -d postgres && go test ./cmd/mnm",
	}})
	recordVerdictForTest(t, runDir, findingID, "validate", "inconclusive", "")

	data, err := buildFinalizeContext(runDir, "run_test")
	if err != nil {
		t.Fatal(err)
	}
	var context finalizeContextFile
	if err := json.Unmarshal(data, &context); err != nil {
		t.Fatalf("context JSON did not parse: %v\n%s", err, data)
	}
	if got := context.Counts["findings_inconclusive"]; got != 1 {
		t.Fatalf("findings_inconclusive = %d, want 1\n%s", got, data)
	}
	if len(context.Findings) != 1 {
		t.Fatalf("findings = %d, want 1\n%s", len(context.Findings), data)
	}
	got := context.Findings[0]
	if got.ID != findingID || got.Status != "validation_inconclusive" || got.Bucket != "inconclusive" {
		t.Fatalf("unexpected finding context: %#v", got)
	}
	if len(got.ValidationBlockers) != 1 {
		t.Fatalf("validation blockers = %#v, want one", got.ValidationBlockers)
	}
	blocker := got.ValidationBlockers[0]
	if blocker.SourcePath != handoffRel ||
		blocker.MissingDependency != "docker compose" ||
		blocker.RequiredService != "postgres" ||
		blocker.SuspectedConfigGap != "DATABASE_URL was unset" ||
		!strings.Contains(blocker.NextCommand, "docker compose up") {
		t.Fatalf("unexpected validation blocker: %#v", blocker)
	}
}

func TestAffectedPathCandidatesUsesUniqueBasenames(t *testing.T) {
	workspaceFiles := []string{
		"NodeGoat/app/routes/allocations.js",
		"NodeGoat/app/routes/benefits.js",
		"NodeGoat/app/routes/index.js",
		"NodeGoat/artifacts/db-reset.js",
		"NodeGoat/test/e2e/plugins/index.js",
	}
	got := affectedPathCandidates("See benefits.js:21 and allocations.js:16, plus index.js:55.", workspaceFiles)
	for _, want := range []string{
		"NodeGoat/app/routes/allocations.js",
		"NodeGoat/app/routes/benefits.js",
	} {
		if !containsString(got, want) {
			t.Fatalf("affected path candidates missing %s: %#v", want, got)
		}
	}
	if containsString(got, "NodeGoat/app/routes/index.js") || containsString(got, "NodeGoat/test/e2e/plugins/index.js") {
		t.Fatalf("ambiguous basename index.js should not be included from basename alone: %#v", got)
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestRunFinalizeTaskRegistersReports(t *testing.T) {
	runDir := newLedgerTestRun(t)
	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
cat > "$MNM_RUN_DIR/report.md" <<'EOF'
# Report

Fake report.
EOF
cat > "$MNM_RUN_DIR/report.json" <<'EOF'
{"run_id":"run_finalize","counts":{"findings_proven":0,"findings_inconclusive":0,"findings_failed":0,"findings_rejected":0,"findings_duplicate":0,"findings_unvalidated":0},"report_paths":{"markdown":"report.md","json":"report.json"},"proven":[],"inconclusive":[],"failed":[],"rejected":[],"duplicate":[],"unvalidated":[]}
EOF
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_report","run_id":"run_finalize","type":"report.finalized","object":"report","object_id":"report_fake","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"markdown_path":"report.md","json_path":"report.json"}}
{"id":"event_done_finalize","run_id":"run_finalize","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Models: ModelConfig{Default: "fake/model"},
	}
	if err := runFinalizeTask(runDir, "run_finalize", t.TempDir(), cfg, opencodePath); err != nil {
		t.Fatal(err)
	}
	if !ledgerReportFinalized(runDir) {
		t.Fatal("expected report to be finalized")
	}
	if !ledgerTaskCompleted(runDir, "task_finalize") {
		t.Fatal("expected finalize task to complete")
	}
	if got := readFile(t, filepath.Join(runDir, "report.md")); !strings.Contains(got, "Fake report") {
		t.Fatalf("unexpected report.md:\n%s", got)
	}
}

func TestRunFinalizeTaskRejectsInvalidDirectReportEvent(t *testing.T) {
	runDir := newLedgerTestRun(t)
	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
cat > "$MNM_RUN_DIR/report.md" <<'EOF'
# Report

Fake report.
EOF
cat > "$MNM_RUN_DIR/report.json" <<'EOF'
{"findings":[]}
EOF
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_report","run_id":"run_finalize","type":"report.finalized","object":"report","object_id":"report_fake","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"markdown_path":"report.md","json_path":"report.json"}}
{"id":"event_done_finalize","run_id":"run_finalize","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Models: ModelConfig{Default: "fake/model"}}
	err := runFinalizeTask(runDir, "run_finalize", t.TempDir(), cfg, opencodePath)
	if err == nil {
		t.Fatal("expected invalid direct report event to fail")
	}
	if !strings.Contains(err.Error(), "validate task bundle report report_fake") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunFinalizeTaskValidatesCompletedExistingFinalizedReport(t *testing.T) {
	runDir := newLedgerTestRun(t)
	writeRunFile(t, runDir, "report.md", "# Report")
	writeRunFile(t, runDir, "report.json", `{"findings":[]}`)
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_finalize",
		Type:     "report.finalized",
		Object:   "report",
		ObjectID: "report_existing",
		TaskID:   "task_finalize",
		Data: map[string]any{
			"markdown_path": "report.md",
			"json_path":     "report.json",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_finalize",
		Type:     "task.completed",
		Object:   "task",
		ObjectID: "task_finalize",
		TaskID:   "task_finalize",
		Data: map[string]any{
			"status":  "completed",
			"summary": "done",
		},
	}); err != nil {
		t.Fatal(err)
	}

	err := runFinalizeTask(runDir, "run_finalize", t.TempDir(), Config{}, filepath.Join(t.TempDir(), "opencode"))
	if err == nil {
		t.Fatal("expected existing invalid report to fail validation")
	}
	if !strings.Contains(err.Error(), "report_existing failed validation") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunFinalizeTaskIgnoresReportsFromOtherTasks(t *testing.T) {
	runDir := newLedgerTestRun(t)
	writeRunFile(t, runDir, "other-report.md", "# Wrong Report")
	writeRunFile(t, runDir, "other-report.json", validReportJSON(t, "run_finalize", "other-report.md", "other-report.json", nil))
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_finalize",
		Type:     "report.finalized",
		Object:   "report",
		ObjectID: "report_other",
		TaskID:   "task_other",
		Data: map[string]any{
			"markdown_path": "other-report.md",
			"json_path":     "other-report.json",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_finalize",
		Type:     "task.completed",
		Object:   "task",
		ObjectID: "task_other",
		TaskID:   "task_other",
		Data: map[string]any{
			"status":  "completed",
			"summary": "other task done",
		},
	}); err != nil {
		t.Fatal(err)
	}

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
cat > "$MNM_RUN_DIR/report.md" <<'EOF'
# Correct Report
EOF
cat > "$MNM_RUN_DIR/report.json" <<'EOF'
{"run_id":"run_finalize","counts":{"findings_proven":0,"findings_inconclusive":0,"findings_failed":0,"findings_rejected":0,"findings_duplicate":0,"findings_unvalidated":0},"report_paths":{"markdown":"report.md","json":"report.json"},"proven":[],"inconclusive":[],"failed":[],"rejected":[],"duplicate":[],"unvalidated":[]}
EOF
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_report_correct","run_id":"run_finalize","type":"report.finalized","object":"report","object_id":"report_correct","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"markdown_path":"report.md","json_path":"report.json"}}
{"id":"event_done_finalize_again","run_id":"run_finalize","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Models: ModelConfig{Default: "fake/model"}}
	if err := runFinalizeTask(runDir, "run_finalize", t.TempDir(), cfg, opencodePath); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(runDir, "report.md")); !strings.Contains(got, "Correct Report") {
		t.Fatalf("expected finalize task to rerun, got:\n%s", got)
	}
}

func TestRunFinalizeTaskRetriesPartialFinalize(t *testing.T) {
	runDir := newLedgerTestRun(t)
	if err := os.WriteFile(filepath.Join(runDir, "report.md"), []byte("# Partial\n"), filePerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "report.json"), []byte(`{"partial":true}`), filePerm); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_finalize",
		Type:     "report.finalized",
		Object:   "report",
		ObjectID: "report_partial",
		TaskID:   "task_finalize",
		Data: map[string]any{
			"markdown_path": "report.md",
			"json_path":     "report.json",
		},
	}); err != nil {
		t.Fatal(err)
	}

	opencodePath := filepath.Join(t.TempDir(), "opencode")
	body := `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
cat > "$MNM_RUN_DIR/report.md" <<'EOF'
# Retried Report
EOF
cat > "$MNM_RUN_DIR/report.json" <<'EOF'
{"run_id":"run_finalize","counts":{"findings_proven":0,"findings_inconclusive":0,"findings_failed":0,"findings_rejected":0,"findings_duplicate":0,"findings_unvalidated":0},"report_paths":{"markdown":"report.md","json":"report.json"},"proven":[],"inconclusive":[],"failed":[],"rejected":[],"duplicate":[],"unvalidated":[]}
EOF
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_report_retry","run_id":"run_finalize","type":"report.finalized","object":"report","object_id":"report_retry","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"markdown_path":"report.md","json_path":"report.json"}}
{"id":"event_done_finalize","run_id":"run_finalize","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Models: ModelConfig{Default: "fake/model"}}
	if err := runFinalizeTask(runDir, "run_finalize", t.TempDir(), cfg, opencodePath); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(runDir, "report.md")); !strings.Contains(got, "Retried Report") {
		t.Fatalf("expected retried report, got:\n%s", got)
	}
}

func TestReportFinalizeRejectsMalformedJSON(t *testing.T) {
	runDir := newLedgerTestRun(t)
	if err := writeCurrentTaskForTest(runDir, TaskRecord{
		RunID:       "run_test",
		TaskID:      "task_finalize",
		Phase:       "finalize",
		Title:       "Finalize report",
		Instruction: "Finalize report.",
	}); err != nil {
		t.Fatal(err)
	}
	markdownPath := writeRunFile(t, runDir, "report.md", "# Report\n")
	jsonPath := writeRunFile(t, runDir, "report.json", `{"broken":`)

	err := reportCommand([]string{"finalize", "--run-dir", runDir, "--markdown", markdownPath, "--json", jsonPath}, os.Stdout, os.Stderr)
	if err == nil {
		t.Fatal("expected malformed JSON error")
	}
	if !strings.Contains(err.Error(), "report JSON must parse") {
		t.Fatalf("unexpected error: %v", err)
	}
}
