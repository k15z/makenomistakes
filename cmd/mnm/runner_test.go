package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunnerCommandExtractsSnapshotAndWritesLifecycleEvents(t *testing.T) {
	prependFakeOpenCode(t, opencodeVersion+"\n")
	source := t.TempDir()
	writeWorkspaceFile(t, source, "repo/app.go", "package main")
	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.zst")
	if err := createWorkspaceSnapshot(SnapshotOptions{
		WorkspaceRoot: source,
		WorkspaceDir:  source,
		OutputPath:    snapshot,
	}); err != nil {
		t.Fatal(err)
	}

	runDir := t.TempDir()
	configPath := filepath.Join(runDir, "mnm.toml")
	if err := os.WriteFile(configPath, []byte(defaultConfig()), filePerm); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"runner",
		"--run-id", "run_test",
		"--run-dir", runDir,
		"--snapshot", snapshot,
		"--config", configPath,
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runner failed: %v\nstderr: %s", err, stderr.String())
	}

	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	types := eventTypes(events)
	for _, want := range []string{"runner.started", "evidence.added", "runner.completed"} {
		if !contains(types, want) {
			t.Fatalf("missing event type %q in %#v", want, types)
		}
	}
	for _, want := range []string{"task.started", "lead.created", "task.completed"} {
		if !contains(types, want) {
			t.Fatalf("missing recon event type %q in %#v", want, types)
		}
	}
	for _, want := range []string{"finding.created", "lead.closed", "verdict.recorded"} {
		if !contains(types, want) {
			t.Fatalf("missing audit event type %q in %#v", want, types)
		}
	}
	if !ledgerTaskCompleted(runDir, "task_deduplicate") {
		t.Fatal("expected deduplicate task to complete")
	}
	if !ledgerTaskCompleted(runDir, "task_validate_finding_fake_lead_fake_auth") {
		t.Fatal("expected validate task to complete")
	}
	if !ledgerTaskCompleted(runDir, "task_finalize") {
		t.Fatal("expected finalize task to complete")
	}
	if !ledgerReportFinalized(runDir) {
		t.Fatal("expected report to be finalized")
	}
	manifest := readFile(t, filepath.Join(runDir, "evidence", "runner-manifest.json"))
	if !strings.Contains(manifest, "repo/app.go") {
		t.Fatalf("manifest missing unpacked workspace file:\n%s", manifest)
	}
	if strings.Contains(manifest, "mutated-by-opencode") {
		t.Fatalf("manifest included task-local mutation:\n%s", manifest)
	}
	if !strings.Contains(manifest, `"opencode_version": "`+opencodeVersion+`"`) {
		t.Fatalf("manifest missing opencode version:\n%s", manifest)
	}
}

func TestRunnerCommandStopsAfterRecon(t *testing.T) {
	prependFakeOpenCode(t, opencodeVersion+"\n")
	source := t.TempDir()
	writeWorkspaceFile(t, source, "repo/app.go", "package main")
	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.zst")
	if err := createWorkspaceSnapshot(SnapshotOptions{
		WorkspaceRoot: source,
		WorkspaceDir:  source,
		OutputPath:    snapshot,
	}); err != nil {
		t.Fatal(err)
	}

	runDir := t.TempDir()
	configPath := filepath.Join(runDir, "mnm.toml")
	if err := os.WriteFile(configPath, []byte(defaultConfig()), filePerm); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"runner",
		"--run-id", "run_test",
		"--run-dir", runDir,
		"--snapshot", snapshot,
		"--config", configPath,
		"--stop-after", "recon",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runner failed: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "runner stopped after recon") {
		t.Fatalf("stdout missing stop-after message:\n%s", stdout.String())
	}
	if !ledgerTaskCompleted(runDir, "task_recon") {
		t.Fatal("expected recon task to complete")
	}
	if ledgerTaskCompleted(runDir, "task_investigate_lead_fake_auth") {
		t.Fatal("investigate should not run after stop-after recon")
	}
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	types := eventTypes(events)
	if !contains(types, "lead.created") {
		t.Fatalf("expected recon lead in %#v", types)
	}
	if !contains(types, "runner.stopped") {
		t.Fatalf("expected runner.stopped in %#v", types)
	}
	if contains(types, "runner.completed") {
		t.Fatalf("runner.completed should not be written on stop-after recon: %#v", types)
	}
	for _, event := range events {
		if event.Type == "runner.stopped" {
			if event.ObjectID != "run_test" || event.Data["phase"] != "recon" {
				t.Fatalf("unexpected runner.stopped event: %#v", event)
			}
		}
	}
}

func TestEnsureOpenCodeInstallsWhenExistingVersionMismatches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	prependFakeOpenCode(t, "0.0.0\n")
	prependFakeOpenCodeInstaller(t, opencodeVersion+"\n")

	path, version, err := ensureOpenCode()
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(home, ".opencode", "bin", "opencode")
	if path != wantPath {
		t.Fatalf("expected managed opencode path %q, got %q", wantPath, path)
	}
	if strings.TrimSpace(version) != opencodeVersion {
		t.Fatalf("expected opencode version %q, got %q", opencodeVersion, strings.TrimSpace(version))
	}
}

func TestRunnerCommandRejectsUnsafeRunID(t *testing.T) {
	runDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	err := run([]string{
		"runner",
		"--run-id", "run/../../victim",
		"--run-dir", runDir,
		"--snapshot", filepath.Join(runDir, "snapshot.tar.zst"),
		"--config", filepath.Join(runDir, "mnm.toml"),
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected unsafe run id error")
	}
	if !strings.Contains(err.Error(), "invalid run id") {
		t.Fatalf("expected invalid run id error, got %v", err)
	}
}

func TestRunReconTaskRejectsFailedTaskCompletion(t *testing.T) {
	runDir := t.TempDir()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "evidence"), dirPerm); err != nil {
		t.Fatal(err)
	}
	opencodePath := writeFakeOpenCode(t, opencodeVersion+"\n", `
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
cat >> "$MNM_RUN_DIR/events.jsonl" <<'EOF'
{"id":"event_failed_done","run_id":"run_test","type":"task.completed","object":"task","object_id":"task_recon","task_id":"task_recon","timestamp":"2026-01-01T00:00:03Z","data":{"status":"failed","summary":"Recon failed"}}
EOF
exit 0
`)

	err := runReconTask(runDir, "run_test", workspace, reconTestConfig(), opencodePath)
	if err == nil {
		t.Fatal("expected failed recon completion error")
	}
	if !strings.Contains(err.Error(), "did not complete successfully") {
		t.Fatalf("expected completion status error, got %v", err)
	}
}

func TestValidateReconLedgerOutputsUsesLatestCompletionStatus(t *testing.T) {
	runDir := newLedgerTestRun(t)
	for _, event := range []LedgerEvent{
		{
			RunID:    "run_recon",
			Type:     "evidence.added",
			Object:   "evidence",
			ObjectID: "evidence_map",
			TaskID:   "task_recon",
			Data: map[string]any{
				"kind":  "markdown",
				"title": "Recon codebase map",
				"path":  "evidence/recon-codebase-map.md",
			},
		},
		{
			RunID:    "run_recon",
			Type:     "evidence.added",
			Object:   "evidence",
			ObjectID: "evidence_risk",
			TaskID:   "task_recon",
			Data: map[string]any{
				"kind":  "markdown",
				"title": "Recon risk register",
				"path":  "evidence/recon-risk-register.md",
			},
		},
		{
			RunID:    "run_recon",
			Type:     "lead.created",
			Object:   "lead",
			ObjectID: "lead_auth",
			TaskID:   "task_recon",
			Data: map[string]any{
				"title":     "Investigate auth",
				"category":  "authz",
				"priority":  "high",
				"body_path": "evidence/lead-auth.md",
			},
		},
		{
			RunID:    "run_recon",
			Type:     "task.completed",
			Object:   "task",
			ObjectID: "task_recon",
			TaskID:   "task_recon",
			Data:     map[string]any{"status": "completed"},
		},
		{
			RunID:    "run_recon",
			Type:     "task.completed",
			Object:   "task",
			ObjectID: "task_recon",
			TaskID:   "task_recon",
			Data:     map[string]any{"status": "failed"},
		},
	} {
		if err := appendLedgerEvent(runDir, event); err != nil {
			t.Fatal(err)
		}
	}

	err := validateReconLedgerOutputs(runDir, "task_recon")
	if err == nil {
		t.Fatal("expected latest failed recon completion to be rejected")
	}
	if !strings.Contains(err.Error(), "did not complete successfully") {
		t.Fatalf("expected completion status error, got %v", err)
	}
}

func TestRunReconTaskRequiresRegisteredOutputs(t *testing.T) {
	runDir := t.TempDir()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "evidence"), dirPerm); err != nil {
		t.Fatal(err)
	}
	opencodePath := writeFakeOpenCode(t, opencodeVersion+"\n", `
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
cat >> "$MNM_RUN_DIR/events.jsonl" <<'EOF'
{"id":"event_done_only","run_id":"run_test","type":"task.completed","object":"task","object_id":"task_recon","task_id":"task_recon","timestamp":"2026-01-01T00:00:03Z","data":{"status":"completed","summary":"Recon completed"}}
EOF
exit 0
`)

	err := runReconTask(runDir, "run_test", workspace, reconTestConfig(), opencodePath)
	if err == nil {
		t.Fatal("expected missing recon output error")
	}
	if !strings.Contains(err.Error(), "recon-codebase-map.md") {
		t.Fatalf("expected missing codebase map error, got %v", err)
	}
}

func TestRunnerCommandRecordsFailureEvidence(t *testing.T) {
	dir := t.TempDir()
	opencodePath := filepath.Join(dir, "opencode")
	body := `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then
  printf '` + opencodeVersion + `\n'
  exit 0
fi
if [ "${1:-}" = "run" ]; then
  printf 'fatal recon failure\n'
  exit 1
fi
exit 0
`
	if err := os.WriteFile(opencodePath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	source := t.TempDir()
	writeWorkspaceFile(t, source, "repo/app.go", "package main")
	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.zst")
	if err := createWorkspaceSnapshot(SnapshotOptions{
		WorkspaceRoot: source,
		WorkspaceDir:  source,
		OutputPath:    snapshot,
	}); err != nil {
		t.Fatal(err)
	}

	runDir := t.TempDir()
	configPath := filepath.Join(runDir, "mnm.toml")
	if err := os.WriteFile(configPath, []byte(defaultConfig()), filePerm); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"runner",
		"--run-id", "run_failure",
		"--run-dir", runDir,
		"--snapshot", snapshot,
		"--config", configPath,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected runner failure")
	}
	if !strings.Contains(err.Error(), "task_recon") {
		t.Fatalf("unexpected error: %v", err)
	}

	failure := readFile(t, filepath.Join(runDir, "evidence", "runner-failure.json"))
	for _, want := range []string{
		`"run_id": "run_failure"`,
		`"stage": "recon"`,
		"fatal recon failure",
		`"opencode_version": "` + opencodeVersion + `"`,
	} {
		if !strings.Contains(failure, want) {
			t.Fatalf("failure manifest missing %q:\n%s", want, failure)
		}
	}

	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	types := eventTypes(events)
	if !contains(types, "runner.started") {
		t.Fatalf("expected runner.started event in %#v", types)
	}
	if contains(types, "runner.completed") {
		t.Fatalf("did not expect runner.completed event in %#v", types)
	}
	foundFailure := false
	foundFailureEvidence := false
	for _, event := range events {
		if event.Type == "runner.failed" && event.ObjectID == "run_failure" {
			foundFailure = true
			if event.Data["stage"] != "recon" || event.Data["path"] != "evidence/runner-failure.json" {
				t.Fatalf("unexpected runner.failed data: %#v", event.Data)
			}
		}
		if event.Type == "evidence.added" && event.Data["path"] == "evidence/runner-failure.json" {
			foundFailureEvidence = true
		}
	}
	if !foundFailure {
		t.Fatalf("missing runner.failed event in %#v", events)
	}
	if !foundFailureEvidence {
		t.Fatalf("missing failure evidence event in %#v", events)
	}
}

func TestRunnerFailureEvidenceRegistrationIsIdempotent(t *testing.T) {
	runDir := t.TempDir()
	failure := runnerFailureContext{
		RunID:  "run_failure",
		RunDir: runDir,
		Stage:  "recon",
	}

	if err := failure.Record(errors.New("first failure")); err != nil {
		t.Fatalf("first failure record failed: %v", err)
	}
	if err := failure.Record(errors.New("second failure")); err != nil {
		t.Fatalf("second failure record failed: %v", err)
	}

	assertEvidenceEventCount(t, runDir, "", "evidence/runner-failure.json", 1)
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	var failures int
	for _, event := range events {
		if event.Type == "runner.failed" && event.ObjectID == "run_failure" {
			failures++
		}
	}
	if failures != 2 {
		t.Fatalf("runner.failed event count = %d, want 2", failures)
	}
}

func TestLimaRunnerCommandSequence(t *testing.T) {
	runDir := t.TempDir()
	payload := filepath.Join(runDir, "mnm-linux-test")
	if err := os.WriteFile(payload, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MNM_LINUX_RUNNER_PAYLOAD", payload)
	snapshot := filepath.Join(runDir, "snapshot.tar.zst")
	if err := os.WriteFile(snapshot, []byte("snapshot"), filePerm); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(runDir, "mnm.toml")
	if err := os.WriteFile(configPath, []byte(defaultConfig()), filePerm); err != nil {
		t.Fatal(err)
	}

	executor := &recordingExecutor{}
	runner := LimaRunner{Executor: executor, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	err := runner.Run(context.Background(), RunnerRequest{
		Run: RunRecord{
			ID:                 "run_abc",
			RunDir:             runDir,
			SnapshotPath:       snapshot,
			ConfigSnapshotPath: configPath,
		},
		Config:         Config{Runner: RunnerConfig{CPUs: 2, MemoryGB: 4, DiskGB: 20}},
		StopAfterPhase: "recon",
	})
	if err != nil {
		t.Fatalf("runner failed: %v", err)
	}

	joined := strings.Join(executor.commands, "\n")
	for _, want := range []string{
		"limactl create --tty=false --name mnm-run-abc --cpus 2 --memory 4 --disk 20 template:docker",
		"limactl start --tty=false mnm-run-abc",
		"limactl copy --backend=scp " + payload + " mnm-run-abc:/tmp/mnm",
		"limactl shell mnm-run-abc bash -lc",
		"command -v rg",
		"apt-get install -y ripgrep",
		"--stop-after 'recon'",
		"limactl stop --tty=false mnm-run-abc",
		"limactl delete --force --tty=false mnm-run-abc",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}
	if _, err := os.Stat(filepath.Join(runDir, ".events.lock")); !os.IsNotExist(err) {
		t.Fatalf("stale copied ledger lock should be dropped, stat err=%v", err)
	}
}

func TestLimaRunnerSeedsExistingRunDirectoryWhenResuming(t *testing.T) {
	runDir := t.TempDir()
	payload := filepath.Join(runDir, "mnm-linux-test")
	if err := os.WriteFile(payload, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MNM_LINUX_RUNNER_PAYLOAD", payload)
	snapshot := filepath.Join(runDir, "snapshot.tar.zst")
	if err := os.WriteFile(snapshot, []byte("snapshot"), filePerm); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(runDir, "mnm.toml")
	if err := os.WriteFile(configPath, []byte(defaultConfig()), filePerm); err != nil {
		t.Fatal(err)
	}

	executor := &recordingExecutor{}
	runner := LimaRunner{Executor: executor, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	err := runner.Run(context.Background(), RunnerRequest{
		Run: RunRecord{
			ID:                 "run_resume",
			RunDir:             runDir,
			SnapshotPath:       snapshot,
			ConfigSnapshotPath: configPath,
		},
		Config: Config{Runner: RunnerConfig{CPUs: 2, MemoryGB: 4, DiskGB: 20}},
		Resume: true,
	})
	if err != nil {
		t.Fatalf("runner failed: %v", err)
	}

	joined := strings.Join(executor.commands, "\n")
	for _, want := range []string{
		"limactl shell mnm-run-resume bash -lc rm -rf /tmp/mnm-run && mkdir -p /tmp/mnm-run",
		"limactl copy --backend=scp -r " + filepath.Clean(runDir) + "/. mnm-run-resume:/tmp/mnm-run",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing resume command %q in:\n%s", want, joined)
		}
	}
}

func TestLimaRunnerPreflightRequiresLima(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := (LimaRunner{}).Preflight(context.Background(), RunnerPreflightRequest{})
	if err == nil {
		t.Fatal("expected missing limactl error")
	}
	if !strings.Contains(err.Error(), "limactl is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLimaRunnerPreflightRequiresGoWhenBuildingPayload(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "limactl"))
	t.Setenv("PATH", dir)
	t.Setenv("MNM_LINUX_RUNNER_PAYLOAD", "")

	err := (LimaRunner{}).Preflight(context.Background(), RunnerPreflightRequest{})
	if err == nil {
		t.Fatal("expected missing go error")
	}
	if !strings.Contains(err.Error(), "go is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLimaRunnerPreflightAcceptsProvidedPayloadWithoutGo(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "limactl"))
	t.Setenv("PATH", dir)
	t.Setenv("MNM_LINUX_RUNNER_PAYLOAD", filepath.Join(dir, "mnm-linux"))

	runner := LimaRunner{ResourceDetector: func() (HostResources, error) {
		return HostResources{}, nil
	}}
	if err := runner.Preflight(context.Background(), RunnerPreflightRequest{}); err != nil {
		t.Fatalf("preflight failed: %v", err)
	}
}

func TestLimaRunnerPreflightRejectsCPURequestAboveHost(t *testing.T) {
	runner := limaRunnerWithHostResources(t, HostResources{
		CPUs:          4,
		MemoryBytes:   16 * bytesPerGiB,
		DiskFreeBytes: 100 * bytesPerGiB,
		DiskPath:      "/tmp",
	})

	err := runner.Preflight(context.Background(), RunnerPreflightRequest{
		Config: Config{Runner: RunnerConfig{CPUs: 8}},
	})
	if err == nil {
		t.Fatal("expected CPU resource error")
	}
	if !strings.Contains(err.Error(), "runner.cpus requests 8 CPUs, but host reports 4") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLimaRunnerPreflightRejectsMemoryRequestAboveHost(t *testing.T) {
	runner := limaRunnerWithHostResources(t, HostResources{
		CPUs:          8,
		MemoryBytes:   8 * bytesPerGiB,
		DiskFreeBytes: 100 * bytesPerGiB,
		DiskPath:      "/tmp",
	})

	err := runner.Preflight(context.Background(), RunnerPreflightRequest{
		Config: Config{Runner: RunnerConfig{MemoryGB: 16}},
	})
	if err == nil {
		t.Fatal("expected memory resource error")
	}
	if !strings.Contains(err.Error(), "runner.memory_gb requests 16 GiB") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLimaRunnerPreflightRejectsDiskRequestAboveLimaFreeSpace(t *testing.T) {
	runner := limaRunnerWithHostResources(t, HostResources{
		CPUs:          8,
		MemoryBytes:   16 * bytesPerGiB,
		DiskFreeBytes: 40 * bytesPerGiB,
		DiskPath:      "/tmp/lima",
	})

	err := runner.Preflight(context.Background(), RunnerPreflightRequest{
		Config: Config{Runner: RunnerConfig{DiskGB: 80}},
	})
	if err == nil {
		t.Fatal("expected disk resource error")
	}
	if !strings.Contains(err.Error(), "runner.disk_gb requests 80 GiB") || !strings.Contains(err.Error(), "/tmp/lima") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLimaRunnerPreflightAcceptsSufficientHostResources(t *testing.T) {
	runner := limaRunnerWithHostResources(t, HostResources{
		CPUs:          8,
		MemoryBytes:   16 * bytesPerGiB,
		DiskFreeBytes: 100 * bytesPerGiB,
		DiskPath:      "/tmp/lima",
	})

	if err := runner.Preflight(context.Background(), RunnerPreflightRequest{
		Config: Config{Runner: RunnerConfig{
			CPUs:     4,
			MemoryGB: 8,
			DiskGB:   80,
		}},
	}); err != nil {
		t.Fatalf("preflight failed: %v", err)
	}
}

func TestGuestRunnerCommandBootstrapsRipgrepBeforeRunner(t *testing.T) {
	command := guestRunnerCommand(RunRecord{ID: "run_quote'value"}, "")

	ripgrepInstall := "apt-get install -y ripgrep"
	runnerStart := "/tmp/mnm runner --run-id 'run_quote'\\''value'"
	installIndex := strings.Index(command, ripgrepInstall)
	runnerIndex := strings.Index(command, runnerStart)
	if installIndex == -1 {
		t.Fatalf("guest runner command missing ripgrep install:\n%s", command)
	}
	if runnerIndex == -1 {
		t.Fatalf("guest runner command missing quoted runner invocation:\n%s", command)
	}
	if installIndex > runnerIndex {
		t.Fatalf("ripgrep install should happen before runner starts:\n%s", command)
	}
	if !strings.Contains(command, "rm -f /tmp/mnm-run/.events.lock") {
		t.Fatalf("guest runner command should clear stale ledger locks before runner starts:\n%s", command)
	}
	if !strings.Contains(command, "ripgrep is required in the audit VM") {
		t.Fatalf("guest runner command should fail clearly when ripgrep cannot be installed:\n%s", command)
	}
	if _, err := exec.LookPath("bash"); err == nil {
		check := exec.Command("bash", "-n")
		check.Stdin = strings.NewReader(command)
		if output, err := check.CombinedOutput(); err != nil {
			t.Fatalf("guest runner command has invalid bash syntax: %v\n%s\n%s", err, output, command)
		}
	}
}

func TestGuestRunnerCommandPassesStopAfterPhase(t *testing.T) {
	command := guestRunnerCommand(RunRecord{ID: "run_checkpoint"}, "recon")

	if !strings.Contains(command, "/tmp/mnm runner --run-id 'run_checkpoint'") {
		t.Fatalf("guest runner command missing runner invocation:\n%s", command)
	}
	if !strings.Contains(command, "--stop-after 'recon'") {
		t.Fatalf("guest runner command missing stop-after phase:\n%s", command)
	}
	if _, err := exec.LookPath("bash"); err == nil {
		check := exec.Command("bash", "-n")
		check.Stdin = strings.NewReader(command)
		if output, err := check.CombinedOutput(); err != nil {
			t.Fatalf("guest runner command has invalid bash syntax: %v\n%s\n%s", err, output, command)
		}
	}
}

func TestRunReconTaskSkipsCompletedTask(t *testing.T) {
	runDir := t.TempDir()
	writeRunFile(t, runDir, "evidence/recon-codebase-map.md", "# Codebase map\n\nMap.\n")
	writeRunFile(t, runDir, "evidence/recon-risk-register.md", "# Risk register\n\nRisks.\n")
	writeRunFile(t, runDir, "evidence/lead-auth.md", "# Lead\n\nInvestigate auth.\n")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_resume",
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_map",
		TaskID:   "task_recon",
		Data: map[string]any{
			"kind":           "markdown",
			"title":          "Recon codebase map",
			"path":           "evidence/recon-codebase-map.md",
			"content_sha256": runFileSHA256ForTest(t, runDir, "evidence/recon-codebase-map.md"),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_resume",
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_risk",
		TaskID:   "task_recon",
		Data: map[string]any{
			"kind":           "markdown",
			"title":          "Recon risk register",
			"path":           "evidence/recon-risk-register.md",
			"content_sha256": runFileSHA256ForTest(t, runDir, "evidence/recon-risk-register.md"),
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_resume",
		Type:     "lead.created",
		Object:   "lead",
		ObjectID: "lead_auth",
		TaskID:   "task_recon",
		Data: map[string]any{
			"title":     "Investigate auth",
			"category":  "authz",
			"priority":  "high",
			"body_path": "evidence/lead-auth.md",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_resume",
		Type:     "task.completed",
		Object:   "task",
		ObjectID: "task_recon",
		TaskID:   "task_recon",
		Data: map[string]any{
			"status":  "completed",
			"summary": "Recon already completed",
		},
	}); err != nil {
		t.Fatal(err)
	}
	opencodePath := writeFakeOpenCode(t, opencodeVersion+"\n", `#!/bin/sh
printf 'recon should not run\n' >&2
exit 42
`)

	err := runReconTask(runDir, "run_resume", t.TempDir(), Config{}, opencodePath)
	if err != nil {
		t.Fatalf("completed recon should be skipped, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "evidence", "recon-prompt.md")); !os.IsNotExist(err) {
		t.Fatalf("completed recon should not rewrite prompt, stat err=%v", err)
	}
}

func TestRunReconTaskRetriesCompletedTaskWithMissingOutputs(t *testing.T) {
	runDir := newLedgerTestRun(t)
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_resume",
		Type:     "task.completed",
		Object:   "task",
		ObjectID: "task_recon",
		TaskID:   "task_recon",
		Data: map[string]any{
			"status":  "completed",
			"summary": "Recon completed without outputs",
		},
	}); err != nil {
		t.Fatal(err)
	}
	opencodePath := writeFakeOpenCode(t, opencodeVersion+"\n", `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
cat > "$MNM_RUN_DIR/evidence/recon-codebase-map.md" <<'EOF'
# Codebase map

Map.
EOF
cat > "$MNM_RUN_DIR/evidence/recon-risk-register.md" <<'EOF'
# Risk register

Risks.
EOF
cat > "$MNM_RUN_DIR/evidence/lead-auth.md" <<'EOF'
# Lead

Investigate auth.
EOF
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_recon_map_$$","run_id":"run_resume","type":"evidence.added","object":"evidence","object_id":"evidence_recon_map_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Recon codebase map","path":"evidence/recon-codebase-map.md","content_sha256":"7ceb463d44eead4e12069f563210b74451e5c80d8db60ac9fcad006a1ce7d555"}}
{"id":"event_recon_risk_$$","run_id":"run_resume","type":"evidence.added","object":"evidence","object_id":"evidence_recon_risk_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"kind":"markdown","title":"Recon risk register","path":"evidence/recon-risk-register.md","content_sha256":"bae9973f8742bc2bb69737139307dc93c649cb06e5a3b2e0b4efc5f21d33c46c"}}
{"id":"event_recon_lead_$$","run_id":"run_resume","type":"lead.created","object":"lead","object_id":"lead_auth","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"title":"Investigate auth","category":"authz","priority":"high","body_path":"evidence/lead-auth.md"}}
{"id":"event_recon_done_$$","run_id":"run_resume","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:03Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`)

	err := runReconTask(runDir, "run_resume", t.TempDir(), Config{Runner: RunnerConfig{MaxLeads: 3}, Models: ModelConfig{Default: "fake/model"}}, opencodePath)
	if err != nil {
		t.Fatalf("recon should retry incomplete completion, got: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "evidence", "recon-prompt.md")); err != nil {
		t.Fatalf("recon should rerun and write prompt: %v", err)
	}
}

func TestRegisterTaskStartedIsIdempotentForSameMetadata(t *testing.T) {
	runDir := t.TempDir()
	task := TaskRecord{
		RunID:       "run_task_start",
		TaskID:      "task_deduplicate",
		Phase:       "deduplicate",
		Title:       "Deduplicate reviewed findings",
		Instruction: "Cluster findings.",
	}
	extra := map[string]any{
		"findings": []string{"finding_one", "finding_two"},
	}

	if err := registerTaskStarted(runDir, task, extra); err != nil {
		t.Fatalf("first task start failed: %v", err)
	}
	if err := registerTaskStarted(runDir, task, extra); err != nil {
		t.Fatalf("idempotent task start failed: %v", err)
	}
	assertTaskStartedEventCount(t, runDir, task.TaskID, 1)
}

func TestRegisterTaskStartedIsAtomicForParallelSameMetadata(t *testing.T) {
	runDir := t.TempDir()
	task := TaskRecord{
		RunID:       "run_task_start",
		TaskID:      "task_deduplicate",
		Phase:       "deduplicate",
		Title:       "Deduplicate reviewed findings",
		Instruction: "Cluster findings.",
	}
	extra := map[string]any{
		"findings": []string{"finding_one", "finding_two"},
	}

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- registerTaskStarted(runDir, task, extra)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("parallel task start failed: %v", err)
		}
	}
	assertTaskStartedEventCount(t, runDir, task.TaskID, 1)
}

func TestRegisterTaskStartedRejectsConflictingMetadata(t *testing.T) {
	runDir := t.TempDir()
	task := TaskRecord{
		RunID:       "run_task_start",
		TaskID:      "task_review_finding_auth",
		Phase:       "review",
		Title:       "Review: auth finding",
		Instruction: "Review finding.",
	}

	if err := registerTaskStarted(runDir, task, map[string]any{
		"finding_id": "finding_auth",
	}); err != nil {
		t.Fatalf("first task start failed: %v", err)
	}
	err := registerTaskStarted(runDir, task, map[string]any{
		"finding_id": "finding_other",
	})
	if err == nil {
		t.Fatal("expected conflicting task start metadata error")
	}
	if !strings.Contains(err.Error(), "task task_review_finding_auth already started with different metadata") {
		t.Fatalf("unexpected error: %v", err)
	}
	assertTaskStartedEventCount(t, runDir, task.TaskID, 1)
}

func TestRunReconTaskRequiresContextEvidence(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	opencodePath := writeFakeOpenCode(t, opencodeVersion+"\n", `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_recon_done_$$","run_id":"run_recon","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`)

	err := runReconTask(runDir, "run_recon", t.TempDir(), Config{Runner: RunnerConfig{MaxLeads: 3}, Models: ModelConfig{Default: "fake/model"}}, opencodePath)
	if err == nil {
		t.Fatal("expected missing recon context evidence error")
	}
	if !strings.Contains(err.Error(), "did not register required evidence evidence/recon-codebase-map.md") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunReconTaskReregistersPromptEvidenceIdempotently(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	opencodePath := writeFakeOpenCode(t, opencodeVersion+"\n", `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
count_file="$MNM_RUN_DIR/recon-attempt-count"
count=0
if [ -f "$count_file" ]; then
  count="$(cat "$count_file")"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$count_file"
if [ "$count" -le 3 ]; then
  printf '{"type":"done","attempt":%s}\n' "$count"
  exit 0
fi
cat > "$MNM_RUN_DIR/evidence/recon-codebase-map.md" <<'EOF'
# Codebase map

Map.
EOF
cat > "$MNM_RUN_DIR/evidence/recon-risk-register.md" <<'EOF'
# Risk register

Risks.
EOF
cat > "$MNM_RUN_DIR/evidence/lead-auth.md" <<'EOF'
# Lead

Investigate auth.
EOF
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_recon_map_$count","run_id":"run_recon","type":"evidence.added","object":"evidence","object_id":"evidence_recon_map_$count","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Recon codebase map","path":"evidence/recon-codebase-map.md","content_sha256":"7ceb463d44eead4e12069f563210b74451e5c80d8db60ac9fcad006a1ce7d555"}}
{"id":"event_recon_risk_$count","run_id":"run_recon","type":"evidence.added","object":"evidence","object_id":"evidence_recon_risk_$count","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"kind":"markdown","title":"Recon risk register","path":"evidence/recon-risk-register.md","content_sha256":"bae9973f8742bc2bb69737139307dc93c649cb06e5a3b2e0b4efc5f21d33c46c"}}
{"id":"event_recon_lead_$count","run_id":"run_recon","type":"lead.created","object":"lead","object_id":"lead_auth","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"title":"Investigate auth","category":"authz","priority":"high","body_path":"evidence/lead-auth.md"}}
{"id":"event_recon_done_$count","run_id":"run_recon","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:03Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done","attempt":%s}\n' "$count"
`)

	err := runReconTask(runDir, "run_recon", t.TempDir(), Config{Runner: RunnerConfig{MaxLeads: 3}, Models: ModelConfig{Default: "fake/model"}}, opencodePath)
	if err == nil {
		t.Fatal("expected first recon attempt to fail postconditions")
	}
	if err := runReconTask(runDir, "run_recon", t.TempDir(), Config{Runner: RunnerConfig{MaxLeads: 3}, Models: ModelConfig{Default: "fake/model"}}, opencodePath); err != nil {
		t.Fatalf("second recon run failed: %v", err)
	}

	assertEvidenceEventCount(t, runDir, "task_recon", "evidence/recon-prompt.md", 1)
	assertEvidenceEventCount(t, runDir, "task_recon", "evidence/opencode-recon.jsonl", 1)
	assertTaskStartedEventCount(t, runDir, "task_recon", 1)
}

func TestRunReconTaskRequiresContextEvidenceFiles(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	opencodePath := writeFakeOpenCode(t, opencodeVersion+"\n", `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
cat > "$MNM_RUN_DIR/evidence/lead-auth.md" <<'EOF'
# Lead

Investigate auth.
EOF
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_recon_map_$$","run_id":"run_recon","type":"evidence.added","object":"evidence","object_id":"evidence_recon_map_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Recon codebase map","path":"evidence/recon-codebase-map.md"}}
{"id":"event_recon_risk_$$","run_id":"run_recon","type":"evidence.added","object":"evidence","object_id":"evidence_recon_risk_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"kind":"markdown","title":"Recon risk register","path":"evidence/recon-risk-register.md"}}
{"id":"event_recon_lead_$$","run_id":"run_recon","type":"lead.created","object":"lead","object_id":"lead_auth","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"title":"Investigate auth","category":"authz","priority":"high","body_path":"evidence/lead-auth.md"}}
{"id":"event_recon_done_$$","run_id":"run_recon","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:03Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`)

	err := runReconTask(runDir, "run_recon", t.TempDir(), Config{Runner: RunnerConfig{MaxLeads: 3}, Models: ModelConfig{Default: "fake/model"}}, opencodePath)
	if err == nil {
		t.Fatal("expected missing recon evidence file error")
	}
	if !strings.Contains(err.Error(), "read evidence file evidence/recon-codebase-map.md") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunReconTaskRequiresLeads(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	opencodePath := writeFakeOpenCode(t, opencodeVersion+"\n", `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
cat > "$MNM_RUN_DIR/evidence/recon-codebase-map.md" <<'EOF'
# Codebase map

Map.
EOF
cat > "$MNM_RUN_DIR/evidence/recon-risk-register.md" <<'EOF'
# Risk register

Risks.
EOF
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_recon_map_$$","run_id":"run_recon","type":"evidence.added","object":"evidence","object_id":"evidence_recon_map_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Recon codebase map","path":"evidence/recon-codebase-map.md","content_sha256":"7ceb463d44eead4e12069f563210b74451e5c80d8db60ac9fcad006a1ce7d555"}}
{"id":"event_recon_risk_$$","run_id":"run_recon","type":"evidence.added","object":"evidence","object_id":"evidence_recon_risk_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"kind":"markdown","title":"Recon risk register","path":"evidence/recon-risk-register.md","content_sha256":"bae9973f8742bc2bb69737139307dc93c649cb06e5a3b2e0b4efc5f21d33c46c"}}
{"id":"event_recon_done_$$","run_id":"run_recon","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`)

	err := runReconTask(runDir, "run_recon", t.TempDir(), Config{Runner: RunnerConfig{MaxLeads: 3}, Models: ModelConfig{Default: "fake/model"}}, opencodePath)
	if err == nil {
		t.Fatal("expected missing recon lead error")
	}
	if !strings.Contains(err.Error(), "did not create any investigation leads") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunReconTaskRequiresRegisteredOutputHashes(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	opencodePath := writeFakeOpenCode(t, opencodeVersion+"\n", `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
cat > "$MNM_RUN_DIR/evidence/recon-codebase-map.md" <<'EOF'
# Codebase map

Map.
EOF
cat > "$MNM_RUN_DIR/evidence/recon-risk-register.md" <<'EOF'
# Risk register

Risks.
EOF
cat > "$MNM_RUN_DIR/evidence/lead-auth.md" <<'EOF'
# Lead
EOF
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_recon_map_$$","run_id":"run_recon","type":"evidence.added","object":"evidence","object_id":"evidence_recon_map_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Recon codebase map","path":"evidence/recon-codebase-map.md"}}
{"id":"event_recon_risk_$$","run_id":"run_recon","type":"evidence.added","object":"evidence","object_id":"evidence_recon_risk_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"kind":"markdown","title":"Recon risk register","path":"evidence/recon-risk-register.md","content_sha256":"bae9973f8742bc2bb69737139307dc93c649cb06e5a3b2e0b4efc5f21d33c46c"}}
{"id":"event_recon_lead_$$","run_id":"run_recon","type":"lead.created","object":"lead","object_id":"lead_auth","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"title":"Investigate auth","category":"authz","priority":"high","body_path":"evidence/lead-auth.md"}}
{"id":"event_recon_done_$$","run_id":"run_recon","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:03Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`)

	err := runReconTask(runDir, "run_recon", t.TempDir(), Config{Runner: RunnerConfig{MaxLeads: 3}, Models: ModelConfig{Default: "fake/model"}}, opencodePath)
	if err == nil {
		t.Fatal("expected missing recon output hash error")
	}
	if !strings.Contains(err.Error(), "evidence file evidence/recon-codebase-map.md is missing registered content hash") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunReconTaskRejectsChangedRegisteredOutputs(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	opencodePath := writeFakeOpenCode(t, opencodeVersion+"\n", `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
cat > "$MNM_RUN_DIR/evidence/recon-codebase-map.md" <<'EOF'
# Codebase map

Map.
EOF
cat > "$MNM_RUN_DIR/evidence/recon-risk-register.md" <<'EOF'
# Risk register

Risks.
EOF
cat > "$MNM_RUN_DIR/evidence/lead-auth.md" <<'EOF'
# Lead
EOF
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_recon_map_$$","run_id":"run_recon","type":"evidence.added","object":"evidence","object_id":"evidence_recon_map_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Recon codebase map","path":"evidence/recon-codebase-map.md","content_sha256":"7ceb463d44eead4e12069f563210b74451e5c80d8db60ac9fcad006a1ce7d555"}}
{"id":"event_recon_risk_$$","run_id":"run_recon","type":"evidence.added","object":"evidence","object_id":"evidence_recon_risk_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"kind":"markdown","title":"Recon risk register","path":"evidence/recon-risk-register.md","content_sha256":"bae9973f8742bc2bb69737139307dc93c649cb06e5a3b2e0b4efc5f21d33c46c"}}
{"id":"event_recon_lead_$$","run_id":"run_recon","type":"lead.created","object":"lead","object_id":"lead_auth","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"title":"Investigate auth","category":"authz","priority":"high","body_path":"evidence/lead-auth.md"}}
{"id":"event_recon_done_$$","run_id":"run_recon","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:03Z","data":{"status":"completed","summary":"done"}}
EOF
cat > "$MNM_RUN_DIR/evidence/recon-codebase-map.md" <<'EOF'
# Codebase map

Changed after registration.
EOF
printf '{"type":"done"}\n'
`)

	err := runReconTask(runDir, "run_recon", t.TempDir(), Config{Runner: RunnerConfig{MaxLeads: 3}, Models: ModelConfig{Default: "fake/model"}}, opencodePath)
	if err == nil {
		t.Fatal("expected changed recon output error")
	}
	if !strings.Contains(err.Error(), "evidence file evidence/recon-codebase-map.md changed after registration") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunReconTaskRejectsTooManyLeads(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := newLedgerTestRun(t)
	opencodePath := writeFakeOpenCode(t, opencodeVersion+"\n", `#!/bin/sh
set -eu
: "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
: "${MNM_TASK_ID:?MNM_TASK_ID is required}"
cat > "$MNM_RUN_DIR/evidence/recon-codebase-map.md" <<'EOF'
# Codebase map

Map.
EOF
cat > "$MNM_RUN_DIR/evidence/recon-risk-register.md" <<'EOF'
# Risk register

Risks.
EOF
cat > "$MNM_RUN_DIR/evidence/lead-one.md" <<'EOF'
# Lead one
EOF
cat > "$MNM_RUN_DIR/evidence/lead-two.md" <<'EOF'
# Lead two
EOF
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_recon_map_$$","run_id":"run_recon","type":"evidence.added","object":"evidence","object_id":"evidence_recon_map_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Recon codebase map","path":"evidence/recon-codebase-map.md","content_sha256":"7ceb463d44eead4e12069f563210b74451e5c80d8db60ac9fcad006a1ce7d555"}}
{"id":"event_recon_risk_$$","run_id":"run_recon","type":"evidence.added","object":"evidence","object_id":"evidence_recon_risk_$$","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:01Z","data":{"kind":"markdown","title":"Recon risk register","path":"evidence/recon-risk-register.md","content_sha256":"bae9973f8742bc2bb69737139307dc93c649cb06e5a3b2e0b4efc5f21d33c46c"}}
{"id":"event_recon_lead_one_$$","run_id":"run_recon","type":"lead.created","object":"lead","object_id":"lead_one","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:02Z","data":{"title":"Investigate one","category":"authz","priority":"high","body_path":"evidence/lead-one.md"}}
{"id":"event_recon_lead_two_$$","run_id":"run_recon","type":"lead.created","object":"lead","object_id":"lead_two","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:03Z","data":{"title":"Investigate two","category":"authz","priority":"medium","body_path":"evidence/lead-two.md"}}
{"id":"event_recon_done_$$","run_id":"run_recon","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:04Z","data":{"status":"completed","summary":"done"}}
EOF
printf '{"type":"done"}\n'
`)

	err := runReconTask(runDir, "run_recon", t.TempDir(), Config{Runner: RunnerConfig{MaxLeads: 1}, Models: ModelConfig{Default: "fake/model"}}, opencodePath)
	if err == nil {
		t.Fatal("expected too many leads error")
	}
	if !strings.Contains(err.Error(), "created 2 leads, exceeding configured max_leads 1") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunOpenCodeTaskRetriesTransientProviderFailure(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "evidence"), dirPerm); err != nil {
		t.Fatal(err)
	}
	opencodePath := writeRetryFakeOpenCode(t, `#!/bin/sh
set -eu
count_file="$MNM_RUN_DIR/attempt-count"
count=0
if [ -f "$count_file" ]; then
  count="$(cat "$count_file")"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$count_file"
if [ "$count" -eq 1 ]; then
  printf '{"code":502,"message":"Network connection lost.","metadata":{"error_type":"provider_unavailable"}}\n'
  exit 1
fi
printf '{"type":"done"}\n'
`)

	err := runOpenCodeTask(opencodePath, t.TempDir(), runDir, opencodeTask{
		RunID:   "run_retry",
		TaskID:  "task_retry",
		Phase:   "review",
		Title:   "mnm retry test",
		Model:   "openrouter/test",
		Prompt:  "retry me",
		LogPath: filepath.Join(runDir, "evidence", "opencode-retry.jsonl"),
	})
	if err != nil {
		t.Fatalf("expected retry to recover, got: %v", err)
	}

	count := strings.TrimSpace(readFile(t, filepath.Join(runDir, "attempt-count")))
	if count != "2" {
		t.Fatalf("attempt count = %s, want 2", count)
	}
	log := readFile(t, filepath.Join(runDir, "evidence", "opencode-retry.jsonl"))
	if !strings.Contains(log, "provider_unavailable") || !strings.Contains(log, `"type":"done"`) {
		t.Fatalf("retry log did not preserve both attempts:\n%s", log)
	}
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	retries := 0
	for _, event := range events {
		if event.Type == "task.retrying" && event.ObjectID == "task_retry" {
			retries++
		}
	}
	if retries != 1 {
		t.Fatalf("retry event count = %d, want 1", retries)
	}
}

func TestRunOpenCodeTaskRetriesMissingPostcondition(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "evidence"), dirPerm); err != nil {
		t.Fatal(err)
	}
	opencodePath := writeRetryFakeOpenCode(t, `#!/bin/sh
set -eu
count_file="$MNM_RUN_DIR/attempt-count"
count=0
if [ -f "$count_file" ]; then
  count="$(cat "$count_file")"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$count_file"
printf '{"type":"done","attempt":%s}\n' "$count"
`)

	err := runOpenCodeTask(opencodePath, t.TempDir(), runDir, opencodeTask{
		RunID:   "run_postcondition_retry",
		TaskID:  "task_postcondition_retry",
		Phase:   "validate",
		Title:   "mnm postcondition retry test",
		Model:   "openrouter/test",
		Prompt:  "retry until the verdict exists",
		LogPath: filepath.Join(runDir, "evidence", "opencode-postcondition-retry.jsonl"),
		Verify: func() error {
			count := strings.TrimSpace(readFile(t, filepath.Join(runDir, "attempt-count")))
			if count != "2" {
				return errors.New("validate opencode task did not record validation verdict for finding finding_retry")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("expected postcondition retry to recover, got: %v", err)
	}

	count := strings.TrimSpace(readFile(t, filepath.Join(runDir, "attempt-count")))
	if count != "2" {
		t.Fatalf("attempt count = %s, want 2", count)
	}
	log := readFile(t, filepath.Join(runDir, "evidence", "opencode-postcondition-retry.jsonl"))
	if !strings.Contains(log, `"attempt":1`) || !strings.Contains(log, `"attempt":2`) {
		t.Fatalf("retry log did not preserve both attempts:\n%s", log)
	}
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	retries := 0
	for _, event := range events {
		if event.Type == "task.retrying" && event.ObjectID == "task_postcondition_retry" {
			retries++
			reason, _ := event.Data["reason"].(string)
			if !strings.Contains(reason, "validation verdict") {
				t.Fatalf("retry reason = %q, want validation verdict context", reason)
			}
		}
	}
	if retries != 1 {
		t.Fatalf("retry event count = %d, want 1", retries)
	}
}

func TestRunOpenCodeTaskCleansProcessGroupChildren(t *testing.T) {
	if !supportsCommandProcessGroupCleanup() {
		t.Skip("process group cleanup is not supported on this platform")
	}

	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "evidence"), dirPerm); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(runDir, "child-heartbeat")
	opencodePath := writeRetryFakeOpenCode(t, `#!/bin/sh
set -eu
marker="$MNM_RUN_DIR/child-heartbeat"
(
  printf 'alive\n' >> "$marker"
  while true; do
    printf 'alive\n' >> "$marker"
    sleep 0.05
  done
) &
while [ ! -s "$marker" ]; do
  sleep 0.01
done
printf '{"type":"done"}\n'
`)

	err := runOpenCodeTask(opencodePath, t.TempDir(), runDir, opencodeTask{
		RunID:   "run_process_cleanup",
		TaskID:  "task_process_cleanup",
		Phase:   "validate",
		Title:   "mnm process cleanup test",
		Model:   "openrouter/test",
		Prompt:  "spawn a child process",
		LogPath: filepath.Join(runDir, "evidence", "opencode-process-cleanup.jsonl"),
	})
	if err != nil {
		t.Fatalf("expected opencode task to succeed, got: %v", err)
	}

	before := fileSizeOrZero(t, markerPath)
	if before == 0 {
		t.Fatal("expected background child to write a heartbeat before cleanup")
	}
	time.Sleep(350 * time.Millisecond)
	after := fileSizeOrZero(t, markerPath)
	if after != before {
		t.Fatalf("background child kept running after opencode task returned: size before=%d after=%d", before, after)
	}
}

func TestRunOpenCodeTaskDoesNotRetryMissingPostconditionAfterLedgerWrite(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "evidence"), dirPerm); err != nil {
		t.Fatal(err)
	}
	opencodePath := writeRetryFakeOpenCode(t, `#!/bin/sh
set -eu
count_file="$MNM_RUN_DIR/attempt-count"
count=0
if [ -f "$count_file" ]; then
  count="$(cat "$count_file")"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$count_file"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_attempt_$count","run_id":"run_dirty_postcondition","type":"evidence.added","object":"evidence","object_id":"evidence_attempt_$count","task_id":"task_dirty_postcondition","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"log","title":"Attempt $count","path":"evidence/attempt.log"}}
EOF
printf '{"type":"done","attempt":%s}\n' "$count"
`)

	err := runOpenCodeTask(opencodePath, t.TempDir(), runDir, opencodeTask{
		RunID:   "run_dirty_postcondition",
		TaskID:  "task_dirty_postcondition",
		Phase:   "validate",
		Title:   "mnm dirty postcondition retry test",
		Model:   "openrouter/test",
		Prompt:  "do not retry after partial ledger writes",
		LogPath: filepath.Join(runDir, "evidence", "opencode-dirty-postcondition.jsonl"),
		Verify: func() error {
			return errors.New("validate opencode task did not record validation verdict for finding finding_dirty")
		},
	})
	if err == nil {
		t.Fatal("expected dirty missing-postcondition failure")
	}

	count := strings.TrimSpace(readFile(t, filepath.Join(runDir, "attempt-count")))
	if count != "1" {
		t.Fatalf("attempt count = %s, want 1", count)
	}
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "task.retrying" {
			t.Fatalf("unexpected retry event after ledger write: %#v", event)
		}
	}
}

func TestRunOpenCodeTaskDoesNotRetryNonTransientFailure(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "evidence"), dirPerm); err != nil {
		t.Fatal(err)
	}
	opencodePath := writeRetryFakeOpenCode(t, `#!/bin/sh
set -eu
count_file="$MNM_RUN_DIR/attempt-count"
count=0
if [ -f "$count_file" ]; then
  count="$(cat "$count_file")"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$count_file"
printf 'invalid prompt\n'
exit 1
`)

	err := runOpenCodeTask(opencodePath, t.TempDir(), runDir, opencodeTask{
		RunID:   "run_no_retry",
		TaskID:  "task_no_retry",
		Phase:   "review",
		Title:   "mnm no retry test",
		Model:   "openrouter/test",
		Prompt:  "do not retry me",
		LogPath: filepath.Join(runDir, "evidence", "opencode-no-retry.jsonl"),
	})
	if err == nil {
		t.Fatal("expected non-transient failure")
	}

	count := strings.TrimSpace(readFile(t, filepath.Join(runDir, "attempt-count")))
	if count != "1" {
		t.Fatalf("attempt count = %s, want 1", count)
	}
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "task.retrying" {
			t.Fatalf("unexpected retry event: %#v", event)
		}
	}
}

func TestRunOpenCodeTaskDoesNotRetryAfterLedgerWrite(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "evidence"), dirPerm); err != nil {
		t.Fatal(err)
	}
	opencodePath := writeRetryFakeOpenCode(t, `#!/bin/sh
set -eu
count_file="$MNM_RUN_DIR/attempt-count"
count=0
if [ -f "$count_file" ]; then
  count="$(cat "$count_file")"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$count_file"
cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_attempt_$count","run_id":"run_dirty_retry","type":"finding.created","object":"finding","object_id":"finding_attempt_$count","task_id":"task_dirty_retry","timestamp":"2026-01-01T00:00:00Z","data":{"title":"Attempt $count","lead_id":"","category":"test","severity":"medium","confidence":"medium","body_path":"evidence/body.md"}}
EOF
printf '{"code":502,"message":"Network connection lost.","metadata":{"error_type":"provider_unavailable"}}\n'
exit 1
`)

	err := runOpenCodeTask(opencodePath, t.TempDir(), runDir, opencodeTask{
		RunID:   "run_dirty_retry",
		TaskID:  "task_dirty_retry",
		Phase:   "investigate",
		Title:   "mnm dirty retry test",
		Model:   "openrouter/test",
		Prompt:  "do not retry after ledger writes",
		LogPath: filepath.Join(runDir, "evidence", "opencode-dirty-retry.jsonl"),
	})
	if err == nil {
		t.Fatal("expected dirty transient failure")
	}

	count := strings.TrimSpace(readFile(t, filepath.Join(runDir, "attempt-count")))
	if count != "1" {
		t.Fatalf("attempt count = %s, want 1", count)
	}
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "task.retrying" {
			t.Fatalf("unexpected retry event after ledger write: %#v", event)
		}
	}
}

func TestRunOpenCodeTaskClassifiesOnlyLatestAttemptForRetry(t *testing.T) {
	oldDelay := openCodeRetryDelay
	openCodeRetryDelay = 0
	defer func() { openCodeRetryDelay = oldDelay }()

	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "evidence"), dirPerm); err != nil {
		t.Fatal(err)
	}
	opencodePath := writeRetryFakeOpenCode(t, `#!/bin/sh
set -eu
count_file="$MNM_RUN_DIR/attempt-count"
count=0
if [ -f "$count_file" ]; then
  count="$(cat "$count_file")"
fi
count=$((count + 1))
printf '%s\n' "$count" > "$count_file"
if [ "$count" -eq 1 ]; then
  printf '{"code":502,"message":"Network connection lost.","metadata":{"error_type":"provider_unavailable"}}\n'
  exit 1
fi
printf 'invalid prompt\n'
exit 1
`)

	err := runOpenCodeTask(opencodePath, t.TempDir(), runDir, opencodeTask{
		RunID:   "run_retry_suffix",
		TaskID:  "task_retry_suffix",
		Phase:   "review",
		Title:   "mnm retry suffix test",
		Model:   "openrouter/test",
		Prompt:  "stop after hard failure",
		LogPath: filepath.Join(runDir, "evidence", "opencode-retry-suffix.jsonl"),
	})
	if err == nil {
		t.Fatal("expected hard second-attempt failure")
	}

	count := strings.TrimSpace(readFile(t, filepath.Join(runDir, "attempt-count")))
	if count != "2" {
		t.Fatalf("attempt count = %s, want 2", count)
	}
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	retries := 0
	for _, event := range events {
		if event.Type == "task.retrying" {
			retries++
		}
	}
	if retries != 1 {
		t.Fatalf("retry event count = %d, want 1", retries)
	}
}

func writeRetryFakeOpenCode(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertTaskStartedEventCount(t *testing.T, runDir, taskID string, want int) {
	t.Helper()
	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	var got int
	for _, event := range events {
		if event.Type == "task.started" && event.ObjectID == taskID {
			got++
		}
	}
	if got != want {
		t.Fatalf("task.started event count for %s = %d, want %d", taskID, got, want)
	}
}

func fileSizeOrZero(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func limaRunnerWithHostResources(t *testing.T, resources HostResources) LimaRunner {
	t.Helper()
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "limactl"))
	t.Setenv("PATH", dir)
	t.Setenv("MNM_LINUX_RUNNER_PAYLOAD", filepath.Join(dir, "mnm-linux"))
	return LimaRunner{
		ResourceDetector: func() (HostResources, error) {
			return resources, nil
		},
	}
}

func TestReconPromptIncludesLeadBodyFileCommand(t *testing.T) {
	cfg := Config{
		Runner: RunnerConfig{MaxLeads: 3},
		Instructions: InstructionConfig{
			Scope: "Focus on parser bugs.",
		},
	}
	prompt := reconPrompt("/tmp/run", "/tmp/workspace", cfg)
	for _, want := range []string{
		"Maximum leads: 3",
		"Focus on parser bugs.",
		"mnm lead create --title",
		"--body-file /tmp/run/evidence/lead-specific-name.md",
		"Recon maps the workspace and schedules focused work; Investigate and Validate prove, exploit, or falsify issues.",
		"If scope instructions ask for tests or proofs, treat them as requirements for later Investigate or Validate",
		"Do not build end-to-end proof scripts, start long-lived services",
		"Unregistered files are scratch and may be lost.",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func reconTestConfig() Config {
	return Config{
		Models: ModelConfig{Recon: "openrouter/test"},
		Runner: RunnerConfig{MaxLeads: 3},
		Instructions: InstructionConfig{
			Scope: "Focus on parser bugs.",
		},
	}
}

type recordingExecutor struct {
	commands []string
}

func (executor *recordingExecutor) Run(_ context.Context, name string, args ...string) error {
	executor.commands = append(executor.commands, name+" "+strings.Join(args, " "))
	if name == "limactl" && len(args) >= 5 && args[0] == "copy" && strings.HasSuffix(args[len(args)-2], ":/tmp/mnm-run") {
		dst := args[len(args)-1]
		outDir := filepath.Join(dst, "mnm-run")
		if err := os.MkdirAll(filepath.Join(outDir, "evidence"), dirPerm); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outDir, ".events.lock"), []byte("stale"), filePerm); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outDir, eventsFile), []byte(""), filePerm); err != nil {
			return err
		}
	}
	return nil
}

func prependFakeOpenCode(t *testing.T, version string) {
	t.Helper()
	writeFakeOpenCode(t, version, `
  : "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
  : "${MNM_TASK_ID:?MNM_TASK_ID is required}"
  prompt=""
  workspace=""
  while [ "$#" -gt 0 ]; do
    if [ "$1" = "--dir" ]; then
      shift
      workspace="${1:-}"
    fi
    prompt="$1"
    shift
  done
  : "${workspace:?workspace is required}"
  printf '%s\n' "$MNM_TASK_ID" > "$workspace/mutated-by-opencode"
  mkdir -p "$MNM_RUN_DIR/evidence"
  if printf '%s' "$prompt" | grep -q 'makenomistakes Finalize'; then
    cat > "$MNM_RUN_DIR/report.md" <<'EOF'
# Report

Fake final report for finding_fake_lead_fake_auth with evidence/validate-finding_fake_lead_fake_auth-proof.log.
EOF
    cat > "$MNM_RUN_DIR/report.json" <<'EOF'
{"run_id":"run_test","counts":{"findings_proven":1,"findings_inconclusive":0,"findings_failed":0,"findings_rejected":0,"findings_duplicate":0,"findings_unvalidated":0},"report_paths":{"markdown":"report.md","json":"report.json"},"proven":[{"id":"finding_fake_lead_fake_auth","title":"Fake candidate finding","category":"authz","severity":"high","confidence":"medium","source_lead_id":"lead_fake_auth","status":"validation_proven","verdicts":["review accepted","deduplicate canonical","validation proven"],"evidence_paths":["evidence/validate-finding_fake_lead_fake_auth-proof.log"],"summary":"Fake finding proven by fake validate.","affected_paths":["repo/app.go"]}],"inconclusive":[],"failed":[],"rejected":[],"duplicate":[],"unvalidated":[]}
EOF
    cat >> "$MNM_RUN_DIR/events.jsonl" <<'EOF'
{"id":"event_fake_report","run_id":"run_test","type":"report.finalized","object":"report","object_id":"report_fake","task_id":"task_finalize","timestamp":"2026-01-01T00:00:13Z","data":{"markdown_path":"report.md","json_path":"report.json"}}
{"id":"event_fake_finalize_done","run_id":"run_test","type":"task.completed","object":"task","object_id":"task_finalize","task_id":"task_finalize","timestamp":"2026-01-01T00:00:14Z","data":{"status":"completed","summary":"Finalized fake report"}}
EOF
    printf '{"type":"done"}\n'
    exit 0
  fi
  if printf '%s' "$prompt" | grep -q 'makenomistakes Validate'; then
    : "${MNM_FINDING_ID:?MNM_FINDING_ID is required}"
    cat > "$MNM_RUN_DIR/evidence/validate-$MNM_FINDING_ID-notes.md" <<'EOF'
# Validation notes

Fake validate proof for tests.
EOF
    cat > "$MNM_RUN_DIR/evidence/validate-$MNM_FINDING_ID-proof.log" <<'EOF'
fake proof command observed the issue
EOF
    cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_fake_validate_evidence_$MNM_FINDING_ID","run_id":"run_test","type":"evidence.added","object":"evidence","object_id":"evidence_fake_validate_$MNM_FINDING_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:11Z","data":{"kind":"markdown","title":"Validation notes","path":"evidence/validate-$MNM_FINDING_ID-notes.md","content_sha256":"57edc8df5ef1d937269fa86ae284e7e5b701dea91b34b3cc512b2b36c7911e6c","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_fake_validate_proof_$MNM_FINDING_ID","run_id":"run_test","type":"evidence.added","object":"evidence","object_id":"evidence_fake_validate_proof_$MNM_FINDING_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:12Z","data":{"kind":"log","title":"Validation proof","path":"evidence/validate-$MNM_FINDING_ID-proof.log","content_sha256":"c36995e1241d001aa3fd14787f46e5a2ef059a179dac1890d6f298f9edec548c","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_fake_validate_$MNM_FINDING_ID","run_id":"run_test","type":"verdict.recorded","object":"verdict","object_id":"verdict_fake_validate_$MNM_FINDING_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:13Z","data":{"finding_id":"$MNM_FINDING_ID","phase":"validate","value":"proven","reason":"Proven by fake validate.","canonical_finding_id":""}}
{"id":"event_fake_validate_done_$MNM_FINDING_ID","run_id":"run_test","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:14Z","data":{"status":"completed","summary":"Validated $MNM_FINDING_ID"}}
EOF
    printf '{"type":"done"}\n'
    exit 0
  fi
  if printf '%s' "$prompt" | grep -q 'makenomistakes Deduplicate'; then
    cat > "$MNM_RUN_DIR/evidence/deduplicate-notes.md" <<'EOF'
# Deduplication notes

Fake deduplication notes for tests.
EOF
    cat >> "$MNM_RUN_DIR/events.jsonl" <<'EOF'
{"id":"event_fake_deduplicate_evidence","run_id":"run_test","type":"evidence.added","object":"evidence","object_id":"evidence_fake_deduplicate_notes","task_id":"task_deduplicate","timestamp":"2026-01-01T00:00:09Z","data":{"kind":"markdown","title":"Deduplication notes","path":"evidence/deduplicate-notes.md","content_sha256":"12cd072b30c1feba2bfc924e75e5dee362f4ff239c0cb4b1d6f5d676e3075cda"}}
{"id":"event_fake_deduplicate","run_id":"run_test","type":"verdict.recorded","object":"verdict","object_id":"verdict_fake_deduplicate","task_id":"task_deduplicate","timestamp":"2026-01-01T00:00:10Z","data":{"finding_id":"finding_fake_lead_fake_auth","phase":"deduplicate","value":"canonical","reason":"Unique in fake runner.","canonical_finding_id":""}}
{"id":"event_fake_deduplicate_done","run_id":"run_test","type":"task.completed","object":"task","object_id":"task_deduplicate","task_id":"task_deduplicate","timestamp":"2026-01-01T00:00:11Z","data":{"status":"completed","summary":"Deduplicated fake finding"}}
EOF
    printf '{"type":"done"}\n'
    exit 0
  fi
  if printf '%s' "$prompt" | grep -q 'makenomistakes Review'; then
    : "${MNM_FINDING_ID:?MNM_FINDING_ID is required}"
    cat > "$MNM_RUN_DIR/evidence/review-$MNM_FINDING_ID-notes.md" <<'EOF'
# Review notes

Fake review notes for tests.
EOF
    cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_fake_review_evidence_$MNM_FINDING_ID","run_id":"run_test","type":"evidence.added","object":"evidence","object_id":"evidence_fake_review_$MNM_FINDING_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:07Z","data":{"kind":"markdown","title":"Review notes","path":"evidence/review-$MNM_FINDING_ID-notes.md","content_sha256":"ad3899e2ddf4134f602646d7a65035fd673efb36e906c437d4950529912b0042","lead_id":"","finding_id":"$MNM_FINDING_ID"}}
{"id":"event_fake_review_$MNM_FINDING_ID","run_id":"run_test","type":"verdict.recorded","object":"verdict","object_id":"verdict_fake_$MNM_FINDING_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:08Z","data":{"finding_id":"$MNM_FINDING_ID","phase":"review","value":"accepted","reason":"Accepted by fake review."}}
{"id":"event_fake_review_done_$MNM_FINDING_ID","run_id":"run_test","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:09Z","data":{"status":"completed","summary":"Reviewed $MNM_FINDING_ID"}}
EOF
    printf '{"type":"done"}\n'
    exit 0
  fi
  if printf '%s' "$prompt" | grep -q 'makenomistakes Investigate'; then
    : "${MNM_LEAD_ID:?MNM_LEAD_ID is required}"
    cat > "$MNM_RUN_DIR/evidence/investigate-$MNM_LEAD_ID-notes.md" <<'EOF'
# Investigation notes

Fake investigation notes for tests.
EOF
    cat > "$MNM_RUN_DIR/evidence/finding-$MNM_LEAD_ID.md" <<'EOF'
# Finding

Fake finding for tests.
EOF
    cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_fake_investigate_evidence_$MNM_LEAD_ID","run_id":"run_test","type":"evidence.added","object":"evidence","object_id":"evidence_fake_investigate_$MNM_LEAD_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:04Z","data":{"kind":"markdown","title":"Investigation notes","path":"evidence/investigate-$MNM_LEAD_ID-notes.md","content_sha256":"6445080467aad59a102894b4042719a97fee1ddc694ae52d0314bce52cbaac33","lead_id":"$MNM_LEAD_ID","finding_id":""}}
{"id":"event_fake_finding_$MNM_LEAD_ID","run_id":"run_test","type":"finding.created","object":"finding","object_id":"finding_fake_$MNM_LEAD_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:05Z","data":{"title":"Fake candidate finding","lead_id":"$MNM_LEAD_ID","category":"authz","severity":"high","confidence":"medium","body_path":"evidence/finding-$MNM_LEAD_ID.md"}}
{"id":"event_fake_finding_evidence_$MNM_LEAD_ID","run_id":"run_test","type":"evidence.added","object":"evidence","object_id":"evidence_fake_finding_$MNM_LEAD_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:06Z","data":{"kind":"markdown","title":"Fake finding evidence","path":"evidence/finding-$MNM_LEAD_ID.md","content_sha256":"422e8e852039204ed5048483d7d3a611a102cfa0caa85ea3c11bad50c3595dea","lead_id":"","finding_id":"finding_fake_$MNM_LEAD_ID"}}
{"id":"event_fake_lead_closed_$MNM_LEAD_ID","run_id":"run_test","type":"lead.closed","object":"lead","object_id":"$MNM_LEAD_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:07Z","data":{"status":"promoted_to_finding","reason":"Promoted by fake investigate."}}
{"id":"event_fake_investigate_done_$MNM_LEAD_ID","run_id":"run_test","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:08Z","data":{"status":"completed","summary":"Investigated $MNM_LEAD_ID"}}
EOF
    printf '{"type":"done"}\n'
    exit 0
  fi
  cat > "$MNM_RUN_DIR/evidence/recon-codebase-map.md" <<'EOF'
# Codebase Map

Fake map for tests.
EOF
  cat > "$MNM_RUN_DIR/evidence/recon-risk-register.md" <<'EOF'
# Risk Register

Fake risk register for tests.
EOF
  cat > "$MNM_RUN_DIR/evidence/lead-auth.md" <<'EOF'
# Lead

Investigate authentication boundaries.
EOF
  cat >> "$MNM_RUN_DIR/events.jsonl" <<'EOF'
{"id":"event_fake_map","run_id":"run_test","type":"evidence.added","object":"evidence","object_id":"evidence_fake_map","task_id":"task_recon","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Recon codebase map","path":"evidence/recon-codebase-map.md","content_sha256":"65436a552f256fcf8ebab9137a9da88cf3f7423cf2803802ebaf24ad183a637d"}}
{"id":"event_fake_risk","run_id":"run_test","type":"evidence.added","object":"evidence","object_id":"evidence_fake_risk","task_id":"task_recon","timestamp":"2026-01-01T00:00:01Z","data":{"kind":"markdown","title":"Recon risk register","path":"evidence/recon-risk-register.md","content_sha256":"5b33af9248d2149e751ada01be70c5e946e802ece2008241ee7c62d5b272d25f"}}
{"id":"event_fake_lead","run_id":"run_test","type":"lead.created","object":"lead","object_id":"lead_fake_auth","task_id":"task_recon","timestamp":"2026-01-01T00:00:02Z","data":{"title":"Investigate authentication boundaries","category":"authz","priority":"high","body_path":"evidence/lead-auth.md"}}
{"id":"event_fake_done","run_id":"run_test","type":"task.completed","object":"task","object_id":"task_recon","task_id":"task_recon","timestamp":"2026-01-01T00:00:03Z","data":{"status":"completed","summary":"Recon completed"}}
EOF
  printf '{"type":"done"}\n'
  exit 0
`)
}

func writeFakeOpenCode(t *testing.T, version, runScript string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode")
	body := fakeOpenCodeScript(version, runScript)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return path
}

func prependFakeOpenCodeInstaller(t *testing.T, version string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bash")
	body := `#!/bin/sh
set -eu
mkdir -p "$HOME/.opencode/bin"
cat > "$HOME/.opencode/bin/opencode" <<'SCRIPT'
` + fakeOpenCodeScript(version, "") + `SCRIPT
chmod +x "$HOME/.opencode/bin/opencode"
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func fakeOpenCodeScript(version, runScript string) string {
	return `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then
  printf '` + version + `'
  exit 0
fi
if [ "${1:-}" = "run" ]; then
` + runScript + `
fi
printf 'fake opencode\n'
`
}
