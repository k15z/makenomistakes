package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type AttemptPipelineRunner struct {
	AttemptRunner                opencodeTaskAttemptRunner
	Stdout                       io.Writer
	Stderr                       io.Writer
	ManifestOpenCodePath         string
	ManifestOpenCodeVersion      string
	BootstrapWorkspaceToolchains bool
}

type HostPipelineRunner struct {
	TaskRunner LimaRunner
	Stdout     io.Writer
	Stderr     io.Writer
}

func (runner HostPipelineRunner) Preflight(ctx context.Context, request RunnerPreflightRequest) error {
	taskRunner := runner.taskRunner()
	return taskRunner.Preflight(ctx, request)
}

func (runner HostPipelineRunner) Run(ctx context.Context, request RunnerRequest) error {
	taskRunner := runner.taskRunner()
	return AttemptPipelineRunner{
		AttemptRunner: LimaTaskAttemptRunner{
			Runner:       taskRunner,
			Config:       request.Config.Runner,
			SnapshotPath: request.Run.SnapshotPath,
			ModelAPIKey:  request.ModelAPIKey,
			ModelAuth:    request.ModelAuth,
			KeepVM:       request.KeepVM,
		},
		Stdout:                  runner.Stdout,
		Stderr:                  runner.Stderr,
		ManifestOpenCodePath:    "task-vm",
		ManifestOpenCodeVersion: "task-vm",
	}.Run(ctx, request)
}

func (runner HostPipelineRunner) taskRunner() LimaRunner {
	taskRunner := runner.TaskRunner
	if taskRunner.Executor == nil {
		taskRunner.Executor = ShellExecutor{Stdout: runner.Stdout, Stderr: runner.Stderr}
	}
	if taskRunner.Stdout == nil {
		taskRunner.Stdout = runner.Stdout
	}
	if taskRunner.Stderr == nil {
		taskRunner.Stderr = runner.Stderr
	}
	return taskRunner
}

func (runner AttemptPipelineRunner) Run(ctx context.Context, request RunnerRequest) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if runner.AttemptRunner == nil {
		return errors.New("pipeline attempt runner is required")
	}
	stopAfterPhase, err := normalizeStopAfterPhase(request.StopAfterPhase)
	if err != nil {
		return err
	}
	run := request.Run
	if run.ID == "" || run.RunDir == "" || run.SnapshotPath == "" || run.ConfigSnapshotPath == "" {
		return errors.New("pipeline runner requires run id, run dir, snapshot, and config snapshot")
	}
	if err := validateRunnerRunID(run.ID); err != nil {
		return err
	}
	stdout := runner.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	if err := os.MkdirAll(filepath.Join(run.RunDir, "evidence"), dirPerm); err != nil {
		return err
	}
	failure := runnerFailureContext{
		RunID:           run.ID,
		RunDir:          run.RunDir,
		Stage:           "load_config",
		OpenCodePath:    runner.manifestOpenCodePath(),
		OpenCodeVersion: runner.manifestOpenCodeVersion(),
	}
	defer func() {
		if err == nil {
			return
		}
		if recordErr := failure.Record(err); recordErr != nil {
			err = errors.Join(err, fmt.Errorf("record runner failure: %w", recordErr))
		}
	}()

	cfg, err := loadConfig(run.ConfigSnapshotPath)
	if err != nil {
		return err
	}

	failure.Stage = "extract_snapshot"
	workspace := filepath.Join(os.TempDir(), "mnm-workspace-"+run.ID)
	failure.Workspace = workspace
	if err := os.RemoveAll(workspace); err != nil {
		return err
	}
	if err := os.MkdirAll(workspace, dirPerm); err != nil {
		return err
	}
	if err := extractWorkspaceSnapshot(run.SnapshotPath, workspace); err != nil {
		return err
	}
	if runner.BootstrapWorkspaceToolchains {
		failure.Stage = "toolchain_bootstrap"
		if err := ensureWorkspaceToolchains(workspace); err != nil {
			return err
		}
	}

	failure.Stage = "runner_manifest"
	if err := writeAndRegisterRunnerManifest(run.RunDir, run.ID, workspace, runner.manifestOpenCodePath(), runner.manifestOpenCodeVersion()); err != nil {
		return err
	}

	failure.Stage = "runner_started_event"
	if err := appendLedgerEvent(run.RunDir, LedgerEvent{
		RunID:    run.ID,
		Type:     "runner.started",
		Object:   "run",
		ObjectID: run.ID,
		Data: map[string]any{
			"workspace": workspace,
		},
	}); err != nil {
		return err
	}

	if err := runner.runPhases(ctx, run, workspace, cfg, stopAfterPhase, &failure, stdout); err != nil {
		return err
	}
	return nil
}

func (runner AttemptPipelineRunner) runPhases(ctx context.Context, run RunRecord, workspace string, cfg Config, stopAfterPhase string, failure *runnerFailureContext, stdout io.Writer) error {
	failure.Stage = "recon"
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := runReconTaskWithAttemptRunnerContext(ctx, run.RunDir, run.ID, workspace, cfg, runner.AttemptRunner); err != nil {
		return err
	}
	if shouldStopAfterPhase(stopAfterPhase, "recon") {
		return recordRunnerStopped(run.RunDir, run.ID, workspace, "recon", stdout)
	}
	failure.Stage = "investigate"
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := runInvestigatePhaseWithAttemptRunnerContext(ctx, run.RunDir, run.ID, workspace, cfg, runner.AttemptRunner); err != nil {
		return err
	}
	if shouldStopAfterPhase(stopAfterPhase, "investigate") {
		return recordRunnerStopped(run.RunDir, run.ID, workspace, "investigate", stdout)
	}
	failure.Stage = "review"
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := runReviewPhaseWithAttemptRunnerContext(ctx, run.RunDir, run.ID, workspace, cfg, runner.AttemptRunner); err != nil {
		return err
	}
	if shouldStopAfterPhase(stopAfterPhase, "review") {
		return recordRunnerStopped(run.RunDir, run.ID, workspace, "review", stdout)
	}
	failure.Stage = "deduplicate"
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := runDeduplicatePhaseWithAttemptRunnerContext(ctx, run.RunDir, run.ID, workspace, cfg, runner.AttemptRunner); err != nil {
		return err
	}
	if shouldStopAfterPhase(stopAfterPhase, "deduplicate") {
		return recordRunnerStopped(run.RunDir, run.ID, workspace, "deduplicate", stdout)
	}
	failure.Stage = "validate"
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := runValidatePhaseWithAttemptRunnerContext(ctx, run.RunDir, run.ID, workspace, cfg, runner.AttemptRunner); err != nil {
		return err
	}
	if shouldStopAfterPhase(stopAfterPhase, "validate") {
		return recordRunnerStopped(run.RunDir, run.ID, workspace, "validate", stdout)
	}
	failure.Stage = "finalize"
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := runFinalizeTaskWithAttemptRunnerContext(ctx, run.RunDir, run.ID, workspace, cfg, runner.AttemptRunner); err != nil {
		return err
	}

	failure.Stage = "runner_completed_event"
	if err := appendLedgerEvent(run.RunDir, LedgerEvent{
		RunID:    run.ID,
		Type:     "runner.completed",
		Object:   "run",
		ObjectID: run.ID,
		Data: map[string]any{
			"workspace": workspace,
		},
	}); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "runner completed for %s\n", run.ID)
	return nil
}

func (runner AttemptPipelineRunner) manifestOpenCodePath() string {
	if runner.ManifestOpenCodePath != "" {
		return runner.ManifestOpenCodePath
	}
	return "attempt-runner"
}

func (runner AttemptPipelineRunner) manifestOpenCodeVersion() string {
	if runner.ManifestOpenCodeVersion != "" {
		return runner.ManifestOpenCodeVersion
	}
	return "attempt-runner"
}
