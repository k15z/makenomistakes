package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	manifest := readFile(t, filepath.Join(runDir, "evidence", "runner-manifest.json"))
	if !strings.Contains(manifest, "repo/app.go") {
		t.Fatalf("manifest missing unpacked workspace file:\n%s", manifest)
	}
	if !strings.Contains(manifest, `"opencode_version": "`+opencodeVersion+`"`) {
		t.Fatalf("manifest missing opencode version:\n%s", manifest)
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
		Config: Config{Runner: RunnerConfig{CPUs: 2, MemoryGB: 4, DiskGB: 20}},
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
		"limactl stop --tty=false mnm-run-abc",
		"limactl delete --force --tty=false mnm-run-abc",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}
}

type recordingExecutor struct {
	commands []string
}

func (executor *recordingExecutor) Run(_ context.Context, name string, args ...string) error {
	executor.commands = append(executor.commands, name+" "+strings.Join(args, " "))
	if name == "limactl" && len(args) >= 5 && args[0] == "copy" && args[len(args)-2] == "mnm-run-abc:/tmp/mnm-run" {
		dst := args[len(args)-1]
		outDir := filepath.Join(dst, "mnm-run")
		if err := os.MkdirAll(filepath.Join(outDir, "evidence"), dirPerm); err != nil {
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
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode")
	if err := os.WriteFile(path, []byte(fakeOpenCodeScript(version)), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func prependFakeOpenCodeInstaller(t *testing.T, version string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bash")
	body := `#!/bin/sh
set -eu
mkdir -p "$HOME/.opencode/bin"
cat > "$HOME/.opencode/bin/opencode" <<'SCRIPT'
` + fakeOpenCodeScript(version) + `SCRIPT
chmod +x "$HOME/.opencode/bin/opencode"
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func fakeOpenCodeScript(version string) string {
	return "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then printf '" + version + "'; exit 0; fi\nprintf 'fake opencode\\n'\n"
}
