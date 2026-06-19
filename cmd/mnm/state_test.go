package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreCreatesAndReadsRun(t *testing.T) {
	dir := t.TempDir()
	store, err := openStore(filepath.Join(dir, "mnm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now().UTC().Round(0)
	run := RunRecord{
		ID:                 "run_test",
		Status:             RunStatusCreated,
		WorkspaceDir:       dir,
		WorkspaceRoot:      dir,
		ConfigPath:         filepath.Join(dir, "mnm.toml"),
		ConfigSnapshotPath: filepath.Join(dir, ".mnm", "runs", "run_test", "mnm.toml"),
		RunDir:             filepath.Join(dir, ".mnm", "runs", "run_test"),
		Model:              "openrouter/z-ai/glm-5.2",
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := store.CreateRun(run); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateRunStatus(run.ID, RunStatusPrepared); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetRun(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != RunStatusPrepared {
		t.Fatalf("expected status %q, got %q", RunStatusPrepared, got.Status)
	}
	if got.Model != run.Model {
		t.Fatalf("expected model %q, got %q", run.Model, got.Model)
	}
}

func TestStoreRejectsInvalidStatus(t *testing.T) {
	dir := t.TempDir()
	store, err := openStore(filepath.Join(dir, "mnm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	err = store.UpdateRunStatus("missing", "not-a-status")
	if err == nil {
		t.Fatal("expected invalid status error")
	}
}
