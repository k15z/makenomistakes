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

const bytesPerGiB = 1024 * 1024 * 1024

type LimaRunner struct {
	Executor         CommandExecutor
	ResourceDetector HostResourceDetector
	Stdout           io.Writer
	Stderr           io.Writer
}

type HostResourceDetector func() (HostResources, error)

type HostResources struct {
	CPUs          int
	MemoryBytes   uint64
	DiskFreeBytes uint64
	DiskPath      string
}

func newDefaultRunner(stdout, stderr io.Writer) AnalyzeRunner {
	return LimaRunner{
		Executor: ShellExecutor{Stdout: stdout, Stderr: stderr},
		Stdout:   stdout,
		Stderr:   stderr,
	}
}

func (runner LimaRunner) Preflight(ctx context.Context, request RunnerPreflightRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := exec.LookPath("limactl"); err != nil {
		return fmt.Errorf("limactl is required to run the audit VM; install Lima or use mnm analyze --prepare-only: %w", err)
	}
	if os.Getenv("MNM_LINUX_RUNNER_PAYLOAD") == "" {
		if _, err := exec.LookPath("go"); err != nil {
			return fmt.Errorf("go is required to build the Linux runner payload; install Go or set MNM_LINUX_RUNNER_PAYLOAD: %w", err)
		}
	}
	resources, err := runner.detectHostResources()
	if err != nil {
		return fmt.Errorf("inspect host resources: %w", err)
	}
	if err := validateHostResources(resources, request.Config.Runner); err != nil {
		return err
	}
	return nil
}

func (runner LimaRunner) detectHostResources() (HostResources, error) {
	detector := runner.ResourceDetector
	if detector == nil {
		detector = defaultHostResourceDetector
	}
	return detector()
}

func defaultHostResourceDetector() (HostResources, error) {
	memoryBytes, err := hostMemoryBytes()
	if err != nil {
		return HostResources{}, err
	}
	diskPath, err := limaDiskPath()
	if err != nil {
		return HostResources{}, err
	}
	diskFreeBytes, err := freeDiskBytes(diskPath)
	if err != nil {
		return HostResources{}, err
	}
	return HostResources{
		CPUs:          runtime.NumCPU(),
		MemoryBytes:   memoryBytes,
		DiskFreeBytes: diskFreeBytes,
		DiskPath:      diskPath,
	}, nil
}

func validateHostResources(resources HostResources, runner RunnerConfig) error {
	if runner.CPUs > 0 {
		if resources.CPUs <= 0 {
			return errors.New("host CPU count could not be detected")
		}
		if runner.CPUs > resources.CPUs {
			return fmt.Errorf("runner.cpus requests %d CPUs, but host reports %d", runner.CPUs, resources.CPUs)
		}
	}
	if runner.MemoryGB > 0 {
		if resources.MemoryBytes == 0 {
			return errors.New("host memory could not be detected")
		}
		required := uint64(runner.MemoryGB) * bytesPerGiB
		if required > resources.MemoryBytes {
			return fmt.Errorf("runner.memory_gb requests %d GiB, but host reports %s GiB total memory", runner.MemoryGB, gibString(resources.MemoryBytes))
		}
	}
	if runner.DiskGB > 0 {
		if resources.DiskFreeBytes == 0 {
			return fmt.Errorf("free disk space could not be detected for %s", resources.DiskPath)
		}
		required := uint64(runner.DiskGB) * bytesPerGiB
		if required > resources.DiskFreeBytes {
			return fmt.Errorf("runner.disk_gb requests %d GiB, but %s has %s GiB free", runner.DiskGB, resources.DiskPath, gibString(resources.DiskFreeBytes))
		}
	}
	return nil
}

func hostMemoryBytes() (uint64, error) {
	switch runtime.GOOS {
	case "darwin":
		sysctlPath := "sysctl"
		if _, err := exec.LookPath(sysctlPath); err != nil {
			if _, statErr := os.Stat("/usr/sbin/sysctl"); statErr == nil {
				sysctlPath = "/usr/sbin/sysctl"
			}
		}
		output, err := exec.Command(sysctlPath, "-n", "hw.memsize").CombinedOutput()
		if err != nil {
			return 0, fmt.Errorf("detect host memory with sysctl: %w\n%s", err, string(output))
		}
		value, err := strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("parse sysctl hw.memsize: %w", err)
		}
		return value, nil
	case "linux":
		b, err := os.ReadFile("/proc/meminfo")
		if err != nil {
			return 0, fmt.Errorf("read /proc/meminfo: %w", err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "MemTotal:" {
				kib, err := strconv.ParseUint(fields[1], 10, 64)
				if err != nil {
					return 0, fmt.Errorf("parse MemTotal: %w", err)
				}
				return kib * 1024, nil
			}
		}
		return 0, errors.New("MemTotal not found in /proc/meminfo")
	default:
		return 0, fmt.Errorf("host memory detection is not supported on %s", runtime.GOOS)
	}
}

func limaDiskPath() (string, error) {
	if override := os.Getenv("LIMA_HOME"); override != "" {
		return nearestExistingPath(override)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return nearestExistingPath(filepath.Join(home, ".lima"))
}

func nearestExistingPath(path string) (string, error) {
	original := path
	for {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(path)
		if parent == path {
			return "", fmt.Errorf("no existing parent for %s", original)
		}
		path = parent
	}
}

func gibString(bytes uint64) string {
	return strconv.FormatFloat(float64(bytes)/bytesPerGiB, 'f', 1, 64)
}

func (runner LimaRunner) Run(ctx context.Context, request RunnerRequest) error {
	if runner.Executor == nil {
		return errors.New("runner executor is required")
	}
	if err := validateRunnerRunID(request.Run.ID); err != nil {
		return err
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
	if err := runner.copyInputs(ctx, instanceName, payloadPath, request); err != nil {
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

func (runner LimaRunner) copyInputs(ctx context.Context, instanceName, payloadPath string, request RunnerRequest) error {
	copies := [][2]string{
		{payloadPath, instanceName + ":/tmp/mnm"},
		{request.Run.SnapshotPath, instanceName + ":/tmp/snapshot.tar.zst"},
		{request.Run.ConfigSnapshotPath, instanceName + ":/tmp/mnm.toml"},
	}
	for _, item := range copies {
		if err := runner.Executor.Run(ctx, "limactl", "copy", "--backend=scp", item[0], item[1]); err != nil {
			return err
		}
	}
	if request.Resume {
		if err := runner.Executor.Run(ctx, "limactl", "shell", instanceName, "bash", "-lc", "rm -rf /tmp/mnm-run && mkdir -p /tmp/mnm-run"); err != nil {
			return err
		}
		runDirContents := filepath.Clean(request.Run.RunDir) + string(filepath.Separator) + "."
		if err := runner.Executor.Run(ctx, "limactl", "copy", "--backend=scp", "-r", runDirContents, instanceName+":/tmp/mnm-run"); err != nil {
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

func (runner LimaRunner) runGuestRunner(ctx context.Context, instanceName string, run RunRecord) error {
	return runner.Executor.Run(ctx, "limactl", "shell", instanceName, "bash", "-lc", guestRunnerCommand(run))
}

func guestRunnerCommand(run RunRecord) string {
	return strings.Join([]string{
		"set -euo pipefail",
		"chmod +x /tmp/mnm",
		bootstrapAuditToolbeltCommand(),
		"mkdir -p \"$HOME/.local/share/opencode\"",
		"if [ -f /tmp/opencode-auth.json ]; then mv /tmp/opencode-auth.json \"$HOME/.local/share/opencode/auth.json\"; chmod 600 \"$HOME/.local/share/opencode/auth.json\"; fi",
		"mkdir -p /tmp/mnm-run",
		"rm -f /tmp/mnm-run/.events.lock",
		fmt.Sprintf("/tmp/mnm runner --run-id %s --run-dir /tmp/mnm-run --snapshot /tmp/snapshot.tar.zst --config /tmp/mnm.toml", shellQuote(run.ID)),
	}, "\n")
}

func bootstrapAuditToolbeltCommand() string {
	return strings.Join([]string{
		"if ! command -v rg >/dev/null 2>&1; then",
		"  if ! command -v apt-get >/dev/null 2>&1; then",
		"    echo \"mnm: ripgrep is required in the audit VM but apt-get is unavailable\" >&2",
		"    exit 1",
		"  fi",
		"  apt_install_prefix=\"\"",
		"  if command -v sudo >/dev/null 2>&1; then apt_install_prefix=\"sudo\"; fi",
		"  $apt_install_prefix env DEBIAN_FRONTEND=noninteractive apt-get update",
		"  $apt_install_prefix env DEBIAN_FRONTEND=noninteractive apt-get install -y ripgrep",
		"fi",
	}, "\n")
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
	copiedRunDir := filepath.Join(tempDir, "mnm-run")
	if err := removeStaleLedgerLock(copiedRunDir); err != nil {
		return err
	}
	return copyDirContents(copiedRunDir, runDir)
}

func removeStaleLedgerLock(runDir string) error {
	err := os.Remove(filepath.Join(runDir, ".events.lock"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
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
