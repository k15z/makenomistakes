package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttemptPipelineRunnerStopsAfterRecon(t *testing.T) {
	source := t.TempDir()
	writeWorkspaceFile(t, source, "repo/app.go", "package main\n")
	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.zst")
	if err := createWorkspaceSnapshot(SnapshotOptions{
		WorkspaceRoot: source,
		WorkspaceDir:  source,
		OutputPath:    snapshot,
	}); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	configPath := filepath.Join(runDir, "mnm.toml")
	if err := os.WriteFile(configPath, []byte(defaultConfig()), filePerm); err != nil {
		t.Fatal(err)
	}

	attemptRunner := &reconPipelineAttemptRunner{}
	var stdout bytes.Buffer
	err := (AttemptPipelineRunner{
		AttemptRunner:           attemptRunner,
		Stdout:                  &stdout,
		ManifestOpenCodePath:    "test-attempt-runner",
		ManifestOpenCodeVersion: "test-version",
	}).Run(context.Background(), RunnerRequest{
		Run: RunRecord{
			ID:                 "run_pipeline",
			RunDir:             runDir,
			SnapshotPath:       snapshot,
			ConfigSnapshotPath: configPath,
		},
		StopAfterPhase: "recon",
	})
	if err != nil {
		t.Fatalf("pipeline runner failed: %v", err)
	}
	if !strings.Contains(stdout.String(), "runner stopped after recon for run_pipeline") {
		t.Fatalf("stdout missing stop message:\n%s", stdout.String())
	}
	if len(attemptRunner.calls) != 1 || attemptRunner.calls[0] != "task_recon" {
		t.Fatalf("attempt calls = %#v, want task_recon", attemptRunner.calls)
	}
	if !ledgerTaskCompleted(runDir, "task_recon") {
		t.Fatal("recon task should complete")
	}
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	types := eventTypes(events)
	if !contains(types, "runner.started") {
		t.Fatalf("missing runner.started in %#v", types)
	}
	if !contains(types, "runner.stopped") {
		t.Fatalf("missing runner.stopped in %#v", types)
	}
	if contains(types, "runner.completed") {
		t.Fatalf("runner.completed should not be written after stop-after recon: %#v", types)
	}
	for _, event := range events {
		if event.Type == "runner.stopped" && stringData(event.Data, "phase") != "recon" {
			t.Fatalf("unexpected runner.stopped event: %#v", event)
		}
	}
	manifest := readFile(t, filepath.Join(runDir, "evidence", "runner-manifest.json"))
	for _, want := range []string{
		"repo/app.go",
		`"opencode_path": "test-attempt-runner"`,
		`"opencode_version": "test-version"`,
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("manifest missing %q:\n%s", want, manifest)
		}
	}
}

func TestAttemptPipelineRunnerRecordsFailure(t *testing.T) {
	source := t.TempDir()
	writeWorkspaceFile(t, source, "repo/app.go", "package main\n")
	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.zst")
	if err := createWorkspaceSnapshot(SnapshotOptions{
		WorkspaceRoot: source,
		WorkspaceDir:  source,
		OutputPath:    snapshot,
	}); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	configPath := filepath.Join(runDir, "mnm.toml")
	if err := os.WriteFile(configPath, []byte(defaultConfig()), filePerm); err != nil {
		t.Fatal(err)
	}

	err := (AttemptPipelineRunner{
		AttemptRunner: failingAttemptRunner{},
	}).Run(context.Background(), RunnerRequest{
		Run: RunRecord{
			ID:                 "run_pipeline_failure",
			RunDir:             runDir,
			SnapshotPath:       snapshot,
			ConfigSnapshotPath: configPath,
		},
		StopAfterPhase: "recon",
	})
	if err == nil {
		t.Fatal("expected pipeline failure")
	}
	failure := readFile(t, filepath.Join(runDir, "evidence", "runner-failure.json"))
	for _, want := range []string{
		`"run_id": "run_pipeline_failure"`,
		`"stage": "recon"`,
		"attempt failed",
	} {
		if !strings.Contains(failure, want) {
			t.Fatalf("failure manifest missing %q:\n%s", want, failure)
		}
	}
}

func TestAttemptPipelineRunnerRejectsUnsafeRunID(t *testing.T) {
	err := (AttemptPipelineRunner{
		AttemptRunner: failingAttemptRunner{},
	}).Run(context.Background(), RunnerRequest{
		Run: RunRecord{
			ID:                 "../run_pipeline",
			RunDir:             t.TempDir(),
			SnapshotPath:       "snapshot.tar.zst",
			ConfigSnapshotPath: "mnm.toml",
		},
	})
	if err == nil {
		t.Fatal("expected invalid run id error")
	}
	if !strings.Contains(err.Error(), "run id") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAttemptPipelineRunnerCancelsCurrentAttempt(t *testing.T) {
	source := t.TempDir()
	writeWorkspaceFile(t, source, "repo/app.go", "package main\n")
	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.zst")
	if err := createWorkspaceSnapshot(SnapshotOptions{
		WorkspaceRoot: source,
		WorkspaceDir:  source,
		OutputPath:    snapshot,
	}); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	configPath := filepath.Join(runDir, "mnm.toml")
	if err := os.WriteFile(configPath, []byte(defaultConfig()), filePerm); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	attemptRunner := &cancelingPipelineAttemptRunner{cancel: cancel}
	err := (AttemptPipelineRunner{
		AttemptRunner:           attemptRunner,
		ManifestOpenCodePath:    "test-attempt-runner",
		ManifestOpenCodeVersion: "test-version",
	}).Run(ctx, RunnerRequest{
		Run: RunRecord{
			ID:                 "run_pipeline_cancel",
			RunDir:             runDir,
			SnapshotPath:       snapshot,
			ConfigSnapshotPath: configPath,
		},
		StopAfterPhase: "recon",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pipeline cancellation error = %v, want context.Canceled", err)
	}
	if !attemptRunner.sawCanceledContext {
		t.Fatal("attempt runner did not receive the canceled pipeline context")
	}
}

type reconPipelineAttemptRunner struct {
	calls []string
}

func (runner *reconPipelineAttemptRunner) RunOpenCodeTaskAttempt(_ context.Context, _ string, runDir string, task opencodeTask, attempt int) (openCodeAttemptResult, error) {
	runner.calls = append(runner.calls, task.TaskID)
	outputDir, _, err := prepareOpenCodeTaskBundleAttempt(runDir, task, attempt)
	result := openCodeAttemptResult{TaskRunDir: outputDir, Bundle: true}
	if err != nil {
		return result, err
	}
	if err := os.MkdirAll(filepath.Join(outputDir, "evidence"), dirPerm); err != nil {
		return result, err
	}
	if err := os.WriteFile(filepath.Join(outputDir, "evidence", "recon-codebase-map.md"), []byte("# Map\n"), filePerm); err != nil {
		return result, err
	}
	if err := os.WriteFile(filepath.Join(outputDir, "evidence", "recon-risk-register.md"), []byte("# Risks\n"), filePerm); err != nil {
		return result, err
	}
	if err := os.WriteFile(filepath.Join(outputDir, "evidence", "lead-auth.md"), []byte("# Lead\n"), filePerm); err != nil {
		return result, err
	}
	if task.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(task.LogPath), dirPerm); err != nil {
			return result, err
		}
		if err := os.WriteFile(task.LogPath, []byte(`{"type":"done"}`+"\n"), filePerm); err != nil {
			return result, err
		}
	}
	mapHash, err := fileDigestHex(filepath.Join(outputDir, "evidence", "recon-codebase-map.md"))
	if err != nil {
		return result, err
	}
	riskHash, err := fileDigestHex(filepath.Join(outputDir, "evidence", "recon-risk-register.md"))
	if err != nil {
		return result, err
	}
	return result, appendLedgerEvents(outputDir, []LedgerEvent{
		{
			RunID:    task.RunID,
			Type:     "evidence.added",
			Object:   "evidence",
			ObjectID: "evidence_recon_map",
			TaskID:   task.TaskID,
			Data: map[string]any{
				"kind":           "markdown",
				"title":          "Recon codebase map",
				"path":           "evidence/recon-codebase-map.md",
				"content_sha256": mapHash,
			},
		},
		{
			RunID:    task.RunID,
			Type:     "evidence.added",
			Object:   "evidence",
			ObjectID: "evidence_recon_risk",
			TaskID:   task.TaskID,
			Data: map[string]any{
				"kind":           "markdown",
				"title":          "Recon risk register",
				"path":           "evidence/recon-risk-register.md",
				"content_sha256": riskHash,
			},
		},
		{
			RunID:    task.RunID,
			Type:     "lead.created",
			Object:   "lead",
			ObjectID: "lead_auth",
			TaskID:   task.TaskID,
			Data: map[string]any{
				"title":     "Investigate auth",
				"category":  "authz",
				"priority":  "high",
				"body_path": "evidence/lead-auth.md",
			},
		},
		{
			RunID:    task.RunID,
			Type:     "task.completed",
			Object:   "task",
			ObjectID: task.TaskID,
			TaskID:   task.TaskID,
			Data: map[string]any{
				"status":  "completed",
				"summary": "done",
			},
		},
	})
}

type failingAttemptRunner struct{}

func (failingAttemptRunner) RunOpenCodeTaskAttempt(context.Context, string, string, opencodeTask, int) (openCodeAttemptResult, error) {
	return openCodeAttemptResult{}, errAttemptFailed{}
}

type errAttemptFailed struct{}

func (errAttemptFailed) Error() string { return "attempt failed" }

type cancelingPipelineAttemptRunner struct {
	cancel             context.CancelFunc
	sawCanceledContext bool
}

func (runner *cancelingPipelineAttemptRunner) RunOpenCodeTaskAttempt(ctx context.Context, _ string, _ string, _ opencodeTask, _ int) (openCodeAttemptResult, error) {
	runner.cancel()
	<-ctx.Done()
	runner.sawCanceledContext = errors.Is(ctx.Err(), context.Canceled)
	return openCodeAttemptResult{}, ctx.Err()
}
