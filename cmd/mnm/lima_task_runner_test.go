package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLimaTaskRunnerCommandSequence(t *testing.T) {
	runDir := t.TempDir()
	ledgerDir := filepath.Join(runDir, "ledger")
	outputDir := filepath.Join(runDir, "task-output")
	for _, dir := range []string{ledgerDir, outputDir} {
		if err := os.MkdirAll(filepath.Join(dir, "evidence"), dirPerm); err != nil {
			t.Fatal(err)
		}
	}
	payload := filepath.Join(runDir, "mnm-linux-test")
	if err := os.WriteFile(payload, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MNM_LINUX_RUNNER_PAYLOAD", payload)
	snapshot := filepath.Join(runDir, "snapshot.tar.zst")
	if err := os.WriteFile(snapshot, []byte("snapshot"), filePerm); err != nil {
		t.Fatal(err)
	}
	promptPath := filepath.Join(runDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("prompt"), filePerm); err != nil {
		t.Fatal(err)
	}
	task := TaskRecord{
		RunID:  "run_abc",
		TaskID: "task_recon",
		Phase:  "recon",
	}
	if err := writeTaskFile(filepath.Join(outputDir, currentTaskFile), task); err != nil {
		t.Fatal(err)
	}

	executor := &recordingExecutor{}
	runner := LimaRunner{Executor: executor, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	err := runner.RunTask(context.Background(), LimaTaskRequest{
		RunID:        "run_abc",
		Task:         task,
		Attempt:      2,
		Config:       RunnerConfig{CPUs: 2, MemoryGB: 4, DiskGB: 20, OpenCodeTaskTimeoutMinutes: 7},
		SnapshotPath: snapshot,
		LedgerDir:    ledgerDir,
		OutputDir:    outputDir,
		PromptPath:   promptPath,
		Model:        "openrouter/test",
	})
	if err != nil {
		t.Fatalf("task runner failed: %v", err)
	}

	instanceName := limaTaskInstanceName("run_abc", "task_recon", 2)
	joined := strings.Join(executor.commands, "\n")
	for _, want := range []string{
		"limactl delete --force --tty=false " + instanceName,
		"limactl create --tty=false --name " + instanceName + " --cpus 2 --memory 4 --disk 20 template:docker",
		"limactl start --tty=false " + instanceName,
		"limactl shell " + instanceName + " bash -lc rm -rf /tmp/mnm-ledger /tmp/mnm-output /tmp/mnm-workspace",
		"limactl copy --backend=scp -r " + payload + " " + instanceName + ":/tmp/mnm",
		"limactl copy --backend=scp -r " + snapshot + " " + instanceName + ":/tmp/snapshot.tar.zst",
		"limactl copy --backend=scp -r " + promptPath + " " + instanceName + ":/tmp/mnm-prompt.md",
		"limactl copy --backend=scp -r " + filepath.Clean(ledgerDir) + "/. " + instanceName + ":/tmp/mnm-ledger",
		"limactl copy --backend=scp -r " + filepath.Clean(outputDir) + "/. " + instanceName + ":/tmp/mnm-output",
		"/tmp/mnm runner task --run-dir '/tmp/mnm-output' --ledger-dir '/tmp/mnm-ledger' --workspace '/tmp/mnm-workspace' --snapshot '/tmp/snapshot.tar.zst'",
		"--task-file '/tmp/mnm-output/current-task.json'",
		"--prompt-file '/tmp/mnm-prompt.md'",
		"--model 'openrouter/test'",
		"--log-path 'evidence/opencode-task_recon.jsonl'",
		"--timeout-minutes 7",
		"limactl copy --backend=scp -r " + instanceName + ":/tmp/mnm-output",
		"limactl stop --tty=false " + instanceName,
		"limactl delete --force --tty=false " + instanceName,
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}
	if _, err := os.Stat(filepath.Join(outputDir, ".events.lock")); !os.IsNotExist(err) {
		t.Fatalf("stale copied task lock should be dropped, stat err=%v", err)
	}
	if got := readFile(t, filepath.Join(outputDir, "evidence", "task-output-marker.txt")); !strings.Contains(got, "copied") {
		t.Fatalf("task output was not copied back:\n%s", got)
	}
}

func TestLimaTaskRunnerKeepsVMWhenRequested(t *testing.T) {
	runDir := t.TempDir()
	ledgerDir := filepath.Join(runDir, "ledger")
	outputDir := filepath.Join(runDir, "task-output")
	for _, dir := range []string{ledgerDir, outputDir} {
		if err := os.MkdirAll(dir, dirPerm); err != nil {
			t.Fatal(err)
		}
	}
	payload := filepath.Join(runDir, "mnm-linux-test")
	if err := os.WriteFile(payload, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MNM_LINUX_RUNNER_PAYLOAD", payload)
	snapshot := filepath.Join(runDir, "snapshot.tar.zst")
	if err := os.WriteFile(snapshot, []byte("snapshot"), filePerm); err != nil {
		t.Fatal(err)
	}
	promptPath := filepath.Join(runDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("prompt"), filePerm); err != nil {
		t.Fatal(err)
	}
	task := TaskRecord{RunID: "run_keep", TaskID: "task_recon", Phase: "recon"}
	if err := writeTaskFile(filepath.Join(outputDir, currentTaskFile), task); err != nil {
		t.Fatal(err)
	}

	executor := &recordingExecutor{}
	runner := LimaRunner{Executor: executor, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	err := runner.RunTask(context.Background(), LimaTaskRequest{
		RunID:        "run_keep",
		Task:         task,
		Attempt:      1,
		Config:       RunnerConfig{CPUs: 1, MemoryGB: 2, DiskGB: 10},
		SnapshotPath: snapshot,
		LedgerDir:    ledgerDir,
		OutputDir:    outputDir,
		PromptPath:   promptPath,
		Model:        "openrouter/test",
		KeepVM:       true,
	})
	if err != nil {
		t.Fatalf("task runner failed: %v", err)
	}

	joined := strings.Join(executor.commands, "\n")
	instanceName := limaTaskInstanceName("run_keep", "task_recon", 1)
	deleteCommand := "limactl delete --force --tty=false " + instanceName
	if strings.Count(joined, deleteCommand) != 1 {
		t.Fatalf("task VM should only be deleted during pre-create cleanup when kept:\n%s", joined)
	}
	if !strings.Contains(joined, "limactl stop --tty=false "+instanceName) {
		t.Fatalf("task VM should still be stopped:\n%s", joined)
	}
}

func TestLimaTaskInstanceNameAvoidsLongNameCollisions(t *testing.T) {
	runID := "run_12345678-1234-4234-9234-123456789abc"
	first := limaTaskInstanceName(runID, "task_investigate_payment_authorization_bypass_alpha", 1)
	second := limaTaskInstanceName(runID, "task_investigate_payment_authorization_bypass_beta", 1)

	if first == second {
		t.Fatalf("task instance names collided:\n%s", first)
	}
	for _, name := range []string{first, second} {
		if len(name) > maxLimaTaskInstanceNameLen {
			t.Fatalf("instance name %q has length %d, want <= %d", name, len(name), maxLimaTaskInstanceNameLen)
		}
	}
}

func TestLimaTaskInstanceNameShortensLongNames(t *testing.T) {
	name := limaTaskInstanceName("run_004311f6-f32f-4c30-b608-012bde0a6e11", "task_recon", 1)
	if len(name) > maxLimaTaskInstanceNameLen {
		t.Fatalf("task instance name length = %d, want <= %d: %s", len(name), maxLimaTaskInstanceNameLen, name)
	}
	if !strings.HasPrefix(name, "mnm-run-004311f6") {
		t.Fatalf("task instance name lost readable prefix: %s", name)
	}
	if !strings.Contains(name, "-") {
		t.Fatalf("task instance name should include hash separator: %s", name)
	}
	if name == limaTaskInstanceName("run_004311f6-f32f-4c30-b608-012bde0a6e11", "task_recon", 2) {
		t.Fatalf("different attempts should produce different task VM names: %s", name)
	}
}

func TestLimaTaskAttemptRunnerRunsTaskVMBundle(t *testing.T) {
	runDir := t.TempDir()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "tasks"), dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(runDir, "evidence"), dirPerm); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(runDir, "mnm-linux-test")
	if err := os.WriteFile(payload, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MNM_LINUX_RUNNER_PAYLOAD", payload)
	snapshot := filepath.Join(runDir, "snapshot.tar.zst")
	if err := os.WriteFile(snapshot, []byte("snapshot"), filePerm); err != nil {
		t.Fatal(err)
	}
	task := TaskRecord{RunID: "run_attempt", TaskID: "task_review_finding_auth", Phase: "review"}
	taskPath := filepath.Join(runDir, "tasks", task.TaskID+".json")
	if err := writeTaskFile(taskPath, task); err != nil {
		t.Fatal(err)
	}

	executor := &recordingExecutor{}
	attemptRunner := LimaTaskAttemptRunner{
		Runner:       LimaRunner{Executor: executor, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}},
		Config:       RunnerConfig{CPUs: 2, MemoryGB: 4, DiskGB: 20, OpenCodeTaskTimeoutMinutes: 9},
		SnapshotPath: snapshot,
	}
	result, err := attemptRunner.RunOpenCodeTaskAttempt(context.Background(), workspace, runDir, opencodeTask{
		RunID:    task.RunID,
		TaskID:   task.TaskID,
		Phase:    task.Phase,
		Title:    "mnm review auth",
		Model:    "openrouter/test",
		Prompt:   "Workspace: " + workspace + "\nRun directory: " + runDir + "\nLedger path: " + filepath.Join(runDir, eventsFile),
		LogPath:  filepath.Join(runDir, "evidence", "opencode-review-auth.jsonl"),
		TaskFile: taskPath,
	}, 3)
	if err != nil {
		t.Fatalf("attempt runner failed: %v", err)
	}

	wantOutputDir := filepath.Join(runDir, taskBundlesDir, safeFileID(task.TaskID), "attempt-3")
	if !result.Bundle || result.TaskRunDir != wantOutputDir {
		t.Fatalf("result = %#v, want bundle output %s", result, wantOutputDir)
	}
	if _, err := os.Stat(filepath.Join(wantOutputDir, currentTaskFile)); err != nil {
		t.Fatalf("task file was not staged in output bundle: %v", err)
	}
	prompt := readFile(t, filepath.Join(wantOutputDir, "prompt.md"))
	for _, want := range []string{
		"Task output directory: /tmp/mnm-output",
		"Ledger snapshot directory: /tmp/mnm-ledger",
		"Workspace: /tmp/mnm-workspace",
		"Run directory: /tmp/mnm-output",
		"Ledger path: /tmp/mnm-ledger/events.jsonl",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, workspace) || strings.Contains(prompt, runDir) {
		t.Fatalf("prompt leaked host paths:\n%s", prompt)
	}

	instanceName := limaTaskInstanceName(task.RunID, task.TaskID, 3)
	joined := strings.Join(executor.commands, "\n")
	for _, want := range []string{
		"limactl create --tty=false --name " + instanceName,
		"/tmp/mnm runner task --run-dir '/tmp/mnm-output' --ledger-dir '/tmp/mnm-ledger'",
		"--model 'openrouter/test'",
		"--log-path 'evidence/opencode-review-auth.jsonl'",
		"--timeout-minutes 9",
		"--skip-bundle-verify",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, filepath.Clean(runDir)+"/. "+instanceName+":/tmp/mnm-ledger") {
		t.Fatalf("attempt runner should copy a pruned ledger snapshot, not the full run dir:\n%s", joined)
	}
	if got := readFile(t, filepath.Join(wantOutputDir, "evidence", "task-output-marker.txt")); !strings.Contains(got, "copied") {
		t.Fatalf("task VM output was not copied into attempt bundle:\n%s", got)
	}
	if got := readFile(t, filepath.Join(runDir, "evidence", "opencode-review-auth.jsonl")); !strings.Contains(got, `"type":"done"`) {
		t.Fatalf("task transcript was not copied to central evidence:\n%s", got)
	}
}

func TestLimaTaskAttemptRunnerCopiesFailureLogForRetry(t *testing.T) {
	runDir := t.TempDir()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "tasks"), dirPerm); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(runDir, "mnm-linux-test")
	if err := os.WriteFile(payload, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MNM_LINUX_RUNNER_PAYLOAD", payload)
	snapshot := filepath.Join(runDir, "snapshot.tar.zst")
	if err := os.WriteFile(snapshot, []byte("snapshot"), filePerm); err != nil {
		t.Fatal(err)
	}
	taskRecord := TaskRecord{RunID: "run_attempt_fail", TaskID: "task_review_finding_auth", Phase: "review"}
	taskPath := filepath.Join(runDir, "tasks", taskRecord.TaskID+".json")
	if err := writeTaskFile(taskPath, taskRecord); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(runDir, "evidence", "opencode-review-auth.jsonl")
	task := opencodeTask{
		RunID:    taskRecord.RunID,
		TaskID:   taskRecord.TaskID,
		Phase:    taskRecord.Phase,
		Title:    "mnm review auth",
		Model:    "openrouter/test",
		Prompt:   "review auth",
		LogPath:  logPath,
		TaskFile: taskPath,
	}

	executor := &failingTaskExecutor{logRelPath: "evidence/opencode-review-auth.jsonl"}
	attemptRunner := LimaTaskAttemptRunner{
		Runner:       LimaRunner{Executor: executor, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}},
		Config:       RunnerConfig{CPUs: 2, MemoryGB: 4, DiskGB: 20},
		SnapshotPath: snapshot,
	}
	result, err := attemptRunner.RunOpenCodeTaskAttempt(context.Background(), workspace, runDir, task, 1)
	if err == nil {
		t.Fatal("expected failing VM attempt")
	}
	var attemptErr openCodeAttemptError
	if !errors.As(err, &attemptErr) {
		t.Fatalf("error %T should wrap openCodeAttemptError: %v", err, err)
	}
	if !strings.Contains(attemptErr.logText, `"code":502`) {
		t.Fatalf("attempt error missing copied log text: %#v", attemptErr)
	}
	if !retryableOpenCodeError(task.LogPath, err) {
		t.Fatalf("copied VM log should make error retryable: %v", err)
	}
	if got := readFile(t, logPath); !strings.Contains(got, `"code":502`) {
		t.Fatalf("host log was not copied from VM output:\n%s", got)
	}
	if !result.Bundle {
		t.Fatalf("failed VM attempt should still report bundle output: %#v", result)
	}
}

func TestLimaTaskAttemptRunnerRequiresBundleTask(t *testing.T) {
	result, err := (LimaTaskAttemptRunner{}).RunOpenCodeTaskAttempt(context.Background(), t.TempDir(), t.TempDir(), opencodeTask{
		RunID:  "run_no_bundle",
		TaskID: "task_recon",
		Phase:  "recon",
	}, 1)
	if err == nil {
		t.Fatal("expected missing task bundle metadata error")
	}
	if !strings.Contains(err.Error(), "require task bundle metadata") {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Bundle {
		t.Fatalf("non-bundle task should not report bundle result: %#v", result)
	}
}

type failingTaskExecutor struct {
	commands   []string
	logRelPath string
}

func (executor *failingTaskExecutor) Run(_ context.Context, name string, args ...string) error {
	executor.commands = append(executor.commands, name+" "+strings.Join(args, " "))
	if name == "limactl" && len(args) >= 5 && args[0] == "copy" && strings.HasSuffix(args[len(args)-2], ":/tmp/mnm-output") {
		dst := args[len(args)-1]
		outDir := filepath.Join(dst, "mnm-output")
		if err := os.MkdirAll(filepath.Dir(filepath.Join(outDir, filepath.FromSlash(executor.logRelPath))), dirPerm); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outDir, filepath.FromSlash(executor.logRelPath)), []byte(`{"code":502,"message":"bad gateway"}`+"\n"), filePerm); err != nil {
			return err
		}
	}
	if name == "limactl" && len(args) >= 4 && args[0] == "shell" && strings.Contains(strings.Join(args, " "), "/tmp/mnm runner task") {
		return errors.New("guest task failed")
	}
	return nil
}
