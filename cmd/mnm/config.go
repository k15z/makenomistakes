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
	APIKeyEnv string `toml:"api_key_env"`
	Default   string `toml:"default"`
	Recon     string `toml:"recon"`
}

type RunnerConfig struct {
	CPUs           int `toml:"cpus"`
	MemoryGB       int `toml:"memory_gb"`
	DiskGB         int `toml:"disk_gb"`
	TimeoutMinutes int `toml:"timeout_minutes"`
	MaxLeads       int `toml:"max_leads"`
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
	if cfg.Runner.MaxLeads <= 0 {
		return ResolvedConfig{}, errors.New("runner.max_leads must be greater than zero")
	}

	return ResolvedConfig{
		ConfigPath:    filepath.Join(workspaceDir, "mnm.toml"),
		WorkspaceRoot: workspaceRoot,
		APIKeyEnv:     apiKeyEnv,
		Model:         model,
		Timeout:       time.Duration(cfg.Runner.TimeoutMinutes) * time.Minute,
	}, nil
}
