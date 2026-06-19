package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

const taskBundlesDir = "task-bundles"

type validatedTaskBundle struct {
	events        []LedgerEvent
	artifactPaths []string
}

type taskBundleIngestOptions struct {
	AllowAfterCompleted bool
}

func ingestTaskBundle(runDir string, task TaskRecord, bundleDir string) error {
	return ingestTaskBundleWithOptions(runDir, task, bundleDir, taskBundleIngestOptions{})
}

func ingestTaskBundleWithOptions(runDir string, task TaskRecord, bundleDir string, options taskBundleIngestOptions) error {
	bundle, err := validateTaskBundle(bundleDir, task)
	if err != nil {
		return err
	}
	for _, relPath := range bundle.artifactPaths {
		if err := validateTaskBundleArtifactTarget(bundleDir, runDir, relPath); err != nil {
			return err
		}
	}
	var copied []string
	for _, relPath := range bundle.artifactPaths {
		didCopy, err := copyTaskBundleArtifact(bundleDir, runDir, relPath)
		if err != nil {
			removeCopiedTaskBundleArtifacts(runDir, copied)
			return err
		}
		if didCopy {
			copied = append(copied, relPath)
		}
	}
	if err := validateTaskBundleReports(runDir, task, bundle.events); err != nil {
		removeCopiedTaskBundleArtifacts(runDir, copied)
		return err
	}
	return appendTaskBundleEvents(runDir, task, bundleDir, bundle.artifactPaths, bundle.events, options)
}

func validateTaskBundle(bundleDir string, task TaskRecord) (validatedTaskBundle, error) {
	if task.RunID == "" || task.TaskID == "" || task.Phase == "" {
		return validatedTaskBundle{}, errors.New("scheduled task must include run_id, task_id, and phase")
	}
	events, err := readTaskBundleEvents(filepath.Join(bundleDir, eventsFile))
	if err != nil {
		return validatedTaskBundle{}, err
	}
	if len(events) == 0 {
		return validatedTaskBundle{}, fmt.Errorf("task bundle %s has no ledger events", bundleDir)
	}

	artifactSet := map[string]struct{}{}
	terminalEvents := 0
	for index, event := range events {
		if event.RunID != task.RunID {
			return validatedTaskBundle{}, fmt.Errorf("task bundle event %d run_id = %q, want %q", index+1, event.RunID, task.RunID)
		}
		if !taskBundleEventAllowed(event.Type) {
			return validatedTaskBundle{}, fmt.Errorf("task bundle event %d type %q is not task-scoped", index+1, event.Type)
		}
		if event.TaskID != task.TaskID {
			return validatedTaskBundle{}, fmt.Errorf("task bundle event %d task_id = %q, want %q", index+1, event.TaskID, task.TaskID)
		}
		if err := validateTaskBundlePhaseEvent(task, event); err != nil {
			return validatedTaskBundle{}, fmt.Errorf("task bundle event %d: %w", index+1, err)
		}
		if event.Object == "task" && event.ObjectID != task.TaskID {
			return validatedTaskBundle{}, fmt.Errorf("task bundle event %d task object_id = %q, want %q", index+1, event.ObjectID, task.TaskID)
		}
		if event.Type == "task.completed" {
			if event.ObjectID != task.TaskID {
				return validatedTaskBundle{}, fmt.Errorf("task bundle completion object_id = %q, want %q", event.ObjectID, task.TaskID)
			}
			if index != len(events)-1 {
				return validatedTaskBundle{}, fmt.Errorf("task bundle completion for %s must be the final event", task.TaskID)
			}
			terminalEvents++
		}
		for _, relPath := range taskBundleArtifactPaths(event) {
			if err := validateTaskBundleArtifact(bundleDir, relPath); err != nil {
				return validatedTaskBundle{}, fmt.Errorf("task bundle event %d artifact %s: %w", index+1, relPath, err)
			}
			if event.Type == "evidence.added" {
				if err := validateTaskBundleEvidenceDigest(bundleDir, event); err != nil {
					return validatedTaskBundle{}, fmt.Errorf("task bundle event %d artifact %s: %w", index+1, relPath, err)
				}
			}
			artifactSet[relPath] = struct{}{}
		}
	}
	if terminalEvents == 0 {
		return validatedTaskBundle{}, fmt.Errorf("task bundle for %s is missing terminal task.completed event", task.TaskID)
	}
	if terminalEvents > 1 {
		return validatedTaskBundle{}, fmt.Errorf("task bundle for %s has %d terminal task.completed events", task.TaskID, terminalEvents)
	}

	artifactPaths := make([]string, 0, len(artifactSet))
	for relPath := range artifactSet {
		artifactPaths = append(artifactPaths, relPath)
	}
	sort.Strings(artifactPaths)
	return validatedTaskBundle{events: events, artifactPaths: artifactPaths}, nil
}

func validateTaskBundlePhaseEvent(task TaskRecord, event LedgerEvent) error {
	switch event.Type {
	case "task.started", "task.retrying":
		if phase := stringData(event.Data, "phase"); phase != task.Phase {
			return fmt.Errorf("phase = %q, want %q", phase, task.Phase)
		}
	case "lead.created":
		if !oneOf(task.Phase, "recon", "investigate") {
			return fmt.Errorf("phase %q cannot create leads", task.Phase)
		}
	case "lead.closed":
		if task.Phase != "investigate" {
			return fmt.Errorf("phase %q cannot close leads", task.Phase)
		}
	case "finding.created":
		if task.Phase != "investigate" {
			return fmt.Errorf("phase %q cannot create findings", task.Phase)
		}
	case "evidence.added":
		leadID := stringData(event.Data, "lead_id")
		findingID := stringData(event.Data, "finding_id")
		switch {
		case leadID != "" && findingID != "":
			return errors.New("evidence cannot attach to both lead and finding")
		case leadID != "":
			if task.Phase != "investigate" {
				return fmt.Errorf("phase %q cannot attach lead evidence", task.Phase)
			}
		case findingID != "":
			if !oneOf(task.Phase, "investigate", "review", "validate") {
				return fmt.Errorf("phase %q cannot attach finding evidence", task.Phase)
			}
		default:
			if !oneOf(task.Phase, "recon", "deduplicate") {
				return fmt.Errorf("phase %q cannot register unowned evidence", task.Phase)
			}
		}
	case "verdict.recorded":
		phase := stringData(event.Data, "phase")
		if phase != task.Phase {
			return fmt.Errorf("verdict phase = %q, want %q", phase, task.Phase)
		}
	case "report.finalized":
		if task.Phase != "finalize" {
			return fmt.Errorf("phase %q cannot finalize reports", task.Phase)
		}
	}
	return nil
}

func taskBundleEventAllowed(eventType string) bool {
	switch eventType {
	case "task.started",
		"task.completed",
		"task.retrying",
		"evidence.added",
		"lead.created",
		"lead.closed",
		"finding.created",
		"verdict.recorded",
		"report.finalized",
		"runner.failed":
		return true
	default:
		return false
	}
}

func readTaskBundleEvents(path string) ([]LedgerEvent, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read task bundle events: %w", err)
	}
	defer file.Close()

	var events []LedgerEvent
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event LedgerEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("parse task bundle event on line %d: %w", len(events)+1, err)
		}
		if err := validateLedgerEvent(event); err != nil {
			return nil, fmt.Errorf("invalid task bundle event on line %d: %w", len(events)+1, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func taskBundleArtifactPaths(event LedgerEvent) []string {
	switch event.Type {
	case "evidence.added":
		return []string{stringData(event.Data, "path")}
	case "lead.created":
		return []string{stringData(event.Data, "body_path")}
	case "finding.created":
		return []string{stringData(event.Data, "body_path")}
	case "report.finalized":
		return []string{
			stringData(event.Data, "markdown_path"),
			stringData(event.Data, "json_path"),
		}
	case "runner.failed":
		return []string{stringData(event.Data, "path")}
	default:
		return nil
	}
}

func validateTaskBundleArtifact(bundleDir, relPath string) error {
	sourcePath, _, err := taskBundleArtifactSource(bundleDir, relPath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return errors.New("artifact file must not be empty or whitespace-only")
	}
	return nil
}

func appendTaskBundleEvents(runDir string, task TaskRecord, bundleDir string, artifactPaths []string, events []LedgerEvent, options taskBundleIngestOptions) error {
	if len(events) == 0 {
		return nil
	}
	if err := os.MkdirAll(runDir, dirPerm); err != nil {
		return err
	}
	unlock, err := lockRunDir(runDir)
	if err != nil {
		return err
	}
	defer unlock()
	existing, err := readLedgerEventsUnlocked(runDir)
	if err != nil {
		return err
	}
	for _, relPath := range artifactPaths {
		if err := requireIngestedTaskBundleArtifact(bundleDir, runDir, relPath); err != nil {
			return err
		}
	}
	toAppend, err := taskBundleEventsToAppend(existing, task, events, options)
	if err != nil {
		return err
	}
	if len(toAppend) == 0 {
		return nil
	}
	prepared, err := prepareLedgerEvents(runDir, toAppend)
	if err != nil {
		return err
	}
	return appendLedgerEventsUnlocked(runDir, prepared)
}

func requireIngestedTaskBundleArtifact(bundleDir, runDir, relPath string) error {
	sourcePath, _, err := taskBundleArtifactSource(bundleDir, relPath)
	if err != nil {
		return err
	}
	targetPath, err := taskBundleArtifactTargetPath(runDir, relPath)
	if err != nil {
		return err
	}
	exists, err := existingTaskBundleArtifactMatches(sourcePath, targetPath, relPath)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("target artifact %s was not ingested", relPath)
	}
	return nil
}

func taskBundleEventsToAppend(existing []LedgerEvent, task TaskRecord, events []LedgerEvent, options taskBundleIngestOptions) ([]LedgerEvent, error) {
	if len(events) == 0 {
		return nil, nil
	}
	existingByID := map[string]LedgerEvent{}
	taskCompleted := false
	for _, event := range existing {
		existingByID[event.ID] = event
		if event.Type == "task.completed" && event.Object == "task" && event.ObjectID == task.TaskID {
			taskCompleted = true
		}
	}
	var toAppend []LedgerEvent
	for _, event := range events {
		if existing, ok := existingByID[event.ID]; ok {
			if !reflect.DeepEqual(existing, event) {
				return nil, fmt.Errorf("task bundle event %s already exists with different contents", event.ID)
			}
			continue
		}
		toAppend = append(toAppend, event)
	}
	if taskCompleted && len(toAppend) > 0 {
		if options.AllowAfterCompleted {
			return toAppend, nil
		}
		return nil, fmt.Errorf("task %s already completed; refusing non-idempotent bundle ingest", task.TaskID)
	}
	return toAppend, nil
}

func validateTaskBundleReports(runDir string, task TaskRecord, events []LedgerEvent) error {
	for _, event := range events {
		if event.Type != "report.finalized" {
			continue
		}
		markdownRel := stringData(event.Data, "markdown_path")
		jsonRel := stringData(event.Data, "json_path")
		if err := validateReportArtifacts(runDir, task, markdownRel, jsonRel); err != nil {
			return fmt.Errorf("validate task bundle report %s: %w", event.ObjectID, err)
		}
	}
	return nil
}

func removeCopiedTaskBundleArtifacts(runDir string, relPaths []string) {
	for _, relPath := range relPaths {
		_ = os.Remove(filepath.Join(runDir, filepath.FromSlash(relPath)))
	}
}

func validateTaskBundleEvidenceDigest(bundleDir string, event LedgerEvent) error {
	relPath := stringData(event.Data, "path")
	want, err := requireEventNonEmptyStringValue(event, "content_sha256")
	if err != nil {
		return err
	}
	sourcePath, _, err := taskBundleArtifactSource(bundleDir, relPath)
	if err != nil {
		return err
	}
	got, err := fileDigestHex(sourcePath)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("content_sha256 = %q, want %q", want, got)
	}
	return nil
}

func validateTaskBundleArtifactTarget(bundleDir, runDir, relPath string) error {
	sourcePath, _, err := taskBundleArtifactSource(bundleDir, relPath)
	if err != nil {
		return err
	}
	targetPath, err := taskBundleArtifactTargetPath(runDir, relPath)
	if err != nil {
		return err
	}
	_, err = existingTaskBundleArtifactMatches(sourcePath, targetPath, relPath)
	return err
}

func copyTaskBundleArtifact(bundleDir, runDir, relPath string) (bool, error) {
	sourcePath, mode, err := taskBundleArtifactSource(bundleDir, relPath)
	if err != nil {
		return false, err
	}
	targetPath, err := taskBundleArtifactTargetPath(runDir, relPath)
	if err != nil {
		return false, err
	}
	exists, err := existingTaskBundleArtifactMatches(sourcePath, targetPath, relPath)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	if err := copyTaskBundleFileExclusive(sourcePath, targetPath, mode); err != nil {
		if errors.Is(err, os.ErrExist) {
			exists, matchErr := existingTaskBundleArtifactMatches(sourcePath, targetPath, relPath)
			if matchErr != nil {
				return false, matchErr
			}
			if exists {
				return false, nil
			}
		}
		return false, err
	}
	return true, nil
}

func existingTaskBundleArtifactMatches(sourcePath, targetPath, relPath string) (bool, error) {
	if info, err := os.Lstat(targetPath); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("target artifact %s is a symlink", relPath)
		}
		if !info.Mode().IsRegular() {
			return false, fmt.Errorf("target artifact %s is not a regular file", relPath)
		}
		same, err := sameFileDigest(sourcePath, targetPath)
		if err != nil {
			return false, err
		}
		if !same {
			return false, fmt.Errorf("target artifact %s already exists with different contents", relPath)
		}
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return false, nil
}

func taskBundleArtifactSource(bundleDir, relPath string) (string, os.FileMode, error) {
	if err := validateTaskBundleRelPath(relPath); err != nil {
		return "", 0, err
	}
	absBundleDir, err := filepath.Abs(bundleDir)
	if err != nil {
		return "", 0, err
	}
	realBundleDir, err := filepath.EvalSymlinks(absBundleDir)
	if err != nil {
		return "", 0, fmt.Errorf("resolve task bundle directory: %w", err)
	}
	candidate := filepath.Join(realBundleDir, filepath.FromSlash(relPath))
	info, err := os.Lstat(candidate)
	if err != nil {
		return "", 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", 0, errors.New("artifact file must not be a symlink")
	}
	realCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", 0, err
	}
	relToBundle, err := filepath.Rel(realBundleDir, realCandidate)
	if err != nil {
		return "", 0, err
	}
	if relToBundle == "." || strings.HasPrefix(relToBundle, ".."+string(filepath.Separator)) || relToBundle == ".." || filepath.IsAbs(relToBundle) {
		return "", 0, errors.New("artifact file must stay inside task bundle")
	}
	info, err = os.Stat(realCandidate)
	if err != nil {
		return "", 0, err
	}
	if !info.Mode().IsRegular() {
		return "", 0, errors.New("artifact file must be a regular file")
	}
	return realCandidate, info.Mode(), nil
}

func taskBundleArtifactTargetPath(runDir, relPath string) (string, error) {
	if err := validateTaskBundleRelPath(relPath); err != nil {
		return "", err
	}
	targetPath := filepath.Join(runDir, filepath.FromSlash(relPath))
	current := runDir
	parts := strings.Split(relPath, "/")
	for _, part := range parts[:len(parts)-1] {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("target artifact parent %s is a symlink", part)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("target artifact parent %s is not a directory", part)
		}
	}
	return targetPath, nil
}

func copyTaskBundleFileExclusive(src, dst string, mode os.FileMode) error {
	input, err := os.Open(src)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(dst), dirPerm); err != nil {
		return err
	}
	output, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	copyErr := error(nil)
	if _, err := io.Copy(output, input); err != nil {
		copyErr = err
	}
	closeErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(dst)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return closeErr
	}
	return nil
}

func validateTaskBundleRelPath(relPath string) error {
	relPath = strings.TrimSpace(relPath)
	if relPath == "" {
		return errors.New("artifact path is required")
	}
	if strings.Contains(relPath, "\\") {
		return fmt.Errorf("artifact path %q must use forward slashes", relPath)
	}
	if path.IsAbs(relPath) {
		return fmt.Errorf("artifact path %q must be relative", relPath)
	}
	clean := path.Clean(relPath)
	if clean == "." || clean != relPath || strings.HasPrefix(clean, "../") || clean == ".." {
		return fmt.Errorf("artifact path %q must be clean and stay inside the run directory", relPath)
	}
	return nil
}

func sameFileDigest(leftPath, rightPath string) (bool, error) {
	left, err := fileDigest(leftPath)
	if err != nil {
		return false, err
	}
	right, err := fileDigest(rightPath)
	if err != nil {
		return false, err
	}
	return left == right, nil
}

func fileDigestHex(path string) (string, error) {
	digest, err := fileDigest(path)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(digest[:]), nil
}

func fileDigest(path string) ([sha256.Size]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return [sha256.Size]byte{}, err
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}
