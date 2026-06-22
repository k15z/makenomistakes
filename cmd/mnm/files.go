package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

func copyDirContents(src, dst string) error {
	return copyDirContentsFiltered(src, dst, nil)
}

func copyDirContentsFiltered(src, dst string, shouldCopyFile func(relPath string) bool) error {
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		if shouldCopyFile != nil && !shouldCopyFile(rel) {
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		}
		return copyFileMode(path, target, info.Mode())
	})
}

func copyFileMode(src, dst string, mode fs.FileMode) error {
	input, err := os.Open(src)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(dst), dirPerm); err != nil {
		return err
	}
	output, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func writeFileAtomic(path string, data []byte, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tempPath)
		}
	}()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func validateNonEmptyEvidenceFile(runDir, relPath string) error {
	path := filepath.Join(runDir, filepath.FromSlash(relPath))
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read evidence file %s: %w", relPath, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("evidence file %s must not be empty", relPath)
	}
	return nil
}

func evidenceFileSHA256(runDir, relPath string) (string, error) {
	data, err := os.ReadFile(filepath.Join(runDir, filepath.FromSlash(relPath)))
	if err != nil {
		return "", err
	}
	return evidenceBytesSHA256(data), nil
}

func evidenceBytesSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
