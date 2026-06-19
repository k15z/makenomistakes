package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfigRejectsUnsupportedVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mnm.toml")
	if err := os.WriteFile(path, []byte(strings.Replace(defaultConfig(), "version = 1", "version = 2", 1)), filePerm); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.validate(dir)
	if err == nil {
		t.Fatal("expected unsupported version error")
	}
	if !strings.Contains(err.Error(), "unsupported config version") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigUsesReconModelWhenSet(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mnm.toml")
	config := strings.Replace(defaultConfig(), `recon = "openrouter/z-ai/glm-5.2"`, `recon = "openrouter/test-recon"`, 1)
	if err := os.WriteFile(path, []byte(config), filePerm); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := cfg.validate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Model != "openrouter/test-recon" {
		t.Fatalf("expected recon model, got %q", resolved.Model)
	}
}
