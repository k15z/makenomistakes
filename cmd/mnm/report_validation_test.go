package main

import "testing"

func TestReportBucketForFinding(t *testing.T) {
	tests := []struct {
		name     string
		verdicts map[string]VerdictRecord
		want     string
	}{
		{
			name: "unreviewed",
			want: "unvalidated",
		},
		{
			name: "review rejected",
			verdicts: map[string]VerdictRecord{
				"review": {Phase: "review", Value: "rejected"},
			},
			want: "rejected",
		},
		{
			name: "accepted without dedup",
			verdicts: map[string]VerdictRecord{
				"review": {Phase: "review", Value: "accepted"},
			},
			want: "unvalidated",
		},
		{
			name: "duplicate",
			verdicts: map[string]VerdictRecord{
				"review":      {Phase: "review", Value: "accepted"},
				"deduplicate": {Phase: "deduplicate", Value: "duplicate"},
			},
			want: "duplicate",
		},
		{
			name: "proven",
			verdicts: map[string]VerdictRecord{
				"review":      {Phase: "review", Value: "accepted"},
				"deduplicate": {Phase: "deduplicate", Value: "canonical"},
				"validate":    {Phase: "validate", Value: "proven"},
			},
			want: "proven",
		},
		{
			name: "failed",
			verdicts: map[string]VerdictRecord{
				"review":      {Phase: "review", Value: "accepted"},
				"deduplicate": {Phase: "deduplicate", Value: "canonical"},
				"validate":    {Phase: "validate", Value: "failed"},
			},
			want: "failed",
		},
		{
			name: "inconclusive",
			verdicts: map[string]VerdictRecord{
				"review":      {Phase: "review", Value: "accepted"},
				"deduplicate": {Phase: "deduplicate", Value: "canonical"},
				"validate":    {Phase: "validate", Value: "inconclusive"},
			},
			want: "inconclusive",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := reportBucketForFinding(test.verdicts); got != test.want {
				t.Fatalf("bucket = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReportKnownStateIgnoresVerdictsWithoutCompletedTask(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "verdict.recorded",
		Object:   "verdict",
		ObjectID: "verdict_taskless",
		Data: map[string]any{
			"finding_id": findingID,
			"phase":      "review",
			"value":      "accepted",
			"reason":     "taskless verdict must not count",
		},
	}); err != nil {
		t.Fatal(err)
	}
	state, err := reportKnownState(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reportBucketForFinding(state.Verdicts[findingID]); got != "unvalidated" {
		t.Fatalf("bucket = %q, want unvalidated", got)
	}
}

func TestReportKnownStateIgnoresReviewVerdictWithoutReviewEvidence(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)

	taskID := "task_review_" + safeFileID(findingID)
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "verdict.recorded",
		Object:   "verdict",
		ObjectID: "verdict_review_without_evidence",
		TaskID:   taskID,
		Data: map[string]any{
			"finding_id": findingID,
			"phase":      "review",
			"value":      "accepted",
			"reason":     "missing review evidence",
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
			"summary": "Completed without review evidence.",
		},
	}); err != nil {
		t.Fatal(err)
	}
	notesRel := reviewNotesRelPath(findingID)
	writeRunFile(t, runDir, notesRel, "Late review evidence.")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_review_after_stale_verdict",
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
	state, err := reportKnownState(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reportBucketForFinding(state.Verdicts[findingID]); got != "unvalidated" {
		t.Fatalf("bucket = %q, want unvalidated", got)
	}
}

func TestReportKnownStateIgnoresReviewVerdictWhenEvidenceContentChanges(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)

	taskID := "task_review_" + safeFileID(findingID)
	notesRel := reviewNotesRelPath(findingID)
	writeRunFile(t, runDir, notesRel, " \n")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_review_blank",
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
		ObjectID: "verdict_review_blank",
		TaskID:   taskID,
		Data: map[string]any{
			"finding_id": findingID,
			"phase":      "review",
			"value":      "accepted",
			"reason":     "registered blank notes first",
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
			"summary": "Completed with blank notes.",
		},
	}); err != nil {
		t.Fatal(err)
	}
	writeRunFile(t, runDir, notesRel, "Late review evidence.")

	state, err := reportKnownState(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reportBucketForFinding(state.Verdicts[findingID]); got != "unvalidated" {
		t.Fatalf("bucket = %q, want unvalidated", got)
	}
}

func TestReportKnownStateIgnoresValidateVerdictWithoutValidationEvidence(t *testing.T) {
	runDir := newLedgerTestRun(t)
	leadID := createLeadForTest(t, runDir)
	findingID := createFindingForTest(t, runDir, leadID)
	recordVerdictForTest(t, runDir, findingID, "review", "accepted", "")
	recordVerdictForTest(t, runDir, findingID, "deduplicate", "canonical", "")

	taskID := "task_validate_" + safeFileID(findingID)
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "verdict.recorded",
		Object:   "verdict",
		ObjectID: "verdict_validate_without_evidence",
		TaskID:   taskID,
		Data: map[string]any{
			"finding_id": findingID,
			"phase":      "validate",
			"value":      "proven",
			"reason":     "missing validation evidence",
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
			"summary": "Completed without validation evidence.",
		},
	}); err != nil {
		t.Fatal(err)
	}
	notesRel := validationNotesRelPath(findingID)
	writeRunFile(t, runDir, notesRel, "Late validation evidence.")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_validate_after_stale_verdict",
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

	state, err := reportKnownState(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reportBucketForFinding(state.Verdicts[findingID]); got != "unvalidated" {
		t.Fatalf("bucket = %q, want unvalidated", got)
	}
}

func TestReportStatusAllowedForBucket(t *testing.T) {
	tests := []struct {
		bucket string
		status string
		want   bool
	}{
		{bucket: "proven", status: "validation_proven", want: true},
		{bucket: "proven", status: "validation_failed", want: false},
		{bucket: "inconclusive", status: "validation_inconclusive", want: true},
		{bucket: "failed", status: "validation_failed", want: true},
		{bucket: "rejected", status: "review_rejected", want: true},
		{bucket: "duplicate", status: "duplicate", want: true},
		{bucket: "unvalidated", status: "candidate", want: true},
		{bucket: "unvalidated", status: "reviewed", want: true},
		{bucket: "unvalidated", status: "validation_pending", want: true},
		{bucket: "unvalidated", status: "validation_proven", want: false},
	}

	for _, test := range tests {
		t.Run(test.bucket+"/"+test.status, func(t *testing.T) {
			if got := reportStatusAllowedForBucket(test.bucket, test.status); got != test.want {
				t.Fatalf("allowed = %t, want %t", got, test.want)
			}
		})
	}
}
