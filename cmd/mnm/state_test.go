package main

import (
	"database/sql"
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
		SnapshotPath:       filepath.Join(dir, ".mnm", "runs", "run_test", "snapshot.tar.zst"),
		RunDir:             filepath.Join(dir, ".mnm", "runs", "run_test"),
		Model:              "openrouter/deepseek/deepseek-v4-pro",
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

func TestStoreListsRunsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	store, err := openStore(filepath.Join(dir, "mnm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	older := testRunRecord(dir, "run_old", RunStatusStopped, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	newer := testRunRecord(dir, "run_new", RunStatusCompleted, time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC))
	if err := store.CreateRun(older); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRun(newer); err != nil {
		t.Fatal(err)
	}

	runs, err := store.ListRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("run count = %d, want 2", len(runs))
	}
	if runs[0].ID != "run_new" || runs[1].ID != "run_old" {
		t.Fatalf("runs not sorted newest first: %#v", runs)
	}
}

func TestStoreListsRunsNewestFirstWithFractionalSeconds(t *testing.T) {
	dir := t.TempDir()
	store, err := openStore(filepath.Join(dir, "mnm.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	exactSecond := testRunRecord(dir, "run_exact", RunStatusStopped, time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC))
	fractionalSecond := testRunRecord(dir, "run_fractional", RunStatusCompleted, time.Date(2026, 1, 1, 12, 0, 0, 100_000_000, time.UTC))
	if err := store.CreateRun(exactSecond); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRun(fractionalSecond); err != nil {
		t.Fatal(err)
	}

	runs, err := store.ListRuns()
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("run count = %d, want 2", len(runs))
	}
	if runs[0].ID != "run_fractional" || runs[1].ID != "run_exact" {
		t.Fatalf("runs not sorted by parsed timestamps: %#v", runs)
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

func TestStoreMigratesSnapshotColumns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mnm.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`create table runs (
		id text primary key,
		status text not null,
		workspace_dir text not null,
		workspace_root text not null,
		config_path text not null,
		run_dir text not null,
		model text not null,
		created_at text not null,
		updated_at text not null
	)`)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Round(0).Format(time.RFC3339Nano)
	_, err = db.Exec(`insert into runs (
		id,
		status,
		workspace_dir,
		workspace_root,
		config_path,
		run_dir,
		model,
		created_at,
		updated_at
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"run_old",
		RunStatusCreated,
		dir,
		dir,
		filepath.Join(dir, "mnm.toml"),
		filepath.Join(dir, ".mnm", "runs", "run_old"),
		"openrouter/deepseek/deepseek-v4-pro",
		now,
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := openStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	got, err := store.GetRun("run_old")
	if err != nil {
		t.Fatal(err)
	}
	if got.ConfigSnapshotPath != "" {
		t.Fatalf("expected empty migrated config snapshot path, got %q", got.ConfigSnapshotPath)
	}
	if got.SnapshotPath != "" {
		t.Fatalf("expected empty migrated snapshot path, got %q", got.SnapshotPath)
	}
}

func testRunRecord(workspace, id, status string, timestamp time.Time) RunRecord {
	return RunRecord{
		ID:                 id,
		Status:             status,
		WorkspaceDir:       workspace,
		WorkspaceRoot:      workspace,
		ConfigPath:         filepath.Join(workspace, "mnm.toml"),
		ConfigSnapshotPath: filepath.Join(workspace, ".mnm", "runs", id, "mnm.toml"),
		SnapshotPath:       filepath.Join(workspace, ".mnm", "runs", id, "snapshot.tar.zst"),
		RunDir:             filepath.Join(workspace, ".mnm", "runs", id),
		Model:              "openrouter/deepseek/deepseek-v4-pro",
		CreatedAt:          timestamp,
		UpdatedAt:          timestamp,
	}
}
