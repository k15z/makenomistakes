package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/tabwriter"
)

func runsCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runs", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "write runs as JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("runs accepts at most one path")
	}

	workspace := "."
	if flags.NArg() == 1 {
		workspace = flags.Arg(0)
	}
	workspaceDir, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	runs, err := loadWorkspaceRuns(workspaceDir)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeRunsJSON(stdout, runs)
	}
	return writeRunsText(stdout, workspaceDir, runs)
}

func loadWorkspaceRuns(workspaceDir string) ([]RunRecord, error) {
	dbPath := filepath.Join(workspaceDir, ".mnm", "mnm.sqlite")
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	store, err := openStore(dbPath)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return store.ListRuns()
}

func writeRunsText(stdout io.Writer, workspaceDir string, runs []RunRecord) error {
	if len(runs) == 0 {
		fmt.Fprintf(stdout, "no runs found in %s\n", workspaceDir)
		return nil
	}
	table := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "RUN ID\tSTATUS\tRESUMABLE\tUPDATED\tRUN DIR")
	for _, run := range runs {
		fmt.Fprintf(table, "%s\t%s\t%t\t%s\t%s\n",
			run.ID,
			run.Status,
			resumableRunStatus(run.Status),
			run.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			run.RunDir,
		)
	}
	return table.Flush()
}

func writeRunsJSON(stdout io.Writer, runs []RunRecord) error {
	type runJSON struct {
		ID                 string `json:"id"`
		Status             string `json:"status"`
		WorkspaceDir       string `json:"workspace_dir"`
		WorkspaceRoot      string `json:"workspace_root"`
		ConfigPath         string `json:"config_path"`
		ConfigSnapshotPath string `json:"config_snapshot_path"`
		SnapshotPath       string `json:"snapshot_path"`
		RunDir             string `json:"run_dir"`
		Model              string `json:"model"`
		CreatedAt          string `json:"created_at"`
		UpdatedAt          string `json:"updated_at"`
		Resumable          bool   `json:"resumable"`
	}
	out := struct {
		Runs []runJSON `json:"runs"`
	}{Runs: make([]runJSON, 0, len(runs))}
	for _, run := range runs {
		out.Runs = append(out.Runs, runJSON{
			ID:                 run.ID,
			Status:             run.Status,
			WorkspaceDir:       run.WorkspaceDir,
			WorkspaceRoot:      run.WorkspaceRoot,
			ConfigPath:         run.ConfigPath,
			ConfigSnapshotPath: run.ConfigSnapshotPath,
			SnapshotPath:       run.SnapshotPath,
			RunDir:             run.RunDir,
			Model:              run.Model,
			CreatedAt:          run.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			UpdatedAt:          run.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			Resumable:          resumableRunStatus(run.Status),
		})
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(out)
}
