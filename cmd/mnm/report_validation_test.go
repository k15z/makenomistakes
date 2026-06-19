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
