package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type LimaRunner struct {
	Executor CommandExecutor
	Stdout   io.Writer
	Stderr   io.Writer
}

func newDefaultRunner(stdout, stderr io.Writer) AnalyzeRunner {
	return LimaRunner{
		Executor: ShellExecutor{Stdout: stdout, Stderr: stderr},
		Stdout:   stdout,
		Stderr:   stderr,
	}
}

func (runner LimaRunner) Run(ctx context.Context, request RunnerRequest) error {
	if runner.Executor == nil {
		return errors.New("runner executor is required")
	}
	instanceName := limaInstanceName(request.Run.ID)
	payloadPath, cleanupPayload, err := buildLinuxRunnerPayload(request.Run.RunDir)
	if err != nil {
		return err
	}
	defer cleanupPayload()

	if err := runner.Executor.Run(ctx, "limactl", "delete", "--force", "--tty=false", instanceName); err != nil {
		// Deleting a missing instance is best-effort cleanup before create.
		fmt.Fprintf(runner.Stderr, "mnm: ignoring pre-create cleanup error: %v\n", err)
	}

	cpus := strconv.Itoa(request.Config.Runner.CPUs)
	memory := strconv.Itoa(request.Config.Runner.MemoryGB)
	disk := strconv.Itoa(request.Config.Runner.DiskGB)
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
	if err := runner.copyInputs(ctx, instanceName, payloadPath, request.Run); err != nil {
		return err
	}
	if err := runner.runGuestRunner(ctx, instanceName, request.Run); err != nil {
		_ = runner.copyOutputs(context.Background(), instanceName, request.Run.RunDir)
		return err
	}
	if err := runner.copyOutputs(ctx, instanceName, request.Run.RunDir); err != nil {
		return err
	}
	return nil
}

func (runner LimaRunner) copyInputs(ctx context.Context, instanceName, payloadPath string, run RunRecord) error {
	copies := [][2]string{
		{payloadPath, instanceName + ":/tmp/mnm"},
		{run.SnapshotPath, instanceName + ":/tmp/snapshot.tar.zst"},
		{run.ConfigSnapshotPath, instanceName + ":/tmp/mnm.toml"},
	}
	for _, item := range copies {
		if err := runner.Executor.Run(ctx, "limactl", "copy", "--backend=scp", item[0], item[1]); err != nil {
			return err
		}
	}
	return nil
}

func (runner LimaRunner) runGuestRunner(ctx context.Context, instanceName string, run RunRecord) error {
	command := strings.Join([]string{
		"set -euo pipefail",
		"chmod +x /tmp/mnm",
		"rm -rf /tmp/mnm-run",
		"mkdir -p /tmp/mnm-run",
		fmt.Sprintf("/tmp/mnm runner --run-id %s --run-dir /tmp/mnm-run --snapshot /tmp/snapshot.tar.zst --config /tmp/mnm.toml", shellQuote(run.ID)),
	}, "\n")
	return runner.Executor.Run(ctx, "limactl", "shell", instanceName, "bash", "-lc", command)
}

func (runner LimaRunner) copyOutputs(ctx context.Context, instanceName, runDir string) error {
	tempDir, err := os.MkdirTemp("", "mnm-runner-output-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tempDir)
	if err := runner.Executor.Run(ctx, "limactl", "copy", "--backend=scp", "-r", instanceName+":/tmp/mnm-run", tempDir); err != nil {
		return err
	}
	return copyDirContents(filepath.Join(tempDir, "mnm-run"), runDir)
}

func buildLinuxRunnerPayload(runDir string) (string, func(), error) {
	if override := os.Getenv("MNM_LINUX_RUNNER_PAYLOAD"); override != "" {
		return override, func() {}, nil
	}
	sourceRoot, err := findSourceRoot()
	if err != nil {
		return "", nil, err
	}
	output := filepath.Join(runDir, "mnm-linux-"+runtime.GOARCH)
	goarch := runtime.GOARCH
	if goarch == "amd64" {
		goarch = "amd64"
	}
	command := exec.Command("go", "build", "-o", output, "./cmd/mnm")
	command.Dir = sourceRoot
	command.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0")
	combined, err := command.CombinedOutput()
	if err != nil {
		return "", nil, fmt.Errorf("build linux runner payload: %w\n%s", err, string(combined))
	}
	return output, func() { _ = os.Remove(output) }, nil
}

func findSourceRoot() (string, error) {
	if override := os.Getenv("MNM_SOURCE_DIR"); override != "" {
		return override, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil && strings.Contains(string(b), "github.com/k15z/makenomistakes") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("could not find source root; set MNM_SOURCE_DIR")
}

func limaInstanceName(runID string) string {
	name := "mnm-" + strings.ToLower(runID)
	name = strings.ReplaceAll(name, "_", "-")
	return name
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
