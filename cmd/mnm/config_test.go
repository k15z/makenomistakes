package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	config := strings.Replace(defaultConfig(), `recon = "openrouter/deepseek/deepseek-v4-pro"`, `recon = "openrouter/test-recon"`, 1)
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

func TestConfigDefaultsOpenCodeTaskTimeoutWhenOmitted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mnm.toml")
	config := strings.Replace(defaultConfig(), "opencode_task_timeout_minutes = 30\n", "", 1)
	if err := os.WriteFile(path, []byte(config), filePerm); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.validate(dir); err != nil {
		t.Fatal(err)
	}
	if got := openCodeTaskTimeout(cfg); got != 30*time.Minute {
		t.Fatalf("OpenCode task timeout = %s, want 30m", got)
	}
}

func TestConfigClampsDefaultOpenCodeTaskTimeoutToRunTimeout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mnm.toml")
	config := strings.Replace(defaultConfig(), "timeout_minutes = 120", "timeout_minutes = 10", 1)
	config = strings.Replace(config, "opencode_task_timeout_minutes = 30\n", "", 1)
	if err := os.WriteFile(path, []byte(config), filePerm); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "test-key")

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.validate(dir); err != nil {
		t.Fatal(err)
	}
	if got := openCodeTaskTimeout(cfg); got != 10*time.Minute {
		t.Fatalf("OpenCode task timeout = %s, want 10m", got)
	}
}

func TestConfigRejectsInvalidOpenCodeTaskTimeout(t *testing.T) {
	tests := []struct {
		name     string
		replace  string
		wantText string
	}{
		{
			name:     "negative",
			replace:  "opencode_task_timeout_minutes = -1",
			wantText: "must not be negative",
		},
		{
			name:     "longer than run",
			replace:  "opencode_task_timeout_minutes = 121",
			wantText: "must not exceed runner.timeout_minutes",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "mnm.toml")
			config := strings.Replace(defaultConfig(), "opencode_task_timeout_minutes = 30", tt.replace, 1)
			if err := os.WriteFile(path, []byte(config), filePerm); err != nil {
				t.Fatal(err)
			}
			t.Setenv("OPENROUTER_API_KEY", "test-key")

			cfg, err := loadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = cfg.validate(dir)
			if err == nil {
				t.Fatal("expected OpenCode task timeout validation error")
			}
			if !strings.Contains(err.Error(), tt.wantText) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestPhaseModelUsesInvestigateOverride(t *testing.T) {
	cfg := Config{
		Models: ModelConfig{
			Default:     "openrouter/default",
			Recon:       "openrouter/recon",
			Investigate: "openrouter/investigate",
			Review:      "openrouter/review",
			Deduplicate: "openrouter/deduplicate",
			Validate:    "openrouter/validate",
			Finalize:    "openrouter/finalize",
		},
	}
	if got := phaseModel(cfg, "recon"); got != "openrouter/recon" {
		t.Fatalf("recon model = %q", got)
	}
	if got := phaseModel(cfg, "investigate"); got != "openrouter/investigate" {
		t.Fatalf("investigate model = %q", got)
	}
	if got := phaseModel(cfg, "review"); got != "openrouter/review" {
		t.Fatalf("review model = %q", got)
	}
	if got := phaseModel(cfg, "deduplicate"); got != "openrouter/deduplicate" {
		t.Fatalf("deduplicate model = %q", got)
	}
	if got := phaseModel(cfg, "validate"); got != "openrouter/validate" {
		t.Fatalf("validate model = %q", got)
	}
	if got := phaseModel(cfg, "finalize"); got != "openrouter/finalize" {
		t.Fatalf("finalize model = %q", got)
	}
	cfg.Models.Investigate = ""
	if got := phaseModel(cfg, "investigate"); got != "openrouter/default" {
		t.Fatalf("investigate fallback = %q", got)
	}
	cfg.Models.Review = ""
	if got := phaseModel(cfg, "review"); got != "openrouter/default" {
		t.Fatalf("review fallback = %q", got)
	}
	cfg.Models.Deduplicate = ""
	if got := phaseModel(cfg, "deduplicate"); got != "openrouter/default" {
		t.Fatalf("deduplicate fallback = %q", got)
	}
	cfg.Models.Validate = ""
	if got := phaseModel(cfg, "validate"); got != "openrouter/default" {
		t.Fatalf("validate fallback = %q", got)
	}
	cfg.Models.Finalize = ""
	if got := phaseModel(cfg, "finalize"); got != "openrouter/default" {
		t.Fatalf("finalize fallback = %q", got)
	}
	cfg.Models.Default = ""
	if got := phaseModel(cfg, "investigate"); got != "openrouter/recon" {
		t.Fatalf("investigate recon fallback = %q", got)
	}
	if got := phaseModel(cfg, "review"); got != "openrouter/recon" {
		t.Fatalf("review recon fallback = %q", got)
	}
	if got := phaseModel(cfg, "deduplicate"); got != "openrouter/recon" {
		t.Fatalf("deduplicate recon fallback = %q", got)
	}
	if got := phaseModel(cfg, "validate"); got != "openrouter/recon" {
		t.Fatalf("validate recon fallback = %q", got)
	}
	if got := phaseModel(cfg, "finalize"); got != "openrouter/recon" {
		t.Fatalf("finalize recon fallback = %q", got)
	}
}
