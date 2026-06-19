package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAnalyzePreparesRunState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"init", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	if err := run([]string{"analyze", "--prepare-only", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("analyze failed: %v\nstderr: %s", err, stderr.String())
	}

	output := stdout.String()
	if !strings.Contains(output, "prepared run run_") {
		t.Fatalf("stdout missing prepared run id:\n%s", output)
	}

	db, err := sql.Open("sqlite", filepath.Join(dir, ".mnm", "mnm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var count int
	if err := db.QueryRow(`select count(*) from runs where status = ?`, RunStatusPrepared).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one prepared run, got %d", count)
	}

	var configSnapshotPath string
	var snapshotPath string
	if err := db.QueryRow(`select config_snapshot_path, snapshot_path from runs where status = ?`, RunStatusPrepared).Scan(&configSnapshotPath, &snapshotPath); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(configSnapshotPath, filepath.Join("mnm.toml")) {
		t.Fatalf("expected config snapshot path, got %q", configSnapshotPath)
	}
	if _, err := os.Stat(configSnapshotPath); err != nil {
		t.Fatalf("config snapshot not readable: %v", err)
	}
	if !strings.HasSuffix(snapshotPath, "snapshot.tar.zst") {
		t.Fatalf("expected snapshot path, got %q", snapshotPath)
	}
	if _, err := os.Stat(snapshotPath); err != nil {
		t.Fatalf("snapshot not readable: %v", err)
	}
}

func TestAnalyzeRequiresConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	var stdout, stderr bytes.Buffer
	err := run([]string{"analyze", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing config error")
	}
	if !strings.Contains(err.Error(), "run mnm init first") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAnalyzeRequiresModelKey(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENROUTER_API_KEY", "")
	var stdout, stderr bytes.Buffer
	if err := run([]string{"init", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	err := run([]string{"analyze", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected missing model key error")
	}
	if !strings.Contains(err.Error(), "OPENROUTER_API_KEY") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAnalyzeRunsConfiguredRunnerByDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"init", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	runner := &recordingRunner{}
	err := analyzeWorkspace(t.Context(), AnalyzeOptions{
		WorkspaceDir: dir,
		Stdout:       &stdout,
		Stderr:       &stderr,
	}, runner)
	if err != nil {
		t.Fatalf("analyzeWorkspace failed: %v", err)
	}
	if !runner.called {
		t.Fatal("expected analyzeWorkspace to call runner")
	}

	db, err := sql.Open("sqlite", filepath.Join(dir, ".mnm", "mnm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`select count(*) from runs where status = ?`, RunStatusCompleted).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one completed run, got %d", count)
	}
}

func TestAnalyzeMarksDeadlineExceededRunsTimedOut(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"init", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	err := analyzeWorkspace(ctx, AnalyzeOptions{
		WorkspaceDir: dir,
		Stdout:       &stdout,
		Stderr:       &stderr,
	}, deadlineRunner{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(dir, ".mnm", "mnm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`select count(*) from runs where status = ?`, RunStatusTimedOut).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one timed out run, got %d", count)
	}
}

func TestAnalyzeMarksCanceledRunsStopped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	var stdout, stderr bytes.Buffer
	if err := run([]string{"init", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := analyzeWorkspace(ctx, AnalyzeOptions{
		WorkspaceDir: dir,
		Stdout:       &stdout,
		Stderr:       &stderr,
	}, canceledRunner{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context canceled, got %v", err)
	}

	db, err := sql.Open("sqlite", filepath.Join(dir, ".mnm", "mnm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`select count(*) from runs where status = ?`, RunStatusStopped).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one stopped run, got %d", count)
	}
}

type recordingRunner struct {
	called bool
}

func (runner *recordingRunner) Run(_ context.Context, request RunnerRequest) error {
	runner.called = true
	if request.Run.ID == "" {
		return errors.New("missing run id")
	}
	if _, err := os.Stat(request.Run.SnapshotPath); err != nil {
		return err
	}
	return nil
}

type deadlineRunner struct{}

func (deadlineRunner) Run(ctx context.Context, _ RunnerRequest) error {
	<-ctx.Done()
	return ctx.Err()
}

type canceledRunner struct{}

func (canceledRunner) Run(ctx context.Context, _ RunnerRequest) error {
	<-ctx.Done()
	return ctx.Err()
}
