package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinalizePromptIncludesRequiredReportCommands(t *testing.T) {
	runDir := newLedgerTestRun(t)
	prompt, err := finalizePrompt(runDir, "/workspace", Config{
		Instructions: InstructionConfig{Scope: "Security and correctness only."},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"# makenomistakes Finalize",
		"Ledger path: " + filepath.ToSlash(filepath.Join(runDir, "events.jsonl")),
		"Security and correctness only.",
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
{"retried":true}
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
	if !strings.Contains(err.Error(), "not valid JSON") {
		t.Fatalf("unexpected error: %v", err)
	}
}
