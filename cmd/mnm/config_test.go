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
	if resolved.ModelProvider != "openrouter" {
		t.Fatalf("expected openrouter provider, got %q", resolved.ModelProvider)
	}
	if resolved.ModelAuth["openrouter"] != "test-key" {
		t.Fatalf("unexpected model auth: %#v", resolved.ModelAuth)
	}
}

func TestConfigSupportsOfficialModelProviders(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		model    string
		envName  string
	}{
		{name: "openrouter", provider: "openrouter", model: "openrouter/anthropic/claude-sonnet-4-5", envName: "OPENROUTER_API_KEY"},
		{name: "openai", provider: "openai", model: "openai/gpt-5.1", envName: "OPENAI_API_KEY"},
		{name: "anthropic", provider: "anthropic", model: "anthropic/claude-sonnet-4-5", envName: "ANTHROPIC_API_KEY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "mnm.toml")
			config := defaultConfig()
			config = strings.ReplaceAll(config, `"openrouter/deepseek/deepseek-v4-pro"`, `"`+tt.model+`"`)
			if err := os.WriteFile(path, []byte(config), filePerm); err != nil {
				t.Fatal(err)
			}
			t.Setenv(tt.envName, "test-key")

			cfg, err := loadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := cfg.validate(dir)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.ModelProvider != tt.provider {
				t.Fatalf("provider = %q, want %q", resolved.ModelProvider, tt.provider)
			}
			if resolved.APIKeyEnv != tt.envName {
				t.Fatalf("API key env = %q, want %q", resolved.APIKeyEnv, tt.envName)
			}
			if resolved.ModelAuth[tt.provider] != "test-key" {
				t.Fatalf("unexpected model auth: %#v", resolved.ModelAuth)
			}
		})
	}
}

func TestConfigNormalizesExplicitProviderModelForOpenCode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mnm.toml")
	config := defaultConfig()
	config = strings.Replace(config, `[models]`+"\n", `[models]`+"\n"+`provider = "openai"`+"\n", 1)
	config = strings.ReplaceAll(config, `"openrouter/deepseek/deepseek-v4-pro"`, `"gpt-5.1"`)
	if err := os.WriteFile(path, []byte(config), filePerm); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "openai-key")

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := cfg.validate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Model != "openai/gpt-5.1" {
		t.Fatalf("model = %q, want openai/gpt-5.1", resolved.Model)
	}
	if got := phaseModel(cfg, "validate"); got != "openai/gpt-5.1" {
		t.Fatalf("validate model = %q, want openai/gpt-5.1", got)
	}
}

func TestConfigSupportsMixedOfficialModelProviders(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mnm.toml")
	config := defaultConfig()
	config = strings.Replace(config, `recon = "openrouter/deepseek/deepseek-v4-pro"`, `recon = "openai/gpt-5.1"`, 1)
	config = strings.Replace(config, `validate = "openrouter/deepseek/deepseek-v4-pro"`, `validate = "anthropic/claude-sonnet-4-5"`, 1)
	if err := os.WriteFile(path, []byte(config), filePerm); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "openrouter-key")
	t.Setenv("OPENAI_API_KEY", "openai-key")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-key")

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := cfg.validate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ModelProvider != "openai" {
		t.Fatalf("primary provider = %q, want openai", resolved.ModelProvider)
	}
	for provider, want := range map[string]string{
		"openrouter": "openrouter-key",
		"openai":     "openai-key",
		"anthropic":  "anthropic-key",
	} {
		if got := resolved.ModelAuth[provider]; got != want {
			t.Fatalf("model auth[%s] = %q, want %q in %#v", provider, got, want, resolved.ModelAuth)
		}
	}
}

func TestConfigIgnoresUnusedDefaultProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mnm.toml")
	config := defaultConfig()
	for _, phase := range []string{"recon", "investigate", "review", "deduplicate", "validate", "finalize"} {
		config = strings.Replace(config, phase+` = "openrouter/deepseek/deepseek-v4-pro"`, phase+` = "openai/gpt-5.1"`, 1)
	}
	if err := os.WriteFile(path, []byte(config), filePerm); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "openai-key")

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := cfg.validate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ModelProvider != "openai" {
		t.Fatalf("provider = %q, want openai", resolved.ModelProvider)
	}
	if _, ok := resolved.ModelAuth["openrouter"]; ok {
		t.Fatalf("unused default provider should not require auth: %#v", resolved.ModelAuth)
	}
}

func TestConfigSupportsLegacyAPIKeyEnvFallback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mnm.toml")
	config := strings.Replace(defaultConfig(), `openrouter_api_key_env = "OPENROUTER_API_KEY"`+"\n", "", 1)
	config = strings.Replace(config, `[models]`+"\n", `[models]`+"\n"+`api_key_env = "CUSTOM_OPENROUTER_KEY"`+"\n", 1)
	if err := os.WriteFile(path, []byte(config), filePerm); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CUSTOM_OPENROUTER_KEY", "custom-key")

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := cfg.validate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.APIKeyEnv != "CUSTOM_OPENROUTER_KEY" {
		t.Fatalf("API key env = %q, want custom legacy env", resolved.APIKeyEnv)
	}
	if resolved.ModelAuth["openrouter"] != "custom-key" {
		t.Fatalf("unexpected model auth: %#v", resolved.ModelAuth)
	}
}

func TestConfigRejectsUnsupportedModelProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mnm.toml")
	config := defaultConfig()
	config = strings.Replace(config, `default = "openrouter/deepseek/deepseek-v4-pro"`, `default = "unsupported/model"`, 1)
	config = strings.Replace(config, `recon = "openrouter/deepseek/deepseek-v4-pro"`, `recon = "unsupported/model"`, 1)
	if err := os.WriteFile(path, []byte(config), filePerm); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.validate(dir)
	if err == nil {
		t.Fatal("expected unsupported provider error")
	}
	if !strings.Contains(err.Error(), "unsupported provider prefix") {
		t.Fatalf("unexpected error: %v", err)
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

func TestConfigAcceptsRunnerSetupHook(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "audit"), dirPerm); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "audit", "setup.sh"), []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "mnm.toml")
	config := strings.Replace(defaultConfig(), `script = ""`, `script = "audit/setup.sh"`, 1)
	config = strings.Replace(config, `mode = "fail"`, `mode = "warn"`, 1)
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
	if got := effectiveRunnerSetupTimeout(cfg.Runner.Setup); got != 15*time.Minute {
		t.Fatalf("setup timeout = %s, want 15m", got)
	}
	if got := runnerSetupMode(cfg.Runner.Setup); got != "warn" {
		t.Fatalf("setup mode = %q, want warn", got)
	}
}

func TestConfigRejectsBlankRiskArea(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mnm.toml")
	config := strings.Replace(defaultConfig(), `risk_areas = []`, `risk_areas = ["authorization", "  "]`, 1)
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
		t.Fatal("expected blank risk area error")
	}
	if !strings.Contains(err.Error(), "instructions.risk_areas[1] must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigRejectsUnsafeRiskAreaCategory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mnm.toml")
	config := strings.Replace(defaultConfig(), `risk_areas = []`, `risk_areas = ["authorization $(bad)"]`, 1)
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
		t.Fatal("expected unsafe risk area error")
	}
	if !strings.Contains(err.Error(), "contains unsupported category characters") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConfigRejectsInvalidRunnerSetupHook(t *testing.T) {
	tests := []struct {
		name     string
		edit     func(string) string
		wantText string
	}{
		{
			name: "absolute script",
			edit: func(config string) string {
				return strings.Replace(config, `script = ""`, `script = "/tmp/setup.sh"`, 1)
			},
			wantText: "path must be relative",
		},
		{
			name: "traversal script",
			edit: func(config string) string {
				return strings.Replace(config, `script = ""`, `script = "../setup.sh"`, 1)
			},
			wantText: "path must stay inside",
		},
		{
			name: "negative timeout",
			edit: func(config string) string {
				return strings.Replace(config, `timeout_minutes = 15`, `timeout_minutes = -1`, 1)
			},
			wantText: "runner.setup.timeout_minutes must not be negative",
		},
		{
			name: "bad mode",
			edit: func(config string) string {
				return strings.Replace(config, `mode = "fail"`, `mode = "continue"`, 1)
			},
			wantText: `runner.setup.mode must be "fail" or "warn"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "mnm.toml")
			config := tt.edit(defaultConfig())
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
				t.Fatal("expected runner setup validation error")
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
