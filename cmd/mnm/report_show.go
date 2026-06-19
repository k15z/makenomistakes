package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func reportShowCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("report show", flag.ContinueOnError)
	flags.SetOutput(stderr)
	jsonOutput := flags.Bool("json", false, "show report.json instead of report.md")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() < 1 || flags.NArg() > 2 {
		return errors.New("report show requires RUN_ID and accepts at most one workspace path")
	}
	workspace := "."
	if flags.NArg() == 2 {
		workspace = flags.Arg(1)
	}
	workspaceDir, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}

	run, err := loadStoredRun(workspaceDir, flags.Arg(0))
	if err != nil {
		return err
	}
	if same, err := samePath(run.WorkspaceDir, workspaceDir); err != nil {
		return err
	} else if !same {
		return fmt.Errorf("run %s belongs to workspace %s, not %s", run.ID, run.WorkspaceDir, workspaceDir)
	}

	reportPath, err := finalizedReportPath(run, *jsonOutput)
	if err != nil {
		return err
	}
	if err := rejectSymlink(reportPath); err != nil {
		return err
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		return err
	}
	_, err = stdout.Write(data)
	return err
}

func loadStoredRun(workspaceDir, runID string) (RunRecord, error) {
	dbPath := filepath.Join(workspaceDir, ".mnm", "mnm.sqlite")
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return RunRecord{}, fmt.Errorf("no mnm run store found in %s; run mnm analyze first", workspaceDir)
	} else if err != nil {
		return RunRecord{}, err
	}
	store, err := openStore(dbPath)
	if err != nil {
		return RunRecord{}, err
	}
	defer store.Close()
	run, err := store.GetRun(runID)
	if err != nil {
		return RunRecord{}, fmt.Errorf("load run %s: %w", runID, err)
	}
	return run, nil
}

func finalizedReportPath(run RunRecord, jsonOutput bool) (string, error) {
	report, ok, err := latestFinalizedReportForTask(run.RunDir, "task_finalize")
	if err != nil {
		return "", err
	}
	if !ok || !ledgerTaskCompleted(run.RunDir, "task_finalize") {
		return "", fmt.Errorf("run %s has no finalized report", run.ID)
	}
	reportRel := report.MarkdownPath
	label := "markdown"
	if jsonOutput {
		reportRel = report.JSONPath
		label = "JSON"
	}
	if reportRel == "" {
		return "", fmt.Errorf("finalized report %s is missing %s path", report.ID, label)
	}
	normalized, err := normalizeReportPath(run.RunDir, reportRel)
	if err != nil {
		return "", fmt.Errorf("%s report path: %w", label, err)
	}
	return filepath.Join(run.RunDir, filepath.FromSlash(normalized)), nil
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("report path must not be a symlink: %s", path)
	}
	return nil
}
