package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	currentTaskFile = "current-task.json"
	eventsFile      = "events.jsonl"
	taskFileEnv     = "MNM_TASK_FILE"
	ledgerDirEnv    = "MNM_LEDGER_DIR"
)

type LedgerEvent struct {
	ID        string         `json:"id"`
	RunID     string         `json:"run_id"`
	Type      string         `json:"type"`
	Object    string         `json:"object"`
	ObjectID  string         `json:"object_id"`
	TaskID    string         `json:"task_id,omitempty"`
	Timestamp string         `json:"timestamp"`
	Data      map[string]any `json:"data"`
}

type TaskRecord struct {
	RunID       string `json:"run_id"`
	TaskID      string `json:"task_id"`
	Phase       string `json:"phase"`
	Title       string `json:"title"`
	Instruction string `json:"instruction"`
}

func resolveRunDir(explicit string) (string, error) {
	runDir := explicit
	if runDir == "" {
		runDir = os.Getenv("MNM_RUN_DIR")
	}
	if runDir == "" {
		return "", errors.New("run directory is required; pass --run-dir or set MNM_RUN_DIR")
	}
	return filepath.Abs(runDir)
}

func readCurrentTask(runDir string) (TaskRecord, error) {
	var task TaskRecord
	path, err := currentTaskPath(runDir)
	if err != nil {
		return task, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return task, fmt.Errorf("read current task: %w", err)
	}
	if err := json.Unmarshal(b, &task); err != nil {
		return task, fmt.Errorf("parse current task: %w", err)
	}
	if task.RunID == "" || task.TaskID == "" || task.Phase == "" {
		return task, errors.New("current task must include run_id, task_id, and phase")
	}
	return task, nil
}

func writeTaskFile(path string, task TaskRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), dirPerm); err != nil {
		return err
	}
	b, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, filePerm)
}

func currentTaskPath(runDir string) (string, error) {
	if override := os.Getenv(taskFileEnv); override != "" {
		absRunDir, err := filepath.Abs(runDir)
		if err != nil {
			return "", err
		}
		absOverride, err := filepath.Abs(override)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(absRunDir, absOverride)
		if err != nil {
			return "", err
		}
		if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
			return "", fmt.Errorf("%s must point inside run directory: %s", taskFileEnv, override)
		}
		return absOverride, nil
	}
	return filepath.Join(runDir, currentTaskFile), nil
}

func appendLedgerEvent(runDir string, event LedgerEvent) error {
	return appendLedgerEvents(runDir, []LedgerEvent{event})
}

func appendLedgerEvents(runDir string, events []LedgerEvent) error {
	if len(events) == 0 {
		return nil
	}
	prepared, err := prepareLedgerEvents(runDir, events)
	if err != nil {
		return err
	}
	unlock, err := lockRunDir(runDir)
	if err != nil {
		return err
	}
	defer unlock()
	return appendLedgerEventsUnlocked(runDir, prepared)
}

func prepareLedgerEvents(runDir string, events []LedgerEvent) ([]LedgerEvent, error) {
	prepared := make([]LedgerEvent, 0, len(events))
	for _, event := range events {
		preparedEvent, err := prepareLedgerEvent(runDir, event)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, preparedEvent)
	}
	return prepared, nil
}

func prepareLedgerEvent(runDir string, event LedgerEvent) (LedgerEvent, error) {
	if event.RunID == "" {
		return LedgerEvent{}, errors.New("event run_id is required")
	}
	if event.Type == "" || event.Object == "" || event.ObjectID == "" {
		return LedgerEvent{}, errors.New("event type, object, and object_id are required")
	}
	if event.ID == "" {
		event.ID = "event_" + uuid.NewString()
	}
	if event.Timestamp == "" {
		event.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if event.Data == nil {
		event.Data = map[string]any{}
	}
	if err := validateLedgerEvent(event); err != nil {
		return LedgerEvent{}, err
	}
	if err := os.MkdirAll(runDir, dirPerm); err != nil {
		return LedgerEvent{}, err
	}
	return event, nil
}

func appendLedgerEventUnlocked(runDir string, event LedgerEvent) error {
	return appendLedgerEventsUnlocked(runDir, []LedgerEvent{event})
}

func appendLedgerEventsUnlocked(runDir string, events []LedgerEvent) error {
	var payload []byte
	for _, event := range events {
		line, err := json.Marshal(event)
		if err != nil {
			return err
		}
		payload = append(payload, line...)
		payload = append(payload, '\n')
	}

	path := filepath.Join(runDir, eventsFile)
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	tmpPath := filepath.Join(runDir, eventsFile+".tmp."+uuid.NewString())
	file, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, filePerm)
	if err != nil {
		return err
	}
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if len(existing) > 0 {
		if _, err := file.Write(existing); err != nil {
			_ = file.Close()
			return err
		}
		if existing[len(existing)-1] != '\n' {
			if _, err := file.Write([]byte{'\n'}); err != nil {
				_ = file.Close()
				return err
			}
		}
	}
	if _, err := file.Write(payload); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	removeTemp = false
	return nil
}

func readLedgerEvents(runDir string) ([]LedgerEvent, error) {
	outputDir, snapshotDir, sameDir, err := ledgerOverlayDirs(runDir)
	if err != nil {
		return nil, err
	}
	if snapshotDir == "" || sameDir {
		return readLedgerEventsFile(outputDir)
	}
	baseEvents, err := readLedgerSnapshotEvents(snapshotDir)
	if err != nil {
		return nil, fmt.Errorf("read ledger snapshot %s: %w", snapshotDir, err)
	}
	outputEvents, err := readLedgerEventsFile(outputDir)
	if err != nil {
		return nil, err
	}
	return append(baseEvents, outputEvents...), nil
}

func readLedgerEventsOverlayUnlocked(runDir string) ([]LedgerEvent, error) {
	outputDir, snapshotDir, sameDir, err := ledgerOverlayDirs(runDir)
	if err != nil {
		return nil, err
	}
	if snapshotDir == "" || sameDir {
		return readLedgerEventsUnlocked(outputDir)
	}
	baseEvents, err := readLedgerSnapshotEvents(snapshotDir)
	if err != nil {
		return nil, fmt.Errorf("read ledger snapshot %s: %w", snapshotDir, err)
	}
	outputEvents, err := readLedgerEventsUnlocked(outputDir)
	if err != nil {
		return nil, err
	}
	return append(baseEvents, outputEvents...), nil
}

func ledgerOverlayDirs(runDir string) (outputDir, snapshotDir string, sameDir bool, err error) {
	absRunDir, err := filepath.Abs(runDir)
	if err != nil {
		return "", "", false, err
	}
	ledgerDir := strings.TrimSpace(os.Getenv(ledgerDirEnv))
	if ledgerDir == "" {
		return absRunDir, "", true, nil
	}
	if err := os.MkdirAll(absRunDir, dirPerm); err != nil {
		return "", "", false, err
	}
	absLedgerDir, err := filepath.Abs(ledgerDir)
	if err != nil {
		return "", "", false, err
	}
	sameDir, err = sameDirectory(absRunDir, absLedgerDir)
	if err != nil {
		return "", "", false, err
	}
	return absRunDir, absLedgerDir, sameDir, nil
}

func sameDirectory(left, right string) (bool, error) {
	realLeft, err := filepath.EvalSymlinks(left)
	if err != nil {
		return false, fmt.Errorf("resolve ledger output directory %s: %w", left, err)
	}
	realRight, err := filepath.EvalSymlinks(right)
	if err != nil {
		return false, fmt.Errorf("resolve ledger snapshot directory %s: %w", right, err)
	}
	return filepath.Clean(realLeft) == filepath.Clean(realRight), nil
}

func readLedgerSnapshotEvents(runDir string) ([]LedgerEvent, error) {
	info, err := os.Stat(runDir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("ledger snapshot path is not a directory: %s", runDir)
	}
	return readLedgerEventsUnlocked(runDir)
}

func readLedgerEventsFile(runDir string) ([]LedgerEvent, error) {
	if err := os.MkdirAll(runDir, dirPerm); err != nil {
		return nil, err
	}
	unlock, err := lockRunDir(runDir)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return readLedgerEventsUnlocked(runDir)
}

func readLedgerEventsUnlocked(runDir string) ([]LedgerEvent, error) {
	path := filepath.Join(runDir, eventsFile)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var events []LedgerEvent
	scanner := bufio.NewScanner(file)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event LedgerEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, err
		}
		if err := validateLedgerEvent(event); err != nil {
			return nil, fmt.Errorf("invalid ledger event on line %d: %w", lineNo, err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func validateLedgerEvent(event LedgerEvent) error {
	if event.ID == "" {
		return errors.New("event id is required")
	}
	if event.RunID == "" {
		return errors.New("event run_id is required")
	}
	if event.Type == "" || event.Object == "" || event.ObjectID == "" {
		return errors.New("event type, object, and object_id are required")
	}
	if event.Timestamp == "" {
		return errors.New("event timestamp is required")
	}
	if _, err := time.Parse(time.RFC3339Nano, event.Timestamp); err != nil {
		return fmt.Errorf("event timestamp %q must be RFC3339: %w", event.Timestamp, err)
	}
	if event.Data == nil {
		return errors.New("event data object is required")
	}
	expectedObject, ok := ledgerEventObject(event.Type)
	if !ok {
		return fmt.Errorf("unknown event type %q", event.Type)
	}
	if event.Object != expectedObject {
		return fmt.Errorf("event type %q must use object %q, got %q", event.Type, expectedObject, event.Object)
	}
	if err := validateLedgerEventData(event); err != nil {
		return err
	}
	return nil
}

func validateLedgerEventData(event LedgerEvent) error {
	switch event.Type {
	case "runner.started", "runner.completed":
		return requireEventString(event, "workspace")
	case "runner.stopped":
		return requireEventStrings(event, "phase", "workspace")
	case "runner.failed":
		return requireEventStrings(event, "stage", "error", "path")
	case "task.started":
		return requireEventString(event, "phase")
	case "task.completed":
		return requireEventOneOf(event, "status", "completed", "failed")
	case "task.retrying":
		if err := requireEventStrings(event, "phase", "reason"); err != nil {
			return err
		}
		if err := requireEventNumber(event, "attempt"); err != nil {
			return err
		}
		return requireEventNumber(event, "max_attempts")
	case "evidence.added":
		if err := requireEventStrings(event, "kind", "title", "path"); err != nil {
			return err
		}
		leadID, err := optionalEventStringValue(event, "lead_id")
		if err != nil {
			return err
		}
		findingID, err := optionalEventStringValue(event, "finding_id")
		if err != nil {
			return err
		}
		if leadID != "" && findingID != "" {
			return fmt.Errorf("%s data.lead_id and data.finding_id are mutually exclusive", event.Type)
		}
		return nil
	case "lead.created":
		if err := requireEventStrings(event, "title", "category", "body_path"); err != nil {
			return err
		}
		return requireEventOneOf(event, "priority", "high", "medium", "low")
	case "lead.closed":
		if err := requireEventOneOf(event, "status", "closed_no_finding", "promoted_to_finding", "superseded"); err != nil {
			return err
		}
		_, err := requireEventStringValue(event, "reason")
		return err
	case "finding.created":
		if err := requireEventStrings(event, "title", "category", "body_path"); err != nil {
			return err
		}
		if err := requireEventOneOf(event, "severity", "critical", "high", "medium", "low", "info"); err != nil {
			return err
		}
		return requireEventOneOf(event, "confidence", "high", "medium", "low")
	case "verdict.recorded":
		findingID, err := requireEventNonEmptyStringValue(event, "finding_id")
		if err != nil {
			return err
		}
		phase, err := requireEventStringValue(event, "phase")
		if err != nil {
			return err
		}
		value, err := requireEventStringValue(event, "value")
		if err != nil {
			return err
		}
		if !validVerdictValue(phase, value) {
			return fmt.Errorf("%s data.value %q is invalid for phase %q", event.Type, value, phase)
		}
		if _, err := requireEventStringValue(event, "reason"); err != nil {
			return err
		}
		if phase == "deduplicate" && value == "duplicate" {
			canonical, err := requireEventNonEmptyStringValue(event, "canonical_finding_id")
			if err != nil {
				return err
			}
			if canonical == findingID {
				return fmt.Errorf("%s data.canonical_finding_id must differ from data.finding_id", event.Type)
			}
		}
		return nil
	case "report.finalized":
		return requireEventStrings(event, "markdown_path", "json_path")
	case "investigate.limit_reached":
		if err := requireEventNumber(event, "limit"); err != nil {
			return err
		}
		if err := requireEventNumber(event, "processed"); err != nil {
			return err
		}
		return requireEventNumber(event, "open_leads")
	default:
		return nil
	}
}

func requireEventStrings(event LedgerEvent, keys ...string) error {
	for _, key := range keys {
		if err := requireEventString(event, key); err != nil {
			return err
		}
	}
	return nil
}

func requireEventString(event LedgerEvent, key string) error {
	_, err := requireEventNonEmptyStringValue(event, key)
	return err
}

func requireEventNonEmptyStringValue(event LedgerEvent, key string) (string, error) {
	value, err := requireEventStringValue(event, key)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", fmt.Errorf("%s data.%s must not be empty", event.Type, key)
	}
	return value, nil
}

func requireEventStringValue(event LedgerEvent, key string) (string, error) {
	value, ok := event.Data[key]
	if !ok {
		return "", fmt.Errorf("%s data.%s is required", event.Type, key)
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s data.%s must be a string", event.Type, key)
	}
	return text, nil
}

func optionalEventStringValue(event LedgerEvent, key string) (string, error) {
	value, ok := event.Data[key]
	if !ok {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s data.%s must be a string", event.Type, key)
	}
	return text, nil
}

func requireEventOneOf(event LedgerEvent, key string, allowed ...string) error {
	value, err := requireEventStringValue(event, key)
	if err != nil {
		return err
	}
	if !oneOf(value, allowed...) {
		return fmt.Errorf("%s data.%s = %q, expected one of: %s", event.Type, key, value, strings.Join(allowed, ", "))
	}
	return nil
}

func requireEventNumber(event LedgerEvent, key string) error {
	value, ok := event.Data[key]
	if !ok {
		return fmt.Errorf("%s data.%s is required", event.Type, key)
	}
	switch value.(type) {
	case int, int32, int64, float32, float64:
		return nil
	default:
		return fmt.Errorf("%s data.%s must be a number", event.Type, key)
	}
}

func ledgerEventObject(eventType string) (string, bool) {
	switch eventType {
	case "runner.started", "runner.completed", "runner.stopped", "runner.failed":
		return "run", true
	case "task.started", "task.completed", "task.retrying":
		return "task", true
	case "evidence.added":
		return "evidence", true
	case "lead.created", "lead.closed":
		return "lead", true
	case "finding.created":
		return "finding", true
	case "verdict.recorded":
		return "verdict", true
	case "report.finalized":
		return "report", true
	case "investigate.limit_reached":
		return "phase", true
	default:
		return "", false
	}
}

func ledgerObjectExists(runDir, object, objectID string) (bool, error) {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return false, err
	}
	for _, event := range events {
		if event.Object == object && event.ObjectID == objectID {
			return true, nil
		}
	}
	return false, nil
}

func requireLedgerObject(runDir, object, objectID string) error {
	ok, err := ledgerObjectExists(runDir, object, objectID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s %s does not exist in ledger", object, objectID)
	}
	return nil
}

func requirePathInsideRunDir(runDir, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is required")
	}
	absRunDir, err := filepath.Abs(runDir)
	if err != nil {
		return "", err
	}
	realRunDir, err := filepath.EvalSymlinks(absRunDir)
	if err != nil {
		return "", fmt.Errorf("resolve run directory %s: %w", runDir, err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realRunDir, realPath)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path must be inside run directory: %s", path)
	}
	info, err := os.Stat(realPath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("path must be a regular file: %s", path)
	}
	return filepath.ToSlash(rel), nil
}

func requireNonEmptyRunFile(runDir, relPath, label string) error {
	path := filepath.Join(runDir, filepath.FromSlash(relPath))
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s %s: %w", label, relPath, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("%s %s must not be empty", label, relPath)
	}
	return nil
}

func lockRunDir(runDir string) (func(), error) {
	lockPath := filepath.Join(runDir, ".events.lock")
	deadline := time.Now().Add(10 * time.Second)
	for {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = file.Close()
			return func() { _ = os.Remove(lockPath) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out waiting for ledger lock: %s", lockPath)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func newLedgerID(prefix string) string {
	return prefix + "_" + uuid.NewString()
}

func oneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}
