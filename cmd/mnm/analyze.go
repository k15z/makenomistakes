package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/google/uuid"
)

func analyzeCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("analyze", flag.ContinueOnError)
	flags.SetOutput(stderr)
	prepareOnly := flags.Bool("prepare-only", false, "prepare the run without starting task runners")
	keepVM := flags.Bool("keep-vm", false, "keep Lima task VMs after attempts exit")
	resumeRunID := flags.String("resume", "", "resume an existing prepared, stopped, timed_out, or failed run")
	stopAfterPhase := flags.String("stop-after", "", "stop cleanly after recon|investigate|review|deduplicate|validate")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *prepareOnly && *resumeRunID != "" {
		return errors.New("analyze --resume cannot be combined with --prepare-only")
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
		WorkspaceDir:   workspaceDir,
		PrepareOnly:    *prepareOnly,
		KeepVM:         *keepVM,
		ResumeRunID:    *resumeRunID,
		StopAfterPhase: *stopAfterPhase,
		Stdout:         stdout,
		Stderr:         stderr,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()
	return analyzeWorkspace(ctx, options, newDefaultRunner(stdout, stderr))
}

type AnalyzeOptions struct {
	WorkspaceDir   string
	PrepareOnly    bool
	KeepVM         bool
	ResumeRunID    string
	StopAfterPhase string
	Stdout         io.Writer
	Stderr         io.Writer
}

func analyzeWorkspace(ctx context.Context, options AnalyzeOptions, runner AnalyzeRunner) error {
	stopAfterPhase, err := normalizeStopAfterPhase(options.StopAfterPhase)
	if err != nil {
		return err
	}
	options.StopAfterPhase = stopAfterPhase
	if options.PrepareOnly && options.StopAfterPhase != "" {
		return errors.New("analyze --stop-after cannot be combined with --prepare-only")
	}
	workspaceDir := options.WorkspaceDir
	if options.ResumeRunID != "" {
		return resumeAnalyzeRun(ctx, options, runner)
	}
	cfg, err := loadConfig(filepath.Join(workspaceDir, "mnm.toml"))
	if err != nil {
		return err
	}
	resolved, err := cfg.validate(workspaceDir)
	if err != nil {
		return err
	}
	if !options.PrepareOnly {
		if err := preflightAnalyzeRunner(ctx, runner, RunnerPreflightRequest{Config: cfg}); err != nil {
			return err
		}
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
	if err := validateRunnerSetupInSnapshot(run.SnapshotPath, cfg.Runner.Setup); err != nil {
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

	return executePreparedRun(ctx, options, store, run, cfg, resolved, runner)
}

func resumeAnalyzeRun(ctx context.Context, options AnalyzeOptions, runner AnalyzeRunner) error {
	workspaceDir := options.WorkspaceDir
	mnmDir := filepath.Join(workspaceDir, ".mnm")
	store, err := openStore(filepath.Join(mnmDir, "mnm.sqlite"))
	if err != nil {
		return err
	}
	defer store.Close()

	run, err := store.GetRun(options.ResumeRunID)
	if err != nil {
		return fmt.Errorf("resume run %s: %w", options.ResumeRunID, err)
	}
	if !resumableRunStatus(run.Status) {
		return fmt.Errorf("run %s cannot be resumed from status %q", run.ID, run.Status)
	}
	if same, err := samePath(run.WorkspaceDir, workspaceDir); err != nil {
		return err
	} else if !same {
		return fmt.Errorf("run %s belongs to workspace %s, not %s", run.ID, run.WorkspaceDir, workspaceDir)
	}
	if _, err := os.Stat(run.RunDir); err != nil {
		return fmt.Errorf("resume run directory %s: %w", run.RunDir, err)
	}
	if _, err := os.Stat(run.SnapshotPath); err != nil {
		return fmt.Errorf("resume snapshot %s: %w", run.SnapshotPath, err)
	}
	if _, err := os.Stat(run.ConfigSnapshotPath); err != nil {
		return fmt.Errorf("resume config snapshot %s: %w", run.ConfigSnapshotPath, err)
	}

	cfg, err := loadConfig(run.ConfigSnapshotPath)
	if err != nil {
		return err
	}
	resolved, err := cfg.validate(workspaceDir)
	if err != nil {
		return err
	}
	if err := validateRunnerSetupInSnapshot(run.SnapshotPath, cfg.Runner.Setup); err != nil {
		return err
	}
	if err := preflightAnalyzeRunner(ctx, runner, RunnerPreflightRequest{
		Config: cfg,
		Resume: true,
	}); err != nil {
		return err
	}

	fmt.Fprintf(options.Stdout, "resuming run %s\n", run.ID)
	fmt.Fprintf(options.Stdout, "workspace: %s\n", run.WorkspaceRoot)
	fmt.Fprintf(options.Stdout, "snapshot: %s\n", run.SnapshotPath)
	fmt.Fprintf(options.Stdout, "run dir: %s\n", run.RunDir)
	return executePreparedRun(ctx, options, store, run, cfg, resolved, runner)
}

func executePreparedRun(ctx context.Context, options AnalyzeOptions, store *Store, run RunRecord, cfg Config, resolved ResolvedConfig, runner AnalyzeRunner) error {
	if err := store.UpdateRunStatus(run.ID, RunStatusVMStarting); err != nil {
		return err
	}
	fmt.Fprintf(options.Stdout, "starting runner\n")
	runCtx, cancel := context.WithTimeout(ctx, resolved.Timeout)
	defer cancel()
	if err := store.UpdateRunStatus(run.ID, RunStatusRunning); err != nil {
		return err
	}
	runnerDone := make(chan struct{})
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		select {
		case <-runCtx.Done():
			select {
			case <-runnerDone:
				return
			default:
			}
			switch {
			case errors.Is(runCtx.Err(), context.DeadlineExceeded):
				updateRunStatusUntilRunnerDone(store, run.ID, RunStatusTimedOut, runnerDone)
			case errors.Is(runCtx.Err(), context.Canceled):
				updateRunStatusUntilRunnerDone(store, run.ID, RunStatusStopping, runnerDone)
			}
		case <-runnerDone:
		}
	}()
	err := runner.Run(runCtx, RunnerRequest{
		Run:            run,
		Config:         cfg,
		ModelAPIKey:    os.Getenv(resolved.APIKeyEnv),
		ModelAuth:      resolved.ModelAuth,
		KeepVM:         options.KeepVM,
		Resume:         options.ResumeRunID != "",
		StopAfterPhase: options.StopAfterPhase,
	})
	close(runnerDone)
	<-monitorDone
	if err != nil {
		status := RunStatusFailed
		switch {
		case errors.Is(err, context.DeadlineExceeded) || errors.Is(runCtx.Err(), context.DeadlineExceeded):
			status = RunStatusTimedOut
		case errors.Is(err, context.Canceled) || errors.Is(runCtx.Err(), context.Canceled):
			status = RunStatusStopped
		}
		if updateErr := store.UpdateRunStatus(run.ID, status); updateErr != nil {
			return errors.Join(err, fmt.Errorf("update run status to %s: %w", status, updateErr))
		}
		return err
	}
	if options.StopAfterPhase != "" {
		if err := store.UpdateRunStatus(run.ID, RunStatusStopped); err != nil {
			return err
		}
		fmt.Fprintf(options.Stdout, "runner stopped after %s\n", options.StopAfterPhase)
		return nil
	}
	if err := store.UpdateRunStatus(run.ID, RunStatusCompleted); err != nil {
		return err
	}
	fmt.Fprintf(options.Stdout, "runner completed\n")
	return nil
}

func updateRunStatusUntilRunnerDone(store *Store, runID, status string, runnerDone <-chan struct{}) {
	for {
		select {
		case <-runnerDone:
			return
		default:
		}
		if err := store.UpdateRunStatus(runID, status); err == nil {
			return
		}
		select {
		case <-runnerDone:
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func preflightAnalyzeRunner(ctx context.Context, runner AnalyzeRunner, request RunnerPreflightRequest) error {
	preflightRunner, ok := runner.(AnalyzePreflightRunner)
	if !ok {
		return nil
	}
	return preflightRunner.Preflight(ctx, request)
}

func resumableRunStatus(status string) bool {
	return status == RunStatusPrepared ||
		status == RunStatusStopped ||
		status == RunStatusTimedOut ||
		status == RunStatusFailed
}

func samePath(a, b string) (bool, error) {
	absA, err := filepath.Abs(a)
	if err != nil {
		return false, err
	}
	absB, err := filepath.Abs(b)
	if err != nil {
		return false, err
	}
	return filepath.Clean(absA) == filepath.Clean(absB), nil
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
