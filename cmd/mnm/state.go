package main

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

const (
	RunStatusCreated      = "created"
	RunStatusSnapshotting = "snapshotting"
	RunStatusPrepared     = "prepared"
	RunStatusVMStarting   = "vm_starting"
	RunStatusRunning      = "running"
	RunStatusCompleted    = "completed"
	RunStatusFailed       = "failed"
	RunStatusTimedOut     = "timed_out"
)

var validRunStatuses = map[string]struct{}{
	RunStatusCreated:      {},
	RunStatusSnapshotting: {},
	RunStatusPrepared:     {},
	RunStatusVMStarting:   {},
	RunStatusRunning:      {},
	RunStatusCompleted:    {},
	RunStatusFailed:       {},
	RunStatusTimedOut:     {},
}

type Store struct {
	db *sql.DB
}

type RunRecord struct {
	ID                 string
	Status             string
	WorkspaceDir       string
	WorkspaceRoot      string
	ConfigPath         string
	ConfigSnapshotPath string
	SnapshotPath       string
	RunDir             string
	Model              string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	store := &Store{db: db}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`create table if not exists runs (
		id text primary key,
		status text not null,
		workspace_dir text not null,
		workspace_root text not null,
		config_path text not null,
		config_snapshot_path text not null,
		snapshot_path text not null,
		run_dir text not null,
		model text not null,
		created_at text not null,
		updated_at text not null
	)`)
	if err != nil {
		return err
	}
	for _, column := range []struct {
		name string
		ddl  string
	}{
		{name: "config_snapshot_path", ddl: "config_snapshot_path text not null default ''"},
		{name: "snapshot_path", ddl: "snapshot_path text not null default ''"},
	} {
		if err := s.ensureRunColumn(column.name, column.ddl); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureRunColumn(name, ddl string) error {
	rows, err := s.db.Query(`pragma table_info(runs)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var columnName string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int
		if err := rows.Scan(&cid, &columnName, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if columnName == name {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.Exec(`alter table runs add column ` + ddl)
	return err
}

func (s *Store) CreateRun(run RunRecord) error {
	if err := validateRunStatus(run.Status); err != nil {
		return err
	}
	_, err := s.db.Exec(`insert into runs (
		id,
		status,
		workspace_dir,
		workspace_root,
		config_path,
		config_snapshot_path,
		snapshot_path,
		run_dir,
		model,
		created_at,
		updated_at
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID,
		run.Status,
		run.WorkspaceDir,
		run.WorkspaceRoot,
		run.ConfigPath,
		run.ConfigSnapshotPath,
		run.SnapshotPath,
		run.RunDir,
		run.Model,
		run.CreatedAt.Format(time.RFC3339Nano),
		run.UpdatedAt.Format(time.RFC3339Nano),
	)
	return err
}

func (s *Store) UpdateRunStatus(runID, status string) error {
	if err := validateRunStatus(status); err != nil {
		return err
	}
	result, err := s.db.Exec(`update runs set status = ?, updated_at = ? where id = ?`,
		status,
		time.Now().UTC().Format(time.RFC3339Nano),
		runID,
	)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) GetRun(runID string) (RunRecord, error) {
	var run RunRecord
	var createdAt string
	var updatedAt string
	err := s.db.QueryRow(`select
		id,
		status,
		workspace_dir,
		workspace_root,
		config_path,
		config_snapshot_path,
		snapshot_path,
		run_dir,
		model,
		created_at,
		updated_at
		from runs where id = ?`, runID).Scan(
		&run.ID,
		&run.Status,
		&run.WorkspaceDir,
		&run.WorkspaceRoot,
		&run.ConfigPath,
		&run.ConfigSnapshotPath,
		&run.SnapshotPath,
		&run.RunDir,
		&run.Model,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return RunRecord{}, err
	}
	run.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return RunRecord{}, err
	}
	run.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	if err != nil {
		return RunRecord{}, err
	}
	return run, nil
}

func validateRunStatus(status string) error {
	if status == "" {
		return errors.New("run status must not be empty")
	}
	if _, ok := validRunStatuses[status]; !ok {
		return fmt.Errorf("invalid run status %q", status)
	}
	return nil
}
