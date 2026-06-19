package main

import (
	"context"
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

type RunnerRequest struct {
	Run         RunRecord
	Config      Config
	ModelAPIKey string
	KeepVM      bool
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
	cfg, err := loadConfig(*configPath)
	if err != nil {
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
	if err := ensureWorkspaceToolchains(workspace); err != nil {
		return err
	}
	opencodePath, opencodeVersionOutput, err := ensureOpenCode()
	if err != nil {
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

	if err := runReconTask(*runDir, *runID, workspace, cfg, opencodePath); err != nil {
		return err
	}
	if err := runInvestigatePhase(*runDir, *runID, workspace, cfg, opencodePath); err != nil {
		return err
	}
	if err := runReviewPhase(*runDir, *runID, workspace, cfg, opencodePath); err != nil {
		return err
	}
	if err := runDeduplicatePhase(*runDir, *runID, workspace, cfg, opencodePath); err != nil {
		return err
	}
	if err := runValidatePhase(*runDir, *runID, workspace, cfg, opencodePath); err != nil {
		return err
	}
	if err := runFinalizeTask(*runDir, *runID, workspace, cfg, opencodePath); err != nil {
		return err
	}

	manifestPath := filepath.Join(*runDir, "evidence", "runner-manifest.json")
	if err := writeRunnerManifest(manifestPath, *runID, workspace, opencodePath, opencodeVersionOutput); err != nil {
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

func runReconTask(runDir, runID, workspace string, cfg Config, opencodePath string) error {
	task := TaskRecord{
		RunID:       runID,
		TaskID:      "task_recon",
		Phase:       "recon",
		Title:       "Recon",
		Instruction: "Map the workspace, interpret scope, identify risks, and create focused leads for later investigation.",
	}
	if err := writeTaskFile(filepath.Join(runDir, currentTaskFile), task); err != nil {
		return err
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    runID,
		Type:     "task.started",
		Object:   "task",
		ObjectID: task.TaskID,
		TaskID:   task.TaskID,
		Data: map[string]any{
			"phase": task.Phase,
			"title": task.Title,
		},
	}); err != nil {
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
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    runID,
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: newLedgerID("evidence"),
		TaskID:   task.TaskID,
		Data: map[string]any{
			"kind":  "markdown",
			"title": "Recon prompt",
			"path":  "evidence/recon-prompt.md",
		},
	}); err != nil {
		return err
	}
	logPath := filepath.Join(runDir, "evidence", "opencode-recon.jsonl")
	if err := runOpenCodeTask(opencodePath, taskWorkspace, runDir, opencodeTask{
		RunID:   runID,
		TaskID:  task.TaskID,
		Phase:   task.Phase,
		Title:   "mnm recon",
		Model:   phaseModel(cfg, "recon"),
		Prompt:  prompt,
		LogPath: logPath,
	}); err != nil {
		return err
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    runID,
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: newLedgerID("evidence"),
		TaskID:   task.TaskID,
		Data: map[string]any{
			"kind":  "jsonl",
			"title": "OpenCode Recon transcript",
			"path":  "evidence/opencode-recon.jsonl",
		},
	}); err != nil {
		return err
	}
	if !ledgerTaskCompleted(runDir, task.TaskID) {
		return errors.New("recon opencode task did not complete through mnm task complete")
	}
	return nil
}

type opencodeTask struct {
	RunID     string
	TaskID    string
	Phase     string
	LeadID    string
	FindingID string
	Title     string
	Model     string
	Prompt    string
	LogPath   string
	TaskFile  string
}

func runOpenCodeTask(opencodePath, workspace, runDir string, task opencodeTask) error {
	var lastErr error
	attempts := 0
	for attempt := 1; attempt <= openCodeMaxAttempts; attempt++ {
		attempts = attempt
		err := runOpenCodeTaskAttempt(opencodePath, workspace, runDir, task, attempt)
		if err == nil {
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
			time.Sleep(openCodeRetryDelay * time.Duration(attempt))
		}
	}
	return fmt.Errorf("opencode task %s failed after %d attempt(s): %w", task.TaskID, attempts, lastErr)
}

func runOpenCodeTaskAttempt(opencodePath, workspace, runDir string, task opencodeTask, attempt int) error {
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	if attempt > 1 {
		flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
	}
	logFile, err := os.OpenFile(task.LogPath, flag, filePerm)
	if err != nil {
		return err
	}
	defer logFile.Close()
	command := exec.Command(opencodePath,
		"run",
		"--format", "json",
		"--dir", workspace,
		"--model", task.Model,
		"--title", task.Title,
		"--dangerously-skip-permissions",
		task.Prompt,
	)
	env := append(os.Environ(),
		"MNM_RUN_DIR="+runDir,
		"MNM_TASK_ID="+task.TaskID,
		"MNM_PHASE="+task.Phase,
		"PATH=/tmp:"+os.Getenv("PATH"),
	)
	if task.LeadID != "" {
		env = append(env, "MNM_LEAD_ID="+task.LeadID)
	}
	if task.FindingID != "" {
		env = append(env, "MNM_FINDING_ID="+task.FindingID)
	}
	if task.TaskFile != "" {
		env = append(env, taskFileEnv+"="+task.TaskFile)
	}
	command.Env = env
	command.Stdout = io.MultiWriter(os.Stdout, logFile)
	command.Stderr = io.MultiWriter(os.Stderr, logFile)
	return command.Run()
}

func retryableOpenCodeError(logPath string, err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	if b, readErr := os.ReadFile(logPath); readErr == nil {
		text += "\n" + strings.ToLower(string(b))
	}
	for _, marker := range []string{
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
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
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
	switch phase {
	case "recon":
		if strings.TrimSpace(cfg.Models.Recon) != "" {
			return strings.TrimSpace(cfg.Models.Recon)
		}
	case "investigate":
		if strings.TrimSpace(cfg.Models.Investigate) != "" {
			return strings.TrimSpace(cfg.Models.Investigate)
		}
	case "review":
		if strings.TrimSpace(cfg.Models.Review) != "" {
			return strings.TrimSpace(cfg.Models.Review)
		}
	case "deduplicate":
		if strings.TrimSpace(cfg.Models.Deduplicate) != "" {
			return strings.TrimSpace(cfg.Models.Deduplicate)
		}
	case "validate":
		if strings.TrimSpace(cfg.Models.Validate) != "" {
			return strings.TrimSpace(cfg.Models.Validate)
		}
	case "finalize":
		if strings.TrimSpace(cfg.Models.Finalize) != "" {
			return strings.TrimSpace(cfg.Models.Finalize)
		}
	}
	return strings.TrimSpace(cfg.Models.Default)
}

func ledgerTaskCompleted(runDir, taskID string) bool {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return false
	}
	for _, event := range events {
		if event.Type == "task.completed" && event.Object == "task" && event.ObjectID == taskID {
			return true
		}
	}
	return false
}

func reconPrompt(runDir, workspace string, cfg Config) string {
	scope := strings.TrimSpace(cfg.Instructions.Scope)
	if scope == "" {
		scope = "No additional scope instructions were provided."
	}
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
9. Create focused leads. For each lead, write a body file under %[2]s/evidence/, then run: mnm lead create --title "Specific lead title" --category security --priority medium --body-file %[2]s/evidence/lead-specific-name.md
10. Create no more than %[3]d leads.
11. Complete the task with: mnm task complete --status completed --summary "Recon completed"

Lead quality bar:

- A lead is a focused question or risk area, not a final finding.
- Prefer specific components, files, flows, trust boundaries, data flows, parsers, auth paths, dependency risks, or runtime setup concerns.
- Include enough context that a later Investigate task can start without redoing Recon.
- Avoid generic leads like "review security" or "check code quality".
`, workspace, runDir, cfg.Runner.MaxLeads, scope)
}

func writeRunnerManifest(path, runID, workspace, opencodePath, opencodeVersionOutput string) error {
	entries, err := workspaceFileList(workspace)
	if err != nil {
		return err
	}
	manifest := map[string]any{
		"run_id":           runID,
		"workspace":        workspace,
		"workspace_files":  entries,
		"opencode_path":    opencodePath,
		"opencode_version": strings.TrimSpace(opencodeVersionOutput),
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

func ensureOpenCode() (string, string, error) {
	if path, err := exec.LookPath("opencode"); err == nil {
		version, err := commandOutput(path, "--version")
		return path, version, err
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
	pathEnv := filepath.Join(home, ".opencode", "bin") + string(os.PathListSeparator) + os.Getenv("PATH")
	os.Setenv("PATH", pathEnv)
	path, err := exec.LookPath("opencode")
	if err != nil {
		return "", "", fmt.Errorf("opencode install completed but binary was not found: %w", err)
	}
	version, err := commandOutput(path, "--version")
	return path, version, err
}

func commandOutput(name string, args ...string) (string, error) {
	command := exec.Command(name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, string(output))
	}
	return string(output), nil
}
