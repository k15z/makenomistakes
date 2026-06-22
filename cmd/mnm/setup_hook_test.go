package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTaskSetupHookFailModeReturnsErrorAndKeepsLog(t *testing.T) {
	workspace := t.TempDir()
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "audit"), dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "audit", "setup.sh"), []byte(`#!/usr/bin/env bash
set -euo pipefail
echo "before failure"
echo "stderr failure" >&2
exit 12
`), 0o755); err != nil {
		t.Fatal(err)
	}

	task := opencodeTask{
		TaskID: "task_recon",
		Phase:  "recon",
		Setup: RunnerSetupConfig{
			Script:         "audit/setup.sh",
			TimeoutMinutes: 1,
			Mode:           "fail",
		},
	}
	result, err := runTaskSetupHook(context.Background(), workspace, runDir, task, 1)
	if err == nil {
		t.Fatal("expected setup failure")
	}
	if !strings.Contains(err.Error(), "setup hook failed for task task_recon") || !strings.Contains(err.Error(), result.LogRelPath) {
		t.Fatalf("unexpected error: %v", err)
	}
	log := readFile(t, result.LogPath)
	for _, want := range []string{"before failure", "stderr failure", "mnm setup hook failed"} {
		if !strings.Contains(log, want) {
			t.Fatalf("setup log missing %q:\n%s", want, log)
		}
	}
}

func TestRunTaskSetupHookWarnModeContinuesAfterFailureAndKeepsLog(t *testing.T) {
	workspace := t.TempDir()
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "audit"), dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "audit", "setup.sh"), []byte(`#!/usr/bin/env bash
set -euo pipefail
echo "warn mode failure"
exit 12
`), 0o755); err != nil {
		t.Fatal(err)
	}

	task := opencodeTask{
		TaskID: "task_review_finding",
		Phase:  "review",
		Setup: RunnerSetupConfig{
			Script:         "audit/setup.sh",
			TimeoutMinutes: 1,
			Mode:           "warn",
		},
	}
	result, err := runTaskSetupHook(context.Background(), workspace, runDir, task, 2)
	if err != nil {
		t.Fatalf("warn mode should continue after setup failure: %v", err)
	}
	if len(result.Env) != 0 {
		t.Fatalf("warn-mode failed setup should not return captured env: %#v", result.Env)
	}
	log := readFile(t, result.LogPath)
	for _, want := range []string{"warn mode failure", "mnm setup hook failed"} {
		if !strings.Contains(log, want) {
			t.Fatalf("setup log missing %q:\n%s", want, log)
		}
	}
}
