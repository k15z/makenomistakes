package main

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

type SnapshotOptions struct {
	WorkspaceRoot string
	WorkspaceDir  string
	OutputPath    string
	ConfigExclude []string
}

func createWorkspaceSnapshot(options SnapshotOptions) error {
	root, err := filepath.Abs(options.WorkspaceRoot)
	if err != nil {
		return err
	}
	workspaceDir, err := filepath.Abs(options.WorkspaceDir)
	if err != nil {
		return err
	}
	if options.OutputPath == "" {
		return errors.New("snapshot output path is required")
	}
	if err := os.MkdirAll(filepath.Dir(options.OutputPath), dirPerm); err != nil {
		return err
	}

	patterns := defaultSnapshotExcludes()
	if ignore, err := os.ReadFile(filepath.Join(workspaceDir, ".mnmignore")); err == nil {
		patterns = append(patterns, parseIgnoreFile(string(ignore))...)
	}
	patterns = append(patterns, options.ConfigExclude...)

	output, err := os.Create(options.OutputPath)
	if err != nil {
		return err
	}
	defer output.Close()

	encoder, err := zstd.NewWriter(output)
	if err != nil {
		return err
	}
	defer encoder.Close()

	tarWriter := tar.NewWriter(encoder)
	defer tarWriter.Close()

	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if shouldExcludeSnapshotPath(rel, entry.IsDir(), patterns) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = rel

		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(path)
			if err != nil {
				return err
			}
			if !safeSymlinkTarget(root, path, linkTarget) {
				return nil
			}
			header.Linkname = linkTarget
			return tarWriter.WriteHeader(header)
		}

		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
}

func extractWorkspaceSnapshot(snapshotPath, dst string) error {
	input, err := os.Open(snapshotPath)
	if err != nil {
		return err
	}
	defer input.Close()
	decoder, err := zstd.NewReader(input)
	if err != nil {
		return err
	}
	defer decoder.Close()
	reader := tar.NewReader(decoder)

	cleanDst, err := filepath.Abs(dst)
	if err != nil {
		return err
	}
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		target, err := safeSnapshotTarget(cleanDst, header.Name)
		if err != nil {
			return err
		}

		mode := fs.FileMode(header.Mode)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, mode); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), dirPerm); err != nil {
				return err
			}
			output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(output, reader)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case tar.TypeSymlink:
			if !safeSymlinkTarget(cleanDst, target, header.Linkname) {
				continue
			}
			if err := os.MkdirAll(filepath.Dir(target), dirPerm); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(header.Linkname, target); err != nil {
				return err
			}
		default:
			continue
		}
	}
	return nil
}

func safeSnapshotTarget(dst, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", fmt.Errorf("unsafe snapshot entry path %q", name)
	}
	cleanName := filepath.Clean(filepath.FromSlash(name))
	if cleanName == "." || strings.HasPrefix(cleanName, ".."+string(filepath.Separator)) || cleanName == ".." {
		return "", fmt.Errorf("unsafe snapshot entry path %q", name)
	}
	target := filepath.Join(dst, cleanName)
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(dst, absTarget)
	if err != nil {
		return "", err
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("unsafe snapshot entry path %q", name)
	}
	return absTarget, nil
}

func defaultSnapshotExcludes() []string {
	return parseIgnoreFile(defaultIgnore())
}

func parseIgnoreFile(text string) []string {
	var patterns []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, filepath.ToSlash(line))
	}
	return patterns
}

func shouldExcludeSnapshotPath(rel string, isDir bool, patterns []string) bool {
	base := pathBase(rel)
	for _, raw := range patterns {
		pattern := strings.TrimSpace(filepath.ToSlash(raw))
		if pattern == "" {
			continue
		}
		dirOnly := strings.HasSuffix(pattern, "/")
		pattern = strings.TrimSuffix(pattern, "/")
		if pattern == "" {
			continue
		}

		if dirOnly {
			if rel == pattern || strings.HasPrefix(rel, pattern+"/") || base == pattern {
				return true
			}
			continue
		}
		if ok, _ := filepath.Match(pattern, rel); ok {
			return true
		}
		if ok, _ := filepath.Match(pattern, base); ok {
			return true
		}
		if rel == pattern || strings.HasPrefix(rel, pattern+"/") {
			return true
		}
		if isDir && base == pattern {
			return true
		}
	}
	return false
}

func safeSymlinkTarget(root, linkPath, target string) bool {
	if target == "" {
		return false
	}
	resolved := target
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(filepath.Dir(linkPath), target)
	}
	resolved, err := filepath.Abs(resolved)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil {
		return false
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return false
	}
	return true
}

func pathBase(rel string) string {
	parts := strings.Split(rel, "/")
	if len(parts) == 0 {
		return rel
	}
	return parts[len(parts)-1]
}
