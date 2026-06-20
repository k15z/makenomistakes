package main

import (
	"bytes"
	"context"
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
		if len(name) > 63 {
			t.Fatalf("instance name %q has length %d, want <= 63", name, len(name))
		}
		if !strings.HasSuffix(name, "-a1") {
			t.Fatalf("instance name %q should keep the attempt suffix", name)
		}
	}
}
