package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const opencodeVersion = "1.17.8"

const openCodeMaxAttempts = 3

var openCodeRetryDelay = 2 * time.Second

type AnalyzeRunner interface {
	Run(context.Context, RunnerRequest) error
}

type AnalyzePreflightRunner interface {
	Preflight(context.Context, RunnerPreflightRequest) error
}

type RunnerPreflightRequest struct {
	Config Config
	Resume bool
}

type RunnerRequest struct {
	Run            RunRecord
	Config         Config
	ModelAPIKey    string
	ModelAuth      map[string]string
	KeepVM         bool
	Resume         bool
	StopAfterPhase string
}

func runnerCommand(args []string, stdout, stderr io.Writer) (err error) {
	return runnerCommandContext(context.Background(), args, stdout, stderr)
}

func runnerCommandContext(ctx context.Context, args []string, stdout, stderr io.Writer) (err error) {
	if len(args) > 0 && args[0] == "task" {
		return runnerTaskCommandContext(ctx, args[1:], stdout, stderr)
	}
	flags := flag.NewFlagSet("runner", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runID := flags.String("run-id", "", "run id")
	runDir := flags.String("run-dir", "", "run directory")
	snapshot := flags.String("snapshot", "", "workspace snapshot")
	configPath := flags.String("config", "", "run config snapshot")
	stopAfter := flags.String("stop-after", "", "stop cleanly after recon|investigate|review|deduplicate|validate")
	if err := flags.Parse(args); err != nil {
		return err
	}
	stopAfterPhase, err := normalizeStopAfterPhase(*stopAfter)
	if err != nil {
		return err
	}
	if *runID == "" || *runDir == "" || *snapshot == "" || *configPath == "" {
		return errors.New("runner requires --run-id, --run-dir, --snapshot, and --config")
	}
	if err := validateRunnerRunID(*runID); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(*runDir, "evidence"), dirPerm); err != nil {
		return err
	}
	failure := runnerFailureContext{
		RunID:  *runID,
		RunDir: *runDir,
		Stage:  "load_config",
	}
	defer func() {
		if err == nil {
			return
		}
		if recordErr := failure.Record(err); recordErr != nil {
			err = errors.Join(err, fmt.Errorf("record runner failure: %w", recordErr))
		}
	}()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}

	failure.Stage = "extract_snapshot"
	workspace := filepath.Join(os.TempDir(), "mnm-workspace-"+*runID)
	failure.Workspace = workspace
	if err := os.RemoveAll(workspace); err != nil {
		return err
	}
	if err := os.MkdirAll(workspace, dirPerm); err != nil {
		return err
	}
	if err := extractWorkspaceSnapshot(*snapshot, workspace); err != nil {
		return err
	}
	failure.Stage = "toolchain_bootstrap"
	if err := ensureWorkspaceToolchains(workspace); err != nil {
		return err
	}
	failure.Stage = "opencode_bootstrap"
	opencodePath, opencodeVersionOutput, err := ensureOpenCode()
	if err != nil {
		return err
	}
	failure.OpenCodePath = opencodePath
	failure.OpenCodeVersion = strings.TrimSpace(opencodeVersionOutput)

	failure.Stage = "runner_manifest"
	if err := writeAndRegisterRunnerManifest(*runDir, *runID, workspace, opencodePath, opencodeVersionOutput); err != nil {
		return err
	}

	failure.Stage = "runner_started_event"
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

	failure.Stage = "recon"
	if err := runReconTaskWithAttemptRunnerContext(ctx, *runDir, *runID, workspace, cfg, directOpenCodeTaskAttemptRunner{opencodePath: opencodePath}); err != nil {
		return err
	}
	if shouldStopAfterPhase(stopAfterPhase, "recon") {
		return recordRunnerStopped(*runDir, *runID, workspace, "recon", stdout)
	}
	failure.Stage = "investigate"
	if err := runInvestigatePhaseWithAttemptRunnerContext(ctx, *runDir, *runID, workspace, cfg, directOpenCodeTaskAttemptRunner{opencodePath: opencodePath}); err != nil {
		return err
	}
	if shouldStopAfterPhase(stopAfterPhase, "investigate") {
		return recordRunnerStopped(*runDir, *runID, workspace, "investigate", stdout)
	}
	failure.Stage = "review"
	if err := runReviewPhaseWithAttemptRunnerContext(ctx, *runDir, *runID, workspace, cfg, directOpenCodeTaskAttemptRunner{opencodePath: opencodePath}); err != nil {
		return err
	}
	if shouldStopAfterPhase(stopAfterPhase, "review") {
		return recordRunnerStopped(*runDir, *runID, workspace, "review", stdout)
	}
	failure.Stage = "deduplicate"
	if err := runDeduplicatePhaseWithAttemptRunnerContext(ctx, *runDir, *runID, workspace, cfg, directOpenCodeTaskAttemptRunner{opencodePath: opencodePath}); err != nil {
		return err
	}
	if shouldStopAfterPhase(stopAfterPhase, "deduplicate") {
		return recordRunnerStopped(*runDir, *runID, workspace, "deduplicate", stdout)
	}
	failure.Stage = "validate"
	if err := runValidatePhaseWithAttemptRunnerContext(ctx, *runDir, *runID, workspace, cfg, directOpenCodeTaskAttemptRunner{opencodePath: opencodePath}); err != nil {
		return err
	}
	if shouldStopAfterPhase(stopAfterPhase, "validate") {
		return recordRunnerStopped(*runDir, *runID, workspace, "validate", stdout)
	}
	failure.Stage = "finalize"
	if err := runFinalizeTaskWithAttemptRunnerContext(ctx, *runDir, *runID, workspace, cfg, directOpenCodeTaskAttemptRunner{opencodePath: opencodePath}); err != nil {
		return err
	}

	failure.Stage = "runner_completed_event"
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

func runnerTaskCommand(args []string, stdout, stderr io.Writer) error {
	return runnerTaskCommandContext(context.Background(), args, stdout, stderr)
}

func runnerTaskCommandContext(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("runner task", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runDir := flags.String("run-dir", "", "task output bundle directory")
	ledgerDir := flags.String("ledger-dir", "", "ledger snapshot directory")
	workspace := flags.String("workspace", "", "workspace directory")
	snapshot := flags.String("snapshot", "", "workspace snapshot")
	taskFile := flags.String("task-file", "", "task JSON file")
	promptFile := flags.String("prompt-file", "", "prompt markdown file")
	model := flags.String("model", "", "opencode model")
	opencodePathFlag := flags.String("opencode-path", "", "opencode executable path")
	logPath := flags.String("log-path", "", "opencode transcript path")
	timeoutMinutes := flags.Int("timeout-minutes", effectiveOpenCodeTaskTimeoutMinutes(Config{}), "opencode task timeout in minutes")
	setupScript := flags.String("setup-script", "", "workspace-relative setup script to source before opencode")
	setupTimeoutMinutes := flags.Int("setup-timeout-minutes", defaultRunnerSetupTimeoutMinutes, "setup script timeout in minutes")
	setupMode := flags.String("setup-mode", "fail", "setup failure mode: fail or warn")
	leadID := flags.String("lead-id", "", "lead id associated with this task")
	findingID := flags.String("finding-id", "", "finding id associated with this task")
	skipBundleVerify := flags.Bool("skip-bundle-verify", false, "skip task bundle verification after opencode exits")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *runDir == "" || *ledgerDir == "" || *taskFile == "" || *promptFile == "" || *model == "" {
		return errors.New("runner task requires --run-dir, --ledger-dir, --task-file, --prompt-file, and --model")
	}
	workspaceDir := *workspace
	if workspaceDir == "" && *snapshot == "" {
		return errors.New("runner task requires --workspace unless --snapshot is provided")
	}
	if *timeoutMinutes <= 0 {
		return errors.New("runner task --timeout-minutes must be positive")
	}
	if *leadID != "" && *findingID != "" {
		return errors.New("runner task --lead-id and --finding-id are mutually exclusive")
	}
	setup := RunnerSetupConfig{
		Script:         *setupScript,
		TimeoutMinutes: *setupTimeoutMinutes,
		Mode:           *setupMode,
	}
	if err := validateRunnerTaskSetupFlags(setup); err != nil {
		return err
	}
	task, err := readTaskFile(*taskFile)
	if err != nil {
		return err
	}
	prompt, err := os.ReadFile(*promptFile)
	if err != nil {
		return fmt.Errorf("read task prompt: %w", err)
	}
	opencodePath := *opencodePathFlag
	if opencodePath == "" {
		var versionOutput string
		opencodePath, versionOutput, err = ensureOpenCode()
		if err != nil {
			return err
		}
		_ = versionOutput
	}
	resolvedLogPath, err := runnerTaskLogPath(*runDir, *logPath, task.TaskID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(*runDir, "evidence"), dirPerm); err != nil {
		return err
	}
	sameLedger, err := sameDirectory(*runDir, *ledgerDir)
	if err != nil {
		return err
	}
	if sameLedger {
		return errors.New("runner task --ledger-dir must differ from --run-dir")
	}
	taskFileInBundle, err := copyRunnerTaskFileIntoBundle(*runDir, *taskFile)
	if err != nil {
		return err
	}
	privateLedgerDir, cleanupLedgerDir, err := prepareRunnerTaskLedgerSnapshot(*ledgerDir)
	if err != nil {
		return err
	}
	defer cleanupLedgerDir()
	if err := os.MkdirAll(filepath.Dir(resolvedLogPath), dirPerm); err != nil {
		return err
	}
	if *snapshot != "" {
		snapshotWorkspace := ""
		if workspaceDir != "" {
			empty, err := prepareSnapshotWorkspace(workspaceDir)
			if err != nil {
				return err
			}
			if empty {
				snapshotWorkspace = workspaceDir
			}
		}
		if snapshotWorkspace == "" {
			tempWorkspace, err := os.MkdirTemp("", "mnm-task-workspace-*")
			if err != nil {
				return err
			}
			defer os.RemoveAll(tempWorkspace)
			snapshotWorkspace = tempWorkspace
		}
		workspaceDir = snapshotWorkspace
		if err := extractWorkspaceSnapshot(*snapshot, workspaceDir); err != nil {
			return err
		}
	}
	if err := ensureWorkspaceToolchains(workspaceDir); err != nil {
		return err
	}
	setupResult, err := runTaskSetupHook(ctx, workspaceDir, *runDir, opencodeTask{
		RunID:     task.RunID,
		TaskID:    task.TaskID,
		Phase:     task.Phase,
		LeadID:    *leadID,
		FindingID: *findingID,
		Setup:     setup,
	}, 1)
	if err != nil {
		return err
	}
	if setup.Script != "" {
		if err := registerSuccessfulTaskSetupLog(*runDir, *runDir, opencodeTask{
			RunID:     task.RunID,
			TaskID:    task.TaskID,
			Phase:     task.Phase,
			Title:     "mnm " + task.Phase + " " + task.TaskID,
			LeadID:    *leadID,
			FindingID: *findingID,
			Setup:     setup,
		}, 1); err != nil {
			return err
		}
	}

	promptPath, err := writeOpenCodePromptFile(*runDir, string(prompt))
	if err != nil {
		return err
	}
	command := openCodeRunCommand(opencodePath, workspaceDir, *model, "mnm "+task.Phase+" "+task.TaskID, promptPath)
	isolateCommandProcessGroup(command)
	baseEnv := mergeEnv(os.Environ(), setupResult.Env)
	env := mergeEnv(baseEnv, []string{
		"MNM_RUN_DIR=" + *runDir,
		ledgerDirEnv + "=" + privateLedgerDir,
		"MNM_TASK_ID=" + task.TaskID,
		"MNM_PHASE=" + task.Phase,
		taskFileEnv + "=" + taskFileInBundle,
		"PATH=/tmp:" + envValue(baseEnv, "PATH"),
	})
	if *leadID != "" {
		env = mergeEnv(env, []string{"MNM_LEAD_ID=" + *leadID})
	}
	if *findingID != "" {
		env = mergeEnv(env, []string{"MNM_FINDING_ID=" + *findingID})
	}
	command.Env = env

	logFile, err := os.OpenFile(resolvedLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePerm)
	if err != nil {
		return err
	}
	defer logFile.Close()
	command.Stdout = io.MultiWriter(stdout, logFile)
	command.Stderr = io.MultiWriter(stderr, logFile)
	taskTimeout := time.Duration(*timeoutMinutes) * time.Minute
	err = runOpenCodeCommand(command, opencodeTask{
		RunID:   task.RunID,
		TaskID:  task.TaskID,
		Phase:   task.Phase,
		Timeout: taskTimeout,
	})
	if cleanupErr := cleanupCommandProcessGroup(command); cleanupErr != nil {
		cleanupErr = fmt.Errorf("clean up opencode task process group: %w", cleanupErr)
		if err != nil {
			err = errors.Join(err, cleanupErr)
		} else {
			err = cleanupErr
		}
	}
	if err != nil {
		return err
	}
	if !*skipBundleVerify {
		_, cleanupVerifyRunDir, err := prepareTaskBundleVerificationRunDir(*ledgerDir, task, *runDir)
		if err != nil {
			return err
		}
		cleanupVerifyRunDir()
	}
	fmt.Fprintf(stdout, "runner task completed for %s\n", task.TaskID)
	return nil
}

func readTaskFile(path string) (TaskRecord, error) {
	var task TaskRecord
	b, err := os.ReadFile(path)
	if err != nil {
		return task, fmt.Errorf("read task file: %w", err)
	}
	if err := json.Unmarshal(b, &task); err != nil {
		return task, fmt.Errorf("parse task file: %w", err)
	}
	if task.RunID == "" || task.TaskID == "" || task.Phase == "" {
		return task, errors.New("task file must include run_id, task_id, and phase")
	}
	return task, nil
}

func runnerTaskLogPath(runDir, logPath, taskID string) (string, error) {
	if strings.TrimSpace(logPath) == "" {
		logPath = filepath.ToSlash(filepath.Join("evidence", "opencode-"+safeFileID(taskID)+".jsonl"))
	}
	if filepath.IsAbs(logPath) {
		return "", errors.New("runner task --log-path must be relative to --run-dir")
	}
	relPath := filepath.ToSlash(filepath.Clean(filepath.FromSlash(logPath)))
	if err := validateTaskBundleRelPath(relPath); err != nil {
		return "", fmt.Errorf("runner task --log-path: %w", err)
	}
	return filepath.Join(runDir, filepath.FromSlash(relPath)), nil
}

func copyRunnerTaskFileIntoBundle(runDir, sourcePath string) (string, error) {
	targetPath := filepath.Join(runDir, currentTaskFile)
	absSource, err := filepath.Abs(sourcePath)
	if err != nil {
		return "", err
	}
	absTarget, err := filepath.Abs(targetPath)
	if err != nil {
		return "", err
	}
	if filepath.Clean(absSource) == filepath.Clean(absTarget) {
		return targetPath, nil
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), dirPerm); err != nil {
		return "", err
	}
	if err := copyFile(sourcePath, targetPath); err != nil {
		return "", fmt.Errorf("copy task file into bundle: %w", err)
	}
	return targetPath, nil
}

func prepareRunnerTaskLedgerSnapshot(ledgerDir string) (string, func(), error) {
	info, err := os.Stat(ledgerDir)
	if err != nil {
		return "", func() {}, fmt.Errorf("stat ledger snapshot: %w", err)
	}
	if !info.IsDir() {
		return "", func() {}, fmt.Errorf("ledger snapshot path is not a directory: %s", ledgerDir)
	}
	privateDir, err := os.MkdirTemp("", "mnm-task-ledger-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(privateDir) }
	if err := copyRunStateForTaskBundleVerification(ledgerDir, privateDir); err != nil {
		cleanup()
		return "", cleanup, err
	}
	return privateDir, cleanup, nil
}

func prepareSnapshotWorkspace(workspace string) (bool, error) {
	info, err := os.Stat(workspace)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(workspace, dirPerm); err != nil {
			return false, err
		}
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("workspace path is not a directory: %s", workspace)
	}
	entries, err := os.ReadDir(workspace)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

func normalizeStopAfterPhase(value string) (string, error) {
	phase := strings.TrimSpace(value)
	if phase == "" {
		return "", nil
	}
	if oneOf(phase, "recon", "investigate", "review", "deduplicate", "validate") {
		return phase, nil
	}
	return "", fmt.Errorf("stop-after phase %q is invalid; expected one of: recon, investigate, review, deduplicate, validate", phase)
}

func shouldStopAfterPhase(stopAfterPhase, completedPhase string) bool {
	return stopAfterPhase != "" && stopAfterPhase == completedPhase
}

func recordRunnerStopped(runDir, runID, workspace, phase string, stdout io.Writer) error {
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    runID,
		Type:     "runner.stopped",
		Object:   "run",
		ObjectID: runID,
		Data: map[string]any{
			"phase":     phase,
			"workspace": workspace,
		},
	}); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "runner stopped after %s for %s\n", phase, runID)
	return nil
}

type runnerFailureContext struct {
	RunID           string
	RunDir          string
	Stage           string
	Workspace       string
	OpenCodePath    string
	OpenCodeVersion string
}

func (failure runnerFailureContext) Record(cause error) error {
	if cause == nil || failure.RunID == "" || failure.RunDir == "" {
		return nil
	}
	summary, category := summarizeRunnerFailure(cause)
	relPath := filepath.ToSlash(filepath.Join("evidence", "runner-failure.json"))
	artifactPath := filepath.Join(failure.RunDir, relPath)
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeJSON(artifactPath, map[string]any{
		"run_id":           failure.RunID,
		"stage":            failure.Stage,
		"summary":          summary,
		"category":         category,
		"error":            cause.Error(),
		"workspace":        failure.Workspace,
		"opencode_path":    failure.OpenCodePath,
		"opencode_version": failure.OpenCodeVersion,
		"timestamp":        timestamp,
	}); err != nil {
		return err
	}
	if _, err := registerRunnerEvidence(failure.RunDir, failure.RunID, "json", "Runner failure manifest", relPath, true); err != nil {
		return err
	}
	return appendLedgerEvent(failure.RunDir, LedgerEvent{
		RunID:    failure.RunID,
		Type:     "runner.failed",
		Object:   "run",
		ObjectID: failure.RunID,
		Data: map[string]any{
			"stage":    failure.Stage,
			"summary":  summary,
			"category": category,
			"error":    cause.Error(),
			"path":     relPath,
		},
	})
}

func summarizeRunnerFailure(cause error) (string, string) {
	message := cause.Error()
	text := strings.ToLower(message)
	switch {
	case strings.Contains(text, "argument list too long"):
		return "opencode launch failed because the generated prompt or arguments exceeded the OS argv limit", "opencode_argv_limit"
	case strings.Contains(text, "limactl start"), strings.Contains(text, "task vm start failed"):
		return "task VM failed to start before opencode could run", "vm_start_failed"
	case strings.Contains(text, "no such file or directory") && strings.Contains(text, "transcript"):
		return "task output transcript was missing after a failed attempt", "missing_task_transcript"
	case strings.Contains(text, "executable file not found"), strings.Contains(text, "permission denied"):
		return "opencode launch failed before the model task could start", "opencode_launch_failed"
	default:
		return runnerFailureRootLine(message), "runner_failed"
	}
}

func runnerFailureRootLine(text string) string {
	const marker = "\nlog excerpt:\n"
	if _, after, ok := strings.Cut(text, marker); ok {
		if line := firstNonEmptyLine(after, ""); line != "" {
			return line
		}
	}
	return firstNonEmptyLine(text, "runner failed")
}

func firstNonEmptyLine(text, fallback string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return fallback
}

func registerRunnerEvidence(runDir, runID, kind, title, relPath string, allowContentChange bool) (string, error) {
	return registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:              runID,
		Kind:               kind,
		Title:              title,
		Path:               relPath,
		AllowContentChange: allowContentChange,
	})
}

func runReconTask(runDir, runID, workspace string, cfg Config, opencodePath string) error {
	return runReconTaskWithAttemptRunner(runDir, runID, workspace, cfg, directOpenCodeTaskAttemptRunner{opencodePath: opencodePath})
}

func runReconTaskWithAttemptRunner(runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner) error {
	return runReconTaskWithAttemptRunnerContext(context.Background(), runDir, runID, workspace, cfg, attemptRunner)
}

func runReconTaskWithAttemptRunnerContext(ctx context.Context, runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	task := TaskRecord{
		RunID:       runID,
		TaskID:      "task_recon",
		Phase:       "recon",
		Title:       "Recon",
		Instruction: "Map the workspace, interpret scope, identify risks, and create focused leads for later investigation.",
	}
	if reconTaskComplete(runDir, task.TaskID, cfg) {
		return nil
	}
	allowRecoveryIngest := ledgerTaskCompleted(runDir, task.TaskID)
	if err := writeTaskFile(filepath.Join(runDir, currentTaskFile), task); err != nil {
		return err
	}
	if err := registerTaskStarted(runDir, task, nil); err != nil {
		return err
	}
	taskWorkspace, cleanupWorkspace, err := prepareTaskWorkspace(workspace, runID, task.TaskID)
	if err != nil {
		return err
	}
	defer cleanupWorkspace()

	prompt := reconPrompt(runDir, taskWorkspace, cfg)
	promptPath := filepath.Join(runDir, "evidence", "recon-prompt.md")
	if err := os.WriteFile(promptPath, []byte(prompt), filePerm); err != nil {
		return err
	}
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:              runID,
		TaskID:             task.TaskID,
		Kind:               "markdown",
		Title:              "Recon prompt",
		Path:               "evidence/recon-prompt.md",
		AllowContentChange: true,
	}); err != nil {
		return err
	}
	logPath := filepath.Join(runDir, "evidence", "opencode-recon.jsonl")
	if err := runOpenCodeTaskWithAttemptRunnerContext(ctx, attemptRunner, taskWorkspace, runDir, opencodeTask{
		RunID:    runID,
		TaskID:   task.TaskID,
		Phase:    task.Phase,
		Title:    "mnm recon",
		Model:    phaseModel(cfg, "recon"),
		Prompt:   prompt,
		LogPath:  logPath,
		TaskFile: filepath.Join(runDir, currentTaskFile),
		Timeout:  openCodeTaskTimeout(cfg),
		Setup:    cfg.Runner.Setup,
		BundleIngestOptions: taskBundleIngestOptions{
			AllowAfterCompleted: allowRecoveryIngest,
		},
		Verify: func(verifyRunDir string) error {
			return validateReconTaskComplete(verifyRunDir, task.TaskID, cfg)
		},
	}); err != nil {
		return err
	}
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:  runID,
		TaskID: task.TaskID,
		Kind:   "jsonl",
		Title:  "OpenCode Recon transcript",
		Path:   "evidence/opencode-recon.jsonl",
	}); err != nil {
		return err
	}
	return nil
}

func reconTaskComplete(runDir, taskID string, cfg Config) bool {
	return validateReconTaskComplete(runDir, taskID, cfg) == nil
}

func validateReconTaskComplete(runDir, taskID string, cfg Config) error {
	if !ledgerTaskCompleted(runDir, taskID) {
		return errors.New("recon opencode task did not complete successfully through mnm task complete")
	}
	if err := validateReconOutputs(runDir, taskID, cfg); err != nil {
		return err
	}
	return validateReconLedgerOutputs(runDir, taskID, cfg)
}

func validateReconOutputs(runDir, taskID string, cfg Config) error {
	requiredEvidence := []string{
		filepath.ToSlash(filepath.Join("evidence", "recon-codebase-map.md")),
		filepath.ToSlash(filepath.Join("evidence", "recon-risk-register.md")),
	}
	for _, relPath := range requiredEvidence {
		evidence, ok := ledgerTaskEvidence(runDir, taskID, relPath)
		if !ok {
			return fmt.Errorf("recon opencode task did not register required evidence %s", relPath)
		}
		if err := registeredEvidenceFileError(runDir, relPath, evidence.ContentSHA256, validateNonEmptyEvidenceFile); err != nil {
			return err
		}
	}
	leadCount, err := ledgerTaskLeadCount(runDir, taskID)
	if err != nil {
		return err
	}
	if leadCount == 0 {
		return errors.New("recon opencode task did not create any investigation leads")
	}
	maxLeads := cfg.Runner.MaxLeads
	if maxLeads > 0 && leadCount > maxLeads {
		return fmt.Errorf("recon opencode task created %d leads, exceeding configured max_leads %d", leadCount, maxLeads)
	}
	return nil
}

func registerTaskStarted(runDir string, task TaskRecord, extraData map[string]any) error {
	data := map[string]any{
		"phase": task.Phase,
		"title": task.Title,
	}
	for key, value := range extraData {
		data[key] = value
	}
	event, err := prepareLedgerEvent(runDir, LedgerEvent{
		RunID:    task.RunID,
		Type:     "task.started",
		Object:   "task",
		ObjectID: task.TaskID,
		TaskID:   task.TaskID,
		Data:     data,
	})
	if err != nil {
		return err
	}
	unlock, err := lockRunDir(runDir)
	if err != nil {
		return err
	}
	defer unlock()
	existing, exists, err := ledgerTaskStartedDataUnlocked(runDir, task.TaskID)
	if err != nil {
		return err
	}
	if exists {
		equal, err := ledgerDataEqual(existing, data)
		if err != nil {
			return err
		}
		if equal {
			return nil
		}
		return fmt.Errorf("task %s already started with different metadata", task.TaskID)
	}
	return appendLedgerEventUnlocked(runDir, event)
}

func ledgerTaskStartedDataUnlocked(runDir, taskID string) (map[string]any, bool, error) {
	events, err := readLedgerEventsUnlocked(runDir)
	if err != nil {
		return nil, false, err
	}
	data, exists := taskStartedDataFromEvents(events, taskID)
	return data, exists, nil
}

func taskStartedDataFromEvents(events []LedgerEvent, taskID string) (map[string]any, bool) {
	var data map[string]any
	found := false
	for _, event := range events {
		if event.Type == "task.started" && event.Object == "task" && event.ObjectID == taskID {
			data = event.Data
			found = true
		}
	}
	return data, found
}

func ledgerDataEqual(left, right map[string]any) (bool, error) {
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return false, err
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		return false, err
	}
	return bytes.Equal(leftJSON, rightJSON), nil
}

type opencodeTask struct {
	RunID               string
	TaskID              string
	Phase               string
	LeadID              string
	FindingID           string
	Title               string
	Model               string
	Prompt              string
	LogPath             string
	TaskFile            string
	Timeout             time.Duration
	Verify              func(string) error
	BundleIngestOptions taskBundleIngestOptions
	Setup               RunnerSetupConfig
}

type opencodeTaskAttemptRunner interface {
	RunOpenCodeTaskAttempt(ctx context.Context, workspace, runDir string, task opencodeTask, attempt int) (openCodeAttemptResult, error)
}

type directOpenCodeTaskAttemptRunner struct {
	opencodePath string
}

func (runner directOpenCodeTaskAttemptRunner) RunOpenCodeTaskAttempt(ctx context.Context, workspace, runDir string, task opencodeTask, attempt int) (openCodeAttemptResult, error) {
	if runner.opencodePath == "" {
		return openCodeAttemptResult{}, errors.New("opencode path is required")
	}
	if err := ctx.Err(); err != nil {
		return openCodeAttemptResult{}, err
	}
	return runDirectOpenCodeTaskAttempt(ctx, runner.opencodePath, workspace, runDir, task, attempt)
}

func runOpenCodeTask(opencodePath, workspace, runDir string, task opencodeTask) error {
	return runOpenCodeTaskWithAttemptRunner(directOpenCodeTaskAttemptRunner{opencodePath: opencodePath}, workspace, runDir, task)
}

func runOpenCodeTaskWithAttemptRunner(attemptRunner opencodeTaskAttemptRunner, workspace, runDir string, task opencodeTask) error {
	return runOpenCodeTaskWithAttemptRunnerContext(context.Background(), attemptRunner, workspace, runDir, task)
}

func runOpenCodeTaskWithAttemptRunnerContext(ctx context.Context, attemptRunner opencodeTaskAttemptRunner, workspace, runDir string, task opencodeTask) error {
	var lastErr error
	attempts := 0
	for attempt := 1; attempt <= openCodeMaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		attempts = attempt
		result, err := attemptRunner.RunOpenCodeTaskAttempt(ctx, workspace, runDir, task, attempt)
		verifyRunDir := runDir
		cleanupVerifyRunDir := func() {}
		if err == nil && result.Bundle {
			verifyRunDir, cleanupVerifyRunDir, err = prepareTaskBundleVerificationRunDir(runDir, task.taskRecord(), result.TaskRunDir, task.BundleIngestOptions)
			if err != nil {
				err = retryableOpenCodePostconditionError{
					err:            err,
					ledgerModified: result.ledgerModified,
				}
			}
		}
		if err == nil && task.Verify != nil {
			if verifyErr := task.Verify(verifyRunDir); verifyErr != nil {
				err = retryableOpenCodePostconditionError{
					err:            verifyErr,
					ledgerModified: result.ledgerModified,
				}
			}
		}
		cleanupVerifyRunDir()
		if err == nil && result.Bundle {
			if ingestErr := ingestTaskBundleWithOptions(runDir, task.taskRecord(), result.TaskRunDir, task.BundleIngestOptions); ingestErr != nil {
				err = retryableOpenCodePostconditionError{
					err:            ingestErr,
					ledgerModified: true,
				}
			}
		}
		if err == nil {
			if setupErr := registerSuccessfulTaskSetupLog(runDir, result.TaskRunDir, task, attempt); setupErr != nil {
				return setupErr
			}
			return nil
		}
		lastErr = err
		if attempt == openCodeMaxAttempts || !retryableOpenCodeError(task.LogPath, err) {
			break
		}
		if err := appendOpenCodeRetryEvent(runDir, task, attempt, openCodeMaxAttempts, err); err != nil {
			return err
		}
		if openCodeRetryDelay > 0 {
			timer := time.NewTimer(openCodeRetryDelay * time.Duration(attempt))
			select {
			case <-timer.C:
			case <-ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return ctx.Err()
			}
		}
	}
	return fmt.Errorf("opencode task %s failed after %d attempt(s): %w%s", task.TaskID, attempts, lastErr, openCodeLogExcerpt(task.LogPath))
}

func registerSuccessfulTaskSetupLog(runDir, taskRunDir string, task opencodeTask, attempt int) error {
	if strings.TrimSpace(task.Setup.Script) == "" {
		return nil
	}
	relPath := taskSetupLogRelPath(task.TaskID, attempt)
	if _, exists := ledgerTaskEvidence(runDir, task.TaskID, relPath); exists {
		return nil
	}
	source := filepath.Join(taskRunDir, filepath.FromSlash(relPath))
	if _, err := os.Stat(source); err != nil {
		return fmt.Errorf("setup log %s was not captured for task %s: %w", relPath, task.TaskID, err)
	}
	target := filepath.Join(runDir, filepath.FromSlash(relPath))
	if filepath.Clean(source) != filepath.Clean(target) {
		if err := copyFileMode(source, target, filePerm); err != nil {
			return fmt.Errorf("copy setup log %s: %w", relPath, err)
		}
	}
	title := "Setup hook log: " + task.Phase
	if task.Title != "" {
		title = "Setup hook log: " + task.Title
	}
	_, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:              task.RunID,
		TaskID:             task.TaskID,
		Kind:               "log",
		Title:              title,
		Path:               relPath,
		LeadID:             task.LeadID,
		FindingID:          task.FindingID,
		AllowContentChange: true,
	})
	return err
}

type openCodeAttemptResult struct {
	logText        string
	ledgerModified bool
	TaskRunDir     string
	Bundle         bool
}

func runDirectOpenCodeTaskAttempt(ctx context.Context, opencodePath, workspace, runDir string, task opencodeTask, attempt int) (openCodeAttemptResult, error) {
	result := openCodeAttemptResult{TaskRunDir: runDir}
	taskRunDir := runDir
	taskFile := task.TaskFile
	prompt := task.Prompt
	if task.usesTaskBundle() {
		outputDir, preparedTaskFile, err := prepareOpenCodeTaskBundleAttempt(runDir, task, attempt)
		if err != nil {
			return result, err
		}
		taskRunDir = outputDir
		taskFile = preparedTaskFile
		prompt = taskBundlePrompt(task.Prompt, runDir, outputDir)
		result.TaskRunDir = outputDir
		result.Bundle = true
	}
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	var logOffset int64
	if attempt > 1 {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		if info, err := os.Stat(task.LogPath); err == nil {
			logOffset = info.Size()
		}
	}
	ledgerOffset, err := fileSize(filepath.Join(taskRunDir, eventsFile))
	if err != nil {
		return result, err
	}
	setupResult, err := runTaskSetupHook(ctx, workspace, taskRunDir, task, attempt)
	if err != nil {
		return result, err
	}
	logFile, err := os.OpenFile(task.LogPath, flag, filePerm)
	if err != nil {
		return result, err
	}
	promptPath, err := writeOpenCodePromptFile(taskRunDir, prompt)
	if err != nil {
		return result, err
	}
	command := openCodeRunCommand(opencodePath, workspace, task.Model, task.Title, promptPath)
	isolateCommandProcessGroup(command)
	baseEnv := mergeEnv(os.Environ(), setupResult.Env)
	env := mergeEnv(baseEnv, []string{
		"MNM_RUN_DIR=" + taskRunDir,
		"MNM_TASK_ID=" + task.TaskID,
		"MNM_PHASE=" + task.Phase,
		"PATH=/tmp:" + envValue(baseEnv, "PATH"),
	})
	if task.usesTaskBundle() {
		env = mergeEnv(env, []string{ledgerDirEnv + "=" + runDir})
	}
	if task.LeadID != "" {
		env = mergeEnv(env, []string{"MNM_LEAD_ID=" + task.LeadID})
	}
	if task.FindingID != "" {
		env = mergeEnv(env, []string{"MNM_FINDING_ID=" + task.FindingID})
	}
	if taskFile != "" {
		env = mergeEnv(env, []string{taskFileEnv + "=" + taskFile})
	}
	command.Env = env
	command.Stdout = logFile
	command.Stderr = os.Stderr
	runErr := runOpenCodeCommand(command, task)
	if cleanupErr := cleanupCommandProcessGroup(command); cleanupErr != nil {
		cleanupErr = fmt.Errorf("clean up opencode task process group: %w", cleanupErr)
		if runErr != nil {
			runErr = errors.Join(runErr, cleanupErr)
		} else {
			runErr = cleanupErr
		}
	}
	if closeErr := logFile.Close(); closeErr != nil && runErr == nil {
		return result, closeErr
	}
	result.logText = readLogSuffix(task.LogPath, logOffset)
	result.ledgerModified = fileModifiedSince(filepath.Join(taskRunDir, eventsFile), ledgerOffset)
	if result.Bundle {
		result.ledgerModified = false
	}
	if runErr != nil {
		return result, openCodeAttemptError{
			err:            runErr,
			logText:        result.logText,
			ledgerModified: result.ledgerModified,
		}
	}
	return result, nil
}

type openCodeAttemptError struct {
	err            error
	logText        string
	ledgerModified bool
}

func (e openCodeAttemptError) Error() string {
	return e.err.Error()
}

func (e openCodeAttemptError) Unwrap() error {
	return e.err
}

func writeOpenCodePromptFile(taskRunDir, prompt string) (string, error) {
	if err := os.MkdirAll(taskRunDir, dirPerm); err != nil {
		return "", err
	}
	path := filepath.Join(taskRunDir, "prompt.md")
	if err := os.WriteFile(path, []byte(prompt), filePerm); err != nil {
		return "", err
	}
	return path, nil
}

func openCodeRunCommand(opencodePath, workspace, model, title, promptPath string) *exec.Cmd {
	return exec.Command(opencodePath,
		"run",
		"--format", "json",
		"--dir", workspace,
		"--model", model,
		"--title", title,
		"--dangerously-skip-permissions",
		"--file", promptPath,
		"--",
		"Read the attached prompt file and follow its instructions exactly.",
	)
}

func readLogSuffix(logPath string, offset int64) string {
	b, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	if offset < 0 || offset > int64(len(b)) {
		return ""
	}
	return string(b[offset:])
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	return info.Size(), nil
}

func fileModifiedSince(path string, previousSize int64) bool {
	currentSize, err := fileSize(path)
	return err != nil || currentSize != previousSize
}

func (task opencodeTask) usesTaskBundle() bool {
	return task.TaskFile != "" && task.RunID != "" && task.TaskID != "" && task.Phase != ""
}

func (task opencodeTask) taskRecord() TaskRecord {
	return TaskRecord{
		RunID:  task.RunID,
		TaskID: task.TaskID,
		Phase:  task.Phase,
	}
}

func prepareOpenCodeTaskBundleAttempt(runDir string, task opencodeTask, attempt int) (string, string, error) {
	outputDir := filepath.Join(runDir, taskBundlesDir, safeFileID(task.TaskID), fmt.Sprintf("attempt-%d", attempt))
	if err := os.RemoveAll(outputDir); err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(filepath.Join(outputDir, "evidence"), dirPerm); err != nil {
		return "", "", err
	}
	if err := copyRunContextForTaskBundle(runDir, outputDir); err != nil {
		return "", "", err
	}
	taskFile := filepath.Join(outputDir, currentTaskFile)
	if err := copyFile(task.TaskFile, taskFile); err != nil {
		return "", "", err
	}
	_ = os.Remove(filepath.Join(outputDir, eventsFile))
	_ = os.Remove(filepath.Join(outputDir, ".events.lock"))
	return outputDir, taskFile, nil
}

func copyRunContextForTaskBundle(runDir, outputDir string) error {
	for _, relDir := range []string{"evidence"} {
		source := filepath.Join(runDir, relDir)
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := copyDirContentsFiltered(source, filepath.Join(outputDir, relDir), shouldCopyTaskBundleEvidence); err != nil {
			return err
		}
	}
	return nil
}

func shouldCopyTaskBundleEvidence(relPath string) bool {
	base := filepath.Base(relPath)
	if generatedOpenCodeTranscriptName(base) {
		return false
	}
	if generatedPromptEvidenceName(base) {
		return false
	}
	if base == "runner-failure.json" {
		return false
	}
	return true
}

func generatedOpenCodeTranscriptName(base string) bool {
	if !strings.HasPrefix(base, "opencode-") || !strings.HasSuffix(base, ".jsonl") {
		return false
	}
	if oneOf(base, "opencode-recon.jsonl", "opencode-deduplicate.jsonl", "opencode-finalize.jsonl", "opencode-task.jsonl") {
		return true
	}
	for _, prefix := range []string{
		"opencode-investigate-",
		"opencode-review-",
		"opencode-validate-",
		"opencode-task_",
	} {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	return false
}

func generatedPromptEvidenceName(base string) bool {
	if oneOf(base, "recon-prompt.md", "deduplicate-prompt.md", "finalize-prompt.md") {
		return true
	}
	if !strings.HasSuffix(base, "-prompt.md") {
		return false
	}
	for _, prefix := range []string{"investigate-", "review-", "validate-"} {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	return false
}

func taskBundlePrompt(prompt, runDir, outputDir string) string {
	return taskBundlePromptForDirs(rewriteTaskBundlePromptPaths(prompt, runDir, outputDir, runDir), outputDir, runDir)
}

func taskBundlePromptForDirs(prompt, outputDir, ledgerDir string) string {
	return fmt.Sprintf(`Task output directory: %[1]s
Ledger snapshot directory: %[2]s

Write new durable artifacts under the task output directory. The injected mnm CLI reads prior ledger state from the ledger snapshot and appends this task's events only to the task output directory.
Treat any pre-existing evidence files in the task output directory as immutable prior context. Do not overwrite or re-register earlier evidence paths; write fresh task-specific artifacts for new proofs, logs, and notes.
When passing free-form text to mnm flags such as --reason, --summary, or --title, keep the shell argument simple. Do not put Markdown backticks inside double-quoted shell arguments because the shell treats backticks as command substitution; prefer plain prose in CLI arguments and put detailed Markdown/code formatting in evidence files.
For background services, capture process IDs and clean them up with kill "$pid"; wait "$pid" || true. Avoid broad pkill -f patterns because they can match the cleanup command itself.

%[3]s`, outputDir, ledgerDir, prompt)
}

func rewriteTaskBundlePromptPaths(prompt, runDir, outputDir, ledgerDir string) string {
	ledgerReplacements := []struct {
		path        string
		placeholder string
		replacement string
	}{
		{
			path:        filepath.Join(runDir, eventsFile),
			placeholder: "__MNM_LEDGER_EVENTS_PATH__",
			replacement: filepath.Join(ledgerDir, eventsFile),
		},
		{
			path:        filepath.ToSlash(filepath.Join(runDir, eventsFile)),
			placeholder: "__MNM_LEDGER_EVENTS_SLASH_PATH__",
			replacement: filepath.ToSlash(filepath.Join(ledgerDir, eventsFile)),
		},
	}
	rewritten := prompt
	for _, item := range ledgerReplacements {
		rewritten = strings.ReplaceAll(rewritten, item.path, item.placeholder)
	}
	rewritten = strings.ReplaceAll(rewritten, runDir, outputDir)
	for _, item := range ledgerReplacements {
		rewritten = strings.ReplaceAll(rewritten, item.placeholder, item.replacement)
	}
	return rewritten
}

func prepareTaskBundleVerificationRunDir(runDir string, task TaskRecord, bundleDir string, options ...taskBundleIngestOptions) (string, func(), error) {
	verifyDir, err := os.MkdirTemp("", "mnm-task-verify-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(verifyDir) }
	if err := copyRunStateForTaskBundleVerification(runDir, verifyDir); err != nil {
		cleanup()
		return "", cleanup, err
	}
	ingestOptions := taskBundleIngestOptions{}
	if len(options) > 0 {
		ingestOptions = options[0]
	}
	if err := ingestTaskBundleWithOptions(verifyDir, task, bundleDir, ingestOptions); err != nil {
		cleanup()
		return "", cleanup, err
	}
	return verifyDir, cleanup, nil
}

func copyRunStateForTaskBundleVerification(runDir, verifyDir string) error {
	eventsPath := filepath.Join(runDir, eventsFile)
	if _, err := os.Stat(eventsPath); err == nil {
		if err := copyFile(eventsPath, filepath.Join(verifyDir, eventsFile)); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, relDir := range []string{"evidence"} {
		source := filepath.Join(runDir, relDir)
		if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := copyDirContents(source, filepath.Join(verifyDir, relDir)); err != nil {
			return err
		}
	}
	return nil
}

func runOpenCodeCommand(command *exec.Cmd, task opencodeTask) error {
	if task.Timeout <= 0 {
		return command.Run()
	}
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	timer := time.NewTimer(task.Timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-timer.C:
		killErr := command.Process.Kill()
		cleanupErr := cleanupCommandProcessGroup(command)
		err := <-done
		timeoutErr := openCodeTaskTimeoutError{
			taskID:  task.TaskID,
			timeout: task.Timeout,
			err:     err,
		}
		if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			timeoutErr.err = errors.Join(timeoutErr.err, fmt.Errorf("kill timed out opencode process: %w", killErr))
		}
		if cleanupErr != nil {
			timeoutErr.err = errors.Join(timeoutErr.err, fmt.Errorf("clean up timed out opencode task process group: %w", cleanupErr))
		}
		return timeoutErr
	}
}

type openCodeTaskTimeoutError struct {
	taskID  string
	timeout time.Duration
	err     error
}

func (e openCodeTaskTimeoutError) Error() string {
	return fmt.Sprintf("opencode task %s exceeded timeout %s", e.taskID, e.timeout)
}

func (e openCodeTaskTimeoutError) Unwrap() error {
	return e.err
}

type retryableOpenCodePostconditionError struct {
	err            error
	ledgerModified bool
}

func (e retryableOpenCodePostconditionError) Error() string {
	return e.err.Error()
}

func (e retryableOpenCodePostconditionError) Unwrap() error {
	return e.err
}

func retryableOpenCodeError(logPath string, err error) bool {
	if err == nil {
		return false
	}
	var timeoutErr openCodeTaskTimeoutError
	if errors.As(err, &timeoutErr) {
		return false
	}
	var postconditionErr retryableOpenCodePostconditionError
	if errors.As(err, &postconditionErr) {
		return !postconditionErr.ledgerModified
	}
	processText := strings.ToLower(err.Error())
	retryText := processText
	var attemptErr openCodeAttemptError
	if errors.As(err, &attemptErr) {
		if attemptErr.ledgerModified {
			return false
		}
		retryText += "\n" + strings.ToLower(attemptErr.logText)
	} else if b, readErr := os.ReadFile(logPath); readErr == nil {
		retryText += "\n" + strings.ToLower(string(b))
	}
	var stageErr limaTaskStageError
	if errors.As(err, &stageErr) && retryableLimaTaskStage(stageErr.Stage) {
		return true
	}
	for _, marker := range []string{
		"argument list too long",
		"executable file not found",
		"permission denied",
		"exec format error",
	} {
		if strings.Contains(processText, marker) {
			return false
		}
	}
	for _, marker := range []string{
		"task vm create failed",
		"task vm start failed",
		"task vm copy inputs failed",
		"task vm copy output failed",
		"limactl start",
		`"code":502`,
		"provider_unavailable",
		"network connection lost",
		"bad gateway",
		"service unavailable",
		"temporarily unavailable",
		"temporary failure",
		"connection reset",
		"econnreset",
		"timeout",
		"timed out",
	} {
		if strings.Contains(retryText, marker) {
			return true
		}
	}
	return false
}

func openCodeLogExcerpt(logPath string) string {
	if logPath == "" {
		return ""
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return ""
	}
	const maxExcerptBytes = 4000
	if len(text) > maxExcerptBytes {
		text = text[len(text)-maxExcerptBytes:]
	}
	return "\nlog excerpt:\n" + text
}

func appendOpenCodeRetryEvent(runDir string, task opencodeTask, attempt, maxAttempts int, cause error) error {
	if task.RunID == "" {
		return nil
	}
	return appendLedgerEvent(runDir, LedgerEvent{
		RunID:    task.RunID,
		Type:     "task.retrying",
		Object:   "task",
		ObjectID: task.TaskID,
		TaskID:   task.TaskID,
		Data: map[string]any{
			"phase":        task.Phase,
			"attempt":      attempt,
			"max_attempts": maxAttempts,
			"reason":       cause.Error(),
		},
	})
}

func prepareTaskWorkspace(baseWorkspace, runID, taskID string) (string, func(), error) {
	workspace := filepath.Join(os.TempDir(), "mnm-task-workspace-"+safeFileID(runID)+"-"+safeFileID(taskID))
	cleanup := func() { _ = os.RemoveAll(workspace) }
	if err := os.RemoveAll(workspace); err != nil {
		return "", cleanup, err
	}
	if err := os.MkdirAll(workspace, dirPerm); err != nil {
		return "", cleanup, err
	}
	if err := copyDirContents(baseWorkspace, workspace); err != nil {
		cleanup()
		return "", cleanup, err
	}
	return workspace, cleanup, nil
}

func phaseModel(cfg Config, phase string) string {
	defaultModel := strings.TrimSpace(cfg.Models.Default)
	reconModel := strings.TrimSpace(cfg.Models.Recon)
	model := ""
	switch phase {
	case "recon":
		if reconModel != "" {
			model = reconModel
		}
	case "investigate":
		if strings.TrimSpace(cfg.Models.Investigate) != "" {
			model = strings.TrimSpace(cfg.Models.Investigate)
		}
	case "review":
		if strings.TrimSpace(cfg.Models.Review) != "" {
			model = strings.TrimSpace(cfg.Models.Review)
		}
	case "deduplicate":
		if strings.TrimSpace(cfg.Models.Deduplicate) != "" {
			model = strings.TrimSpace(cfg.Models.Deduplicate)
		}
	case "validate":
		if strings.TrimSpace(cfg.Models.Validate) != "" {
			model = strings.TrimSpace(cfg.Models.Validate)
		}
	case "finalize":
		if strings.TrimSpace(cfg.Models.Finalize) != "" {
			model = strings.TrimSpace(cfg.Models.Finalize)
		}
	}
	if model == "" {
		model = defaultModel
	}
	if model == "" {
		model = reconModel
	}
	if model == "" {
		return ""
	}
	if normalized, err := normalizeModelForOpenCode(cfg.Models.Provider, model); err == nil {
		return normalized
	}
	return model
}

func validateReconLedgerOutputs(runDir, taskID string, cfg Config) error {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return err
	}
	hasMap := false
	hasRiskRegister := false
	hasLead := false
	allowedRiskAreas := riskAreaSet(cfg.Instructions.RiskAreas)
	for _, event := range events {
		if event.TaskID != taskID {
			continue
		}
		if event.Type == "evidence.added" && event.Object == "evidence" {
			path, _ := event.Data["path"].(string)
			switch path {
			case "evidence/recon-codebase-map.md":
				hasMap = true
			case "evidence/recon-risk-register.md":
				hasRiskRegister = true
			}
		}
		if event.Type == "lead.created" && event.Object == "lead" {
			hasLead = true
			if len(allowedRiskAreas) != 0 {
				category := strings.ToLower(strings.TrimSpace(stringData(event.Data, "category")))
				if !allowedRiskAreas[category] {
					return fmt.Errorf("recon lead %s category %q is outside configured risk areas", event.ObjectID, stringData(event.Data, "category"))
				}
			}
		}
	}
	if !ledgerTaskCompleted(runDir, taskID) {
		return errors.New("recon opencode task did not complete successfully through mnm task complete")
	}
	if !hasMap {
		return errors.New("recon opencode task did not register the codebase map")
	}
	if !hasRiskRegister {
		return errors.New("recon opencode task did not register the risk register")
	}
	if !hasLead {
		return errors.New("recon opencode task did not create any leads")
	}
	return nil
}

func ledgerTaskCompleted(runDir, taskID string) bool {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return false
	}
	status := ""
	for _, event := range events {
		if event.Type == "task.completed" && event.Object == "task" && event.ObjectID == taskID {
			status, _ = event.Data["status"].(string)
		}
	}
	return status == "completed"
}

func reconPrompt(runDir, workspace string, cfg Config) string {
	scope := scopeText(cfg)
	categoryExample, categoryGuidance := reconLeadCategoryGuidance(cfg)
	return fmt.Sprintf(`# makenomistakes Recon

You are running inside an isolated VM. Your job is to inspect the workspace and create durable Recon outputs through the injected mnm CLI.

Workspace: %[1]s
Run directory: %[2]s
Maximum leads: %[3]d

Scope instructions:

%[4]s

Required actions:

1. Run: mnm task current
2. Inspect the workspace using local tools such as find, rg, package manifests, framework configs, tests, docs, and build files.
3. Treat the workspace as a disposable per-task copy. Write durable audit artifacts only under the run directory.
4. Keep filesystem searches scoped to the workspace and run directory. Do not run broad host filesystem scans such as find / or inspect host mounts like /Users; use /tmp only for temporary tools or repro files.
5. Write a concise codebase map to: %[2]s/evidence/recon-codebase-map.md
6. Register it with: mnm evidence add --kind markdown --title "Recon codebase map" --path %[2]s/evidence/recon-codebase-map.md
7. Write a risk register to: %[2]s/evidence/recon-risk-register.md
8. Register it with: mnm evidence add --kind markdown --title "Recon risk register" --path %[2]s/evidence/recon-risk-register.md
9. Create focused leads. For each lead, write a body file under %[2]s/evidence/, then run: mnm lead create --title "Specific lead title" --category %[5]s --priority medium --body-file %[2]s/evidence/lead-specific-name.md
10. Create no more than %[3]d leads.
11. Complete the task with: mnm task complete --status completed --summary "Recon completed"

Recon coverage matrix:

- injection and command/data/query interpreters
- XSS, HTML/Markdown/template rendering, and browser trust boundaries
- auth/session, credential, cookie, CSRF, and account lifecycle behavior
- authorization, IDOR, tenant isolation, and confused-deputy flows
- SSRF, redirects, URL parsing, callbacks, and outbound fetches
- DoS/ReDoS, parser bombs, resource exhaustion, and unbounded work
- logging, observability, audit, and downstream log-consumer sinks
- sensitive data storage, PII handling, secrets, and encryption/hashing at rest
- transport, deployment defaults, security headers, and runtime configuration
- dependency, framework, runtime, and reachable vulnerable-usage risks

Recon discipline:

- Recon maps the workspace and schedules focused work; Investigate and Validate prove, exploit, or falsify issues.
- If scope instructions ask for tests or proofs, treat them as requirements for later Investigate or Validate unless a cheap smoke command materially improves lead quality.
- If focused risk areas are configured, create leads inside those areas and avoid broad unrelated audit passes.
- Use docs, tutorials, examples, tests, TODOs, comments, and "fix for" notes as candidate lead sources, but never as proof by themselves.
- In the risk register, briefly mark each coverage-matrix class as lead opened, not applicable, or needs targeted pass.
- Run only bounded inspection commands such as find, rg, package metadata reads, and quick tests when needed to understand the workspace.
- Do not build end-to-end proof scripts, start long-lived services, fuzz, install heavy dependencies, or keep exploring after you have enough context for focused leads.
- Register the codebase map and risk register as soon as they are useful, then create leads promptly. Unregistered files are scratch and may be lost.

Lead quality bar:

- A lead is a focused question or risk area, not a final finding.
- %[6]s
- Prefer specific components, files, flows, trust boundaries, data flows, parsers, auth paths, dependency risks, or runtime setup concerns.
- Include enough context that a later Investigate task can start without redoing Recon.
- Avoid generic leads like "review security" or "check code quality".
`, workspace, runDir, cfg.Runner.MaxLeads, scope, shellQuote(categoryExample), categoryGuidance)
}

func reconLeadCategoryGuidance(cfg Config) (string, string) {
	riskAreas, _ := normalizedRiskAreas(cfg.Instructions.RiskAreas)
	if len(riskAreas) == 0 {
		return "security", `Use a concise category such as "authorization", "data exposure", "input parsing", or "deployment boundaries" when it better describes the lead than "security".`
	}
	return riskAreas[0], "Use one configured risk area as each lead category. Allowed categories: " + strings.Join(riskAreas, ", ") + "."
}

func riskAreaSet(items []string) map[string]bool {
	riskAreas, _ := normalizedRiskAreas(items)
	out := map[string]bool{}
	for _, item := range riskAreas {
		out[strings.ToLower(item)] = true
	}
	return out
}

func writeAndRegisterRunnerManifest(runDir, runID, workspace, opencodePath, opencodeVersionOutput string) error {
	relPath := "evidence/runner-manifest.json"
	path := filepath.Join(runDir, filepath.FromSlash(relPath))
	data, err := runnerManifestJSON(runID, workspace, opencodePath, opencodeVersionOutput)
	if err != nil {
		return err
	}
	if existing, ok := ledgerTaskEvidence(runDir, "", relPath); ok {
		if err := registeredEvidenceFileError(runDir, relPath, existing.ContentSHA256, validateNonEmptyEvidenceFile); err != nil {
			return fmt.Errorf("registered runner manifest is unusable: %w", err)
		}
		current, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read registered runner manifest: %w", err)
		}
		if !bytes.Equal(current, data) {
			return fmt.Errorf("registered runner manifest %s has different content", relPath)
		}
		_, err = registerRunnerEvidence(runDir, runID, "json", "Runner lifecycle manifest", relPath, false)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".runner-manifest-*.json")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	_, err = registerRunnerEvidence(runDir, runID, "json", "Runner lifecycle manifest", relPath, false)
	return err
}

func runnerManifestJSON(runID, workspace, opencodePath, opencodeVersionOutput string) ([]byte, error) {
	entries, err := workspaceFileList(workspace)
	if err != nil {
		return nil, err
	}
	manifest := map[string]any{
		"run_id":           runID,
		"workspace":        workspace,
		"workspace_files":  entries,
		"opencode_path":    opencodePath,
		"opencode_version": strings.TrimSpace(opencodeVersionOutput),
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func writeRunnerManifest(path, runID, workspace, opencodePath, opencodeVersionOutput string) error {
	data, err := runnerManifestJSON(runID, workspace, opencodePath, opencodeVersionOutput)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return err
	}
	return os.WriteFile(path, data, filePerm)
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

func ensureOpenCode() (string, string, error) {
	if path, err := exec.LookPath("opencode"); err == nil {
		version, err := commandOutput(path, "--version")
		if err != nil {
			return "", "", err
		}
		if opencodeVersionMatches(version) {
			return path, version, nil
		}
	}

	install := fmt.Sprintf("curl -fsSL https://opencode.ai/install | bash -s -- --version %s --no-modify-path", shellQuote(opencodeVersion))
	command := exec.Command("bash", "-lc", install)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return "", "", fmt.Errorf("install opencode: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	path := filepath.Join(home, ".opencode", "bin", "opencode")
	version, err := commandOutput(path, "--version")
	if err != nil {
		return "", "", err
	}
	if !opencodeVersionMatches(version) {
		return "", "", fmt.Errorf("opencode install produced version %q, want %s", strings.TrimSpace(version), opencodeVersion)
	}
	return path, version, nil
}

func opencodeVersionMatches(output string) bool {
	for _, field := range strings.Fields(output) {
		if field == opencodeVersion {
			return true
		}
	}
	return strings.TrimSpace(output) == opencodeVersion
}

func commandOutput(name string, args ...string) (string, error) {
	command := exec.Command(name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, string(output))
	}
	return string(output), nil
}

func validateRunnerRunID(runID string) error {
	for _, r := range runID {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '_' || r == '-' {
			continue
		}
		return fmt.Errorf("invalid run id %q: use only letters, digits, underscores, and hyphens", runID)
	}
	return nil
}
