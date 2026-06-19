package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
)

func analyzeCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("analyze", flag.ContinueOnError)
	flags.SetOutput(stderr)
	prepareOnly := flags.Bool("prepare-only", false, "prepare the run without starting the VM runner")
	keepVM := flags.Bool("keep-vm", false, "keep the Lima VM after the runner exits")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return errors.New("analyze accepts at most one path")
	}

	workspace := "."
	if flags.NArg() == 1 {
		workspace = flags.Arg(0)
	}
	workspaceDir, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	options := AnalyzeOptions{
		WorkspaceDir: workspaceDir,
		PrepareOnly:  *prepareOnly,
		KeepVM:       *keepVM,
		Stdout:       stdout,
		Stderr:       stderr,
	}
	return analyzeWorkspace(context.Background(), options, newDefaultRunner(stdout, stderr))
}

type AnalyzeOptions struct {
	WorkspaceDir string
	PrepareOnly  bool
	KeepVM       bool
	Stdout       io.Writer
	Stderr       io.Writer
}

func analyzeWorkspace(ctx context.Context, options AnalyzeOptions, runner AnalyzeRunner) error {
	workspaceDir := options.WorkspaceDir
	cfg, err := loadConfig(filepath.Join(workspaceDir, "mnm.toml"))
	if err != nil {
		return err
	}
	resolved, err := cfg.validate(workspaceDir)
	if err != nil {
		return err
	}

	mnmDir := filepath.Join(workspaceDir, ".mnm")
	if err := os.MkdirAll(mnmDir, dirPerm); err != nil {
		return err
	}
	store, err := openStore(filepath.Join(mnmDir, "mnm.sqlite"))
	if err != nil {
		return err
	}
	defer store.Close()

	runID := newRunID()
	runDir := filepath.Join(mnmDir, "runs", runID)
	if err := os.MkdirAll(runDir, dirPerm); err != nil {
		return err
	}
	configSnapshotPath := filepath.Join(runDir, "mnm.toml")
	if err := copyFile(resolved.ConfigPath, configSnapshotPath); err != nil {
		return err
	}

	now := time.Now().UTC()
	run := RunRecord{
		ID:                 runID,
		Status:             RunStatusCreated,
		WorkspaceDir:       workspaceDir,
		WorkspaceRoot:      resolved.WorkspaceRoot,
		ConfigPath:         resolved.ConfigPath,
		ConfigSnapshotPath: configSnapshotPath,
		SnapshotPath:       filepath.Join(runDir, "snapshot.tar.zst"),
		RunDir:             runDir,
		Model:              resolved.Model,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := store.CreateRun(run); err != nil {
		return err
	}
	if err := store.UpdateRunStatus(runID, RunStatusSnapshotting); err != nil {
		return err
	}
	if err := createWorkspaceSnapshot(SnapshotOptions{
		WorkspaceRoot: resolved.WorkspaceRoot,
		WorkspaceDir:  workspaceDir,
		OutputPath:    run.SnapshotPath,
		ConfigExclude: cfg.Workspace.Exclude,
	}); err != nil {
		return err
	}
	if err := store.UpdateRunStatus(runID, RunStatusPrepared); err != nil {
		return err
	}

	fmt.Fprintf(options.Stdout, "prepared run %s\n", runID)
	fmt.Fprintf(options.Stdout, "workspace: %s\n", resolved.WorkspaceRoot)
	fmt.Fprintf(options.Stdout, "snapshot: %s\n", run.SnapshotPath)
	fmt.Fprintf(options.Stdout, "run dir: %s\n", runDir)
	if options.PrepareOnly {
		return nil
	}

	if err := store.UpdateRunStatus(runID, RunStatusVMStarting); err != nil {
		return err
	}
	fmt.Fprintf(options.Stdout, "starting runner VM\n")
	runCtx, cancel := context.WithTimeout(ctx, resolved.Timeout)
	defer cancel()
	if err := store.UpdateRunStatus(runID, RunStatusRunning); err != nil {
		return err
	}
	if err := runner.Run(runCtx, RunnerRequest{
		Run:         run,
		Config:      cfg,
		ModelAPIKey: os.Getenv(resolved.APIKeyEnv),
		KeepVM:      options.KeepVM,
	}); err != nil {
		status := RunStatusFailed
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			status = RunStatusTimedOut
		}
		_ = store.UpdateRunStatus(runID, status)
		return err
	}
	if err := store.UpdateRunStatus(runID, RunStatusCompleted); err != nil {
		return err
	}
	fmt.Fprintf(options.Stdout, "runner completed\n")
	return nil
}

func newRunID() string {
	return "run_" + uuid.NewString()
}

func copyFile(src, dst string) error {
	input, err := os.Open(src)
	if err != nil {
		return err
	}
	defer input.Close()

	output, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePerm)
	if err != nil {
		return err
	}
	defer output.Close()

	_, err = output.ReadFrom(input)
	return err
}
