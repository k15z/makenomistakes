package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceContainsFileFindsPackageManifest(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "apps/api/package.json", `{"scripts":{"test":"node test.js"}}`)
	writeWorkspaceFile(t, workspace, "node_modules/pkg/package.json", `{"ignored":true}`)

	found, err := workspaceContainsFile(workspace, "package.json")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected package.json to be detected")
	}
}

func TestWorkspaceContainsFileSkipsIgnoredToolDirs(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFile(t, workspace, "node_modules/pkg/package.json", `{"ignored":true}`)

	found, err := workspaceContainsFile(workspace, "package.json")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected package.json under node_modules to be ignored")
	}
}

func TestNodeArchivePlatform(t *testing.T) {
	cases := []struct {
		goarch string
		want   string
	}{
		{goarch: "amd64", want: "linux-x64"},
		{goarch: "arm64", want: "linux-arm64"},
	}
	for _, tc := range cases {
		got, err := nodeArchivePlatform("linux", tc.goarch)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("nodeArchivePlatform(linux, %s) = %q, want %q", tc.goarch, got, tc.want)
		}
	}
	if _, err := nodeArchivePlatform("darwin", "arm64"); err == nil {
		t.Fatal("expected unsupported OS error")
	}
}

func TestEnsureNodeToolchainUsesExistingNodeAndNPM(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"node", "npm"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)

	if err := ensureNodeToolchain(); err != nil {
		t.Fatal(err)
	}
}
