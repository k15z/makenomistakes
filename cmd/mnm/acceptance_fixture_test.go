package main

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestVulnerableWorkspaceFixtureIsUsable(t *testing.T) {
	root, err := findSourceRoot()
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(root, "examples", "vulnerable-workspace")

	t.Setenv("OPENROUTER_API_KEY", "test-key")
	cfg, err := loadConfig(filepath.Join(fixture, "mnm.toml"))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := cfg.validate(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.WorkspaceRoot != fixture {
		t.Fatalf("workspace root = %q, want %q", resolved.WorkspaceRoot, fixture)
	}

	hasNodeProject, err := workspaceContainsFile(fixture, "package.json")
	if err != nil {
		t.Fatal(err)
	}
	if !hasNodeProject {
		t.Fatal("fixture should contain at least one package.json so the VM bootstraps Node")
	}

	for _, rel := range []string{
		"repos/file-vault/src/vault.js",
		"repos/file-vault/test/vault.test.js",
		"repos/file-vault/secrets/admin-token.txt",
		"repos/health-check/src/status.js",
		"repos/health-check/test/status.test.js",
	} {
		if _, err := requirePathInsideRunDir(fixture, filepath.Join(fixture, rel)); err != nil {
			t.Fatalf("fixture path %s: %v", rel, err)
		}
	}
}

func TestAcceptanceScriptSyntax(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}
	root, err := findSourceRoot()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "acceptance-vulnerable-workspace.sh")
	command := exec.Command(bash, "-n", script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("acceptance script syntax check failed: %v\n%s", err, string(output))
	}
}
