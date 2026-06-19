package main

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if err := run([]string{"analyze", dir}, &stdout, &stderr); err != nil {
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
	if err := db.QueryRow(`select config_snapshot_path from runs where status = ?`, RunStatusPrepared).Scan(&configSnapshotPath); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(configSnapshotPath, filepath.Join("mnm.toml")) {
		t.Fatalf("expected config snapshot path, got %q", configSnapshotPath)
	}
	if _, err := os.Stat(configSnapshotPath); err != nil {
		t.Fatalf("config snapshot not readable: %v", err)
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
