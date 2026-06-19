package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadLedgerEventsRejectsMalformedEventEnvelope(t *testing.T) {
	runDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runDir, eventsFile), []byte(`
{"id":"event_bad","run_id":"run_test","type":"lead.created","object":"lead","object_id":"lead_bad","timestamp":"not-a-time","data":{}}
`), filePerm); err != nil {
		t.Fatal(err)
	}

	_, err := readLedgerEvents(runDir)
	if err == nil {
		t.Fatal("expected malformed event error")
	}
	if !strings.Contains(err.Error(), "invalid ledger event on line 2") || !strings.Contains(err.Error(), "must be RFC3339") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadLedgerEventsRejectsUnknownEventType(t *testing.T) {
	runDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runDir, eventsFile), []byte(`{"id":"event_bad","run_id":"run_test","type":"lead.promoted","object":"lead","object_id":"lead_bad","timestamp":"2026-01-01T00:00:00Z","data":{}}
`), filePerm); err != nil {
		t.Fatal(err)
	}

	_, err := readLedgerEvents(runDir)
	if err == nil {
		t.Fatal("expected unknown event type error")
	}
	if !strings.Contains(err.Error(), `unknown event type "lead.promoted"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadLedgerEventsRejectsWrongObjectForType(t *testing.T) {
	runDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runDir, eventsFile), []byte(`{"id":"event_bad","run_id":"run_test","type":"finding.created","object":"lead","object_id":"finding_bad","timestamp":"2026-01-01T00:00:00Z","data":{}}
`), filePerm); err != nil {
		t.Fatal(err)
	}

	_, err := readLedgerEvents(runDir)
	if err == nil {
		t.Fatal("expected wrong event object error")
	}
	if !strings.Contains(err.Error(), `event type "finding.created" must use object "finding"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAppendLedgerEventRejectsUnknownEventType(t *testing.T) {
	runDir := t.TempDir()
	err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "finding.promoted",
		Object:   "finding",
		ObjectID: "finding_bad",
	})
	if err == nil {
		t.Fatal("expected append event validation error")
	}
	if !strings.Contains(err.Error(), `unknown event type "finding.promoted"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadLedgerEventsAcceptsKnownEvent(t *testing.T) {
	runDir := t.TempDir()
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "task.started",
		Object:   "task",
		ObjectID: "task_recon",
		Data: map[string]any{
			"phase": "recon",
		},
	}); err != nil {
		t.Fatal(err)
	}

	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "task.started" {
		t.Fatalf("unexpected events: %#v", events)
	}
}
