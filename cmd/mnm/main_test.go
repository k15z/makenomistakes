package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitCreatesConfigAndIgnore(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	if err := run([]string{"init", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("init failed: %v\nstderr: %s", err, stderr.String())
	}

	config := readFile(t, filepath.Join(dir, "mnm.toml"))
	if !strings.Contains(config, `version = 1`) {
		t.Fatalf("config missing version:\n%s", config)
	}
	if !strings.Contains(config, `default = "openrouter/deepseek/deepseek-v4-pro"`) {
		t.Fatalf("config missing default model:\n%s", config)
	}

	ignore := readFile(t, filepath.Join(dir, ".mnmignore"))
	if !strings.Contains(ignore, ".mnm/") {
		t.Fatalf("ignore missing .mnm exclusion:\n%s", ignore)
	}

	if !strings.Contains(stdout.String(), "created") {
		t.Fatalf("stdout should mention created files, got %q", stdout.String())
	}
}

func TestInitRefusesOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	if err := run([]string{"init", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("initial init failed: %v", err)
	}

	stdout.Reset()
	stderr.Reset()
	err := run([]string{"init", dir}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected overwrite error")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInitForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "mnm.toml")
	if err := os.WriteFile(configPath, []byte("old"), filePerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mnmignore"), []byte("old"), filePerm); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := run([]string{"init", "--force", dir}, &stdout, &stderr); err != nil {
		t.Fatalf("force init failed: %v", err)
	}

	config := readFile(t, configPath)
	if config == "old" {
		t.Fatal("config was not overwritten")
	}
}

func TestUnknownCommandFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := run([]string{"wat"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected unknown command error")
	}
	if !strings.Contains(err.Error(), `unknown command "wat"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUsageMentionsRunsAndResume(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(nil, &stdout, &stderr); err != nil {
		t.Fatalf("usage failed: %v", err)
	}
	output := stdout.String()
	for _, want := range []string{
		"mnm analyze [--prepare-only] [--keep-vm] [--resume RUN_ID] [path]",
		"mnm runs [--json] [path]",
		"mnm report show [--json] RUN_ID [path]",
		"runs       List local audit runs, statuses, and resumability.",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("usage missing %q:\n%s", want, output)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
