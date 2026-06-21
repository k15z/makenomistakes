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
	Scope string `toml:"scope"`
}

type WorkspaceConfig struct {
	Root    string   `toml:"root"`
	Exclude []string `toml:"exclude"`
}

type ModelConfig struct {
	APIKeyEnv   string `toml:"api_key_env"`
	Default     string `toml:"default"`
	Recon       string `toml:"recon"`
	Investigate string `toml:"investigate"`
	Review      string `toml:"review"`
	Deduplicate string `toml:"deduplicate"`
	Validate    string `toml:"validate"`
	Finalize    string `toml:"finalize"`
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
	Model         string
	Timeout       time.Duration
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

	apiKeyEnv := strings.TrimSpace(cfg.Models.APIKeyEnv)
	if apiKeyEnv == "" {
		apiKeyEnv = "OPENROUTER_API_KEY"
	}
	if os.Getenv(apiKeyEnv) == "" {
		return ResolvedConfig{}, fmt.Errorf("model API key environment variable %s is not set", apiKeyEnv)
	}

	model := strings.TrimSpace(cfg.Models.Recon)
	if model == "" {
		model = strings.TrimSpace(cfg.Models.Default)
	}
	if model == "" {
		return ResolvedConfig{}, errors.New("models.default or models.recon must be set")
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
		APIKeyEnv:     apiKeyEnv,
		Model:         model,
		Timeout:       time.Duration(cfg.Runner.TimeoutMinutes) * time.Minute,
	}, nil
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
