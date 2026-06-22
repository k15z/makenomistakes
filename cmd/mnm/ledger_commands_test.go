package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
	setCurrentTaskPhaseForTest(t, runDir, "investigate")
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

	if err := writeCurrentTaskForTest(runDir, TaskRecord{
		RunID:       "run_test",
		TaskID:      "task_review_" + safeFileID(findingID),
		Phase:       "review",
		Title:       "Review: Missing authorization check",
		Instruction: "Review finding.",
	}); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	reviewNotesPath := writeRunFile(t, runDir, reviewNotesRelPath(findingID), "Review accepted with specific evidence.")
	if err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--finding", findingID,
		"--kind", "markdown",
		"--title", "Review notes",
		"--path", reviewNotesPath,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("review evidence add failed: %v\nstderr: %s", err, stderr.String())
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

	reportMD := writeRunFile(t, runDir, "report.md", "# Report\n\nFinding: "+findingID+"\n")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSONFromBuckets(t, "run_test", "report.md", "report.json", map[string][]map[string]any{
		"unvalidated": {
			{
				"id":             findingID,
				"title":          "Missing authorization check",
				"category":       "authz",
				"severity":       "high",
				"confidence":     "medium",
				"source_lead_id": leadID,
				"status":         "reviewed",
				"verdicts":       []string{"review accepted"},
				"evidence_paths": []string{},
				"summary":        "Reviewed but not deduplicated or validated in this command-flow test.",
				"affected_paths": []string{},
			},
		},
	}))
	setCurrentTaskPhaseForTest(t, runDir, "finalize")
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

func TestTaskCompleteRequiresSummary(t *testing.T) {
	runDir := newLedgerTestRun(t)
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"task", "complete",
		"--run-dir", runDir,
		"--status", "completed",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing summary error")
	}
	if !strings.Contains(err.Error(), "--summary must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerTaskCompleted(runDir, "task_recon") {
		t.Fatal("task should not complete without a summary")
	}
}

func TestTaskCompleteIsIdempotentForSameStatus(t *testing.T) {
	runDir := newLedgerTestRun(t)
	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"task", "complete",
		"--run-dir", runDir,
		"--status", "completed",
		"--summary", "Recon completed",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("first task complete failed: %v\nstderr: %s", err, stderr.String())
	}
	if err := run([]string{
		"task", "complete",
		"--run-dir", runDir,
		"--status", "completed",
		"--summary", "Retried completion",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("idempotent task complete failed: %v\nstderr: %s", err, stderr.String())
	}
	assertTaskCompletedEventCount(t, runDir, "task_recon", 1)
}

func TestTaskCompleteIsAtomicForParallelSameStatus(t *testing.T) {
	runDir := newLedgerTestRun(t)

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var stdout, stderr bytes.Buffer
			errs <- run([]string{
				"task", "complete",
				"--run-dir", runDir,
				"--status", "completed",
				"--summary", "Parallel completion",
			}, &stdout, &stderr)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("parallel task complete failed: %v", err)
		}
	}
	assertTaskCompletedEventCount(t, runDir, "task_recon", 1)
}

func TestTaskCompleteRejectsConflictingStatus(t *testing.T) {
	runDir := newLedgerTestRun(t)
	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"task", "complete",
		"--run-dir", runDir,
		"--status", "failed",
		"--summary", "Recon failed",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("first task complete failed: %v\nstderr: %s", err, stderr.String())
	}
	err := run([]string{
		"task", "complete",
		"--run-dir", runDir,
		"--status", "completed",
		"--summary", "Retried as completed",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected conflicting completion status error")
	}
	if !strings.Contains(err.Error(), `task task_recon is already completed with status "failed"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	assertTaskCompletedEventCount(t, runDir, "task_recon", 1)
}

func TestReportFinalizeRejectsMalformedJSONWithoutLedgerEvent(t *testing.T) {
	runDir := newLedgerTestRun(t)
	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", `{"run_id":`)

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected malformed report JSON error")
	}
	if !strings.Contains(err.Error(), "report JSON must parse") {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("malformed report should not be finalized")
	}
}

func TestReportFinalizeRejectsWrongCurrentTaskPhase(t *testing.T) {
	runDir := newLedgerTestRun(t)
	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", nil))

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected wrong task phase error")
	}
	if !strings.Contains(err.Error(), `current task phase "recon" cannot run report finalize`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report should not be finalized from the wrong phase")
	}
}

func TestReportFinalizeRequiresExpectedBuckets(t *testing.T) {
	runDir := newLedgerTestRun(t)
	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", `{
		"run_id": "run_test",
		"counts": {},
		"report_paths": {"markdown": "report.md", "json": "report.json"},
		"proven": []
	}`)

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing bucket error")
	}
	if !strings.Contains(err.Error(), `missing "findings_proven" integer`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("incomplete report should not be finalized")
	}
}

func TestReportFinalizeRejectsAbsoluteReportPaths(t *testing.T) {
	runDir := newLedgerTestRun(t)
	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "/tmp/mnm-output/report.md", "/tmp/mnm-output/report.json", nil))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected absolute report path error")
	}
	if !strings.Contains(err.Error(), `report_paths.markdown = "/tmp/mnm-output/report.md", want "report.md"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with absolute report_paths should not be finalized")
	}
}

func TestReportFinalizeRejectsNullScalarFields(t *testing.T) {
	tests := []struct {
		name       string
		reportJSON string
		want       string
	}{
		{
			name: "null run_id",
			reportJSON: `{
				"run_id": null,
				"counts": {
					"findings_proven": 0,
					"findings_inconclusive": 0,
					"findings_failed": 0,
					"findings_rejected": 0,
					"findings_duplicate": 0,
					"findings_unvalidated": 0
				},
				"report_paths": {"markdown": "report.md", "json": "report.json"},
				"proven": [],
				"inconclusive": [],
				"failed": [],
				"rejected": [],
				"duplicate": [],
				"unvalidated": []
			}`,
			want: `field "run_id" must be a string`,
		},
		{
			name: "null count",
			reportJSON: `{
				"run_id": "run_test",
				"counts": {
					"findings_proven": null,
					"findings_inconclusive": 0,
					"findings_failed": 0,
					"findings_rejected": 0,
					"findings_duplicate": 0,
					"findings_unvalidated": 0
				},
				"report_paths": {"markdown": "report.md", "json": "report.json"},
				"proven": [],
				"inconclusive": [],
				"failed": [],
				"rejected": [],
				"duplicate": [],
				"unvalidated": []
			}`,
			want: `field "findings_proven" must be an integer`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runDir := newLedgerTestRun(t)
			reportMD := writeRunFile(t, runDir, "report.md", "# Report")
			reportJSON := writeRunFile(t, runDir, "report.json", tt.reportJSON)

			setCurrentTaskPhaseForTest(t, runDir, "finalize")
			var stdout, stderr bytes.Buffer
			err := run([]string{
				"report", "finalize",
				"--run-dir", runDir,
				"--markdown", reportMD,
				"--json", reportJSON,
			}, &stdout, &stderr)
			if err == nil {
				t.Fatal("expected null scalar field error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("unexpected error: %v", err)
			}
			if ledgerReportFinalized(runDir) {
				t.Fatal("report with null scalar field should not be finalized")
			}
		})
	}
}

func TestReportFinalizeIsIdempotentForSamePaths(t *testing.T) {
	runDir := newLedgerTestRun(t)
	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", nil))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("first report finalize failed: %v\nstderr: %s", err, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("idempotent report finalize failed: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "report finalized") {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
	assertReportFinalizedEventCount(t, runDir, "task_finalize", 1)
}

func TestReportFinalizeIsAtomicForParallelSamePaths(t *testing.T) {
	runDir := newLedgerTestRun(t)
	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", nil))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var stdout, stderr bytes.Buffer
			errs <- run([]string{
				"report", "finalize",
				"--run-dir", runDir,
				"--markdown", reportMD,
				"--json", reportJSON,
			}, &stdout, &stderr)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("parallel report finalize failed: %v", err)
		}
	}
	assertReportFinalizedEventCount(t, runDir, "task_finalize", 1)
}

func TestReportFinalizeRejectsConflictingPathsForSameTask(t *testing.T) {
	runDir := newLedgerTestRun(t)
	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", nil))
	altReportMD := writeRunFile(t, runDir, "alt-report.md", "# Alternate Report")
	altReportJSON := writeRunFile(t, runDir, "alt-report.json", validReportJSON(t, "run_test", "alt-report.md", "alt-report.json", nil))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("first report finalize failed: %v\nstderr: %s", err, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", altReportMD,
		"--json", altReportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected conflicting finalized report path error")
	}
	if !strings.Contains(err.Error(), "already finalized report") || !strings.Contains(err.Error(), "different paths") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertReportFinalizedEventCount(t, runDir, "task_finalize", 1)
}

func TestReportFinalizeValidatesFindingItems(t *testing.T) {
	runDir := newLedgerTestRun(t)
	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             "finding_missing",
			"title":          "Missing auth",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": "",
			"status":         "validation_proven",
			"verdicts":       []string{"proven"},
			"evidence_paths": []string{},
			"summary":        "No ledger item backs this report item.",
			"affected_paths": []string{},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected unknown report item id error")
	}
	if !strings.Contains(err.Error(), "does not reference a known finding") {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("untraceable report item should not be finalized")
	}
}

func TestReportFinalizeRejectsEmptyMarkdown(t *testing.T) {
	runDir := newLedgerTestRun(t)
	reportMD := writeRunFile(t, runDir, "report.md", " \n\t")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", nil))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected empty markdown error")
	}
	if !strings.Contains(err.Error(), "markdown report must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("empty markdown report should not be finalized")
	}
}

func TestReportFinalizeRequiresMatchingSourceLead(t *testing.T) {
	runDir := newLedgerTestRun(t)
	sourceLeadID := createLeadForTest(t, runDir)
	otherLeadBody := writeRunFile(t, runDir, "evidence/lead-other.md", "Investigate something else.")
	var createStdout, createStderr bytes.Buffer
	if err := run([]string{
		"lead", "create",
		"--run-dir", runDir,
		"--title", "Investigate something else",
		"--body-file", otherLeadBody,
	}, &createStdout, &createStderr); err != nil {
		t.Fatalf("other lead create failed: %v\nstderr: %s", err, createStderr.String())
	}
	otherLeadID := strings.TrimSpace(createStdout.String())
	findingID := createFindingForTest(t, runDir, sourceLeadID)
	proofRel := addValidationProofForFindingForTest(t, runDir, findingID)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report\n\nFinding: "+findingID+"\nEvidence: "+proofRel+"\n")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": otherLeadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "Traceable finding with the wrong source lead.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected source lead mismatch error")
	}
	if !strings.Contains(err.Error(), "source_lead_id") || !strings.Contains(err.Error(), sourceLeadID) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with mismatched source lead should not be finalized")
	}
}

func TestReportFinalizeRequiresEvidencePathsToBeFiles(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")
	proofDir := filepath.Join(runDir, "evidence", "proof-dir")
	if err := os.MkdirAll(proofDir, dirPerm); err != nil {
		t.Fatal(err)
	}
	proofRel := filepath.ToSlash(filepath.Join("evidence", "proof-dir"))
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_proof_dir",
		TaskID:   "task_validate",
		Data: map[string]any{
			"kind":       "directory",
			"title":      "Directory proof",
			"path":       proofRel,
			"finding_id": findingID,
		},
	}); err != nil {
		t.Fatal(err)
	}

	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "Directory paths are not durable evidence files.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected directory evidence path error")
	}
	if !strings.Contains(err.Error(), "path must be a regular file") {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with directory evidence path should not be finalized")
	}
}

func TestReportFinalizeAcceptsTraceableFindingItems(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingBody := writeRunFile(t, runDir, "evidence/finding-report.md", "Candidate finding.")
	setCurrentTaskPhaseForTest(t, runDir, "investigate")
	var stdout, stderr bytes.Buffer
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
	proofRel := addValidationProofForFindingForTest(t, runDir, findingID)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report\n\nFinding: "+findingID+"\nEvidence: "+proofRel+"\n")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "Traceable finding.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
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
	if !ledgerReportFinalized(runDir) {
		t.Fatal("expected traceable report to be finalized")
	}
}

func TestReportFinalizeRejectsProvenFindingWithoutValidationProofCitation(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	oldProofPath := writeRunFile(t, runDir, "evidence/investigation-proof.log", "investigation proof")
	oldProofRel := addEvidenceForFindingForTest(t, runDir, findingID, oldProofPath)
	addValidationProofForFindingForTest(t, runDir, findingID)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report\n\nFinding: "+findingID+"\nEvidence: "+oldProofRel+"\n")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
			"evidence_paths": []string{oldProofRel},
			"summary":        "Proven finding that only cites pre-validation evidence.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing validation proof citation error")
	}
	if !strings.Contains(err.Error(), "evidence_paths must include at least one validation proof artifact for a proven finding") {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report without validation proof citation should not be finalized")
	}
}

func TestReportFinalizeRequiresValidationBlockersForInconclusiveFinding(t *testing.T) {
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

	item := map[string]any{
		"id":             findingID,
		"title":          "Missing authorization check",
		"category":       "authz",
		"severity":       "high",
		"confidence":     "medium",
		"source_lead_id": leadID,
		"status":         "validation_inconclusive",
		"verdicts":       []string{"review accepted", "deduplicate canonical", "validation inconclusive"},
		"evidence_paths": []string{},
		"summary":        "Validation remained plausible but blocked by setup.",
		"affected_paths": []string{},
	}
	reportMD := writeRunFile(t, runDir, "report.md", "# Report\n\nFinding: "+findingID+"\n")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSONFromBuckets(t, "run_test", "report.md", "report.json", map[string][]map[string]any{
		"inconclusive": {item},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing validation blockers error")
	}
	if !strings.Contains(err.Error(), "validation_blockers") {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report without validation blockers should not be finalized")
	}

	item["validation_blockers"] = []map[string]any{{
		"summary":              "database service was unavailable",
		"missing_dependency":   "docker compose",
		"failed_command":       "go test ./cmd/mnm",
		"required_service":     "postgres",
		"suspected_config_gap": "DATABASE_URL was unset",
		"next_command":         "docker compose up -d postgres && go test ./cmd/mnm",
		"source_path":          handoffRel,
	}}
	reportMD = writeRunFile(t, runDir, "report.md", "# Report\n\nFinding: "+findingID+"\nNext: docker compose up -d postgres && go test ./cmd/mnm\n")
	reportJSON = writeRunFile(t, runDir, "report.json", validReportJSONFromBuckets(t, "run_test", "report.md", "report.json", map[string][]map[string]any{
		"inconclusive": {item},
	}))

	stdout.Reset()
	stderr.Reset()
	err = run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing markdown validation blocker coverage error")
	}
	if !strings.Contains(err.Error(), "markdown report missing validation blocker") {
		t.Fatalf("unexpected error: %v", err)
	}

	reportMD = writeRunFile(t, runDir, "report.md", "# Report\n\nFinding: "+findingID+`

Blocked validation:
- Summary: database service was unavailable
- Missing dependency: docker compose
- Failed command: go test ./cmd/mnm
- Required service: postgres
- Suspected config gap: DATABASE_URL was unset
- Next command: docker compose up -d postgres && go test ./cmd/mnm
`)
	reportJSON = writeRunFile(t, runDir, "report.json", validReportJSONFromBuckets(t, "run_test", "report.md", "report.json", map[string][]map[string]any{
		"inconclusive": {item},
	}))

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
}

func TestReportFinalizeRejectsInconclusiveFindingWithoutValidationBlockers(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "inconclusive", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report\n\nFinding: "+findingID+"\n")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSONFromBuckets(t, "run_test", "report.md", "report.json", map[string][]map[string]any{
		"inconclusive": {{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_inconclusive",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation inconclusive"},
			"evidence_paths": []string{},
			"summary":        "Validation was recorded as inconclusive without a blocker handoff.",
			"affected_paths": []string{},
		}},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected inconclusive validation without blocker error")
	}
	if !strings.Contains(err.Error(), "must include at least one blocker") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReportFinalizeRejectsUnexpectedValidationBlockers(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)

	reportMD := writeRunFile(t, runDir, "report.md", "# Report\n\nFinding: "+findingID+"\n")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSONFromBuckets(t, "run_test", "report.md", "report.json", map[string][]map[string]any{
		"unvalidated": {{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "candidate",
			"verdicts":       []string{},
			"evidence_paths": []string{},
			"summary":        "Candidate finding.",
			"affected_paths": []string{},
			"validation_blockers": []map[string]any{{
				"summary":              "fabricated blocker",
				"missing_dependency":   "",
				"failed_command":       "",
				"required_service":     "postgres",
				"suspected_config_gap": "",
				"next_command":         "docker compose up -d postgres",
				"source_path":          "evidence/handoff-validate-finding.json",
			}},
		}},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected unexpected validation blockers error")
	}
	if !strings.Contains(err.Error(), "no validation handoff blockers were recorded") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReportFinalizeRejectsLateValidationProofCitation(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")
	proofRel := addValidationProofForFindingForTest(t, runDir, findingID)

	reportMD := writeRunFile(t, runDir, "report.md", "# Report\n\nFinding: "+findingID+"\nEvidence: "+proofRel+"\n")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "Proven finding that cites proof registered after the verdict.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected late validation proof citation error")
	}
	if !strings.Contains(err.Error(), "evidence_paths must include at least one validation proof artifact for a proven finding") {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with late validation proof citation should not be finalized")
	}
}

func TestReportFinalizeRejectsMarkdownMissingFindingID(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	proofRel := addValidationProofForFindingForTest(t, runDir, findingID)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report\n\nMissing authorization check without the ledger ID.\n")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "Traceable finding in JSON only.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected markdown coverage error")
	}
	if !strings.Contains(err.Error(), "markdown report missing finding "+findingID) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with incomplete markdown coverage should not be finalized")
	}
}

func TestReportFinalizeRejectsMarkdownMissingEvidencePath(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	proofRel := addValidationProofForFindingForTest(t, runDir, findingID)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report\n\nFinding: "+findingID+"\n")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "Traceable finding with evidence omitted from Markdown.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected markdown evidence coverage error")
	}
	if !strings.Contains(err.Error(), "markdown report missing evidence path "+proofRel) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with incomplete markdown evidence coverage should not be finalized")
	}
}

func TestReportFinalizeRejectsMarkdownEvidencePathSubstring(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	proofRel := addValidationProofForFindingForTest(t, runDir, findingID)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report\n\nFinding: "+findingID+"\nEvidence: "+proofRel+".bak\n")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "Traceable finding with a similar evidence path in Markdown.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected markdown evidence path token error")
	}
	if !strings.Contains(err.Error(), "markdown report missing evidence path "+proofRel) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with substring-only evidence path should not be finalized")
	}
}

func TestReportFinalizeRejectsInvalidAffectedPaths(t *testing.T) {
	tests := []struct {
		name          string
		affectedPath  string
		wantErrSubstr string
	}{
		{
			name:          "empty",
			affectedPath:  "",
			wantErrSubstr: "affected_paths[0] must not be empty",
		},
		{
			name:          "leading whitespace",
			affectedPath:  " server/auth.go",
			wantErrSubstr: "must not contain leading or trailing whitespace",
		},
		{
			name:          "absolute path",
			affectedPath:  "/etc/passwd",
			wantErrSubstr: "must be a relative workspace path",
		},
		{
			name:          "windows absolute path",
			affectedPath:  "C:/Windows/System32",
			wantErrSubstr: "must be a relative workspace path",
		},
		{
			name:          "parent traversal",
			affectedPath:  "../secret.txt",
			wantErrSubstr: "must be a relative workspace path",
		},
		{
			name:          "unclean path",
			affectedPath:  "server/../auth.go",
			wantErrSubstr: "want clean relative path",
		},
		{
			name:          "backslash path",
			affectedPath:  `server\auth.go`,
			wantErrSubstr: "must use slash-separated relative paths",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runDir := newLedgerTestRun(t)
			leadID := createLeadForTest(t, runDir)
			findingID := createFindingForTest(t, runDir, leadID)
			proofPath := writeRunFile(t, runDir, "evidence/proof.log", "proof")
			proofRel := addEvidenceForFindingForTest(t, runDir, findingID, proofPath)
			recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
			recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
			recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

			reportMD := writeRunFile(t, runDir, "report.md", "# Report")
			reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
				{
					"id":             findingID,
					"title":          "Missing authorization check",
					"category":       "authz",
					"severity":       "high",
					"confidence":     "medium",
					"source_lead_id": leadID,
					"status":         "validation_proven",
					"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
					"evidence_paths": []string{proofRel},
					"summary":        "Traceable finding with an invalid affected path.",
					"affected_paths": []string{test.affectedPath},
				},
			}))

			setCurrentTaskPhaseForTest(t, runDir, "finalize")
			var stdout, stderr bytes.Buffer
			err := run([]string{
				"report", "finalize",
				"--run-dir", runDir,
				"--markdown", reportMD,
				"--json", reportJSON,
			}, &stdout, &stderr)
			if err == nil {
				t.Fatal("expected invalid affected path error")
			}
			if !strings.Contains(err.Error(), test.wantErrSubstr) {
				t.Fatalf("unexpected error: %v", err)
			}
			if ledgerReportFinalized(runDir) {
				t.Fatal("report with invalid affected path should not be finalized")
			}
		})
	}
}

func TestReportFinalizeRejectsAffectedPathMissingFromWorkspaceManifest(t *testing.T) {
	runDir := newLedgerTestRun(t)
	writeAndRegisterRunnerManifestForTest(t, runDir, map[string]any{
		"run_id":          "run_test",
		"workspace":       "/workspace",
		"workspace_files": []string{"server/auth.go"},
	})

	err := finalizeReportWithAffectedPathForTest(t, runDir, "server/missing.go")
	if err == nil {
		t.Fatal("expected missing affected path error")
	}
	if !strings.Contains(err.Error(), `affected_paths[0] = "server/missing.go" was not found in the runner workspace manifest`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with affected path outside workspace manifest should not be finalized")
	}
}

func TestReportFinalizeRejectsChangedRunnerManifest(t *testing.T) {
	runDir := newLedgerTestRun(t)
	writeAndRegisterRunnerManifestForTest(t, runDir, map[string]any{
		"run_id":          "run_test",
		"workspace":       "/workspace",
		"workspace_files": []string{"server/auth.go"},
	})
	if err := writeJSON(filepath.Join(runDir, "evidence", "runner-manifest.json"), map[string]any{
		"run_id":          "run_test",
		"workspace":       "/workspace",
		"workspace_files": []string{"server/missing.go"},
	}); err != nil {
		t.Fatal(err)
	}

	err := finalizeReportWithAffectedPathForTest(t, runDir, "server/missing.go")
	if err == nil {
		t.Fatal("expected changed runner manifest error")
	}
	if !strings.Contains(err.Error(), "registered runner manifest is unusable") ||
		!strings.Contains(err.Error(), "changed after registration") {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with changed runner manifest should not be finalized")
	}
}

func TestReportFinalizeRejectsMalformedRunnerManifest(t *testing.T) {
	runDir := newLedgerTestRun(t)
	writeAndRegisterRunnerManifestForTest(t, runDir, map[string]any{
		"run_id":    "run_test",
		"workspace": "/workspace",
	})

	err := finalizeReportWithAffectedPathForTest(t, runDir, "server/auth.go")
	if err == nil {
		t.Fatal("expected malformed runner manifest error")
	}
	if !strings.Contains(err.Error(), "runner manifest workspace_files is required") {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with malformed runner manifest should not be finalized")
	}
}

func finalizeReportWithAffectedPathForTest(t *testing.T, runDir, affectedPath string) error {
	t.Helper()
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	proofRel := addValidationProofForFindingForTest(t, runDir, findingID)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report\n\nFinding: "+findingID+"\nEvidence: "+proofRel+"\n")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "Traceable finding with an invented affected path.",
			"affected_paths": []string{affectedPath},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	return run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
}

func writeAndRegisterRunnerManifestForTest(t *testing.T, runDir string, manifest map[string]any) {
	t.Helper()
	if err := writeJSON(filepath.Join(runDir, "evidence", "runner-manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := registerRunnerEvidence(runDir, "run_test", "json", "Runner lifecycle manifest", "evidence/runner-manifest.json", false); err != nil {
		t.Fatal(err)
	}
}

func TestReportFinalizeSkipsWorkspaceManifestCheckForLegacyRuns(t *testing.T) {
	runDir := newLedgerTestRun(t)

	err := finalizeReportWithAffectedPathForTest(t, runDir, "server/missing.go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ledgerReportFinalized(runDir) {
		t.Fatal("legacy report without runner manifest should still finalize")
	}
}

func TestReportFinalizeRejectsFindingMetadataMismatch(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	proofPath := writeRunFile(t, runDir, "evidence/proof.log", "proof")
	proofRel := addEvidenceForFindingForTest(t, runDir, findingID, proofPath)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "medium",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "Traceable finding with the wrong severity.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected metadata mismatch error")
	}
	if !strings.Contains(err.Error(), `proven[0].severity = "medium", want ledger value "high"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with mismatched finding metadata should not be finalized")
	}
}

func TestReportFinalizeRejectsSourceLeadMismatch(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	otherLeadBody := writeRunFile(t, runDir, "evidence/lead-other.md", "Investigate something else.")
	var createStdout, createStderr bytes.Buffer
	if err := run([]string{
		"lead", "create",
		"--run-dir", runDir,
		"--title", "Investigate something else",
		"--body-file", otherLeadBody,
	}, &createStdout, &createStderr); err != nil {
		t.Fatalf("other lead create failed: %v\nstderr: %s", err, createStderr.String())
	}
	otherLeadID := strings.TrimSpace(createStdout.String())
	findingID := createFindingForTest(t, runDir, leadID)
	proofRel := addValidationProofForFindingForTest(t, runDir, findingID)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report\n\nFinding: "+findingID+"\nEvidence: "+proofRel+"\n")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": otherLeadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "Traceable finding with the wrong source lead.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected source lead mismatch error")
	}
	if !strings.Contains(err.Error(), `source_lead_id = "`+otherLeadID+`", want ledger value "`+leadID+`"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with mismatched source lead should not be finalized")
	}
}

func TestReportFinalizeRejectsVerdictMismatch(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	proofPath := writeRunFile(t, runDir, "evidence/proof.log", "proof")
	proofRel := addEvidenceForFindingForTest(t, runDir, findingID, proofPath)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "Traceable finding with an incomplete verdict list.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected verdict mismatch error")
	}
	if !strings.Contains(err.Error(), `want ["review accepted" "deduplicate canonical" "validation proven"] from ledger`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with mismatched verdicts should not be finalized")
	}
}

func TestReportFinalizeRejectsMisbucketedFinding(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	proofPath := writeRunFile(t, runDir, "evidence/proof.log", "proof")
	proofRel := addEvidenceForFindingForTest(t, runDir, findingID, proofPath)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "failed", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation failed"},
			"evidence_paths": []string{proofRel},
			"summary":        "This should not be proven.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected misbucketed report item error")
	}
	if !strings.Contains(err.Error(), `is in bucket "proven", want "failed"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("misbucketed report item should not be finalized")
	}
}

func TestReportFinalizeRejectsUnregisteredFindingEvidence(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	proofRel := "evidence/unregistered-proof.log"
	writeRunFile(t, runDir, proofRel, "proof")
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "The proof file exists but was never registered.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected unregistered evidence error")
	}
	if !strings.Contains(err.Error(), "not registered ledger evidence") {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with unregistered evidence should not be finalized")
	}
}

func TestReportFinalizeRejectsUnnormalizedEvidencePaths(t *testing.T) {
	tests := []struct {
		name        string
		pathForJSON func(runDir, proofRel string) string
	}{
		{
			name: "absolute path",
			pathForJSON: func(runDir, proofRel string) string {
				return filepath.Join(runDir, filepath.FromSlash(proofRel))
			},
		},
		{
			name: "unclean path",
			pathForJSON: func(runDir, proofRel string) string {
				return "evidence/../" + proofRel
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runDir := newLedgerTestRun(t)
			leadID := createLeadForTest(t, runDir)
			findingID := createFindingForTest(t, runDir, leadID)
			proofPath := writeRunFile(t, runDir, "evidence/proof.log", "proof")
			proofRel := addEvidenceForFindingForTest(t, runDir, findingID, proofPath)
			recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
			recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
			recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

			reportMD := writeRunFile(t, runDir, "report.md", "# Report\n\nFinding: "+findingID+"\n")
			reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
				{
					"id":             findingID,
					"title":          "Missing authorization check",
					"category":       "authz",
					"severity":       "high",
					"confidence":     "medium",
					"source_lead_id": leadID,
					"status":         "validation_proven",
					"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
					"evidence_paths": []string{test.pathForJSON(runDir, proofRel)},
					"summary":        "Traceable finding with unstable evidence path spelling.",
					"affected_paths": []string{"server/auth.go"},
				},
			}))

			setCurrentTaskPhaseForTest(t, runDir, "finalize")
			var stdout, stderr bytes.Buffer
			err := run([]string{
				"report", "finalize",
				"--run-dir", runDir,
				"--markdown", reportMD,
				"--json", reportJSON,
			}, &stdout, &stderr)
			if err == nil {
				t.Fatal("expected unnormalized evidence path error")
			}
			if !strings.Contains(err.Error(), "want normalized registered path") {
				t.Fatalf("unexpected error: %v", err)
			}
			if ledgerReportFinalized(runDir) {
				t.Fatal("report with unnormalized evidence path should not be finalized")
			}
		})
	}
}

func TestReportFinalizeRejectsEmptyRegisteredEvidence(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	emptyRel := "evidence/empty-proof.log"
	writeRunFile(t, runDir, emptyRel, "")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_empty",
		TaskID:   "task_validate_" + safeFileID(findingID),
		Data: map[string]any{
			"kind":       "log",
			"title":      "Empty proof",
			"path":       emptyRel,
			"finding_id": findingID,
		},
	}); err != nil {
		t.Fatal(err)
	}
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
			"evidence_paths": []string{emptyRel},
			"summary":        "This cites an empty evidence artifact.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected empty registered evidence error")
	}
	if !strings.Contains(err.Error(), "unusable evidence") || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with empty evidence should not be finalized")
	}
}

func TestReportFinalizeRejectsChangedRegisteredEvidence(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	proofPath := writeRunFile(t, runDir, "evidence/proof.log", "original proof")
	proofRel := addEvidenceForFindingForTest(t, runDir, findingID, proofPath)
	if err := os.WriteFile(proofPath, []byte("changed proof"), filePerm); err != nil {
		t.Fatal(err)
	}
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "This cites evidence that changed after registration.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected changed evidence error")
	}
	if !strings.Contains(err.Error(), "changed after registration") {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with changed evidence should not be finalized")
	}
}

func TestReportFinalizeRejectsSymlinkEvidencePath(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	proofRel := filepath.ToSlash(filepath.Join("evidence", "proof-link.log"))
	proofTarget := writeRunFile(t, runDir, "evidence/proof-target.log", "proof")
	if err := os.Symlink(proofTarget, filepath.Join(runDir, filepath.FromSlash(proofRel))); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_symlink",
		TaskID:   "task_validate_" + safeFileID(findingID),
		Data: map[string]any{
			"kind":           "log",
			"title":          "Symlink proof",
			"path":           proofRel,
			"content_sha256": runFileSHA256ForTest(t, runDir, proofRel),
			"finding_id":     findingID,
		},
	}); err != nil {
		t.Fatal(err)
	}
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "This cites a symlinked evidence artifact.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected symlink evidence error")
	}
	if !strings.Contains(err.Error(), "must not contain symlinks") {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with symlink evidence should not be finalized")
	}
}

func TestValidateFinalizedReportRejectsSymlinkReportPath(t *testing.T) {
	runDir := newLedgerTestRun(t)
	realReport := writeRunFile(t, runDir, "real-report.md", "# Report")
	if err := os.Symlink(realReport, filepath.Join(runDir, "report-link.md")); err != nil {
		t.Fatal(err)
	}
	writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report-link.md", "report.json", nil))

	err := validateFinalizedReport(runDir, TaskRecord{
		RunID:  "run_test",
		TaskID: "task_finalize",
		Phase:  "finalize",
	}, ReportRecord{
		ID:           "report_symlink",
		TaskID:       "task_finalize",
		MarkdownPath: "report-link.md",
		JSONPath:     "report.json",
	})
	if err == nil {
		t.Fatal("expected symlink report validation error")
	}
	if !strings.Contains(err.Error(), "must not contain symlinks") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReportFinalizeRejectsProvenFindingWithoutEvidence(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_proven",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
			"evidence_paths": []string{},
			"summary":        "This cannot be proven without evidence.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing proven evidence error")
	}
	if !strings.Contains(err.Error(), "must include at least one registered evidence path") {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with unsubstantiated proven finding should not be finalized")
	}
}

func TestReportFinalizeRejectsStatusThatDoesNotMatchBucket(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	proofPath := writeRunFile(t, runDir, "evidence/proof.log", "proof")
	proofRel := addEvidenceForFindingForTest(t, runDir, findingID, proofPath)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", []map[string]any{
		{
			"id":             findingID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_failed",
			"verdicts":       []string{"review accepted", "deduplicate canonical", "validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "The bucket is proven, but status says failed.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected mismatched status error")
	}
	if !strings.Contains(err.Error(), `status = "validation_failed", want ledger value "validation_proven"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with mismatched status should not be finalized")
	}
}

func TestReportFinalizeAcceptsDuplicateWithCanonicalFinding(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	canonicalID := createFindingForTest(t, runDir, leadID)
	duplicateID := createFindingForTest(t, runDir, leadID)
	recordVerdictForTest(t, runDir, canonicalID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, canonicalID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, duplicateID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, duplicateID, "deduplicate", "duplicate", canonicalID)

	reportMD := writeRunFile(t, runDir, "report.md", "# Report\n\nFindings: "+canonicalID+" "+duplicateID+"\n")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSONFromBuckets(t, "run_test", "report.md", "report.json", map[string][]map[string]any{
		"unvalidated": {
			{
				"id":             canonicalID,
				"title":          "Missing authorization check",
				"category":       "authz",
				"severity":       "high",
				"confidence":     "medium",
				"source_lead_id": leadID,
				"status":         "validation_pending",
				"verdicts":       []string{"review accepted", "deduplicate canonical"},
				"evidence_paths": []string{},
				"summary":        "Canonical finding has not been validated yet.",
				"affected_paths": []string{"server/auth.go"},
			},
		},
		"duplicate": {
			{
				"id":                   duplicateID,
				"title":                "Missing authorization check",
				"category":             "authz",
				"severity":             "high",
				"confidence":           "medium",
				"source_lead_id":       leadID,
				"status":               "duplicate",
				"verdicts":             []string{"review accepted", "deduplicate duplicate"},
				"evidence_paths":       []string{},
				"summary":              "Duplicate of the canonical authorization finding.",
				"affected_paths":       []string{"server/auth.go"},
				"canonical_finding_id": canonicalID,
			},
		},
	}))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("report finalize failed: %v\nstderr: %s", err, stderr.String())
	}
	if !ledgerReportFinalized(runDir) {
		t.Fatal("expected report with duplicate canonical link to be finalized")
	}
}

func TestReportFinalizeRejectsDuplicateMissingCanonicalFinding(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	canonicalID := createFindingForTest(t, runDir, leadID)
	duplicateID := createFindingForTest(t, runDir, leadID)
	recordVerdictForTest(t, runDir, canonicalID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, canonicalID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, duplicateID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, duplicateID, "deduplicate", "duplicate", canonicalID)

	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSONFromBuckets(t, "run_test", "report.md", "report.json", duplicateReportBuckets(leadID, canonicalID, duplicateID, map[string]any{})))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing canonical finding error")
	}
	if !strings.Contains(err.Error(), `duplicate[0].missing "canonical_finding_id" string`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with missing duplicate canonical link should not be finalized")
	}
}

func TestReportFinalizeRejectsDuplicateCanonicalMismatch(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	canonicalID := createFindingForTest(t, runDir, leadID)
	otherID := createFindingForTest(t, runDir, leadID)
	duplicateID := createFindingForTest(t, runDir, leadID)
	recordVerdictForTest(t, runDir, canonicalID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, canonicalID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, otherID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, otherID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, duplicateID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, duplicateID, "deduplicate", "duplicate", canonicalID)

	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSONFromBuckets(t, "run_test", "report.md", "report.json", duplicateReportBuckets(leadID, canonicalID, duplicateID, map[string]any{
		"canonical_finding_id": otherID,
		"extra_unvalidated": []map[string]any{
			{
				"id":             otherID,
				"title":          "Missing authorization check",
				"category":       "authz",
				"severity":       "high",
				"confidence":     "medium",
				"source_lead_id": leadID,
				"status":         "validation_pending",
				"verdicts":       []string{"review accepted", "deduplicate canonical"},
				"evidence_paths": []string{},
				"summary":        "Another canonical finding.",
				"affected_paths": []string{"server/auth.go"},
			},
		},
	})))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected canonical mismatch error")
	}
	if !strings.Contains(err.Error(), `canonical_finding_id = "`+otherID+`", want ledger value "`+canonicalID+`"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with mismatched duplicate canonical link should not be finalized")
	}
}

func TestReportFinalizeRequiresEveryFinding(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")
	recordVerdictForTest(t, runDir, findingID, "validate", "failed", "")

	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", validReportJSON(t, "run_test", "report.md", "report.json", nil))

	setCurrentTaskPhaseForTest(t, runDir, "finalize")
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"report", "finalize",
		"--run-dir", runDir,
		"--markdown", reportMD,
		"--json", reportJSON,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing report finding error")
	}
	if !strings.Contains(err.Error(), "report JSON missing finding "+findingID+` from bucket "failed"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report missing a finding should not be finalized")
	}
}

func TestReportShowPrintsFinalizedMarkdown(t *testing.T) {
	workspace, runRecord := newStoredReportRun(t)

	var stdout, stderr bytes.Buffer
	if err := run([]string{"report", "show", runRecord.ID, workspace}, &stdout, &stderr); err != nil {
		t.Fatalf("report show failed: %v\nstderr: %s", err, stderr.String())
	}
	if got := stdout.String(); got != "# Final Report\n\nReal findings.\n" {
		t.Fatalf("unexpected markdown report:\n%s", got)
	}
}

func TestReportShowPrintsFinalizedJSON(t *testing.T) {
	workspace, runRecord := newStoredReportRun(t)

	var stdout, stderr bytes.Buffer
	if err := run([]string{"report", "show", "--json", runRecord.ID, workspace}, &stdout, &stderr); err != nil {
		t.Fatalf("report show failed: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"run_id":"`+runRecord.ID+`"`) {
		t.Fatalf("unexpected JSON report:\n%s", stdout.String())
	}
}

func TestReportShowRequiresFinalizedReport(t *testing.T) {
	workspace := t.TempDir()
	runRecord := testRunRecord(workspace, "run_no_report", RunStatusCompleted, nowForTest())
	if err := os.MkdirAll(runRecord.RunDir, dirPerm); err != nil {
		t.Fatal(err)
	}
	store, err := openStore(filepath.Join(workspace, ".mnm", "mnm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRun(runRecord); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err = run([]string{"report", "show", runRecord.ID, workspace}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing finalized report error")
	}
	if !strings.Contains(err.Error(), "has no finalized report") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReportShowRequiresCompletedFinalizeTask(t *testing.T) {
	workspace, runRecord := newStoredReportRun(t)
	if err := appendLedgerEvent(runRecord.RunDir, LedgerEvent{
		RunID:    runRecord.ID,
		Type:     "report.finalized",
		Object:   "report",
		ObjectID: "report_partial",
		TaskID:   "task_finalize_incomplete",
		Data: map[string]any{
			"markdown_path": "report.md",
			"json_path":     "report.json",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runRecord.RunDir, LedgerEvent{
		RunID:    runRecord.ID,
		Type:     "task.completed",
		Object:   "task",
		ObjectID: "task_finalize",
		TaskID:   "task_finalize",
		Data: map[string]any{
			"status":  "failed",
			"summary": "later finalize failed",
		},
	}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{"report", "show", runRecord.ID, workspace}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected incomplete finalized report error")
	}
	if !strings.Contains(err.Error(), "has no finalized report") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReportShowRejectsSymlinkReport(t *testing.T) {
	workspace, runRecord := newStoredReportRun(t)
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("outside secret"), filePerm); err != nil {
		t.Fatal(err)
	}
	reportPath := filepath.Join(runRecord.RunDir, "report.md")
	if err := os.Remove(reportPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, reportPath); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{"report", "show", runRecord.ID, workspace}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected symlink report error")
	}
	if !strings.Contains(err.Error(), "must not contain symlinks") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(stdout.String(), "outside secret") {
		t.Fatalf("report show leaked symlink target:\n%s", stdout.String())
	}
}

func TestReportShowRejectsSymlinkReportParent(t *testing.T) {
	workspace, runRecord := newStoredReportRun(t)
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.md")
	if err := os.WriteFile(outside, []byte("outside secret"), filePerm); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(runRecord.RunDir, "leak")); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runRecord.RunDir, LedgerEvent{
		RunID:    runRecord.ID,
		Type:     "report.finalized",
		Object:   "report",
		ObjectID: "report_symlink_parent",
		TaskID:   "task_finalize",
		Data: map[string]any{
			"markdown_path": "leak/outside.md",
			"json_path":     "report.json",
		},
	}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{"report", "show", runRecord.ID, workspace}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected symlink parent report error")
	}
	if !strings.Contains(err.Error(), "must not contain symlinks") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(stdout.String(), "outside secret") {
		t.Fatalf("report show leaked symlink target:\n%s", stdout.String())
	}
}

func TestReportShowMentionsRunnerFailure(t *testing.T) {
	workspace := t.TempDir()
	runRecord := testRunRecord(workspace, "run_failed_report", RunStatusFailed, nowForTest())
	if err := os.MkdirAll(runRecord.RunDir, dirPerm); err != nil {
		t.Fatal(err)
	}
	store, err := openStore(filepath.Join(workspace, ".mnm", "mnm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRun(runRecord); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	appendRunnerFailureForTest(t, runRecord.RunDir, runRecord.ID, "validate", "validation crashed")

	var stdout, stderr bytes.Buffer
	err = run([]string{"report", "show", runRecord.ID, workspace}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing finalized report error")
	}
	for _, want := range []string{
		"has no finalized report",
		"runner failed during validate",
		"evidence/runner-failure.json",
		"validation crashed",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("report show error missing %q: %v", want, err)
		}
	}
}

func TestReportShowIgnoresStaleRunnerFailureAfterCompletion(t *testing.T) {
	workspace := t.TempDir()
	runRecord := testRunRecord(workspace, "run_completed_without_report", RunStatusCompleted, nowForTest())
	if err := os.MkdirAll(runRecord.RunDir, dirPerm); err != nil {
		t.Fatal(err)
	}
	store, err := openStore(filepath.Join(workspace, ".mnm", "mnm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRun(runRecord); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	appendRunnerFailureForTest(t, runRecord.RunDir, runRecord.ID, "validate", "validation crashed")
	if err := appendLedgerEvent(runRecord.RunDir, LedgerEvent{
		RunID:    runRecord.ID,
		Type:     "runner.completed",
		Object:   "run",
		ObjectID: runRecord.ID,
		Data: map[string]any{
			"workspace": "/tmp/workspace",
		},
	}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err = run([]string{"report", "show", runRecord.ID, workspace}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing finalized report error")
	}
	if !strings.Contains(err.Error(), "has no finalized report") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "validation crashed") {
		t.Fatalf("report show error includes stale runner failure: %v", err)
	}
}

func TestReportShowIgnoresStaleRunnerFailureAfterStopAfter(t *testing.T) {
	workspace := t.TempDir()
	runRecord := testRunRecord(workspace, "run_stopped_without_report", RunStatusStopped, nowForTest())
	if err := os.MkdirAll(runRecord.RunDir, dirPerm); err != nil {
		t.Fatal(err)
	}
	store, err := openStore(filepath.Join(workspace, ".mnm", "mnm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRun(runRecord); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	appendRunnerFailureForTest(t, runRecord.RunDir, runRecord.ID, "validate", "validation crashed")
	appendRunnerStoppedForTest(t, runRecord.RunDir, runRecord.ID, "recon")

	var stdout, stderr bytes.Buffer
	err = run([]string{"report", "show", runRecord.ID, workspace}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing finalized report error")
	}
	if !strings.Contains(err.Error(), "has no finalized report") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "validation crashed") {
		t.Fatalf("report show error includes stale runner failure: %v", err)
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

func TestEvidenceRejectsSymlinkEscape(t *testing.T) {
	runDir := newLedgerTestRun(t)
	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, []byte("nope"), filePerm); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(runDir, "evidence", "outside-link.log")
	if err := os.Symlink(outside, linkPath); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--kind", "log",
		"--title", "Outside link",
		"--path", linkPath,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected symlink escape error")
	}
	if !strings.Contains(err.Error(), "inside run directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEvidenceRejectsEmptyFile(t *testing.T) {
	runDir := newLedgerTestRun(t)
	empty := writeRunFile(t, runDir, "evidence/empty.log", "")

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--kind", "log",
		"--title", "Empty proof",
		"--path", empty,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected empty evidence error")
	}
	if !strings.Contains(err.Error(), "evidence file evidence/empty.log must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}

	blank := writeRunFile(t, runDir, "evidence/blank.log", " \n\t ")
	stdout.Reset()
	stderr.Reset()
	err = run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--kind", "log",
		"--title", "Blank proof",
		"--path", blank,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected blank evidence error")
	}
	if !strings.Contains(err.Error(), "evidence file evidence/blank.log must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEvidenceRejectsLeadAndFindingOwnerTogether(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	proof := writeRunFile(t, runDir, "evidence/proof.log", "proof")

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--lead", leadID,
		"--finding", findingID,
		"--kind", "log",
		"--title", "Ambiguous proof",
		"--path", proof,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected ambiguous evidence owner error")
	}
	if !strings.Contains(err.Error(), "at most one of --lead or --finding") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEvidenceAddRejectsLeadOwnerOutsideInvestigate(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	proof := writeRunFile(t, runDir, "evidence/lead-proof.log", "proof")
	setCurrentTaskPhaseForTest(t, runDir, "review")

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--lead", leadID,
		"--kind", "log",
		"--title", "Lead proof",
		"--path", proof,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected lead evidence phase error")
	}
	if !strings.Contains(err.Error(), `current task phase "review" cannot run evidence add --lead`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEvidenceAddRejectsFindingOwnerOutsideFindingPhases(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	proof := writeRunFile(t, runDir, "evidence/finding-proof.log", "proof")
	setCurrentTaskPhaseForTest(t, runDir, "deduplicate")

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--finding", findingID,
		"--kind", "log",
		"--title", "Finding proof",
		"--path", proof,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected finding evidence phase error")
	}
	if !strings.Contains(err.Error(), `current task phase "deduplicate" cannot run evidence add --finding`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEvidenceAddRejectsUnownedEvidenceOutsideReconOrDeduplicate(t *testing.T) {
	runDir := newLedgerTestRun(t)
	proof := writeRunFile(t, runDir, "evidence/unowned-proof.log", "proof")
	setCurrentTaskPhaseForTest(t, runDir, "validate")

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--kind", "log",
		"--title", "Unowned proof",
		"--path", proof,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected unowned evidence phase error")
	}
	if !strings.Contains(err.Error(), `current task phase "validate" cannot run evidence add`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEvidenceAddIsIdempotentForSameMetadata(t *testing.T) {
	runDir := newLedgerTestRun(t)
	proof := writeRunFile(t, runDir, "evidence/proof.log", "proof")

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--kind", "log",
		"--title", "Proof",
		"--path", proof,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("first evidence add failed: %v\nstderr: %s", err, stderr.String())
	}
	firstID := strings.TrimSpace(stdout.String())

	stdout.Reset()
	stderr.Reset()
	if err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--kind", "log",
		"--title", "Proof",
		"--path", proof,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("idempotent evidence add failed: %v\nstderr: %s", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != firstID {
		t.Fatalf("idempotent evidence id = %q, want %q", got, firstID)
	}
	assertEvidenceEventCount(t, runDir, "task_recon", "evidence/proof.log", 1)
}

func TestEvidenceAddIsAtomicForParallelSameMetadata(t *testing.T) {
	runDir := newLedgerTestRun(t)
	proof := writeRunFile(t, runDir, "evidence/proof.log", "proof")

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var stdout, stderr bytes.Buffer
			errs <- run([]string{
				"evidence", "add",
				"--run-dir", runDir,
				"--kind", "log",
				"--title", "Proof",
				"--path", proof,
			}, &stdout, &stderr)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("parallel evidence add failed: %v", err)
		}
	}
	assertEvidenceEventCount(t, runDir, "task_recon", "evidence/proof.log", 1)
}

func TestEvidenceAddRejectsConflictingMetadataForSameTaskPath(t *testing.T) {
	runDir := newLedgerTestRun(t)
	proof := writeRunFile(t, runDir, "evidence/proof.log", "proof")

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--kind", "log",
		"--title", "Proof",
		"--path", proof,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("first evidence add failed: %v\nstderr: %s", err, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--kind", "markdown",
		"--title", "Different proof",
		"--path", proof,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected conflicting evidence metadata error")
	}
	if !strings.Contains(err.Error(), "already registered evidence path evidence/proof.log with different metadata or content") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEvidenceEventCount(t, runDir, "task_recon", "evidence/proof.log", 1)
}

func TestEvidenceAddRejectsChangedContentForSameTaskPath(t *testing.T) {
	runDir := newLedgerTestRun(t)
	proof := writeRunFile(t, runDir, "evidence/proof.log", "proof")

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--kind", "log",
		"--title", "Proof",
		"--path", proof,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("first evidence add failed: %v\nstderr: %s", err, stderr.String())
	}
	if err := os.WriteFile(proof, []byte("changed proof"), filePerm); err != nil {
		t.Fatal(err)
	}

	stdout.Reset()
	stderr.Reset()
	err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--kind", "log",
		"--title", "Proof",
		"--path", proof,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected changed evidence content error")
	}
	if !strings.Contains(err.Error(), "evidence path evidence/proof.log is already registered with different content") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEvidenceEventCount(t, runDir, "task_recon", "evidence/proof.log", 1)
}

func TestEvidenceAddUsesLatestExistingTaskPathRegistration(t *testing.T) {
	runDir := newLedgerTestRun(t)
	proof := writeRunFile(t, runDir, "evidence/proof.log", "proof")
	proofRel := "evidence/proof.log"
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_old",
		TaskID:   "task_recon",
		Data: map[string]any{
			"kind":           "markdown",
			"title":          "Old proof",
			"path":           proofRel,
			"lead_id":        "",
			"finding_id":     "",
			"content_sha256": runFileSHA256ForTest(t, runDir, proofRel),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_latest",
		TaskID:   "task_recon",
		Data: map[string]any{
			"kind":           "log",
			"title":          "Proof",
			"path":           proofRel,
			"lead_id":        "",
			"finding_id":     "",
			"content_sha256": runFileSHA256ForTest(t, runDir, proofRel),
		},
	}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--kind", "log",
		"--title", "Proof",
		"--path", proof,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("idempotent evidence add failed: %v\nstderr: %s", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "evidence_latest" {
		t.Fatalf("idempotent evidence id = %q, want latest existing event", got)
	}
	assertEvidenceEventCount(t, runDir, "task_recon", proofRel, 2)
}

func TestEvidenceAddAllowsSamePathForLeadThenFinding(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	proof := writeRunFile(t, runDir, "evidence/shared-proof.log", "proof")

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--lead", leadID,
		"--kind", "log",
		"--title", "Lead proof",
		"--path", proof,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("lead evidence add failed: %v\nstderr: %s", err, stderr.String())
	}
	leadEvidenceID := strings.TrimSpace(stdout.String())

	stdout.Reset()
	stderr.Reset()
	if err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--finding", findingID,
		"--kind", "log",
		"--title", "Finding proof",
		"--path", proof,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("finding evidence add failed: %v\nstderr: %s", err, stderr.String())
	}
	findingEvidenceID := strings.TrimSpace(stdout.String())
	if findingEvidenceID == leadEvidenceID {
		t.Fatalf("finding evidence reused lead evidence id %q", findingEvidenceID)
	}

	if _, ok := ledgerLeadTaskEvidence(runDir, leadID, "task_investigate", "evidence/shared-proof.log"); !ok {
		t.Fatal("expected proof evidence associated with lead")
	}
	if !ledgerFindingHasTaskEvidencePath(runDir, findingID, "task_investigate", "evidence/shared-proof.log") {
		t.Fatal("expected proof evidence associated with finding")
	}
	assertEvidenceEventCount(t, runDir, "task_investigate", "evidence/shared-proof.log", 2)
}

func TestRegisterTaskEvidenceIsIdempotentForSameMetadata(t *testing.T) {
	runDir := newLedgerTestRun(t)
	writeRunFile(t, runDir, "evidence/recon-prompt.md", "prompt")
	registration := taskEvidenceRegistration{
		RunID:  "run_test",
		TaskID: "task_recon",
		Kind:   "markdown",
		Title:  "Recon prompt",
		Path:   "evidence/recon-prompt.md",
	}

	firstID, err := registerTaskEvidence(runDir, registration)
	if err != nil {
		t.Fatalf("first evidence registration failed: %v", err)
	}
	secondID, err := registerTaskEvidence(runDir, registration)
	if err != nil {
		t.Fatalf("idempotent evidence registration failed: %v", err)
	}
	if secondID != firstID {
		t.Fatalf("idempotent evidence id = %q, want %q", secondID, firstID)
	}
	assertEvidenceEventCount(t, runDir, "task_recon", "evidence/recon-prompt.md", 1)
}

func TestRegisterTaskEvidenceAllowsChangedContentForMutablePrompt(t *testing.T) {
	runDir := newLedgerTestRun(t)
	writeRunFile(t, runDir, "evidence/recon-prompt.md", "prompt v1")
	registration := taskEvidenceRegistration{
		RunID:              "run_test",
		TaskID:             "task_recon",
		Kind:               "markdown",
		Title:              "Recon prompt",
		Path:               "evidence/recon-prompt.md",
		AllowContentChange: true,
	}

	firstID, err := registerTaskEvidence(runDir, registration)
	if err != nil {
		t.Fatalf("first evidence registration failed: %v", err)
	}
	writeRunFile(t, runDir, "evidence/recon-prompt.md", "prompt v2")
	secondID, err := registerTaskEvidence(runDir, registration)
	if err != nil {
		t.Fatalf("mutable prompt evidence registration failed: %v", err)
	}
	if secondID != firstID {
		t.Fatalf("mutable prompt evidence id = %q, want %q", secondID, firstID)
	}
	assertEvidenceEventCount(t, runDir, "task_recon", "evidence/recon-prompt.md", 1)
}

func TestRegisterTaskEvidenceRejectsConflictingMetadata(t *testing.T) {
	runDir := newLedgerTestRun(t)
	writeRunFile(t, runDir, "evidence/recon-prompt.md", "prompt")
	registration := taskEvidenceRegistration{
		RunID:  "run_test",
		TaskID: "task_recon",
		Kind:   "markdown",
		Title:  "Recon prompt",
		Path:   "evidence/recon-prompt.md",
	}

	if _, err := registerTaskEvidence(runDir, registration); err != nil {
		t.Fatalf("first evidence registration failed: %v", err)
	}
	registration.Title = "Different prompt"
	_, err := registerTaskEvidence(runDir, registration)
	if err == nil {
		t.Fatal("expected conflicting evidence registration error")
	}
	if !strings.Contains(err.Error(), "already registered evidence path evidence/recon-prompt.md with different metadata or content") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertEvidenceEventCount(t, runDir, "task_recon", "evidence/recon-prompt.md", 1)
}

func TestLeadCreateRejectsBlankCategoryAndEmptyBody(t *testing.T) {
	runDir := newLedgerTestRun(t)
	body := writeRunFile(t, runDir, "evidence/lead-auth.md", "Investigate auth.")

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"lead", "create",
		"--run-dir", runDir,
		"--title", "Investigate auth",
		"--category", "   ",
		"--body-file", body,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected blank category error")
	}
	if !strings.Contains(err.Error(), "--category must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}

	emptyBody := writeRunFile(t, runDir, "evidence/lead-empty.md", "")
	stdout.Reset()
	stderr.Reset()
	err = run([]string{
		"lead", "create",
		"--run-dir", runDir,
		"--title", "Investigate auth",
		"--category", "authz",
		"--body-file", emptyBody,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected empty lead body error")
	}
	if !strings.Contains(err.Error(), "lead body file evidence/lead-empty.md must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}

	blankBody := writeRunFile(t, runDir, "evidence/lead-blank.md", " \n\t ")
	stdout.Reset()
	stderr.Reset()
	err = run([]string{
		"lead", "create",
		"--run-dir", runDir,
		"--title", "Investigate auth",
		"--category", "authz",
		"--body-file", blankBody,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected blank lead body error")
	}
	if !strings.Contains(err.Error(), "lead body file evidence/lead-blank.md must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLeadCreateIsIdempotentForSameMetadata(t *testing.T) {
	runDir := newLedgerTestRun(t)
	body := writeRunFile(t, runDir, "evidence/lead-auth.md", "Investigate auth.")

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"lead", "create",
		"--run-dir", runDir,
		"--title", "Investigate auth",
		"--category", "authz",
		"--priority", "high",
		"--body-file", body,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("first lead create failed: %v\nstderr: %s", err, stderr.String())
	}
	firstID := strings.TrimSpace(stdout.String())

	stdout.Reset()
	stderr.Reset()
	if err := run([]string{
		"lead", "create",
		"--run-dir", runDir,
		"--title", "Investigate auth",
		"--category", "authz",
		"--priority", "high",
		"--body-file", body,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("idempotent lead create failed: %v\nstderr: %s", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != firstID {
		t.Fatalf("idempotent lead id = %q, want %q", got, firstID)
	}
	assertLeadCreatedEventCount(t, runDir, "task_recon", "evidence/lead-auth.md", 1)
}

func TestLeadCreateIsAtomicForParallelSameMetadata(t *testing.T) {
	runDir := newLedgerTestRun(t)
	body := writeRunFile(t, runDir, "evidence/lead-auth.md", "Investigate auth.")

	const workers = 8
	start := make(chan struct{})
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var stdout, stderr bytes.Buffer
			err := run([]string{
				"lead", "create",
				"--run-dir", runDir,
				"--title", "Investigate auth",
				"--category", "authz",
				"--priority", "high",
				"--body-file", body,
			}, &stdout, &stderr)
			if err != nil {
				errs <- fmt.Errorf("%w: %s", err, stderr.String())
				return
			}
			ids <- strings.TrimSpace(stdout.String())
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("parallel lead create failed: %v", err)
		}
	}
	firstID := ""
	for id := range ids {
		if firstID == "" {
			firstID = id
			continue
		}
		if id != firstID {
			t.Fatalf("parallel lead create returned id %q, want %q", id, firstID)
		}
	}
	if firstID == "" {
		t.Fatal("expected at least one lead id")
	}
	assertLeadCreatedEventCount(t, runDir, "task_recon", "evidence/lead-auth.md", 1)
}

func TestLeadCreateRejectsConflictingMetadataForSameTaskBodyPath(t *testing.T) {
	runDir := newLedgerTestRun(t)
	body := writeRunFile(t, runDir, "evidence/lead-auth.md", "Investigate auth.")

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"lead", "create",
		"--run-dir", runDir,
		"--title", "Investigate auth",
		"--category", "authz",
		"--priority", "high",
		"--body-file", body,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("first lead create failed: %v\nstderr: %s", err, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	err := run([]string{
		"lead", "create",
		"--run-dir", runDir,
		"--title", "Investigate auth deeply",
		"--category", "authz",
		"--priority", "high",
		"--body-file", body,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected conflicting lead metadata error")
	}
	if !strings.Contains(err.Error(), "already created lead from body path evidence/lead-auth.md with different metadata") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertLeadCreatedEventCount(t, runDir, "task_recon", "evidence/lead-auth.md", 1)
}

func TestFindingCreateRejectsBlankCategoryAndEmptyBody(t *testing.T) {
	runDir := newLedgerTestRun(t)
	body := writeRunFile(t, runDir, "evidence/finding-auth.md", "Candidate finding.")

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"finding", "create",
		"--run-dir", runDir,
		"--title", "Missing authorization check",
		"--category", "   ",
		"--body-file", body,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected blank category error")
	}
	if !strings.Contains(err.Error(), "--category must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}

	setCurrentTaskPhaseForTest(t, runDir, "investigate")
	emptyBody := writeRunFile(t, runDir, "evidence/finding-empty.md", "")
	stdout.Reset()
	stderr.Reset()
	err = run([]string{
		"finding", "create",
		"--run-dir", runDir,
		"--title", "Missing authorization check",
		"--category", "authz",
		"--body-file", emptyBody,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected empty finding body error")
	}
	if !strings.Contains(err.Error(), "finding body file evidence/finding-empty.md must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}

	blankBody := writeRunFile(t, runDir, "evidence/finding-blank.md", " \n\t ")
	stdout.Reset()
	stderr.Reset()
	err = run([]string{
		"finding", "create",
		"--run-dir", runDir,
		"--title", "Missing authorization check",
		"--category", "authz",
		"--body-file", blankBody,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected blank finding body error")
	}
	if !strings.Contains(err.Error(), "finding body file evidence/finding-blank.md must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLeadCreateRejectsWrongCurrentTaskPhase(t *testing.T) {
	runDir := newLedgerTestRun(t)
	setCurrentTaskPhaseForTest(t, runDir, "review")
	body := writeRunFile(t, runDir, "evidence/lead-review.md", "Review should not create leads.")

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"lead", "create",
		"--run-dir", runDir,
		"--title", "Review-created lead",
		"--body-file", body,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected wrong task phase error")
	}
	if !strings.Contains(err.Error(), `current task phase "review" cannot run lead create`) {
		t.Fatalf("unexpected error: %v", err)
	}
	assertLeadCreatedEventCount(t, runDir, "task_review", "evidence/lead-review.md", 0)
}

func TestFindingCreateRejectsWrongCurrentTaskPhase(t *testing.T) {
	runDir := newLedgerTestRun(t)
	body := writeRunFile(t, runDir, "evidence/finding-auth.md", "Candidate finding.")

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"finding", "create",
		"--run-dir", runDir,
		"--title", "Missing authorization check",
		"--body-file", body,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected wrong task phase error")
	}
	if !strings.Contains(err.Error(), `current task phase "recon" cannot run finding create`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLeadCloseRejectsWrongCurrentTaskPhase(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"lead", "close",
		"--run-dir", runDir,
		"--id", leadID,
		"--status", "closed_no_finding",
		"--reason", "Review should not close leads.",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected wrong task phase error")
	}
	if !strings.Contains(err.Error(), `current task phase "recon" cannot run lead close`) {
		t.Fatalf("unexpected error: %v", err)
	}
	assertLeadClosedEventCount(t, runDir, leadID, 0)
}

func TestLeadCloseRequiresExistingLead(t *testing.T) {
	runDir := newLedgerTestRun(t)
	setCurrentTaskPhaseForTest(t, runDir, "investigate")
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
	setCurrentTaskPhaseForTest(t, runDir, "investigate")

	var stdout, stderr bytes.Buffer
	if err := run(append([]string{
		"lead", "close",
		"--run-dir", runDir,
		"--id", leadID,
		"--status", "closed_no_finding",
		"--reason", "First close.",
	}, negativeProofCLIArgs()...), &stdout, &stderr); err != nil {
		t.Fatalf("first lead close failed: %v\nstderr: %s", err, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if err := run(append([]string{
		"lead", "close",
		"--run-dir", runDir,
		"--id", leadID,
		"--status", "closed_no_finding",
		"--reason", "Retry after checking state.",
	}, negativeProofCLIArgs()...), &stdout, &stderr); err != nil {
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

func TestLeadCloseIsAtomicForParallelSameStatus(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	setCurrentTaskPhaseForTest(t, runDir, "investigate")
	start := make(chan struct{})
	errs := make(chan error, 20)
	var wg sync.WaitGroup
	for i := 0; i < cap(errs); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var stdout, stderr bytes.Buffer
			errs <- run(append([]string{
				"lead", "close",
				"--run-dir", runDir,
				"--id", leadID,
				"--status", "closed_no_finding",
				"--reason", "Parallel close.",
			}, negativeProofCLIArgs()...), &stdout, &stderr)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("parallel lead close failed: %v", err)
		}
	}
	assertLeadClosedEventCount(t, runDir, leadID, 1)
}

func TestLeadCloseRejectsDifferentTerminalStatus(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	setCurrentTaskPhaseForTest(t, runDir, "investigate")

	var stdout, stderr bytes.Buffer
	if err := run(append([]string{
		"lead", "close",
		"--run-dir", runDir,
		"--id", leadID,
		"--status", "closed_no_finding",
		"--reason", "First close.",
	}, negativeProofCLIArgs()...), &stdout, &stderr); err != nil {
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

func TestLeadCloseRequiresReasonForIdempotentClose(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	setCurrentTaskPhaseForTest(t, runDir, "investigate")

	var stdout, stderr bytes.Buffer
	if err := run(append([]string{
		"lead", "close",
		"--run-dir", runDir,
		"--id", leadID,
		"--status", "closed_no_finding",
		"--reason", "First close.",
	}, negativeProofCLIArgs()...), &stdout, &stderr); err != nil {
		t.Fatalf("first lead close failed: %v\nstderr: %s", err, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	err := run([]string{
		"lead", "close",
		"--run-dir", runDir,
		"--id", leadID,
		"--status", "closed_no_finding",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing reason error")
	}
	if !strings.Contains(err.Error(), "--reason must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertLeadClosedEventCount(t, runDir, leadID, 1)
}

func TestLeadCloseRequiresReason(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	setCurrentTaskPhaseForTest(t, runDir, "investigate")

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"lead", "close",
		"--run-dir", runDir,
		"--id", leadID,
		"--status", "closed_no_finding",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing reason error")
	}
	if !strings.Contains(err.Error(), "--reason must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertLeadClosedEventCount(t, runDir, leadID, 0)
}

func TestLeadCloseRequiresNegativeProofForClosedNoFinding(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	setCurrentTaskPhaseForTest(t, runDir, "investigate")

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"lead", "close",
		"--run-dir", runDir,
		"--id", leadID,
		"--status", "closed_no_finding",
		"--reason", "Auth blocks this route.",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing negative proof error")
	}
	if !strings.Contains(err.Error(), "closed_no_finding requires --negative-boundary") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertLeadClosedEventCount(t, runDir, leadID, 0)
}

func TestLeadCloseAllowsInconclusiveWithoutNegativeProof(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	setCurrentTaskPhaseForTest(t, runDir, "investigate")

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"lead", "close",
		"--run-dir", runDir,
		"--id", leadID,
		"--status", "inconclusive",
		"--reason", "Could not confirm deployment exposure.",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("inconclusive lead close failed: %v\nstderr: %s", err, stderr.String())
	}
	status, exists, err := ledgerLeadStatus(runDir, leadID)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || status != "inconclusive" {
		t.Fatalf("status = %q exists=%v, want inconclusive", status, exists)
	}
}

func TestVerdictRejectsInvalidPhaseValue(t *testing.T) {
	runDir := newLedgerTestRun(t)
	findingBody := writeRunFile(t, runDir, "evidence/finding-auth.md", "Candidate auth defect.")
	setCurrentTaskPhaseForTest(t, runDir, "investigate")
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

func TestVerdictRequiresReason(t *testing.T) {
	runDir := newLedgerTestRun(t)
	findingID := createFindingForTest(t, runDir, "")

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"verdict", "record",
		"--run-dir", runDir,
		"--finding", findingID,
		"--phase", "review",
		"--value", "accepted",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing reason error")
	}
	if !strings.Contains(err.Error(), "--reason must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerdictRecordRejectsWrongCurrentTaskPhase(t *testing.T) {
	runDir := newLedgerTestRun(t)
	findingID := createFindingForTest(t, runDir, "")
	setCurrentTaskPhaseForTest(t, runDir, "recon")

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"verdict", "record",
		"--run-dir", runDir,
		"--finding", findingID,
		"--phase", "review",
		"--value", "accepted",
		"--reason", "Specific and supported.",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected wrong task phase error")
	}
	if !strings.Contains(err.Error(), `current task phase "recon" cannot record "review" verdicts`) {
		t.Fatalf("unexpected error: %v", err)
	}
	assertVerdictEventCount(t, runDir, findingID, "review", 0)
}

func TestVerdictRecordIsIdempotentForSameDecision(t *testing.T) {
	runDir := newLedgerTestRun(t)
	findingID := createFindingForTest(t, runDir, "")
	startReviewTaskWithEvidenceForTest(t, runDir, findingID)

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"verdict", "record",
		"--run-dir", runDir,
		"--finding", findingID,
		"--phase", "review",
		"--value", "accepted",
		"--reason", "Specific and supported.",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("first verdict failed: %v\nstderr: %s", err, stderr.String())
	}
	firstID := strings.TrimSpace(stdout.String())

	stdout.Reset()
	stderr.Reset()
	if err := run([]string{
		"verdict", "record",
		"--run-dir", runDir,
		"--finding", findingID,
		"--phase", "review",
		"--value", "accepted",
		"--reason", "Same decision after retry.",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("idempotent verdict failed: %v\nstderr: %s", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != firstID {
		t.Fatalf("idempotent verdict id = %q, want %q", got, firstID)
	}
	assertVerdictEventCount(t, runDir, findingID, "review", 1)
}

func TestVerdictRecordIsAtomicForParallelSameDecision(t *testing.T) {
	runDir := newLedgerTestRun(t)
	findingID := createFindingForTest(t, runDir, "")
	startReviewTaskWithEvidenceForTest(t, runDir, findingID)

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var stdout, stderr bytes.Buffer
			errs <- run([]string{
				"verdict", "record",
				"--run-dir", runDir,
				"--finding", findingID,
				"--phase", "review",
				"--value", "accepted",
				"--reason", "Parallel same decision.",
			}, &stdout, &stderr)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("parallel verdict record failed: %v", err)
		}
	}
	assertVerdictEventCount(t, runDir, findingID, "review", 1)
}

func TestVerdictRecordRejectsConflictingCurrentTaskDecision(t *testing.T) {
	runDir := newLedgerTestRun(t)
	findingID := createFindingForTest(t, runDir, "")
	startReviewTaskWithEvidenceForTest(t, runDir, findingID)

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"verdict", "record",
		"--run-dir", runDir,
		"--finding", findingID,
		"--phase", "review",
		"--value", "accepted",
		"--reason", "Specific and supported.",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("first verdict failed: %v\nstderr: %s", err, stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	err := run([]string{
		"verdict", "record",
		"--run-dir", runDir,
		"--finding", findingID,
		"--phase", "review",
		"--value", "rejected",
		"--reason", "Conflicting decision in same task.",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected conflicting verdict error")
	}
	if !strings.Contains(err.Error(), `already has review verdict "accepted"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	assertVerdictEventCount(t, runDir, findingID, "review", 1)
}

func TestVerdictRecordRejectsConflictingDecision(t *testing.T) {
	runDir := newLedgerTestRun(t)
	findingID := createFindingForTest(t, runDir, "")
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"verdict", "record",
		"--run-dir", runDir,
		"--finding", findingID,
		"--phase", "review",
		"--value", "rejected",
		"--reason", "Changed my mind.",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected conflicting verdict error")
	}
	if !strings.Contains(err.Error(), `already has review verdict "accepted"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	assertVerdictEventCount(t, runDir, findingID, "review", 1)
}

func TestDeduplicateVerdictRejectsChangedCanonicalFinding(t *testing.T) {
	runDir := newLedgerTestRun(t)
	firstID := createFindingForTest(t, runDir, "")
	secondID := createFindingForTest(t, runDir, "")
	duplicateID := createFindingForTest(t, runDir, "")
	recordVerdictForTest(t, runDir, firstID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, secondID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, duplicateID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, duplicateID, "deduplicate", "duplicate", firstID)

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"verdict", "record",
		"--run-dir", runDir,
		"--finding", duplicateID,
		"--phase", "deduplicate",
		"--value", "duplicate",
		"--canonical-finding", secondID,
		"--reason", "Different canonical after retry.",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected conflicting canonical verdict error")
	}
	if !strings.Contains(err.Error(), `already has deduplicate verdict "duplicate"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	assertVerdictEventCount(t, runDir, duplicateID, "deduplicate", 1)
}

func TestValidateVerdictRejectsConflictingDecision(t *testing.T) {
	runDir := newLedgerTestRun(t)
	findingID := createFindingForTest(t, runDir, "")
	recordVerdictForTest(t, runDir, findingID, "validate", "proven", "")

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"verdict", "record",
		"--run-dir", runDir,
		"--finding", findingID,
		"--phase", "validate",
		"--value", "failed",
		"--reason", "Different validation after retry.",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected conflicting validation verdict error")
	}
	if !strings.Contains(err.Error(), `already has validate verdict "proven"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	assertVerdictEventCount(t, runDir, findingID, "validate", 1)
}

func TestDeduplicateDuplicateVerdictRequiresCanonicalFinding(t *testing.T) {
	runDir := newLedgerTestRun(t)
	if err := writeCurrentTaskForTest(runDir, TaskRecord{
		RunID:       "run_test",
		TaskID:      "task_deduplicate",
		Phase:       "deduplicate",
		Title:       "Deduplicate findings",
		Instruction: "Deduplicate reviewed findings.",
	}); err != nil {
		t.Fatal(err)
	}
	firstBody := writeRunFile(t, runDir, "evidence/finding-one.md", "First candidate.")
	secondBody := writeRunFile(t, runDir, "evidence/finding-two.md", "Second candidate.")
	setCurrentTaskPhaseForTest(t, runDir, "investigate")
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
	appendReviewAcceptedVerdictForTest(t, runDir, firstID)
	appendReviewAcceptedVerdictForTest(t, runDir, secondID)
	setCurrentTaskPhaseForTest(t, runDir, "deduplicate")

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

func TestDeduplicateDuplicateVerdictRejectsNonAcceptedCanonical(t *testing.T) {
	runDir := newLedgerTestRun(t)
	if err := writeCurrentTaskForTest(runDir, TaskRecord{
		RunID:       "run_test",
		TaskID:      "task_deduplicate",
		Phase:       "deduplicate",
		Title:       "Deduplicate findings",
		Instruction: "Deduplicate reviewed findings.",
	}); err != nil {
		t.Fatal(err)
	}
	firstBody := writeRunFile(t, runDir, "evidence/finding-one.md", "First candidate.")
	secondBody := writeRunFile(t, runDir, "evidence/finding-two.md", "Second candidate.")
	setCurrentTaskPhaseForTest(t, runDir, "investigate")
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
	appendReviewAcceptedVerdictForTest(t, runDir, secondID)
	setCurrentTaskPhaseForTest(t, runDir, "deduplicate")

	stdout.Reset()
	stderr.Reset()
	err := run([]string{
		"verdict", "record",
		"--run-dir", runDir,
		"--finding", secondID,
		"--phase", "deduplicate",
		"--value", "duplicate",
		"--canonical-finding", firstID,
		"--reason", "Same root issue.",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected non-accepted canonical finding error")
	}
	if !strings.Contains(err.Error(), "must have a completed accepted review verdict") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestVerdictRejectsMismatchedTaskPhase(t *testing.T) {
	runDir := newLedgerTestRun(t)
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "finding.created",
		Object:   "finding",
		ObjectID: "finding_one",
		TaskID:   "task_investigate_lead_one",
		Data: map[string]any{
			"title":      "Missing authorization",
			"lead_id":    "lead_one",
			"category":   "authz",
			"severity":   "high",
			"confidence": "medium",
			"body_path":  "evidence/finding-one.md",
		},
	}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"verdict", "record",
		"--run-dir", runDir,
		"--finding", "finding_one",
		"--phase", "review",
		"--value", "accepted",
		"--reason", "Accepted.",
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected mismatched task phase error")
	}
	if !strings.Contains(err.Error(), `current task phase "recon" cannot record "review" verdicts`) {
		t.Fatalf("unexpected error: %v", err)
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

func runFileSHA256ForTest(t *testing.T, runDir, rel string) string {
	t.Helper()
	hash, err := evidenceFileSHA256(runDir, rel)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func setCurrentTaskPhaseForTest(t *testing.T, runDir, phase string) {
	t.Helper()
	task := TaskRecord{
		RunID:       "run_test",
		TaskID:      "task_" + phase,
		Phase:       phase,
		Title:       "Test " + phase,
		Instruction: "Test " + phase + " task.",
	}
	if err := writeCurrentTaskForTest(runDir, task); err != nil {
		t.Fatal(err)
	}
}

func createLeadForTest(t *testing.T, runDir string) string {
	t.Helper()
	setCurrentTaskPhaseForTest(t, runDir, "recon")
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

func createFindingForTest(t *testing.T, runDir, leadID string) string {
	t.Helper()
	setCurrentTaskPhaseForTest(t, runDir, "investigate")
	safeLeadID := "unlinked"
	if leadID != "" {
		safeLeadID = safeFileID(leadID)
	}
	findingBody := writeRunFile(t, runDir, "evidence/finding-"+safeLeadID+".md", "Candidate auth defect.")
	args := []string{
		"finding", "create",
		"--run-dir", runDir,
		"--title", "Missing authorization check",
		"--category", "authz",
		"--severity", "high",
		"--confidence", "medium",
		"--body-file", findingBody,
	}
	if leadID != "" {
		args = append(args, "--lead", leadID)
	}
	var stdout, stderr bytes.Buffer
	if err := run(args, &stdout, &stderr); err != nil {
		t.Fatalf("finding create failed: %v\nstderr: %s", err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

func startReviewTaskWithEvidenceForTest(t *testing.T, runDir, findingID string) {
	t.Helper()
	if err := writeCurrentTaskForTest(runDir, TaskRecord{
		RunID:       "run_test",
		TaskID:      "task_review_" + safeFileID(findingID),
		Phase:       "review",
		Title:       "Review finding",
		Instruction: "Review one finding.",
	}); err != nil {
		t.Fatal(err)
	}
	notesPath := writeRunFile(t, runDir, reviewNotesRelPath(findingID), "Review evidence for test.")
	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--finding", findingID,
		"--kind", "markdown",
		"--title", "Review notes",
		"--path", notesPath,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("review evidence add failed: %v\nstderr: %s", err, stderr.String())
	}
}

func recordVerdictForTest(t *testing.T, runDir, findingID, phase, value, canonicalFindingID string) {
	t.Helper()
	taskID := "task_" + phase + "_" + safeFileID(findingID)
	if err := writeCurrentTaskForTest(runDir, TaskRecord{
		RunID:       "run_test",
		TaskID:      taskID,
		Phase:       phase,
		Title:       "Test " + phase,
		Instruction: "Record test verdict.",
	}); err != nil {
		t.Fatal(err)
	}
	args := []string{
		"verdict", "record",
		"--run-dir", runDir,
		"--finding", findingID,
		"--phase", phase,
		"--value", value,
		"--reason", "test verdict",
	}
	if canonicalFindingID != "" {
		args = append(args, "--canonical-finding", canonicalFindingID)
	}
	if phase == "review" {
		notesRel := reviewNotesRelPath(findingID)
		writeRunFile(t, runDir, notesRel, "Review evidence for test.")
		if err := appendLedgerEvent(runDir, LedgerEvent{
			RunID:    "run_test",
			Type:     "evidence.added",
			Object:   "evidence",
			ObjectID: "evidence_review_" + safeFileID(findingID),
			TaskID:   taskID,
			Data: map[string]any{
				"kind":           "markdown",
				"title":          "Review notes",
				"path":           notesRel,
				"content_sha256": runFileSHA256ForTest(t, runDir, notesRel),
				"finding_id":     findingID,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if phase == "deduplicate" {
		notesRel := deduplicateNotesRelPath()
		writeRunFile(t, runDir, notesRel, "Deduplication evidence for test.")
		if err := appendLedgerEvent(runDir, LedgerEvent{
			RunID:    "run_test",
			Type:     "evidence.added",
			Object:   "evidence",
			ObjectID: "evidence_deduplicate_" + safeFileID(findingID),
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
	if phase == "validate" {
		notesRel := validationNotesRelPath(findingID)
		writeRunFile(t, runDir, notesRel, "Validation evidence for test.")
		if err := appendLedgerEvent(runDir, LedgerEvent{
			RunID:    "run_test",
			Type:     "evidence.added",
			Object:   "evidence",
			ObjectID: "evidence_validate_" + safeFileID(findingID),
			TaskID:   taskID,
			Data: map[string]any{
				"kind":           "markdown",
				"title":          "Validation notes",
				"path":           notesRel,
				"content_sha256": runFileSHA256ForTest(t, runDir, notesRel),
				"finding_id":     findingID,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	var stdout, stderr bytes.Buffer
	if err := run(args, &stdout, &stderr); err != nil {
		t.Fatalf("verdict record failed: %v\nstderr: %s", err, stderr.String())
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "task.completed",
		Object:   "task",
		ObjectID: taskID,
		TaskID:   taskID,
		Data: map[string]any{
			"status":  "completed",
			"summary": "Completed test verdict.",
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func addEvidenceForFindingForTest(t *testing.T, runDir, findingID, path string) string {
	t.Helper()
	setCurrentTaskPhaseForTest(t, runDir, "investigate")
	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"evidence", "add",
		"--run-dir", runDir,
		"--finding", findingID,
		"--kind", "log",
		"--title", "Proof",
		"--path", path,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("evidence add failed: %v\nstderr: %s", err, stderr.String())
	}
	evidence, err := ledgerEvidenceForFinding(runDir, findingID)
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) == 0 {
		t.Fatalf("expected evidence for finding %s", findingID)
	}
	return evidence[len(evidence)-1].Path
}

func addValidationProofForFindingForTest(t *testing.T, runDir, findingID string) string {
	t.Helper()
	relPath := filepath.ToSlash(filepath.Join("evidence", "validate-"+safeFileID(findingID)+"-proof.log"))
	writeRunFile(t, runDir, relPath, "validation proof")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: newLedgerID("evidence"),
		TaskID:   "task_validate_" + safeFileID(findingID),
		Data: map[string]any{
			"kind":           "log",
			"title":          "Validation proof",
			"path":           relPath,
			"content_sha256": runFileSHA256ForTest(t, runDir, relPath),
			"lead_id":        "",
			"finding_id":     findingID,
		},
	}); err != nil {
		t.Fatal(err)
	}
	return relPath
}

func addValidationHandoffForFindingForTest(t *testing.T, runDir, findingID string, blockers []taskHandoffBlocker) string {
	t.Helper()
	taskID := "task_validate_" + safeFileID(findingID)
	relPath := taskHandoffRelPath("validate", findingID)
	if err := writeJSON(filepath.Join(runDir, filepath.FromSlash(relPath)), taskHandoffFile{
		Version:   phaseHandoffVersion,
		Phase:     "validate",
		TaskID:    taskID,
		FindingID: findingID,
		Blockers:  blockers,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:     "run_test",
		TaskID:    taskID,
		Kind:      "json",
		Title:     "Task handoff: " + findingID,
		Path:      relPath,
		FindingID: findingID,
	}); err != nil {
		t.Fatal(err)
	}
	return relPath
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

func negativeProofCLIArgs() []string {
	return []string{
		"--negative-boundary", "admin route requires authenticated operator role",
		"--negative-enforcement", "server/auth.go RequireRole(\"operator\") middleware",
		"--negative-exposure", "route is mounted only on the internal admin listener",
		"--negative-edge-cases", "checked anonymous, user role, operator role, and alternate /api path",
	}
}

func assertLeadCreatedEventCount(t *testing.T, runDir, taskID, bodyPath string, want int) {
	t.Helper()
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	var got int
	for _, event := range events {
		if event.Type == "lead.created" && event.TaskID == taskID && event.Data["body_path"] == bodyPath {
			got++
		}
	}
	if got != want {
		t.Fatalf("lead.created event count for %s/%s = %d, want %d", taskID, bodyPath, got, want)
	}
}

func assertTaskCompletedEventCount(t *testing.T, runDir, taskID string, want int) {
	t.Helper()
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	var got int
	for _, event := range events {
		if event.Type == "task.completed" && event.ObjectID == taskID {
			got++
		}
	}
	if got != want {
		t.Fatalf("task.completed event count for %s = %d, want %d", taskID, got, want)
	}
}

func assertReportFinalizedEventCount(t *testing.T, runDir, taskID string, want int) {
	t.Helper()
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	var got int
	for _, event := range events {
		if event.Type == "report.finalized" && event.TaskID == taskID {
			got++
		}
	}
	if got != want {
		t.Fatalf("report.finalized event count for %s = %d, want %d", taskID, got, want)
	}
}

func assertVerdictEventCount(t *testing.T, runDir, findingID, phase string, want int) {
	t.Helper()
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	var got int
	for _, event := range events {
		if event.Type != "verdict.recorded" {
			continue
		}
		if event.Data["finding_id"] == findingID && event.Data["phase"] == phase {
			got++
		}
	}
	if got != want {
		t.Fatalf("verdict.recorded event count for %s/%s = %d, want %d", findingID, phase, got, want)
	}
}

func assertEvidenceEventCount(t *testing.T, runDir, taskID, relPath string, want int) {
	t.Helper()
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	var got int
	for _, event := range events {
		if event.Type == "evidence.added" && event.TaskID == taskID && event.Data["path"] == relPath {
			got++
		}
	}
	if got != want {
		t.Fatalf("evidence.added event count for %s/%s = %d, want %d", taskID, relPath, got, want)
	}
}

func validReportJSON(t *testing.T, runID, markdownPath, jsonPath string, proven []map[string]any) string {
	t.Helper()
	if proven == nil {
		proven = []map[string]any{}
	}
	return validReportJSONFromBuckets(t, runID, markdownPath, jsonPath, map[string][]map[string]any{
		"proven": proven,
	})
}

func validReportJSONFromBuckets(t *testing.T, runID, markdownPath, jsonPath string, buckets map[string][]map[string]any) string {
	t.Helper()
	proven := reportBucketItems(buckets, "proven")
	inconclusive := reportBucketItems(buckets, "inconclusive")
	failed := reportBucketItems(buckets, "failed")
	rejected := reportBucketItems(buckets, "rejected")
	duplicate := reportBucketItems(buckets, "duplicate")
	unvalidated := reportBucketItems(buckets, "unvalidated")
	report := map[string]any{
		"run_id": runID,
		"counts": map[string]any{
			"findings_proven":       len(proven),
			"findings_inconclusive": len(inconclusive),
			"findings_failed":       len(failed),
			"findings_rejected":     len(rejected),
			"findings_duplicate":    len(duplicate),
			"findings_unvalidated":  len(unvalidated),
		},
		"report_paths": map[string]any{
			"markdown": markdownPath,
			"json":     jsonPath,
		},
		"proven":       proven,
		"inconclusive": inconclusive,
		"failed":       failed,
		"rejected":     rejected,
		"duplicate":    duplicate,
		"unvalidated":  unvalidated,
	}
	b, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func appendReviewAcceptedVerdictForTest(t *testing.T, runDir, findingID string) {
	t.Helper()
	taskID := "task_review_" + safeFileID(findingID)
	notesRel := reviewNotesRelPath(findingID)
	writeRunFile(t, runDir, notesRel, "Review evidence for test.")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_review_" + safeFileID(findingID),
		TaskID:   taskID,
		Data: map[string]any{
			"kind":           "markdown",
			"title":          "Review notes",
			"path":           notesRel,
			"content_sha256": runFileSHA256ForTest(t, runDir, notesRel),
			"finding_id":     findingID,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "verdict.recorded",
		Object:   "verdict",
		ObjectID: "verdict_review_" + safeFileID(findingID),
		TaskID:   taskID,
		Data: map[string]any{
			"finding_id": findingID,
			"phase":      "review",
			"value":      "accepted",
			"reason":     "Accepted for test.",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "task.completed",
		Object:   "task",
		ObjectID: taskID,
		TaskID:   taskID,
		Data: map[string]any{
			"status":  "completed",
			"summary": "Reviewed for test.",
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func duplicateReportBuckets(leadID, canonicalID, duplicateID string, options map[string]any) map[string][]map[string]any {
	unvalidated := []map[string]any{
		{
			"id":             canonicalID,
			"title":          "Missing authorization check",
			"category":       "authz",
			"severity":       "high",
			"confidence":     "medium",
			"source_lead_id": leadID,
			"status":         "validation_pending",
			"verdicts":       []string{"review accepted", "deduplicate canonical"},
			"evidence_paths": []string{},
			"summary":        "Canonical finding has not been validated yet.",
			"affected_paths": []string{"server/auth.go"},
		},
	}
	if extra, ok := options["extra_unvalidated"].([]map[string]any); ok {
		unvalidated = append(unvalidated, extra...)
	}
	duplicate := map[string]any{
		"id":             duplicateID,
		"title":          "Missing authorization check",
		"category":       "authz",
		"severity":       "high",
		"confidence":     "medium",
		"source_lead_id": leadID,
		"status":         "duplicate",
		"verdicts":       []string{"review accepted", "deduplicate duplicate"},
		"evidence_paths": []string{},
		"summary":        "Duplicate of the canonical authorization finding.",
		"affected_paths": []string{"server/auth.go"},
	}
	if canonicalFindingID, ok := options["canonical_finding_id"]; ok {
		duplicate["canonical_finding_id"] = canonicalFindingID
	}
	return map[string][]map[string]any{
		"unvalidated": unvalidated,
		"duplicate":   {duplicate},
	}
}

func reportBucketItems(buckets map[string][]map[string]any, name string) []map[string]any {
	if buckets == nil || buckets[name] == nil {
		return []map[string]any{}
	}
	return buckets[name]
}

func newStoredReportRun(t *testing.T) (string, RunRecord) {
	t.Helper()
	workspace := t.TempDir()
	runRecord := testRunRecord(workspace, "run_report", RunStatusCompleted, nowForTest())
	if err := os.MkdirAll(runRecord.RunDir, dirPerm); err != nil {
		t.Fatal(err)
	}
	store, err := openStore(filepath.Join(workspace, ".mnm", "mnm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRun(runRecord); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRecord.RunDir, "report.md"), []byte("# Final Report\n\nReal findings.\n"), filePerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runRecord.RunDir, "report.json"), []byte(`{"run_id":"`+runRecord.ID+`","counts":{"findings_proven":0},"report_paths":{"markdown":"report.md","json":"report.json"},"proven":[]}`), filePerm); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runRecord.RunDir, LedgerEvent{
		RunID:    runRecord.ID,
		Type:     "report.finalized",
		Object:   "report",
		ObjectID: "report_final",
		TaskID:   "task_finalize",
		Data: map[string]any{
			"markdown_path": "report.md",
			"json_path":     "report.json",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runRecord.RunDir, LedgerEvent{
		RunID:    runRecord.ID,
		Type:     "task.completed",
		Object:   "task",
		ObjectID: "task_finalize",
		TaskID:   "task_finalize",
		Data: map[string]any{
			"status":  "completed",
			"summary": "Finalized report",
		},
	}); err != nil {
		t.Fatal(err)
	}
	return workspace, runRecord
}

func nowForTest() time.Time {
	return time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC)
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
