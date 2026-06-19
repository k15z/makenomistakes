package main

import (
	"bufio"
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
	event, err := prepareLedgerEvent(runDir, event)
	if err != nil {
		return err
	}
	unlock, err := lockRunDir(runDir)
	if err != nil {
		return err
	}
	defer unlock()
	return appendLedgerEventUnlocked(runDir, event)
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
	path := filepath.Join(runDir, eventsFile)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, filePerm)
	if err != nil {
		return err
	}
	defer file.Close()

	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(line, '\n')); err != nil {
		return err
	}
	return nil
}

func readLedgerEvents(runDir string) ([]LedgerEvent, error) {
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
	return nil
}

func ledgerEventObject(eventType string) (string, bool) {
	switch eventType {
	case "runner.started", "runner.completed", "runner.failed":
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
	if path == "" {
		return "", errors.New("path is required")
	}
	absRunDir, err := filepath.Abs(runDir)
	if err != nil {
		return "", err
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRunDir, absPath)
	if err != nil {
		return "", err
	}
	if rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path must be inside run directory: %s", path)
	}
	if _, err := os.Stat(absPath); err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
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
