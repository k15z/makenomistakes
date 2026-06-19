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

func TestReadLedgerEventsRejectsMissingRequiredEventData(t *testing.T) {
	runDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runDir, eventsFile), []byte(`{"id":"event_bad","run_id":"run_test","type":"lead.created","object":"lead","object_id":"lead_bad","timestamp":"2026-01-01T00:00:00Z","data":{"title":"Lead without body","category":"security","priority":"high"}}
`), filePerm); err != nil {
		t.Fatal(err)
	}

	_, err := readLedgerEvents(runDir)
	if err == nil {
		t.Fatal("expected missing event data error")
	}
	if !strings.Contains(err.Error(), "lead.created data.body_path is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAppendLedgerEventRejectsInvalidEventData(t *testing.T) {
	runDir := t.TempDir()
	err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "task.completed",
		Object:   "task",
		ObjectID: "task_recon",
		Data: map[string]any{
			"status": "done",
		},
	})
	if err == nil {
		t.Fatal("expected invalid event data error")
	}
	if !strings.Contains(err.Error(), `task.completed data.status = "done"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadLedgerEventsRejectsAmbiguousEvidenceOwner(t *testing.T) {
	runDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runDir, eventsFile), []byte(`{"id":"event_bad","run_id":"run_test","type":"evidence.added","object":"evidence","object_id":"evidence_bad","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Ambiguous proof","path":"evidence/proof.md","lead_id":"lead_one","finding_id":"finding_one"}}
`), filePerm); err != nil {
		t.Fatal(err)
	}

	_, err := readLedgerEvents(runDir)
	if err == nil {
		t.Fatal("expected ambiguous evidence owner error")
	}
	if !strings.Contains(err.Error(), "data.lead_id and data.finding_id are mutually exclusive") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadLedgerEventsRejectsInvalidVerdictData(t *testing.T) {
	runDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runDir, eventsFile), []byte(`{"id":"event_bad","run_id":"run_test","type":"verdict.recorded","object":"verdict","object_id":"verdict_bad","timestamp":"2026-01-01T00:00:00Z","data":{"finding_id":"finding_one","phase":"deduplicate","value":"duplicate","reason":"self duplicate","canonical_finding_id":"finding_one"}}
`), filePerm); err != nil {
		t.Fatal(err)
	}

	_, err := readLedgerEvents(runDir)
	if err == nil {
		t.Fatal("expected invalid verdict data error")
	}
	if !strings.Contains(err.Error(), "data.canonical_finding_id must differ from data.finding_id") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadLedgerEventsRejectsEmptyVerdictFindingID(t *testing.T) {
	runDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runDir, eventsFile), []byte(`{"id":"event_bad","run_id":"run_test","type":"verdict.recorded","object":"verdict","object_id":"verdict_bad","timestamp":"2026-01-01T00:00:00Z","data":{"finding_id":"","phase":"review","value":"accepted","reason":"accepted"}}
`), filePerm); err != nil {
		t.Fatal(err)
	}

	_, err := readLedgerEvents(runDir)
	if err == nil {
		t.Fatal("expected empty finding id error")
	}
	if !strings.Contains(err.Error(), "verdict.recorded data.finding_id must not be empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadLedgerEventsRejectsEmptyDuplicateCanonicalFindingID(t *testing.T) {
	runDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(runDir, eventsFile), []byte(`{"id":"event_bad","run_id":"run_test","type":"verdict.recorded","object":"verdict","object_id":"verdict_bad","timestamp":"2026-01-01T00:00:00Z","data":{"finding_id":"finding_one","phase":"deduplicate","value":"duplicate","reason":"duplicate","canonical_finding_id":""}}
`), filePerm); err != nil {
		t.Fatal(err)
	}

	_, err := readLedgerEvents(runDir)
	if err == nil {
		t.Fatal("expected empty canonical finding id error")
	}
	if !strings.Contains(err.Error(), "verdict.recorded data.canonical_finding_id must not be empty") {
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
