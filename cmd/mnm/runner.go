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
)

const opencodeVersion = "1.15.11"

type AnalyzeRunner interface {
	Run(context.Context, RunnerRequest) error
}

type RunnerRequest struct {
	Run    RunRecord
	Config Config
	KeepVM bool
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
	if err := validateRunnerRunID(*runID); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(*runDir, "evidence"), dirPerm); err != nil {
		return err
	}
	if _, err := loadConfig(*configPath); err != nil {
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
