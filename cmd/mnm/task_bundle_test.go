package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIngestTaskBundleCopiesArtifactsAndAppendsEvents(t *testing.T) {
	runDir := t.TempDir()
	bundleDir := t.TempDir()
	task := TaskRecord{
		RunID:  "run_bundle",
		TaskID: "task_recon",
		Phase:  "recon",
	}
	writeTaskBundleFile(t, bundleDir, "evidence/recon-codebase-map.md", "# Map\n")
	writeTaskBundleFile(t, bundleDir, "evidence/lead-auth.md", "# Lead\n")
	writeTaskBundleEvents(t, bundleDir,
		LedgerEvent{
			ID:        "event_started",
			RunID:     task.RunID,
			Type:      "task.started",
			Object:    "task",
			ObjectID:  task.TaskID,
			TaskID:    task.TaskID,
			Timestamp: "2026-01-01T00:00:00Z",
			Data: map[string]any{
				"phase": task.Phase,
			},
		},
		LedgerEvent{
			ID:        "event_map",
			RunID:     task.RunID,
			Type:      "evidence.added",
			Object:    "evidence",
			ObjectID:  "evidence_map",
			TaskID:    task.TaskID,
			Timestamp: "2026-01-01T00:00:01Z",
			Data: map[string]any{
				"kind":           "markdown",
				"title":          "Recon map",
				"path":           "evidence/recon-codebase-map.md",
				"content_sha256": taskBundleFileSHA256ForTest(t, bundleDir, "evidence/recon-codebase-map.md"),
			},
		},
		LedgerEvent{
			ID:        "event_lead",
			RunID:     task.RunID,
			Type:      "lead.created",
			Object:    "lead",
			ObjectID:  "lead_auth",
			TaskID:    task.TaskID,
			Timestamp: "2026-01-01T00:00:02Z",
			Data: map[string]any{
				"title":     "Investigate auth",
				"category":  "authz",
				"priority":  "high",
				"body_path": "evidence/lead-auth.md",
			},
		},
		LedgerEvent{
			ID:        "event_done",
			RunID:     task.RunID,
			Type:      "task.completed",
			Object:    "task",
			ObjectID:  task.TaskID,
			TaskID:    task.TaskID,
			Timestamp: "2026-01-01T00:00:03Z",
			Data: map[string]any{
				"status":  "completed",
				"summary": "Recon done",
			},
		},
	)

	if err := ingestTaskBundle(runDir, task, bundleDir); err != nil {
		t.Fatalf("ingest task bundle failed: %v", err)
	}

	if got := readFile(t, filepath.Join(runDir, "evidence", "recon-codebase-map.md")); got != "# Map\n" {
		t.Fatalf("copied map = %q", got)
	}
	if got := readFile(t, filepath.Join(runDir, "evidence", "lead-auth.md")); got != "# Lead\n" {
		t.Fatalf("copied lead = %q", got)
	}
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("event count = %d, want 4", len(events))
	}
	if !ledgerTaskCompleted(runDir, task.TaskID) {
		t.Fatal("expected task completion from ingested bundle")
	}
}

func TestIngestTaskBundleIsIdempotent(t *testing.T) {
	runDir := t.TempDir()
	bundleDir := t.TempDir()
	task := TaskRecord{
		RunID:  "run_bundle",
		TaskID: "task_recon",
		Phase:  "recon",
	}
	writeTaskBundleFile(t, bundleDir, "evidence/recon-codebase-map.md", "# Map\n")
	writeTaskBundleEvents(t, bundleDir,
		taskStartedEvent(task),
		taskEvidenceAddedEvent(t, bundleDir, task, "event_map", "evidence_map", "evidence/recon-codebase-map.md"),
		taskCompletedEvent(task),
	)

	if err := ingestTaskBundle(runDir, task, bundleDir); err != nil {
		t.Fatalf("first ingest failed: %v", err)
	}
	if err := ingestTaskBundle(runDir, task, bundleDir); err != nil {
		t.Fatalf("second ingest should be idempotent: %v", err)
	}
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("event count after idempotent ingest = %d, want 3", len(events))
	}
}

func TestIngestTaskBundleCanRecoverAfterStaleCompletion(t *testing.T) {
	runDir := t.TempDir()
	bundleDir := t.TempDir()
	task := TaskRecord{
		RunID:  "run_bundle",
		TaskID: "task_recon",
		Phase:  "recon",
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		ID:        "event_stale_done",
		RunID:     task.RunID,
		Type:      "task.completed",
		Object:    "task",
		ObjectID:  task.TaskID,
		TaskID:    task.TaskID,
		Timestamp: "2026-01-01T00:00:00Z",
		Data: map[string]any{
			"status":  "completed",
			"summary": "stale completion before required outputs existed",
		},
	}); err != nil {
		t.Fatal(err)
	}
	writeTaskBundleFile(t, bundleDir, "evidence/recon-codebase-map.md", "# Map\n")
	writeTaskBundleEvents(t, bundleDir,
		taskStartedEvent(task),
		taskEvidenceAddedEvent(t, bundleDir, task, "event_map", "evidence_map", "evidence/recon-codebase-map.md"),
		taskCompletedEvent(task),
	)

	options := taskBundleIngestOptions{AllowAfterCompleted: true}
	if err := ingestTaskBundleWithOptions(runDir, task, bundleDir, options); err != nil {
		t.Fatalf("recovery ingest failed: %v", err)
	}
	if err := ingestTaskBundleWithOptions(runDir, task, bundleDir, options); err != nil {
		t.Fatalf("recovery ingest should remain idempotent: %v", err)
	}
	if got := readFile(t, filepath.Join(runDir, "evidence", "recon-codebase-map.md")); got != "# Map\n" {
		t.Fatalf("copied map = %q", got)
	}
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("event count = %d, want 4", len(events))
	}
	assertTaskCompletedEventCount(t, runDir, task.TaskID, 2)
}

func TestIngestTaskBundleRejectsDifferentEventsAfterTaskCompletion(t *testing.T) {
	runDir := t.TempDir()
	bundleDir := t.TempDir()
	task := TaskRecord{
		RunID:  "run_bundle",
		TaskID: "task_recon",
		Phase:  "recon",
	}
	writeTaskBundleFile(t, bundleDir, "evidence/recon-codebase-map.md", "# Map\n")
	writeTaskBundleEvents(t, bundleDir,
		taskStartedEvent(task),
		taskEvidenceAddedEvent(t, bundleDir, task, "event_map", "evidence_map", "evidence/recon-codebase-map.md"),
		taskCompletedEvent(task),
	)
	if err := ingestTaskBundle(runDir, task, bundleDir); err != nil {
		t.Fatalf("first ingest failed: %v", err)
	}

	otherBundleDir := t.TempDir()
	writeTaskBundleFile(t, otherBundleDir, "evidence/recon-risk-register.md", "# Risk\n")
	writeTaskBundleEvents(t, otherBundleDir,
		taskStartedEvent(task),
		taskEvidenceAddedEvent(t, otherBundleDir, task, "event_risk", "evidence_risk", "evidence/recon-risk-register.md"),
		taskCompletedEvent(task),
	)

	err := ingestTaskBundle(runDir, task, otherBundleDir)
	if err == nil {
		t.Fatal("expected non-idempotent ingest error")
	}
	if !strings.Contains(err.Error(), "already completed") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTaskBundleRejectsWrongTask(t *testing.T) {
	bundleDir := t.TempDir()
	task := TaskRecord{RunID: "run_bundle", TaskID: "task_recon", Phase: "recon"}
	writeTaskBundleEvents(t, bundleDir, LedgerEvent{
		ID:        "event_done",
		RunID:     task.RunID,
		Type:      "task.completed",
		Object:    "task",
		ObjectID:  "task_other",
		TaskID:    "task_other",
		Timestamp: "2026-01-01T00:00:00Z",
		Data: map[string]any{
			"status":  "completed",
			"summary": "done",
		},
	})

	_, err := validateTaskBundle(bundleDir, task)
	if err == nil {
		t.Fatal("expected task mismatch error")
	}
	if !strings.Contains(err.Error(), `task_id = "task_other", want "task_recon"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTaskBundleRequiresTerminalTaskCompletion(t *testing.T) {
	bundleDir := t.TempDir()
	task := TaskRecord{RunID: "run_bundle", TaskID: "task_recon", Phase: "recon"}
	writeTaskBundleEvents(t, bundleDir, LedgerEvent{
		ID:        "event_started",
		RunID:     task.RunID,
		Type:      "task.started",
		Object:    "task",
		ObjectID:  task.TaskID,
		TaskID:    task.TaskID,
		Timestamp: "2026-01-01T00:00:00Z",
		Data: map[string]any{
			"phase": task.Phase,
		},
	})

	_, err := validateTaskBundle(bundleDir, task)
	if err == nil {
		t.Fatal("expected missing terminal event error")
	}
	if !strings.Contains(err.Error(), "missing terminal task.completed event") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTaskBundleRequiresTaskCompletionLast(t *testing.T) {
	bundleDir := t.TempDir()
	task := TaskRecord{RunID: "run_bundle", TaskID: "task_recon", Phase: "recon"}
	writeTaskBundleFile(t, bundleDir, "evidence/recon-codebase-map.md", "# Map\n")
	writeTaskBundleEvents(t, bundleDir,
		taskCompletedEvent(task),
		taskEvidenceAddedEvent(t, bundleDir, task, "event_map", "evidence_map", "evidence/recon-codebase-map.md"),
	)

	_, err := validateTaskBundle(bundleDir, task)
	if err == nil {
		t.Fatal("expected completion ordering error")
	}
	if !strings.Contains(err.Error(), "must be the final event") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTaskBundleRejectsMissingArtifact(t *testing.T) {
	bundleDir := t.TempDir()
	task := TaskRecord{RunID: "run_bundle", TaskID: "task_recon", Phase: "recon"}
	writeTaskBundleEvents(t, bundleDir,
		LedgerEvent{
			ID:        "event_map",
			RunID:     task.RunID,
			Type:      "evidence.added",
			Object:    "evidence",
			ObjectID:  "evidence_map",
			TaskID:    task.TaskID,
			Timestamp: "2026-01-01T00:00:00Z",
			Data: map[string]any{
				"kind":  "markdown",
				"title": "Recon map",
				"path":  "evidence/recon-codebase-map.md",
			},
		},
		taskCompletedEvent(task),
	)

	_, err := validateTaskBundle(bundleDir, task)
	if err == nil {
		t.Fatal("expected missing artifact error")
	}
	if !strings.Contains(err.Error(), "evidence/recon-codebase-map.md") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTaskBundleRejectsWhitespaceOnlyArtifact(t *testing.T) {
	bundleDir := t.TempDir()
	task := TaskRecord{RunID: "run_bundle", TaskID: "task_recon", Phase: "recon"}
	writeTaskBundleFile(t, bundleDir, "evidence/recon-codebase-map.md", " \n\t\n")
	writeTaskBundleEvents(t, bundleDir,
		taskEvidenceAddedEvent(t, bundleDir, task, "event_map", "evidence_map", "evidence/recon-codebase-map.md"),
		taskCompletedEvent(task),
	)

	_, err := validateTaskBundle(bundleDir, task)
	if err == nil {
		t.Fatal("expected whitespace artifact error")
	}
	if !strings.Contains(err.Error(), "whitespace-only") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTaskBundleRejectsEscapingArtifactPath(t *testing.T) {
	bundleDir := t.TempDir()
	task := TaskRecord{RunID: "run_bundle", TaskID: "task_recon", Phase: "recon"}
	writeTaskBundleEvents(t, bundleDir,
		LedgerEvent{
			ID:        "event_map",
			RunID:     task.RunID,
			Type:      "evidence.added",
			Object:    "evidence",
			ObjectID:  "evidence_map",
			TaskID:    task.TaskID,
			Timestamp: "2026-01-01T00:00:00Z",
			Data: map[string]any{
				"kind":  "markdown",
				"title": "Recon map",
				"path":  "../escape.md",
			},
		},
		taskCompletedEvent(task),
	)

	_, err := validateTaskBundle(bundleDir, task)
	if err == nil {
		t.Fatal("expected escaping path error")
	}
	if !strings.Contains(err.Error(), "must be clean and stay inside") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTaskBundleRejectsReportFinalizeOutsideFinalize(t *testing.T) {
	bundleDir := t.TempDir()
	task := TaskRecord{RunID: "run_bundle", TaskID: "task_recon", Phase: "recon"}
	writeTaskBundleFile(t, bundleDir, "report.md", "# Report\n")
	writeTaskBundleFile(t, bundleDir, "report.json", `{"run_id":"run_bundle"}`)
	writeTaskBundleEvents(t, bundleDir,
		LedgerEvent{
			ID:        "event_report",
			RunID:     task.RunID,
			Type:      "report.finalized",
			Object:    "report",
			ObjectID:  "report_recon",
			TaskID:    task.TaskID,
			Timestamp: "2026-01-01T00:00:00Z",
			Data: map[string]any{
				"markdown_path": "report.md",
				"json_path":     "report.json",
			},
		},
		taskCompletedEvent(task),
	)

	_, err := validateTaskBundle(bundleDir, task)
	if err == nil {
		t.Fatal("expected phase error")
	}
	if !strings.Contains(err.Error(), `phase "recon" cannot finalize reports`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTaskBundleRejectsUnownedEvidenceOutsideAllowedPhases(t *testing.T) {
	bundleDir := t.TempDir()
	task := TaskRecord{RunID: "run_bundle", TaskID: "task_review_finding", Phase: "review"}
	writeTaskBundleFile(t, bundleDir, "evidence/review-notes.md", "# Review\n")
	writeTaskBundleEvents(t, bundleDir,
		taskEvidenceAddedEvent(t, bundleDir, task, "event_notes", "evidence_notes", "evidence/review-notes.md"),
		taskCompletedEvent(task),
	)

	_, err := validateTaskBundle(bundleDir, task)
	if err == nil {
		t.Fatal("expected phase error")
	}
	if !strings.Contains(err.Error(), `phase "review" cannot register unowned evidence`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIngestTaskBundleRejectsDivergentExistingArtifactBeforeAppendingEvents(t *testing.T) {
	runDir := t.TempDir()
	bundleDir := t.TempDir()
	task := TaskRecord{RunID: "run_bundle", TaskID: "task_recon", Phase: "recon"}
	writeWorkspaceFile(t, runDir, "evidence/recon-codebase-map.md", "old\n")
	writeTaskBundleFile(t, bundleDir, "evidence/recon-codebase-map.md", "new\n")
	writeTaskBundleEvents(t, bundleDir,
		LedgerEvent{
			ID:        "event_map",
			RunID:     task.RunID,
			Type:      "evidence.added",
			Object:    "evidence",
			ObjectID:  "evidence_map",
			TaskID:    task.TaskID,
			Timestamp: "2026-01-01T00:00:00Z",
			Data: map[string]any{
				"kind":           "markdown",
				"title":          "Recon map",
				"path":           "evidence/recon-codebase-map.md",
				"content_sha256": taskBundleFileSHA256ForTest(t, bundleDir, "evidence/recon-codebase-map.md"),
			},
		},
		taskCompletedEvent(task),
	)

	err := ingestTaskBundle(runDir, task, bundleDir)
	if err == nil {
		t.Fatal("expected divergent artifact error")
	}
	if !strings.Contains(err.Error(), "already exists with different contents") {
		t.Fatalf("unexpected error: %v", err)
	}
	events, readErr := readLedgerEvents(runDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(events) != 0 {
		t.Fatalf("events were appended despite failed ingest: %#v", events)
	}
}

func TestIngestTaskBundleRejectsInvalidFinalReportBeforeAppendingEvents(t *testing.T) {
	runDir := t.TempDir()
	bundleDir := t.TempDir()
	task := TaskRecord{RunID: "run_bundle", TaskID: "task_finalize", Phase: "finalize"}
	writeTaskBundleFile(t, bundleDir, "report.md", "# Report\n")
	writeTaskBundleFile(t, bundleDir, "report.json", "{}\n")
	writeTaskBundleEvents(t, bundleDir,
		taskStartedEvent(task),
		LedgerEvent{
			ID:        "event_report",
			RunID:     task.RunID,
			Type:      "report.finalized",
			Object:    "report",
			ObjectID:  "report_final",
			TaskID:    task.TaskID,
			Timestamp: "2026-01-01T00:00:00Z",
			Data: map[string]any{
				"markdown_path": "report.md",
				"json_path":     "report.json",
			},
		},
		taskCompletedEvent(task),
	)

	err := ingestTaskBundle(runDir, task, bundleDir)
	if err == nil {
		t.Fatal("expected report validation error")
	}
	if !strings.Contains(err.Error(), "report JSON must not be an empty object") {
		t.Fatalf("unexpected error: %v", err)
	}
	events, readErr := readLedgerEvents(runDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(events) != 0 {
		t.Fatalf("events were appended despite failed report validation: %#v", events)
	}
	if _, statErr := os.Stat(filepath.Join(runDir, "report.md")); !os.IsNotExist(statErr) {
		t.Fatalf("report artifact was not cleaned up: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(runDir, "report.json")); !os.IsNotExist(statErr) {
		t.Fatalf("JSON report artifact was not cleaned up: %v", statErr)
	}
}

func TestValidateTaskBundleRejectsMissingEvidenceDigest(t *testing.T) {
	bundleDir := t.TempDir()
	task := TaskRecord{RunID: "run_bundle", TaskID: "task_recon", Phase: "recon"}
	writeTaskBundleFile(t, bundleDir, "evidence/recon-codebase-map.md", "# Map\n")
	writeTaskBundleEvents(t, bundleDir,
		LedgerEvent{
			ID:        "event_map",
			RunID:     task.RunID,
			Type:      "evidence.added",
			Object:    "evidence",
			ObjectID:  "evidence_map",
			TaskID:    task.TaskID,
			Timestamp: "2026-01-01T00:00:00Z",
			Data: map[string]any{
				"kind":  "markdown",
				"title": "Recon map",
				"path":  "evidence/recon-codebase-map.md",
			},
		},
		taskCompletedEvent(task),
	)

	_, err := validateTaskBundle(bundleDir, task)
	if err == nil {
		t.Fatal("expected missing content digest error")
	}
	if !strings.Contains(err.Error(), "content_sha256 is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTaskBundleRejectsMismatchedEvidenceDigest(t *testing.T) {
	bundleDir := t.TempDir()
	task := TaskRecord{RunID: "run_bundle", TaskID: "task_recon", Phase: "recon"}
	writeTaskBundleFile(t, bundleDir, "evidence/recon-codebase-map.md", "# Map\n")
	writeTaskBundleEvents(t, bundleDir,
		LedgerEvent{
			ID:        "event_map",
			RunID:     task.RunID,
			Type:      "evidence.added",
			Object:    "evidence",
			ObjectID:  "evidence_map",
			TaskID:    task.TaskID,
			Timestamp: "2026-01-01T00:00:00Z",
			Data: map[string]any{
				"kind":           "markdown",
				"title":          "Recon map",
				"path":           "evidence/recon-codebase-map.md",
				"content_sha256": "0000000000000000000000000000000000000000000000000000000000000000",
			},
		},
		taskCompletedEvent(task),
	)

	_, err := validateTaskBundle(bundleDir, task)
	if err == nil {
		t.Fatal("expected mismatched content digest error")
	}
	if !strings.Contains(err.Error(), "content_sha256") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIngestTaskBundlePreflightsTargetsBeforeCopyingArtifacts(t *testing.T) {
	runDir := t.TempDir()
	bundleDir := t.TempDir()
	task := TaskRecord{RunID: "run_bundle", TaskID: "task_recon", Phase: "recon"}
	writeTaskBundleFile(t, bundleDir, "evidence/a.md", "new a\n")
	writeTaskBundleFile(t, bundleDir, "evidence/z.md", "new z\n")
	writeWorkspaceFile(t, runDir, "evidence/z.md", "old z\n")
	writeTaskBundleEvents(t, bundleDir,
		taskEvidenceAddedEvent(t, bundleDir, task, "event_a", "evidence_a", "evidence/a.md"),
		taskEvidenceAddedEvent(t, bundleDir, task, "event_z", "evidence_z", "evidence/z.md"),
		taskCompletedEvent(task),
	)

	err := ingestTaskBundle(runDir, task, bundleDir)
	if err == nil {
		t.Fatal("expected divergent artifact error")
	}
	if _, statErr := os.Stat(filepath.Join(runDir, "evidence", "a.md")); !os.IsNotExist(statErr) {
		t.Fatalf("artifact was copied before target preflight completed: %v", statErr)
	}
	events, readErr := readLedgerEvents(runDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(events) != 0 {
		t.Fatalf("events were appended despite failed ingest: %#v", events)
	}
}

func TestAppendTaskBundleEventsRequiresIngestedArtifacts(t *testing.T) {
	runDir := t.TempDir()
	bundleDir := t.TempDir()
	task := TaskRecord{RunID: "run_bundle", TaskID: "task_recon", Phase: "recon"}
	relPath := "evidence/recon-codebase-map.md"
	writeTaskBundleFile(t, bundleDir, relPath, "# Map\n")
	events := []LedgerEvent{
		taskStartedEvent(task),
		taskEvidenceAddedEvent(t, bundleDir, task, "event_map", "evidence_map", relPath),
		taskCompletedEvent(task),
	}

	err := appendTaskBundleEvents(runDir, task, bundleDir, []string{relPath}, events, taskBundleIngestOptions{})
	if err == nil {
		t.Fatal("expected missing ingested artifact error")
	}
	if !strings.Contains(err.Error(), "was not ingested") {
		t.Fatalf("unexpected error: %v", err)
	}
	ledgerEvents, readErr := readLedgerEvents(runDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(ledgerEvents) != 0 {
		t.Fatalf("events were appended without ingested artifacts: %#v", ledgerEvents)
	}
}

func TestIngestTaskBundleRejectsSymlinkedTargetParent(t *testing.T) {
	runDir := t.TempDir()
	bundleDir := t.TempDir()
	task := TaskRecord{RunID: "run_bundle", TaskID: "task_recon", Phase: "recon"}
	writeTaskBundleFile(t, bundleDir, "evidence/a.md", "new a\n")
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(runDir, "evidence")); err != nil {
		t.Fatal(err)
	}
	writeTaskBundleEvents(t, bundleDir,
		taskEvidenceAddedEvent(t, bundleDir, task, "event_a", "evidence_a", "evidence/a.md"),
		taskCompletedEvent(task),
	)

	err := ingestTaskBundle(runDir, task, bundleDir)
	if err == nil {
		t.Fatal("expected symlinked target parent error")
	}
	if !strings.Contains(err.Error(), "target artifact parent evidence is a symlink") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTaskBundleRejectsPhaseMismatch(t *testing.T) {
	bundleDir := t.TempDir()
	task := TaskRecord{RunID: "run_bundle", TaskID: "task_validate_finding", Phase: "validate"}
	writeTaskBundleEvents(t, bundleDir,
		LedgerEvent{
			ID:        "event_verdict",
			RunID:     task.RunID,
			Type:      "verdict.recorded",
			Object:    "verdict",
			ObjectID:  "verdict_review",
			TaskID:    task.TaskID,
			Timestamp: "2026-01-01T00:00:00Z",
			Data: map[string]any{
				"finding_id":           "finding_auth",
				"phase":                "review",
				"value":                "accepted",
				"reason":               "looks real",
				"canonical_finding_id": "",
			},
		},
		taskCompletedEvent(task),
	)

	_, err := validateTaskBundle(bundleDir, task)
	if err == nil {
		t.Fatal("expected phase mismatch error")
	}
	if !strings.Contains(err.Error(), `verdict phase = "review", want "validate"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateTaskBundleRejectsRunLifecycleEvent(t *testing.T) {
	bundleDir := t.TempDir()
	task := TaskRecord{RunID: "run_bundle", TaskID: "task_recon", Phase: "recon"}
	writeTaskBundleEvents(t, bundleDir,
		LedgerEvent{
			ID:        "event_runner_started",
			RunID:     task.RunID,
			Type:      "runner.started",
			Object:    "run",
			ObjectID:  task.RunID,
			TaskID:    task.TaskID,
			Timestamp: "2026-01-01T00:00:00Z",
			Data: map[string]any{
				"workspace": "/workspace",
			},
		},
		taskCompletedEvent(task),
	)

	_, err := validateTaskBundle(bundleDir, task)
	if err == nil {
		t.Fatal("expected run lifecycle event error")
	}
	if !strings.Contains(err.Error(), `type "runner.started" is not task-scoped`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func taskStartedEvent(task TaskRecord) LedgerEvent {
	return LedgerEvent{
		ID:        "event_started",
		RunID:     task.RunID,
		Type:      "task.started",
		Object:    "task",
		ObjectID:  task.TaskID,
		TaskID:    task.TaskID,
		Timestamp: "2026-01-01T00:00:00Z",
		Data: map[string]any{
			"phase": task.Phase,
		},
	}
}

func taskEvidenceAddedEvent(t *testing.T, bundleDir string, task TaskRecord, eventID, evidenceID, relPath string) LedgerEvent {
	t.Helper()
	return LedgerEvent{
		ID:        eventID,
		RunID:     task.RunID,
		Type:      "evidence.added",
		Object:    "evidence",
		ObjectID:  evidenceID,
		TaskID:    task.TaskID,
		Timestamp: "2026-01-01T00:00:00Z",
		Data: map[string]any{
			"kind":           "markdown",
			"title":          "Evidence",
			"path":           relPath,
			"content_sha256": taskBundleFileSHA256ForTest(t, bundleDir, relPath),
		},
	}
}

func taskCompletedEvent(task TaskRecord) LedgerEvent {
	return LedgerEvent{
		ID:        "event_done",
		RunID:     task.RunID,
		Type:      "task.completed",
		Object:    "task",
		ObjectID:  task.TaskID,
		TaskID:    task.TaskID,
		Timestamp: "2026-01-01T00:00:01Z",
		Data: map[string]any{
			"status":  "completed",
			"summary": "done",
		},
	}
}

func taskBundleFileSHA256ForTest(t *testing.T, bundleDir, relPath string) string {
	t.Helper()
	digest, err := fileDigestHex(filepath.Join(bundleDir, filepath.FromSlash(relPath)))
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func writeTaskBundleFile(t *testing.T, bundleDir, relPath, contents string) {
	t.Helper()
	path := filepath.Join(bundleDir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), filePerm); err != nil {
		t.Fatal(err)
	}
}

func writeTaskBundleEvents(t *testing.T, bundleDir string, events ...LedgerEvent) {
	t.Helper()
	if err := os.MkdirAll(bundleDir, dirPerm); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bundleDir, eventsFile)
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(append(line, '\n')); err != nil {
			t.Fatal(err)
		}
	}
}
