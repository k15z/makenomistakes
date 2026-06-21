package main

import (
	"archive/tar"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestCreateWorkspaceSnapshotIncludesAndExcludesExpectedFiles(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "src/app.go", "package main")
	writeWorkspaceFile(t, workspace, ".git/config", "ignored")
	writeWorkspaceFile(t, workspace, ".mnm/state", "ignored")
	writeWorkspaceFile(t, workspace, "node_modules/pkg/index.js", "ignored")
	writeWorkspaceFile(t, workspace, "secrets.txt", "ignored")
	writeWorkspaceFile(t, workspace, ".mnmignore", "secrets.txt\n")

	output := filepath.Join(t.TempDir(), "snapshot.tar.zst")
	if err := createWorkspaceSnapshot(SnapshotOptions{
		WorkspaceRoot: workspace,
		WorkspaceDir:  workspace,
		OutputPath:    output,
	}); err != nil {
		t.Fatalf("create snapshot failed: %v", err)
	}

	names := snapshotEntryNames(t, output)
	if !contains(names, "src/app.go") {
		t.Fatalf("snapshot missing src/app.go: %#v", names)
	}
	for _, excluded := range []string{
		".git/config",
		".mnm/state",
		"node_modules/pkg/index.js",
		"secrets.txt",
	} {
		if contains(names, excluded) {
			t.Fatalf("snapshot included excluded path %q: %#v", excluded, names)
		}
	}
}

func TestCreateWorkspaceSnapshotAppliesConfigExcludes(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "keep.txt", "keep")
	writeWorkspaceFile(t, workspace, "skip/generated.txt", "skip")

	output := filepath.Join(t.TempDir(), "snapshot.tar.zst")
	if err := createWorkspaceSnapshot(SnapshotOptions{
		WorkspaceRoot: workspace,
		WorkspaceDir:  workspace,
		OutputPath:    output,
		ConfigExclude: []string{"skip/"},
	}); err != nil {
		t.Fatalf("create snapshot failed: %v", err)
	}

	names := snapshotEntryNames(t, output)
	if !contains(names, "keep.txt") {
		t.Fatalf("snapshot missing keep.txt: %#v", names)
	}
	if contains(names, "skip/generated.txt") {
		t.Fatalf("snapshot included config-excluded file: %#v", names)
	}
}

func TestValidateRunnerSetupInSnapshotRequiresIncludedScript(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "audit/setup.sh", "#!/usr/bin/env bash\n")
	writeWorkspaceFile(t, workspace, "src/app.go", "package main\n")
	output := filepath.Join(t.TempDir(), "snapshot.tar.zst")
	if err := createWorkspaceSnapshot(SnapshotOptions{
		WorkspaceRoot: workspace,
		WorkspaceDir:  workspace,
		OutputPath:    output,
	}); err != nil {
		t.Fatalf("create snapshot failed: %v", err)
	}

	if err := validateRunnerSetupInSnapshot(output, RunnerSetupConfig{Script: "audit/setup.sh"}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRunnerSetupInSnapshotRejectsExcludedScript(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "audit/setup.sh", "#!/usr/bin/env bash\n")
	writeWorkspaceFile(t, workspace, "src/app.go", "package main\n")
	output := filepath.Join(t.TempDir(), "snapshot.tar.zst")
	if err := createWorkspaceSnapshot(SnapshotOptions{
		WorkspaceRoot: workspace,
		WorkspaceDir:  workspace,
		OutputPath:    output,
		ConfigExclude: []string{"audit/setup.sh"},
	}); err != nil {
		t.Fatalf("create snapshot failed: %v", err)
	}

	err := validateRunnerSetupInSnapshot(output, RunnerSetupConfig{Script: "audit/setup.sh"})
	if err == nil {
		t.Fatal("expected missing setup script error")
	}
	if !strings.Contains(err.Error(), "workspace snapshot does not contain setup script audit/setup.sh") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRunnerSetupInSnapshotRejectsExcludedSymlinkTarget(t *testing.T) {
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(workspace, "audit"), dirPerm); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, workspace, "generated/setup-target.sh", "#!/usr/bin/env bash\n")
	if err := os.Symlink("../generated/setup-target.sh", filepath.Join(workspace, "audit", "setup.sh")); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "snapshot.tar.zst")
	if err := createWorkspaceSnapshot(SnapshotOptions{
		WorkspaceRoot: workspace,
		WorkspaceDir:  workspace,
		OutputPath:    output,
		ConfigExclude: []string{"generated/"},
	}); err != nil {
		t.Fatalf("create snapshot failed: %v", err)
	}

	err := validateRunnerSetupInSnapshot(output, RunnerSetupConfig{Script: "audit/setup.sh"})
	if err == nil {
		t.Fatal("expected missing setup symlink target error")
	}
	if !strings.Contains(err.Error(), "workspace snapshot does not contain setup script generated/setup-target.sh") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateWorkspaceSnapshotSkipsSymlinkEscape(t *testing.T) {
	workspace := t.TempDir()
	outsideDir := t.TempDir()
	writeWorkspaceFile(t, workspace, "inside.txt", "inside")
	outside := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outside, []byte("outside"), filePerm); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "outside-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside.txt", filepath.Join(workspace, "inside-link")); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(t.TempDir(), "snapshot.tar.zst")
	if err := createWorkspaceSnapshot(SnapshotOptions{
		WorkspaceRoot: workspace,
		WorkspaceDir:  workspace,
		OutputPath:    output,
	}); err != nil {
		t.Fatalf("create snapshot failed: %v", err)
	}

	names := snapshotEntryNames(t, output)
	if contains(names, "outside-link") {
		t.Fatalf("snapshot included symlink escape: %#v", names)
	}
	if !contains(names, "inside-link") {
		t.Fatalf("snapshot skipped safe internal symlink: %#v", names)
	}
}

func writeWorkspaceFile(t *testing.T, workspace, rel, body string) {
	t.Helper()
	path := filepath.Join(workspace, rel)
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), filePerm); err != nil {
		t.Fatal(err)
	}
}

func snapshotEntryNames(t *testing.T, path string) []string {
	t.Helper()
	input, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	decoder, err := zstd.NewReader(input)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	reader := tar.NewReader(decoder)

	var names []string
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(header.Name, "..") {
			t.Fatalf("unsafe snapshot entry name %q", header.Name)
		}
		names = append(names, header.Name)
	}
	return names
}
