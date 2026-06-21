package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type taskSetupResult struct {
	Env        []string
	LogRelPath string
	LogPath    string
}

func runTaskSetupHook(ctx context.Context, workspace, runDir string, task opencodeTask, attempt int) (taskSetupResult, error) {
	if strings.TrimSpace(task.Setup.Script) == "" {
		return taskSetupResult{}, nil
	}
	setup := task.Setup
	logRelPath := taskSetupLogRelPath(task.TaskID, attempt)
	logPath := filepath.Join(runDir, filepath.FromSlash(logRelPath))
	result := taskSetupResult{LogRelPath: logRelPath, LogPath: logPath}

	scriptRel, err := cleanWorkspaceRelativePath(setup.Script)
	if err != nil {
		return result, fmt.Errorf("setup hook script: %w", err)
	}
	scriptPath := filepath.Join(workspace, filepath.FromSlash(scriptRel))
	info, err := os.Stat(scriptPath)
	if err != nil {
		return result, setupHookError(task, logRelPath, fmt.Errorf("setup hook script is not readable: %w", err))
	}
	if info.IsDir() {
		return result, setupHookError(task, logRelPath, fmt.Errorf("setup hook script must be a file: %s", scriptRel))
	}
	if err := os.MkdirAll(filepath.Dir(logPath), dirPerm); err != nil {
		return result, err
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, filePerm)
	if err != nil {
		return result, err
	}
	envFile, err := os.CreateTemp("", "mnm-setup-env-*")
	if err != nil {
		_ = logFile.Close()
		return result, err
	}
	envPath := envFile.Name()
	if closeErr := envFile.Close(); closeErr != nil {
		_ = logFile.Close()
		_ = os.Remove(envPath)
		return result, closeErr
	}
	defer os.Remove(envPath)

	started := time.Now().UTC()
	fmt.Fprintf(logFile, "mnm setup hook started at %s\n", started.Format(time.RFC3339Nano))
	fmt.Fprintf(logFile, "task_id=%s phase=%s attempt=%d\n", task.TaskID, task.Phase, attempt)
	fmt.Fprintf(logFile, "workspace=%s\nscript=%s\nmode=%s timeout=%s\n\n", workspace, scriptRel, runnerSetupMode(setup), effectiveRunnerSetupTimeout(setup))

	setupCtx, cancel := context.WithTimeout(ctx, effectiveRunnerSetupTimeout(setup))
	defer cancel()
	command := exec.CommandContext(setupCtx, "bash", "-lc", setupHookShell(), "mnm-setup", workspace, scriptPath, envPath)
	isolateCommandProcessGroup(command)
	command.Env = append(os.Environ(),
		"MNM_RUN_DIR="+runDir,
		"MNM_TASK_ID="+task.TaskID,
		"MNM_PHASE="+task.Phase,
		"MNM_WORKSPACE="+workspace,
		"MNM_SETUP_LOG="+logPath,
	)
	command.Stdout = logFile
	command.Stderr = logFile
	runErr := command.Run()
	cleanupErr := cleanupCommandProcessGroup(command)
	if runErr == nil && setupCtx.Err() != nil {
		runErr = setupCtx.Err()
	}
	if cleanupErr != nil {
		cleanupErr = fmt.Errorf("clean up setup hook process group: %w", cleanupErr)
		if runErr != nil {
			runErr = errors.Join(runErr, cleanupErr)
		} else {
			runErr = cleanupErr
		}
	}

	ended := time.Now().UTC()
	if runErr != nil {
		fmt.Fprintf(logFile, "\nmnm setup hook failed at %s after %s: %v\n", ended.Format(time.RFC3339Nano), ended.Sub(started).Round(time.Millisecond), runErr)
		closeErr := logFile.Close()
		if closeErr != nil {
			runErr = errors.Join(runErr, closeErr)
		}
		if runnerSetupMode(setup) == "warn" {
			if closeErr != nil {
				return result, closeErr
			}
			return result, nil
		}
		return result, setupHookError(task, logRelPath, runErr)
	}
	fmt.Fprintf(logFile, "\nmnm setup hook completed at %s after %s\n", ended.Format(time.RFC3339Nano), ended.Sub(started).Round(time.Millisecond))
	if closeErr := logFile.Close(); closeErr != nil {
		return result, closeErr
	}
	env, err := readNullSeparatedEnv(envPath)
	if err != nil {
		return result, setupHookError(task, logRelPath, err)
	}
	result.Env = env
	return result, nil
}

func setupHookShell() string {
	return strings.Join([]string{
		"set -euo pipefail",
		"workspace=$1",
		"script=$2",
		"env_file=$3",
		"cd \"$workspace\"",
		"source \"$script\"",
		"env -0 > \"$env_file\"",
	}, "\n")
}

func taskSetupLogRelPath(taskID string, attempt int) string {
	if attempt <= 0 {
		attempt = 1
	}
	return filepath.ToSlash(filepath.Join("evidence", "setup-"+safeFileID(taskID)+fmt.Sprintf("-attempt-%d.log", attempt)))
}

func setupHookError(task opencodeTask, logRelPath string, cause error) error {
	return fmt.Errorf("setup hook failed for task %s (%s); see %s: %w", task.TaskID, task.Phase, logRelPath, cause)
}

func validateRunnerTaskSetupFlags(setup RunnerSetupConfig) error {
	if strings.TrimSpace(setup.Script) == "" {
		if setup.TimeoutMinutes < 0 {
			return errors.New("runner task --setup-timeout-minutes must not be negative")
		}
		if mode := strings.TrimSpace(setup.Mode); mode != "" && !oneOf(mode, "fail", "warn") {
			return errors.New(`runner task --setup-mode must be "fail" or "warn"`)
		}
		return nil
	}
	if _, err := cleanWorkspaceRelativePath(setup.Script); err != nil {
		return fmt.Errorf("runner task --setup-script: %w", err)
	}
	if setup.TimeoutMinutes <= 0 {
		return errors.New("runner task --setup-timeout-minutes must be positive")
	}
	if !oneOf(runnerSetupMode(setup), "fail", "warn") {
		return errors.New(`runner task --setup-mode must be "fail" or "warn"`)
	}
	return nil
}

func readNullSeparatedEnv(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read setup hook environment: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	parts := strings.Split(string(data), "\x00")
	env := make([]string, 0, len(parts))
	for _, item := range parts {
		if item == "" || !strings.Contains(item, "=") {
			continue
		}
		env = append(env, item)
	}
	sort.Strings(env)
	return env, nil
}

func mergeEnv(base []string, overlays ...[]string) []string {
	values := map[string]string{}
	order := make([]string, 0, len(base))
	add := func(item string) {
		index := strings.IndexByte(item, '=')
		if index <= 0 {
			return
		}
		key := item[:index]
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = item[index+1:]
	}
	for _, item := range base {
		add(item)
	}
	for _, overlay := range overlays {
		for _, item := range overlay {
			add(item)
		}
	}
	merged := make([]string, 0, len(order))
	for _, key := range order {
		merged = append(merged, key+"="+values[key])
	}
	return merged
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix)
		}
	}
	return ""
}
