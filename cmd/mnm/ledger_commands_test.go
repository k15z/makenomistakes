package main

import (
	"bytes"
	"encoding/json"
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
		Title:       "Review finding",
		Instruction: "Review one finding.",
	}); err != nil {
		t.Fatal(err)
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

func TestReportFinalizeRejectsMalformedJSONWithoutLedgerEvent(t *testing.T) {
	runDir := newLedgerTestRun(t)
	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", `{"run_id":`)

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

func TestReportFinalizeRequiresExpectedBuckets(t *testing.T) {
	runDir := newLedgerTestRun(t)
	reportMD := writeRunFile(t, runDir, "report.md", "# Report")
	reportJSON := writeRunFile(t, runDir, "report.json", `{
		"run_id": "run_test",
		"counts": {},
		"report_paths": {"markdown": "report.md", "json": "report.json"},
		"proven": []
	}`)

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
	otherLeadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, sourceLeadID)
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
			"source_lead_id": otherLeadID,
			"status":         "validation_proven",
			"verdicts":       []string{"validation proven"},
			"evidence_paths": []string{},
			"summary":        "Traceable finding with the wrong source lead.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

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
	proofRel := addEvidenceForFindingForTest(t, runDir, findingID, proofDir)

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
			"verdicts":       []string{"validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "Directory paths are not durable evidence files.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

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
	if !strings.Contains(err.Error(), "contains directory") {
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
			"summary":        "Traceable finding.",
			"affected_paths": []string{"server/auth.go"},
		},
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
	if !ledgerReportFinalized(runDir) {
		t.Fatal("expected traceable report to be finalized")
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
			"verdicts":       []string{"review accepted", "validation failed"},
			"evidence_paths": []string{proofRel},
			"summary":        "This should not be proven.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

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
			"verdicts":       []string{"review accepted", "validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "The proof file exists but was never registered.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

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
			"verdicts":       []string{"review accepted", "validation proven"},
			"evidence_paths": []string{},
			"summary":        "This cannot be proven without evidence.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

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
			"verdicts":       []string{"review accepted", "validation proven"},
			"evidence_paths": []string{proofRel},
			"summary":        "The bucket is proven, but status says failed.",
			"affected_paths": []string{"server/auth.go"},
		},
	}))

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
	if !strings.Contains(err.Error(), `status = "validation_failed" is not valid for bucket "proven"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if ledgerReportFinalized(runDir) {
		t.Fatal("report with mismatched status should not be finalized")
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

func TestLeadCloseIsAtomicForParallelSameStatus(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	start := make(chan struct{})
	errs := make(chan error, 20)
	var wg sync.WaitGroup
	for i := 0; i < cap(errs); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var stdout, stderr bytes.Buffer
			errs <- run([]string{
				"lead", "close",
				"--run-dir", runDir,
				"--id", leadID,
				"--status", "closed_no_finding",
				"--reason", "Parallel close.",
			}, &stdout, &stderr)
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
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected mismatched task phase error")
	}
	if !strings.Contains(err.Error(), `current task phase "recon" cannot record review verdict`) {
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

func createFindingForTest(t *testing.T, runDir, leadID string) string {
	t.Helper()
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
	var stdout, stderr bytes.Buffer
	if err := run(args, &stdout, &stderr); err != nil {
		t.Fatalf("verdict record failed: %v\nstderr: %s", err, stderr.String())
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
				"kind":       "markdown",
				"title":      "Validation notes",
				"path":       notesRel,
				"finding_id": findingID,
			},
		}); err != nil {
			t.Fatal(err)
		}
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
