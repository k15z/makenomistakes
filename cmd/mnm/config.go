package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

const configVersion = 1
const defaultOpenCodeTaskTimeoutMinutes = 30
const defaultRunnerSetupTimeoutMinutes = 15

type Config struct {
	Version      int               `toml:"version"`
	Instructions InstructionConfig `toml:"instructions"`
	Workspace    WorkspaceConfig   `toml:"workspace"`
	Models       ModelConfig       `toml:"models"`
	Runner       RunnerConfig      `toml:"runner"`
}

type InstructionConfig struct {
	Scope     string   `toml:"scope"`
	RiskAreas []string `toml:"risk_areas"`
}

type WorkspaceConfig struct {
	Root    string   `toml:"root"`
	Exclude []string `toml:"exclude"`
}

type ModelConfig struct {
	Provider         string `toml:"provider"`
	APIKeyEnv        string `toml:"api_key_env"`
	OpenRouterKeyEnv string `toml:"openrouter_api_key_env"`
	OpenAIKeyEnv     string `toml:"openai_api_key_env"`
	AnthropicKeyEnv  string `toml:"anthropic_api_key_env"`
	Default          string `toml:"default"`
	Recon            string `toml:"recon"`
	Investigate      string `toml:"investigate"`
	Review           string `toml:"review"`
	Deduplicate      string `toml:"deduplicate"`
	Validate         string `toml:"validate"`
	Finalize         string `toml:"finalize"`
}

type RunnerConfig struct {
	CPUs                       int               `toml:"cpus"`
	MemoryGB                   int               `toml:"memory_gb"`
	DiskGB                     int               `toml:"disk_gb"`
	TimeoutMinutes             int               `toml:"timeout_minutes"`
	OpenCodeTaskTimeoutMinutes int               `toml:"opencode_task_timeout_minutes"`
	MaxLeads                   int               `toml:"max_leads"`
	MaxInvestigations          int               `toml:"max_investigations"`
	ParallelTasks              int               `toml:"parallel_tasks"`
	Setup                      RunnerSetupConfig `toml:"setup"`
}

type RunnerSetupConfig struct {
	Script         string `toml:"script"`
	TimeoutMinutes int    `toml:"timeout_minutes"`
	Mode           string `toml:"mode"`
}

type ResolvedConfig struct {
	ConfigPath    string
	WorkspaceRoot string
	APIKeyEnv     string
	ModelProvider string
	ModelAuth     map[string]string
	Model         string
	Timeout       time.Duration
}

var supportedModelProviders = map[string]string{
	"openrouter": "OPENROUTER_API_KEY",
	"openai":     "OPENAI_API_KEY",
	"anthropic":  "ANTHROPIC_API_KEY",
}

func loadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("mnm.toml not found in workspace; run mnm init first")
		}
		return Config{}, err
	}
	var cfg Config
	if err := toml.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse mnm.toml: %w", err)
	}
	return cfg, nil
}

func (cfg Config) validate(workspaceDir string) (ResolvedConfig, error) {
	if cfg.Version != configVersion {
		return ResolvedConfig{}, fmt.Errorf("unsupported config version %d", cfg.Version)
	}
	if strings.TrimSpace(cfg.Instructions.Scope) == "" {
		return ResolvedConfig{}, errors.New("instructions.scope must not be empty")
	}
	if _, err := normalizedRiskAreas(cfg.Instructions.RiskAreas); err != nil {
		return ResolvedConfig{}, err
	}

	root := strings.TrimSpace(cfg.Workspace.Root)
	if root == "" {
		root = "."
	}
	workspaceRoot := root
	if !filepath.IsAbs(workspaceRoot) {
		workspaceRoot = filepath.Join(workspaceDir, workspaceRoot)
	}
	workspaceRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return ResolvedConfig{}, err
	}
	info, err := os.Stat(workspaceRoot)
	if err != nil {
		return ResolvedConfig{}, fmt.Errorf("workspace.root is not readable: %w", err)
	}
	if !info.IsDir() {
		return ResolvedConfig{}, fmt.Errorf("workspace.root must be a directory: %s", workspaceRoot)
	}

	model := phaseModel(cfg, "recon")
	if model == "" {
		return ResolvedConfig{}, errors.New("models.default or models.recon must be set")
	}
	modelAuth, primaryProvider, primaryAPIKeyEnv, err := resolveModelAuth(cfg.Models, model)
	if err != nil {
		return ResolvedConfig{}, err
	}

	if cfg.Runner.CPUs <= 0 {
		return ResolvedConfig{}, errors.New("runner.cpus must be greater than zero")
	}
	if cfg.Runner.MemoryGB <= 0 {
		return ResolvedConfig{}, errors.New("runner.memory_gb must be greater than zero")
	}
	if cfg.Runner.DiskGB <= 0 {
		return ResolvedConfig{}, errors.New("runner.disk_gb must be greater than zero")
	}
	if cfg.Runner.TimeoutMinutes <= 0 {
		return ResolvedConfig{}, errors.New("runner.timeout_minutes must be greater than zero")
	}
	if cfg.Runner.OpenCodeTaskTimeoutMinutes < 0 {
		return ResolvedConfig{}, errors.New("runner.opencode_task_timeout_minutes must not be negative")
	}
	if cfg.Runner.OpenCodeTaskTimeoutMinutes > cfg.Runner.TimeoutMinutes {
		return ResolvedConfig{}, errors.New("runner.opencode_task_timeout_minutes must not exceed runner.timeout_minutes")
	}
	if cfg.Runner.MaxLeads <= 0 {
		return ResolvedConfig{}, errors.New("runner.max_leads must be greater than zero")
	}
	if cfg.Runner.MaxInvestigations < 0 {
		return ResolvedConfig{}, errors.New("runner.max_investigations must not be negative")
	}
	if cfg.Runner.ParallelTasks < 0 {
		return ResolvedConfig{}, errors.New("runner.parallel_tasks must not be negative")
	}
	if err := validateRunnerSetupConfig(cfg.Runner.Setup); err != nil {
		return ResolvedConfig{}, err
	}

	return ResolvedConfig{
		ConfigPath:    filepath.Join(workspaceDir, "mnm.toml"),
		WorkspaceRoot: workspaceRoot,
		APIKeyEnv:     primaryAPIKeyEnv,
		ModelProvider: primaryProvider,
		ModelAuth:     modelAuth,
		Model:         model,
		Timeout:       time.Duration(cfg.Runner.TimeoutMinutes) * time.Minute,
	}, nil
}

func resolveModelAuth(models ModelConfig, primaryModel string) (map[string]string, string, string, error) {
	providers, err := configuredModelProviders(models)
	if err != nil {
		return nil, "", "", err
	}
	if len(providers) == 0 {
		return nil, "", "", errors.New("models.default or models.recon must be set")
	}
	auth := map[string]string{}
	for _, provider := range providers {
		envName := modelProviderAPIKeyEnv(models, provider, len(providers) == 1)
		if os.Getenv(envName) == "" {
			return nil, "", "", fmt.Errorf("model provider %s API key environment variable %s is not set", provider, envName)
		}
		auth[provider] = os.Getenv(envName)
	}
	primaryProvider, err := modelProvider(models.Provider, primaryModel)
	if err != nil {
		return nil, "", "", err
	}
	return auth, primaryProvider, modelProviderAPIKeyEnv(models, primaryProvider, len(providers) == 1), nil
}

func configuredModelProviders(models ModelConfig) ([]string, error) {
	cfg := Config{Models: models}
	modelsToCheck := []string{
		phaseModel(cfg, "recon"),
		phaseModel(cfg, "investigate"),
		phaseModel(cfg, "review"),
		phaseModel(cfg, "deduplicate"),
		phaseModel(cfg, "validate"),
		phaseModel(cfg, "finalize"),
	}
	seen := map[string]bool{}
	var providers []string
	for _, model := range modelsToCheck {
		if strings.TrimSpace(model) == "" {
			continue
		}
		provider, err := modelProvider(models.Provider, model)
		if err != nil {
			return nil, err
		}
		if !seen[provider] {
			seen[provider] = true
			providers = append(providers, provider)
		}
	}
	return providers, nil
}

func modelProvider(explicitProvider, model string) (string, error) {
	explicitProvider = strings.ToLower(strings.TrimSpace(explicitProvider))
	if explicitProvider != "" {
		if _, ok := supportedModelProviders[explicitProvider]; !ok {
			return "", fmt.Errorf("models.provider = %q, want one of %s", explicitProvider, supportedProviderList())
		}
	}
	model = strings.TrimSpace(model)
	prefix := model
	if slash := strings.Index(prefix, "/"); slash >= 0 {
		prefix = prefix[:slash]
	} else {
		prefix = ""
	}
	prefix = strings.ToLower(strings.TrimSpace(prefix))
	if prefix == "" {
		if explicitProvider == "" {
			return "", fmt.Errorf("model %q must use a supported provider prefix or set models.provider to one of %s", model, supportedProviderList())
		}
		return explicitProvider, nil
	}
	if _, ok := supportedModelProviders[prefix]; !ok {
		if explicitProvider != "" {
			return explicitProvider, nil
		}
		return "", fmt.Errorf("model %q uses unsupported provider prefix %q; supported providers: %s", model, prefix, supportedProviderList())
	}
	if explicitProvider != "" && prefix != explicitProvider {
		return "", fmt.Errorf("model %q uses provider %q but models.provider = %q", model, prefix, explicitProvider)
	}
	return prefix, nil
}

func normalizeModelForOpenCode(explicitProvider, model string) (string, error) {
	model = strings.TrimSpace(model)
	provider, err := modelProvider(explicitProvider, model)
	if err != nil {
		return "", err
	}
	prefix := ""
	if slash := strings.Index(model, "/"); slash >= 0 {
		prefix = strings.ToLower(strings.TrimSpace(model[:slash]))
	}
	if prefix == provider {
		return model, nil
	}
	return provider + "/" + model, nil
}

func modelProviderAPIKeyEnv(models ModelConfig, provider string, allowLegacy bool) string {
	switch provider {
	case "openrouter":
		if strings.TrimSpace(models.OpenRouterKeyEnv) != "" {
			return strings.TrimSpace(models.OpenRouterKeyEnv)
		}
	case "openai":
		if strings.TrimSpace(models.OpenAIKeyEnv) != "" {
			return strings.TrimSpace(models.OpenAIKeyEnv)
		}
	case "anthropic":
		if strings.TrimSpace(models.AnthropicKeyEnv) != "" {
			return strings.TrimSpace(models.AnthropicKeyEnv)
		}
	}
	if allowLegacy && strings.TrimSpace(models.APIKeyEnv) != "" {
		return strings.TrimSpace(models.APIKeyEnv)
	}
	return supportedModelProviders[provider]
}

func supportedProviderList() string {
	return "anthropic, openai, openrouter"
}

func validateRunnerSetupConfig(setup RunnerSetupConfig) error {
	return validateRunnerSetupSyntax(setup)
}

func validateRunnerSetupSyntax(setup RunnerSetupConfig) error {
	if strings.TrimSpace(setup.Script) == "" {
		if setup.TimeoutMinutes < 0 {
			return errors.New("runner.setup.timeout_minutes must not be negative")
		}
		if mode := strings.TrimSpace(setup.Mode); mode != "" && !oneOf(mode, "fail", "warn") {
			return errors.New(`runner.setup.mode must be "fail" or "warn"`)
		}
		return nil
	}
	if _, err := cleanWorkspaceRelativePath(setup.Script); err != nil {
		return fmt.Errorf("runner.setup.script: %w", err)
	}
	if setup.TimeoutMinutes < 0 {
		return errors.New("runner.setup.timeout_minutes must not be negative")
	}
	if mode := strings.TrimSpace(setup.Mode); mode != "" && !oneOf(mode, "fail", "warn") {
		return errors.New(`runner.setup.mode must be "fail" or "warn"`)
	}
	return nil
}

func cleanWorkspaceRelativePath(value string) (string, error) {
	relPath := strings.TrimSpace(filepath.ToSlash(value))
	if relPath == "" {
		return "", errors.New("path must not be empty")
	}
	if strings.Contains(relPath, `\`) {
		return "", errors.New("path must use slash-separated relative paths")
	}
	if filepath.IsAbs(relPath) || strings.HasPrefix(relPath, "/") {
		return "", errors.New("path must be relative to the workspace root")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relPath)))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path must stay inside the workspace root")
	}
	if clean != relPath {
		return "", fmt.Errorf("path = %q, want clean relative path %q", relPath, clean)
	}
	return clean, nil
}

func effectiveOpenCodeTaskTimeoutMinutes(cfg Config) int {
	if cfg.Runner.OpenCodeTaskTimeoutMinutes > 0 {
		return cfg.Runner.OpenCodeTaskTimeoutMinutes
	}
	if cfg.Runner.TimeoutMinutes > 0 && cfg.Runner.TimeoutMinutes < defaultOpenCodeTaskTimeoutMinutes {
		return cfg.Runner.TimeoutMinutes
	}
	return defaultOpenCodeTaskTimeoutMinutes
}

func openCodeTaskTimeout(cfg Config) time.Duration {
	return time.Duration(effectiveOpenCodeTaskTimeoutMinutes(cfg)) * time.Minute
}

func effectiveRunnerSetupTimeout(setup RunnerSetupConfig) time.Duration {
	minutes := setup.TimeoutMinutes
	if minutes == 0 {
		minutes = defaultRunnerSetupTimeoutMinutes
	}
	return time.Duration(minutes) * time.Minute
}

func runnerSetupMode(setup RunnerSetupConfig) string {
	mode := strings.TrimSpace(setup.Mode)
	if mode == "" {
		return "fail"
	}
	return mode
}

func effectiveParallelTasks(runner RunnerConfig) int {
	if runner.ParallelTasks > 0 {
		return runner.ParallelTasks
	}
	if runner.CPUs > 1 {
		return 2
	}
	return 1
}
