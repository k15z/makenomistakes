package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLedgerCommandFlow(t *testing.T) {
	runDir := newLedgerTestRun(t)

	leadBody := writeRunFile(t, runDir, "evidence/lead-auth.md", "Investigate auth boundaries.")
	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"lead", "create",
		"--run-dir", runDir,
		"--title", "Check auth boundaries",
		"--category", "authz",
		"--priority", "high",
		"--body-file", leadBody,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("lead create failed: %v\nstderr: %s", err, stderr.String())
	}
	leadID := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(leadID, "lead_") {
		t.Fatalf("expected lead id, got %q", leadID)
	}

	stdout.Reset()
	stderr.Reset()
	findingBody := writeRunFile(t, runDir, "evidence/finding-auth.md", "Candidate auth defect.")
	if err := run([]string{
		"finding", "create",
		"--run-dir", runDir,
		"--lead", leadID,
		"--title", "Missing authorization check",
		"--category", "authz",
		"--severity", "high",
		"--confidence", "medium",
		"--body-file", findingBody,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("finding create failed: %v\nstderr: %s", err, stderr.String())
	}
	findingID := strings.TrimSpace(stdout.String())
	if !strings.HasPrefix(findingID, "finding_") {
		t.Fatalf("expected finding id, got %q", findingID)
	}

	stdout.Reset()
	stderr.Reset()
	logPath := writeRunFile(t, runDir, "evidence/auth.log", "request/response evidence")
	if err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--finding", findingID,
		"--kind", "log",
		"--title", "Auth request log",
		"--path", logPath,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("evidence add failed: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.HasPrefix(strings.TrimSpace(stdout.String()), "evidence_") {
		t.Fatalf("expected evidence id, got %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if err := run([]string{
		"verdict", "record",
		"--run-dir", runDir,
		"--finding", findingID,
		"--phase", "review",
		"--value", "accepted",
		"--reason", "Evidence is specific and in scope.",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("verdict record failed: %v\nstderr: %s", err, stderr.String())
	}

	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", `{"findings":[]}`)
	stdout.Reset()
	stderr.Reset()
	if err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("report finalize failed: %v\nstderr: %s", err, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if err := run([]string{
		"task", "complete",
		"--run-dir", runDir,
		"--status", "completed",
		"--summary", "Done",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("task complete failed: %v\nstderr: %s", err, stderr.String())
	}

	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	types := eventTypes(events)
	for _, want := range []string{
		"lead.created",
		"finding.created",
		"evidence.added",
		"verdict.recorded",
		"report.finalized",
		"task.completed",
	} {
		if !contains(types, want) {
			t.Fatalf("missing event type %q in %#v", want, types)
		}
	}
}

func TestTaskCurrentPrintsCurrentTask(t *testing.T) {
	runDir := newLedgerTestRun(t)
	var stdout, stderr bytes.Buffer
	if err := run([]string{"task", "current", "--run-dir", runDir}, &stdout, &stderr); err != nil {
		t.Fatalf("task current failed: %v", err)
	}
	if !strings.Contains(stdout.String(), `"task_id": "task_recon"`) {
		t.Fatalf("unexpected task current output:\n%s", stdout.String())
	}
}

func TestTaskCurrentUsesTaskFileEnv(t *testing.T) {
	runDir := newLedgerTestRun(t)
	task := TaskRecord{
		RunID:       "run_test",
		TaskID:      "task_investigate_lead_test",
		Phase:       "investigate",
		Title:       "Investigate lead",
		Instruction: "Investigate one lead.",
	}
	taskPath := filepath.Join(runDir, "tasks", task.TaskID+".json")
	if err := writeTaskFile(taskPath, task); err != nil {
		t.Fatal(err)
	}
	t.Setenv(taskFileEnv, taskPath)

	var stdout, stderr bytes.Buffer
	if err := run([]string{"task", "current", "--run-dir", runDir}, &stdout, &stderr); err != nil {
		t.Fatalf("task current failed: %v", err)
	}
	if !strings.Contains(stdout.String(), `"task_id": "task_investigate_lead_test"`) {
		t.Fatalf("unexpected task current output:\n%s", stdout.String())
	}
}

func TestEvidenceRejectsPathOutsideRunDir(t *testing.T) {
	runDir := newLedgerTestRun(t)
	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, []byte("nope"), filePerm); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--kind", "log",
		"--title", "Outside",
		"--path", outside,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected outside path error")
	}
	if !strings.Contains(err.Error(), "inside run directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLeadCloseRequiresExistingLead(t *testing.T) {
	runDir := newLedgerTestRun(t)
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"lead", "close",
		"--run-dir", runDir,
		"--id", "lead_missing",
		"--status", "closed_no_finding",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing lead error")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLeadCloseIsIdempotentForSameStatus(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"lead", "close",
		"--run-dir", runDir,
		"--id", leadID,
		"--status", "closed_no_finding",
		"--reason", "First close.",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("first lead close failed: %v\nstderr: %s", err, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if err := run([]string{
		"lead", "close",
		"--run-dir", runDir,
		"--id", leadID,
		"--status", "closed_no_finding",
		"--reason", "Retry after checking state.",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("idempotent lead close failed: %v\nstderr: %s", err, stderr.String())
	}

	assertLeadClosedEventCount(t, runDir, leadID, 1)
	leads, err := ledgerLeads(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(leads) != 1 || leads[0].Status != "closed_no_finding" {
		t.Fatalf("unexpected lead state: %#v", leads)
	}
}

func TestLeadCloseRejectsDifferentTerminalStatus(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"lead", "close",
		"--run-dir", runDir,
		"--id", leadID,
		"--status", "closed_no_finding",
		"--reason", "First close.",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("first lead close failed: %v\nstderr: %s", err, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	err := run([]string{
		"lead", "close",
		"--run-dir", runDir,
		"--id", leadID,
		"--status", "superseded",
		"--reason", "Conflicting retry.",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected conflicting lead close to fail")
	}
	if !strings.Contains(err.Error(), "already closed with status") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertLeadClosedEventCount(t, runDir, leadID, 1)
}

func TestVerdictRejectsInvalidPhaseValue(t *testing.T) {
	runDir := newLedgerTestRun(t)
	findingBody := writeRunFile(t, runDir, "evidence/finding-auth.md", "Candidate auth defect.")
	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"finding", "create",
		"--run-dir", runDir,
		"--title", "Missing authorization check",
		"--severity", "high",
		"--confidence", "medium",
		"--body-file", findingBody,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("finding create failed: %v\nstderr: %s", err, stderr.String())
	}
	findingID := strings.TrimSpace(stdout.String())

	stdout.Reset()
	stderr.Reset()
	err := run([]string{
		"verdict", "record",
		"--run-dir", runDir,
		"--finding", findingID,
		"--phase", "review",
		"--value", "maybe",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected invalid verdict value error")
	}
	if !strings.Contains(err.Error(), "invalid review verdict value") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDeduplicateDuplicateVerdictRequiresCanonicalFinding(t *testing.T) {
	runDir := newLedgerTestRun(t)
	firstBody := writeRunFile(t, runDir, "evidence/finding-one.md", "First candidate.")
	secondBody := writeRunFile(t, runDir, "evidence/finding-two.md", "Second candidate.")
	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"finding", "create",
		"--run-dir", runDir,
		"--title", "First finding",
		"--body-file", firstBody,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("first finding create failed: %v\nstderr: %s", err, stderr.String())
	}
	firstID := strings.TrimSpace(stdout.String())

	stdout.Reset()
	stderr.Reset()
	if err := run([]string{
		"finding", "create",
		"--run-dir", runDir,
		"--title", "Second finding",
		"--body-file", secondBody,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("second finding create failed: %v\nstderr: %s", err, stderr.String())
	}
	secondID := strings.TrimSpace(stdout.String())

	stdout.Reset()
	stderr.Reset()
	err := run([]string{
		"verdict", "record",
		"--run-dir", runDir,
		"--finding", secondID,
		"--phase", "deduplicate",
		"--value", "duplicate",
		"--reason", "Same root issue.",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing canonical finding error")
	}
	if !strings.Contains(err.Error(), "require --canonical-finding") {
		t.Fatalf("unexpected error: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if err := run([]string{
		"verdict", "record",
		"--run-dir", runDir,
		"--finding", secondID,
		"--phase", "deduplicate",
		"--value", "duplicate",
		"--canonical-finding", firstID,
		"--reason", "Same root issue.",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("deduplicate verdict failed: %v\nstderr: %s", err, stderr.String())
	}
	verdicts, err := ledgerVerdicts(runDir)
	if err != nil {
		t.Fatal(err)
	}
	last := verdicts[len(verdicts)-1]
	if last.CanonicalFindingID != firstID {
		t.Fatalf("canonical finding id = %q, want %q", last.CanonicalFindingID, firstID)
	}
}

func newLedgerTestRun(t *testing.T) string {
	t.Helper()
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "evidence"), dirPerm); err != nil {
		t.Fatal(err)
	}
	task := TaskRecord{
		RunID:       "run_test",
		TaskID:      "task_recon",
		Phase:       "recon",
		Title:       "Recon",
		Instruction: "Map the workspace.",
	}
	if err := writeCurrentTaskForTest(runDir, task); err != nil {
		t.Fatal(err)
	}
	return runDir
}

func writeCurrentTaskForTest(runDir string, task TaskRecord) error {
	b, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(filepath.Join(runDir, currentTaskFile), b, filePerm)
}

func writeRunFile(t *testing.T, runDir, rel, body string) string {
	t.Helper()
	path := filepath.Join(runDir, rel)
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), filePerm); err != nil {
		t.Fatal(err)
	}
	return path
}

func createLeadForTest(t *testing.T, runDir string) string {
	t.Helper()
	leadBody := writeRunFile(t, runDir, "evidence/lead-test.md", "Investigate something.")
	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"lead", "create",
		"--run-dir", runDir,
		"--title", "Investigate something",
		"--body-file", leadBody,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("lead create failed: %v\nstderr: %s", err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

func assertLeadClosedEventCount(t *testing.T, runDir, leadID string, want int) {
	t.Helper()
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	var got int
	for _, event := range events {
		if event.Type == "lead.closed" && event.ObjectID == leadID {
			got++
		}
	}
	if got != want {
		t.Fatalf("lead.closed event count = %d, want %d", got, want)
	}
}

func eventTypes(events []LedgerEvent) []string {
	out := make([]string, 0, len(events))
	for _, event := range events {
		out = append(out, event.Type)
	}
	return out
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
