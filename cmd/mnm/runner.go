package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type AnalyzeRunner interface {
	Run(context.Context, RunnerRequest) error
}

type RunnerRequest struct {
	Run    RunRecord
	Config Config
	KeepVM bool
}

func runnerCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run-id", "", "run id")
	runDir := flags.String("run-dir", "", "run directory")
	snapshot := flags.String("snapshot", "", "workspace snapshot")
	configPath := flags.String("config", "", "run config snapshot")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *runID == "" || *runDir == "" || *snapshot == "" || *configPath == "" {
		return errors.New("runner requires --run-id, --run-dir, --snapshot, and --config")
	}
	if err := os.MkdirAll(filepath.Join(*runDir, "evidence"), dirPerm); err != nil {
		return err
	}
	if _, err := loadConfig(*configPath); err != nil {
		return err
	}

	workspace := filepath.Join(os.TempDir(), "mnm-workspace-"+*runID)
	if err := os.RemoveAll(workspace); err != nil {
		return err
	}
	if err := os.MkdirAll(workspace, dirPerm); err != nil {
		return err
	}
	if err := extractWorkspaceSnapshot(*snapshot, workspace); err != nil {
		return err
	}

	if err := appendLedgerEvent(*runDir, LedgerEvent{
		RunID:    *runID,
		Type:     "runner.started",
		Object:   "run",
		ObjectID: *runID,
		Data: map[string]any{
			"workspace": workspace,
		},
	}); err != nil {
		return err
	}

	manifestPath := filepath.Join(*runDir, "evidence", "runner-manifest.json")
	if err := writeRunnerManifest(manifestPath, *runID, workspace); err != nil {
		return err
	}
	if err := appendLedgerEvent(*runDir, LedgerEvent{
		RunID:    *runID,
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: newLedgerID("evidence"),
		Data: map[string]any{
			"kind":  "json",
			"title": "Runner lifecycle manifest",
			"path":  "evidence/runner-manifest.json",
		},
	}); err != nil {
		return err
	}
	if err := appendLedgerEvent(*runDir, LedgerEvent{
		RunID:    *runID,
		Type:     "runner.completed",
		Object:   "run",
		ObjectID: *runID,
		Data: map[string]any{
			"workspace": workspace,
		},
	}); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "runner completed for %s\n", *runID)
	return nil
}

func writeRunnerManifest(path, runID, workspace string) error {
	entries, err := workspaceFileList(workspace)
	if err != nil {
		return err
	}
	manifest := map[string]any{
		"run_id":          runID,
		"workspace":       workspace,
		"workspace_files": entries,
	}
	return writeJSON(path, manifest)
}

func workspaceFileList(root string) ([]string, error) {
	var entries []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root || entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries = append(entries, filepath.ToSlash(rel))
		return nil
	})
	return entries, err
}
