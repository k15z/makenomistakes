package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunsCommandReportsNoRuns(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := run([]string{"runs", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("runs failed: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "no runs found in "+dir) {
		t.Fatalf("unexpected output:\n%s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".mnm", "mnm.sqlite")); !os.IsNotExist(err) {
		t.Fatalf("runs should not create a store when none exists, stat err=%v", err)
	}
}

func TestRunsCommandListsRuns(t *testing.T) {
	dir := t.TempDir()
	createStoredRun(t, dir, "run_stopped", RunStatusStopped, time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC))
	createStoredRun(t, dir, "run_completed", RunStatusCompleted, time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC))

	var stdout, stderr bytes.Buffer
	if err := run([]string{"runs", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("runs failed: %v\nstderr: %s", err, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{
		"RUN ID",
		"STATUS",
		"RESUMABLE",
		"UPDATED",
		"FAILURE",
		"RUN DIR",
		"run_stopped",
		RunStatusStopped,
		"true",
		"2026-01-03T10:00:00Z",
		"run_completed",
		RunStatusCompleted,
		"false",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("runs output missing %q:\n%s", want, output)
		}
	}
	if strings.Index(output, "run_stopped") > strings.Index(output, "run_completed") {
		t.Fatalf("runs output should be newest first:\n%s", output)
	}
}

func TestRunsCommandShowsRunnerFailure(t *testing.T) {
	dir := t.TempDir()
	record := createStoredRun(t, dir, "run_failed", RunStatusFailed, time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC))
	appendRunnerFailureForTest(t, record.RunDir, record.ID, "validate", "validation environment failed")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"runs", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("runs failed: %v\nstderr: %s", err, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{
		"run_failed",
		RunStatusFailed,
		"failed during validate",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("runs output missing %q:\n%s", want, output)
		}
	}
}

func TestRunsCommandIgnoresStaleRunnerFailureAfterCompletion(t *testing.T) {
	dir := t.TempDir()
	record := createStoredRun(t, dir, "run_completed", RunStatusCompleted, time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC))
	appendRunnerFailureForTest(t, record.RunDir, record.ID, "validate", "validation environment failed")
	if err := appendLedgerEvent(record.RunDir, LedgerEvent{
		RunID:    record.ID,
		Type:     "runner.completed",
		Object:   "run",
		ObjectID: record.ID,
		Data: map[string]any{
			"workspace": "/tmp/workspace",
		},
	}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{"runs", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("runs failed: %v\nstderr: %s", err, stderr.String())
	}
	output := stdout.String()
	if strings.Contains(output, "failed during validate") {
		t.Fatalf("runs output includes stale failure:\n%s", output)
	}
}

func TestRunsCommandListsRunsAsJSON(t *testing.T) {
	dir := t.TempDir()
	failed := createStoredRun(t, dir, "run_stopped", RunStatusStopped, time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC))
	createStoredRun(t, dir, "run_completed", RunStatusCompleted, time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC))
	appendRunnerFailureForTest(t, failed.RunDir, failed.ID, "recon", "recon did not complete")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"runs", "--json", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("runs failed: %v\nstderr: %s", err, stderr.String())
	}
	var parsed struct {
		Runs []struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			UpdatedAt string `json:"updated_at"`
			Resumable bool   `json:"resumable"`
			Failure   *struct {
				Stage string `json:"stage"`
				Error string `json:"error"`
				Path  string `json:"path"`
			} `json:"runner_failure"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &parsed); err != nil {
		t.Fatalf("invalid JSON output: %v\n%s", err, stdout.String())
	}
	if len(parsed.Runs) != 2 {
		t.Fatalf("JSON run count = %d, want 2", len(parsed.Runs))
	}
	if parsed.Runs[0].ID != "run_stopped" || !parsed.Runs[0].Resumable {
		t.Fatalf("first JSON run = %#v, want resumable run_stopped", parsed.Runs[0])
	}
	if parsed.Runs[0].Failure == nil || parsed.Runs[0].Failure.Stage != "recon" || parsed.Runs[0].Failure.Path != "evidence/runner-failure.json" {
		t.Fatalf("first JSON failure = %#v, want recon failure", parsed.Runs[0].Failure)
	}
	if parsed.Runs[1].ID != "run_completed" || parsed.Runs[1].Resumable {
		t.Fatalf("second JSON run = %#v, want non-resumable run_completed", parsed.Runs[1])
	}
	if parsed.Runs[1].Failure != nil {
		t.Fatalf("second JSON failure = %#v, want nil", parsed.Runs[1].Failure)
	}
}

func createStoredRun(t *testing.T, workspace, id, status string, timestamp time.Time) RunRecord {
	t.Helper()
	mnmDir := filepath.Join(workspace, ".mnm")
	if err := os.MkdirAll(mnmDir, dirPerm); err != nil {
		t.Fatal(err)
	}
	store, err := openStore(filepath.Join(mnmDir, "mnm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	run := testRunRecord(workspace, id, status, timestamp)
	if err := store.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	return run
}

func appendRunnerFailureForTest(t *testing.T, runDir, runID, stage, message string) {
	t.Helper()
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    runID,
		Type:     "runner.failed",
		Object:   "run",
		ObjectID: runID,
		Data: map[string]any{
			"stage": stage,
			"error": message,
			"path":  "evidence/runner-failure.json",
		},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunsCommandRejectsTooManyPaths(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"runs", t.TempDir(), t.TempDir()}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected runs argument error")
	}
	if !strings.Contains(err.Error(), "at most one path") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunsCommandDoesNotRequireConfig(t *testing.T) {
	dir := t.TempDir()
	createStoredRun(t, dir, "run_test", RunStatusPrepared, time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC))

	var stdout, stderr bytes.Buffer
	if err := run([]string{"runs", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("runs should not require mnm.toml: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "run_test") {
		t.Fatalf("runs output missing run:\n%s", stdout.String())
	}
}
