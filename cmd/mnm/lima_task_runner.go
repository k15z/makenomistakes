package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	maxLimaTaskInstanceNameLen = 48

	guestTaskSnapshotPath = "/tmp/snapshot.tar.zst"
	guestTaskLedgerDir    = "/tmp/mnm-ledger"
	guestTaskOutputDir    = "/tmp/mnm-output"
	guestTaskPromptPath   = "/tmp/mnm-prompt.md"
	guestTaskWorkspaceDir = "/tmp/mnm-workspace"
)

type LimaTaskRequest struct {
	RunID        string
	Task         TaskRecord
	Attempt      int
	Config       RunnerConfig
	SnapshotPath string
	LedgerDir    string
	OutputDir    string
	PromptPath   string
	LogRelPath   string
	Model        string
	ModelAPIKey  string
	KeepVM       bool
	SkipVerify   bool
}

type LimaTaskAttemptRunner struct {
	Runner       LimaRunner
	Config       RunnerConfig
	SnapshotPath string
	ModelAPIKey  string
	KeepVM       bool
}

func (runner LimaTaskAttemptRunner) RunOpenCodeTaskAttempt(ctx context.Context, workspace, runDir string, task opencodeTask, attempt int) (openCodeAttemptResult, error) {
	result := openCodeAttemptResult{TaskRunDir: runDir}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !task.usesTaskBundle() {
		return result, errors.New("Lima task attempts require task bundle metadata")
	}
	outputDir, _, err := prepareOpenCodeTaskBundleAttempt(runDir, task, attempt)
	if err != nil {
		return result, err
	}
	result = openCodeAttemptResult{TaskRunDir: outputDir, Bundle: true}
	promptPath := filepath.Join(outputDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte(taskVMPrompt(task.Prompt, workspace, runDir)), filePerm); err != nil {
		return result, err
	}
	logRelPath, err := taskLogRelPath(runDir, task)
	if err != nil {
		return result, err
	}
	ledgerDir, cleanupLedgerDir, err := prepareRunnerTaskLedgerSnapshot(runDir)
	if err != nil {
		return result, err
	}
	defer cleanupLedgerDir()
	runErr := runner.Runner.RunTask(ctx, LimaTaskRequest{
		RunID:        task.RunID,
		Task:         task.taskRecord(),
		Attempt:      attempt,
		Config:       runner.Config,
		SnapshotPath: runner.SnapshotPath,
		LedgerDir:    ledgerDir,
		OutputDir:    outputDir,
		PromptPath:   promptPath,
		LogRelPath:   logRelPath,
		Model:        task.Model,
		ModelAPIKey:  runner.ModelAPIKey,
		KeepVM:       runner.KeepVM,
		SkipVerify:   true,
	})
	logText, logErr := copyLimaAttemptLog(outputDir, logRelPath, task.LogPath)
	if runErr != nil {
		if logErr != nil {
			runErr = errors.Join(runErr, logErr)
		}
		return result, openCodeAttemptError{
			err:            runErr,
			logText:        logText,
			ledgerModified: false,
		}
	}
	if logErr != nil {
		return result, logErr
	}
	return result, nil
}

func taskVMPrompt(prompt, workspace, runDir string) string {
	rebasedPrompt := strings.ReplaceAll(prompt, workspace, guestTaskWorkspaceDir)
	rebasedPrompt = rewriteTaskBundlePromptPaths(rebasedPrompt, runDir, guestTaskOutputDir, guestTaskLedgerDir)
	return taskBundlePromptForDirs(rebasedPrompt, guestTaskOutputDir, guestTaskLedgerDir)
}

func taskLogRelPath(runDir string, task opencodeTask) (string, error) {
	if strings.TrimSpace(task.LogPath) == "" {
		return filepath.ToSlash(filepath.Join("evidence", "opencode-"+safeFileID(task.TaskID)+".jsonl")), nil
	}
	absRunDir, err := filepath.Abs(runDir)
	if err != nil {
		return "", err
	}
	absLogPath, err := filepath.Abs(task.LogPath)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRunDir, absLogPath)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("opencode log path must point inside run directory: %s", task.LogPath)
	}
	return filepath.ToSlash(rel), nil
}

func copyLimaAttemptLog(outputDir, logRelPath, hostLogPath string) (string, error) {
	attemptLogPath := filepath.Join(outputDir, filepath.FromSlash(logRelPath))
	logText := readLogSuffix(attemptLogPath, 0)
	if hostLogPath == "" {
		return logText, nil
	}
	if _, err := os.Stat(attemptLogPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return logText, nil
		}
		return logText, err
	}
	if err := os.MkdirAll(filepath.Dir(hostLogPath), dirPerm); err != nil {
		return logText, err
	}
	if err := copyFileMode(attemptLogPath, hostLogPath, filePerm); err != nil {
		return logText, err
	}
	return logText, nil
}

func (runner LimaRunner) RunTask(ctx context.Context, request LimaTaskRequest) error {
	if runner.Executor == nil {
		return errors.New("runner executor is required")
	}
	if err := validateLimaTaskRequest(request); err != nil {
		return err
	}
	payloadBuildDir, err := os.MkdirTemp("", "mnm-task-payload-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(payloadBuildDir)
	payloadPath, cleanupPayload, err := buildLinuxRunnerPayload(payloadBuildDir)
	if err != nil {
		return err
	}
	defer cleanupPayload()

	instanceName := limaTaskInstanceName(request.RunID, request.Task.TaskID, request.Attempt)
	if err := runner.Executor.Run(ctx, "limactl", "delete", "--force", "--tty=false", instanceName); err != nil {
		fmt.Fprintf(runner.Stderr, "mnm: ignoring pre-create cleanup error: %v\n", err)
	}

	cpus := strconv.Itoa(request.Config.CPUs)
	memory := strconv.Itoa(request.Config.MemoryGB)
	disk := strconv.Itoa(request.Config.DiskGB)
	if err := runner.Executor.Run(ctx,
		"limactl", "create", "--tty=false",
		"--name", instanceName,
		"--cpus", cpus,
		"--memory", memory,
		"--disk", disk,
		"template:docker",
	); err != nil {
		return err
	}

	defer func() {
		_ = runner.Executor.Run(context.Background(), "limactl", "stop", "--tty=false", instanceName)
		if !request.KeepVM {
			_ = runner.Executor.Run(context.Background(), "limactl", "delete", "--force", "--tty=false", instanceName)
		}
	}()

	if err := runner.Executor.Run(ctx, "limactl", "start", "--tty=false", instanceName); err != nil {
		return err
	}
	if err := runner.copyTaskInputs(ctx, instanceName, payloadPath, request); err != nil {
		return err
	}
	if err := runner.runGuestTask(ctx, instanceName, request); err != nil {
		_ = runner.copyTaskOutput(context.Background(), instanceName, request.OutputDir)
		return err
	}
	return runner.copyTaskOutput(ctx, instanceName, request.OutputDir)
}

func validateLimaTaskRequest(request LimaTaskRequest) error {
	if request.RunID == "" {
		return errors.New("task VM request run id is required")
	}
	if request.Task.RunID == "" || request.Task.TaskID == "" || request.Task.Phase == "" {
		return errors.New("task VM request task must include run_id, task_id, and phase")
	}
	if request.Task.RunID != request.RunID {
		return fmt.Errorf("task VM request task run_id %q does not match run id %q", request.Task.RunID, request.RunID)
	}
	if request.SnapshotPath == "" || request.LedgerDir == "" || request.OutputDir == "" || request.PromptPath == "" || request.Model == "" {
		return errors.New("task VM request requires snapshot, ledger dir, output dir, prompt path, and model")
	}
	if request.Attempt <= 0 {
		return errors.New("task VM request attempt must be greater than zero")
	}
	if request.Config.CPUs <= 0 || request.Config.MemoryGB <= 0 || request.Config.DiskGB <= 0 {
		return errors.New("task VM request runner cpus, memory_gb, and disk_gb must be greater than zero")
	}
	return nil
}

func (runner LimaRunner) copyTaskInputs(ctx context.Context, instanceName, payloadPath string, request LimaTaskRequest) error {
	if err := runner.Executor.Run(ctx, "limactl", "shell", instanceName, "bash", "-lc", "rm -rf /tmp/mnm-ledger /tmp/mnm-output /tmp/mnm-workspace && mkdir -p /tmp/mnm-ledger /tmp/mnm-output /tmp/mnm-workspace"); err != nil {
		return err
	}
	copies := [][2]string{
		{payloadPath, instanceName + ":/tmp/mnm"},
		{request.SnapshotPath, instanceName + ":" + guestTaskSnapshotPath},
		{request.PromptPath, instanceName + ":" + guestTaskPromptPath},
		{filepath.Clean(request.LedgerDir) + string(filepath.Separator) + ".", instanceName + ":" + guestTaskLedgerDir},
		{filepath.Clean(request.OutputDir) + string(filepath.Separator) + ".", instanceName + ":" + guestTaskOutputDir},
	}
	for _, item := range copies {
		if err := runner.Executor.Run(ctx, "limactl", "copy", "--backend=scp", "-r", item[0], item[1]); err != nil {
			return err
		}
	}
	if request.ModelAPIKey != "" {
		authPath, cleanup, err := writeOpenCodeAuthFile(request.ModelAPIKey)
		if err != nil {
			return err
		}
		defer cleanup()
		if err := runner.Executor.Run(ctx, "limactl", "copy", "--backend=scp", authPath, instanceName+":/tmp/opencode-auth.json"); err != nil {
			return err
		}
	}
	return nil
}

func (runner LimaRunner) runGuestTask(ctx context.Context, instanceName string, request LimaTaskRequest) error {
	return runner.Executor.Run(ctx, "limactl", "shell", instanceName, "bash", "-lc", guestTaskRunnerCommand(request))
}

func guestTaskRunnerCommand(request LimaTaskRequest) string {
	timeoutMinutes := effectiveOpenCodeTaskTimeoutMinutes(Config{Runner: request.Config})
	taskFile := filepath.ToSlash(filepath.Join(guestTaskOutputDir, currentTaskFile))
	logRelPath := request.LogRelPath
	if logRelPath == "" {
		logRelPath = filepath.ToSlash(filepath.Join("evidence", "opencode-"+safeFileID(request.Task.TaskID)+".jsonl"))
	}
	runnerCommand := fmt.Sprintf(
		"/tmp/mnm runner task --run-dir %s --ledger-dir %s --workspace %s --snapshot %s --task-file %s --prompt-file %s --model %s --log-path %s --timeout-minutes %d",
		shellQuote(guestTaskOutputDir),
		shellQuote(guestTaskLedgerDir),
		shellQuote(guestTaskWorkspaceDir),
		shellQuote(guestTaskSnapshotPath),
		shellQuote(taskFile),
		shellQuote(guestTaskPromptPath),
		shellQuote(request.Model),
		shellQuote(logRelPath),
		timeoutMinutes,
	)
	if request.SkipVerify {
		runnerCommand += " --skip-bundle-verify"
	}
	return joinGuestTaskCommands(runnerCommand)
}

func joinGuestTaskCommands(runnerCommand string) string {
	return strings.Join([]string{
		"set -euo pipefail",
		"chmod +x /tmp/mnm",
		bootstrapAuditToolbeltCommand(),
		"mkdir -p \"$HOME/.local/share/opencode\"",
		"if [ -f /tmp/opencode-auth.json ]; then mv /tmp/opencode-auth.json \"$HOME/.local/share/opencode/auth.json\"; chmod 600 \"$HOME/.local/share/opencode/auth.json\"; fi",
		"mkdir -p /tmp/mnm-output /tmp/mnm-ledger /tmp/mnm-workspace",
		"rm -f /tmp/mnm-output/.events.lock /tmp/mnm-ledger/.events.lock",
		runnerCommand,
	}, "\n")
}

func (runner LimaRunner) copyTaskOutput(ctx context.Context, instanceName, outputDir string) error {
	tempDir, err := os.MkdirTemp("", "mnm-task-output-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	if err := runner.Executor.Run(ctx, "limactl", "copy", "--backend=scp", "-r", instanceName+":"+guestTaskOutputDir, tempDir); err != nil {
		return err
	}
	copiedOutputDir := filepath.Join(tempDir, filepath.Base(guestTaskOutputDir))
	if err := removeStaleLedgerLock(copiedOutputDir); err != nil {
		return err
	}
	return copyDirContents(copiedOutputDir, outputDir)
}

func limaTaskInstanceName(runID, taskID string, attempt int) string {
	name := limaInstanceName(runID + "-task-" + taskID)
	if attempt > 0 {
		name += "-a" + strconv.Itoa(attempt)
	}
	return shortenLimaTaskInstanceName(name)
}

func shortenLimaTaskInstanceName(name string) string {
	if len(name) <= maxLimaTaskInstanceNameLen {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	hash := fmt.Sprintf("%x", sum[:])[:8]
	keep := maxLimaTaskInstanceNameLen - len(hash) - 1
	prefix := strings.TrimRight(name[:keep], "-")
	if prefix == "" {
		prefix = "mnm-task"
	}
	return prefix + "-" + hash
}
