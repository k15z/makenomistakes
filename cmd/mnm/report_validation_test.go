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
