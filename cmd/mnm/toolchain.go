package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const nodeToolchainVersion = "22.11.0"

func ensureWorkspaceToolchains(workspace string) error {
	hasNodeProject, err := workspaceContainsFile(workspace, "package.json")
	if err != nil {
		return err
	}
	if hasNodeProject {
		return ensureNodeToolchain()
	}
	return nil
}

func workspaceContainsFile(root, name string) (bool, error) {
	found := false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", ".mnm", "node_modules", "vendor", "dist", "build", "coverage":
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() == name {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil && !errors.Is(err, filepath.SkipAll) {
		return false, err
	}
	return found, nil
}

func ensureNodeToolchain() error {
	if pinnedNodeOnPath() && commandExists("npm") {
		return nil
	}
	platform, err := nodeArchivePlatform(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	installDir := filepath.Join(os.TempDir(), "mnm-tools", "node-v"+nodeToolchainVersion+"-"+platform)
	binDir := filepath.Join(installDir, "bin")
	nodePath := filepath.Join(binDir, "node")
	npmPath := filepath.Join(binDir, "npm")
	if _, err := os.Stat(nodePath); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := installNodeToolchain(platform, installDir); err != nil {
			return err
		}
	}
	if _, err := os.Stat(npmPath); err != nil {
		return fmt.Errorf("node toolchain installed without npm: %s", npmPath)
	}
	prependPATH(binDir)
	return nil
}

func pinnedNodeOnPath() bool {
	path, err := exec.LookPath("node")
	if err != nil {
		return false
	}
	version, err := commandOutput(path, "--version")
	if err != nil {
		return false
	}
	return strings.TrimSpace(version) == "v"+nodeToolchainVersion
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func nodeArchivePlatform(goos, goarch string) (string, error) {
	if goos != "linux" {
		return "", fmt.Errorf("node toolchain bootstrap only supports linux runners, got %s/%s", goos, goarch)
	}
	switch goarch {
	case "amd64":
		return "linux-x64", nil
	case "arm64":
		return "linux-arm64", nil
	default:
		return "", fmt.Errorf("node toolchain bootstrap does not support %s/%s", goos, goarch)
	}
}

func installNodeToolchain(platform, installDir string) error {
	baseDir := filepath.Join(os.TempDir(), "mnm-tools")
	if err := os.MkdirAll(baseDir, dirPerm); err != nil {
		return err
	}
	archive := filepath.Join(baseDir, "node-v"+nodeToolchainVersion+"-"+platform+".tar.xz")
	url := fmt.Sprintf("https://nodejs.org/dist/v%[1]s/node-v%[1]s-%[2]s.tar.xz", nodeToolchainVersion, platform)
	if _, err := os.Stat(archive); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := runToolCommand("curl", "-fsSL", "-o", archive, url); err != nil {
			return fmt.Errorf("download node toolchain: %w", err)
		}
	}

	extractDir, err := os.MkdirTemp(baseDir, "node-extract-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(extractDir)
	if err := runToolCommand("tar", "-xJf", archive, "-C", extractDir); err != nil {
		return fmt.Errorf("extract node toolchain: %w", err)
	}
	extracted := filepath.Join(extractDir, "node-v"+nodeToolchainVersion+"-"+platform)
	if err := os.RemoveAll(installDir); err != nil {
		return err
	}
	if err := os.Rename(extracted, installDir); err != nil {
		return err
	}
	return nil
}

func runToolCommand(name string, args ...string) error {
	command := exec.Command(name, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	return command.Run()
}

func prependPATH(dir string) {
	os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
