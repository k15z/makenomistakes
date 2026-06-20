package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLedgerOverlayReadsSnapshotAndWritesTaskOutput(t *testing.T) {
	ledgerDir := t.TempDir()
	outputDir := t.TempDir()
	task := TaskRecord{
		RunID:  "run_overlay",
		TaskID: "task_investigate_lead_auth",
		Phase:  "investigate",
	}
	if err := appendLedgerEvent(ledgerDir, LedgerEvent{
		ID:        "event_lead",
		RunID:     task.RunID,
		Type:      "lead.created",
		Object:    "lead",
		ObjectID:  "lead_auth",
		TaskID:    "task_recon",
		Timestamp: "2026-01-01T00:00:00Z",
		Data: map[string]any{
			"title":     "Investigate auth",
			"category":  "authz",
			"priority":  "high",
			"body_path": "evidence/lead-auth.md",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeTaskFile(filepath.Join(outputDir, currentTaskFile), task); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, outputDir, "evidence/finding-auth.md", "# Finding\n")
	writeWorkspaceFile(t, outputDir, "evidence/finding-auth-proof.log", "proof\n")

	t.Setenv("MNM_RUN_DIR", outputDir)
	t.Setenv(ledgerDirEnv, ledgerDir)

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"finding", "create",
		"--lead", "lead_auth",
		"--title", "Auth bypass",
		"--category", "authz",
		"--severity", "high",
		"--confidence", "medium",
		"--body-file", filepath.Join(outputDir, "evidence", "finding-auth.md"),
	}, &stdout, &stderr); err != nil {
		t.Fatalf("finding create failed: %v\nstderr: %s", err, stderr.String())
	}
	findingID := strings.TrimSpace(stdout.String())
	if findingID == "" {
		t.Fatal("finding create did not print finding id")
	}

	stdout.Reset()
	stderr.Reset()
	if err := run([]string{
		"evidence", "add",
		"--kind", "log",
		"--title", "Auth proof",
		"--finding", findingID,
		"--path", filepath.Join(outputDir, "evidence", "finding-auth-proof.log"),
	}, &stdout, &stderr); err != nil {
		t.Fatalf("evidence add failed: %v\nstderr: %s", err, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if err := run([]string{
		"task", "complete",
		"--status", "completed",
		"--summary", "Investigated auth",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("task complete failed: %v\nstderr: %s", err, stderr.String())
	}

	snapshotEvents, err := readLedgerEventsFile(ledgerDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshotEvents) != 1 {
		t.Fatalf("snapshot event count changed to %d, want 1", len(snapshotEvents))
	}
	outputEvents, err := readTaskBundleEvents(filepath.Join(outputDir, eventsFile))
	if err != nil {
		t.Fatal(err)
	}
	if got := eventTypes(outputEvents); strings.Join(got, ",") != "finding.created,evidence.added,task.completed" {
		t.Fatalf("output event types = %#v", got)
	}
	if !ledgerTaskCompleted(outputDir, task.TaskID) {
		t.Fatal("overlay ledger should see task completion from output")
	}
	if _, err := validateTaskBundle(outputDir, task); err != nil {
		t.Fatalf("output should be a valid task bundle: %v", err)
	}
}

func TestLedgerOverlayCanCloseSnapshotLead(t *testing.T) {
	ledgerDir := t.TempDir()
	outputDir := t.TempDir()
	task := TaskRecord{
		RunID:  "run_overlay",
		TaskID: "task_investigate_lead_auth",
		Phase:  "investigate",
	}
	if err := appendLedgerEvent(ledgerDir, LedgerEvent{
		ID:        "event_lead",
		RunID:     task.RunID,
		Type:      "lead.created",
		Object:    "lead",
		ObjectID:  "lead_auth",
		TaskID:    "task_recon",
		Timestamp: "2026-01-01T00:00:00Z",
		Data: map[string]any{
			"title":     "Investigate auth",
			"category":  "authz",
			"priority":  "high",
			"body_path": "evidence/lead-auth.md",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeTaskFile(filepath.Join(outputDir, currentTaskFile), task); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MNM_RUN_DIR", outputDir)
	t.Setenv(ledgerDirEnv, ledgerDir)

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"lead", "close",
		"--id", "lead_auth",
		"--status", "closed_no_finding",
		"--reason", "not reproducible",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("lead close failed: %v\nstderr: %s", err, stderr.String())
	}
	outputEvents, err := readTaskBundleEvents(filepath.Join(outputDir, eventsFile))
	if err != nil {
		t.Fatal(err)
	}
	if got := eventTypes(outputEvents); strings.Join(got, ",") != "lead.closed" {
		t.Fatalf("output event types = %#v", got)
	}
	status, exists, err := ledgerLeadStatus(outputDir, "lead_auth")
	if err != nil {
		t.Fatal(err)
	}
	if !exists || status != "closed_no_finding" {
		t.Fatalf("overlay lead status = %q exists=%v, want closed_no_finding", status, exists)
	}
}

func TestLedgerOverlaySeesTaskLocalObjects(t *testing.T) {
	ledgerDir := t.TempDir()
	outputDir := t.TempDir()
	task := TaskRecord{
		RunID:  "run_overlay",
		TaskID: "task_investigate_lead_auth",
		Phase:  "investigate",
	}
	if err := appendLedgerEvent(ledgerDir, LedgerEvent{
		ID:        "event_lead",
		RunID:     task.RunID,
		Type:      "lead.created",
		Object:    "lead",
		ObjectID:  "lead_auth",
		TaskID:    "task_recon",
		Timestamp: "2026-01-01T00:00:00Z",
		Data: map[string]any{
			"title":     "Investigate auth",
			"category":  "authz",
			"priority":  "high",
			"body_path": "evidence/lead-auth.md",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeTaskFile(filepath.Join(outputDir, currentTaskFile), task); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, outputDir, "evidence/finding-auth.md", "# Finding\n")

	t.Setenv("MNM_RUN_DIR", outputDir)
	t.Setenv(ledgerDirEnv, ledgerDir)

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"finding", "create",
		"--lead", "lead_auth",
		"--title", "Auth bypass",
		"--category", "authz",
		"--severity", "high",
		"--confidence", "medium",
		"--body-file", filepath.Join(outputDir, "evidence", "finding-auth.md"),
	}, &stdout, &stderr); err != nil {
		t.Fatalf("finding create failed: %v\nstderr: %s", err, stderr.String())
	}
	findingID := strings.TrimSpace(stdout.String())
	writeWorkspaceFile(t, outputDir, "evidence/finding-auth-proof.log", "proof\n")
	stdout.Reset()
	stderr.Reset()
	if err := run([]string{
		"evidence", "add",
		"--kind", "log",
		"--title", "Auth proof",
		"--finding", findingID,
		"--path", filepath.Join(outputDir, "evidence", "finding-auth-proof.log"),
	}, &stdout, &stderr); err != nil {
		t.Fatalf("evidence add failed: %v\nstderr: %s", err, stderr.String())
	}
	outputEvents, err := readTaskBundleEvents(filepath.Join(outputDir, eventsFile))
	if err != nil {
		t.Fatal(err)
	}
	if got := eventTypes(outputEvents); strings.Join(got, ",") != "finding.created,evidence.added" {
		t.Fatalf("output event types = %#v", got)
	}
}

func TestLedgerOverlayEvidenceRegistrationSeesSnapshot(t *testing.T) {
	ledgerDir := t.TempDir()
	outputDir := t.TempDir()
	task := TaskRecord{
		RunID:  "run_overlay",
		TaskID: "task_review_finding_auth",
		Phase:  "review",
	}
	notesRel := "evidence/review-finding_auth-notes.md"
	if err := writeTaskFile(filepath.Join(outputDir, currentTaskFile), task); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, outputDir, notesRel, "# Review notes\n")
	if err := appendLedgerEvent(ledgerDir, LedgerEvent{
		ID:       "event_finding",
		RunID:    task.RunID,
		Type:     "finding.created",
		Object:   "finding",
		ObjectID: "finding_auth",
		TaskID:   "task_investigate_lead_auth",
		Data: map[string]any{
			"title":      "Auth bypass",
			"category":   "authz",
			"severity":   "high",
			"confidence": "medium",
			"body_path":  "evidence/finding-auth.md",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(ledgerDir, LedgerEvent{
		ID:       "event_existing_review_evidence",
		RunID:    task.RunID,
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_existing_review",
		TaskID:   task.TaskID,
		Data: map[string]any{
			"kind":           "markdown",
			"title":          "Review notes",
			"path":           notesRel,
			"lead_id":        "",
			"finding_id":     "finding_auth",
			"content_sha256": runFileSHA256ForTest(t, outputDir, notesRel),
		},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MNM_RUN_DIR", outputDir)
	t.Setenv(ledgerDirEnv, ledgerDir)

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"evidence", "add",
		"--kind", "markdown",
		"--title", "Review notes",
		"--finding", "finding_auth",
		"--path", filepath.Join(outputDir, filepath.FromSlash(notesRel)),
	}, &stdout, &stderr); err != nil {
		t.Fatalf("evidence add failed: %v\nstderr: %s", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "evidence_existing_review" {
		t.Fatalf("evidence id = %q, want snapshot id", got)
	}
	outputEvents, err := readLedgerEventsFile(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputEvents) != 0 {
		t.Fatalf("output events = %#v, want none", outputEvents)
	}
}

func TestLedgerOverlayTaskCompleteIgnoresSnapshotCompletionForOutput(t *testing.T) {
	ledgerDir := t.TempDir()
	outputDir := t.TempDir()
	task := TaskRecord{
		RunID:  "run_overlay",
		TaskID: "task_recon",
		Phase:  "recon",
	}
	if err := writeTaskFile(filepath.Join(outputDir, currentTaskFile), task); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(ledgerDir, LedgerEvent{
		ID:        "event_stale_done",
		RunID:     task.RunID,
		Type:      "task.completed",
		Object:    "task",
		ObjectID:  task.TaskID,
		TaskID:    task.TaskID,
		Timestamp: "2026-01-01T00:00:00Z",
		Data: map[string]any{
			"status":  "completed",
			"summary": "stale completion from central ledger",
		},
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MNM_RUN_DIR", outputDir)
	t.Setenv(ledgerDirEnv, ledgerDir)

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"task", "complete",
		"--status", "completed",
		"--summary", "fresh completion from VM output",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("task complete failed: %v\nstderr: %s", err, stderr.String())
	}
	outputEvents, err := readLedgerEventsFile(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputEvents) != 1 {
		t.Fatalf("output events = %#v, want one task completion", outputEvents)
	}
	event := outputEvents[0]
	if event.Type != "task.completed" || event.ObjectID != task.TaskID {
		t.Fatalf("output event = %#v, want task completion for %s", event, task.TaskID)
	}
	if got := stringData(event.Data, "summary"); got != "fresh completion from VM output" {
		t.Fatalf("summary = %q", got)
	}
}

func TestLedgerOverlayVerdictRecordSeesSnapshot(t *testing.T) {
	ledgerDir := t.TempDir()
	outputDir := t.TempDir()
	task := TaskRecord{
		RunID:  "run_overlay",
		TaskID: "task_review_finding_auth",
		Phase:  "review",
	}
	notesRel := "evidence/review-finding_auth-notes.md"
	if err := writeTaskFile(filepath.Join(outputDir, currentTaskFile), task); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, outputDir, notesRel, "# Review notes\n")
	for _, event := range []LedgerEvent{
		{
			ID:       "event_finding",
			RunID:    task.RunID,
			Type:     "finding.created",
			Object:   "finding",
			ObjectID: "finding_auth",
			TaskID:   "task_investigate_lead_auth",
			Data: map[string]any{
				"title":      "Auth bypass",
				"category":   "authz",
				"severity":   "high",
				"confidence": "medium",
				"body_path":  "evidence/finding-auth.md",
			},
		},
		{
			ID:       "event_existing_review_evidence",
			RunID:    task.RunID,
			Type:     "evidence.added",
			Object:   "evidence",
			ObjectID: "evidence_existing_review",
			TaskID:   task.TaskID,
			Data: map[string]any{
				"kind":           "markdown",
				"title":          "Review notes",
				"path":           notesRel,
				"lead_id":        "",
				"finding_id":     "finding_auth",
				"content_sha256": runFileSHA256ForTest(t, outputDir, notesRel),
			},
		},
		{
			ID:       "event_existing_review_verdict",
			RunID:    task.RunID,
			Type:     "verdict.recorded",
			Object:   "verdict",
			ObjectID: "verdict_existing_review",
			TaskID:   task.TaskID,
			Data: map[string]any{
				"finding_id":           "finding_auth",
				"phase":                "review",
				"value":                "accepted",
				"reason":               "Accepted already.",
				"canonical_finding_id": "",
			},
		},
		{
			ID:       "event_existing_review_done",
			RunID:    task.RunID,
			Type:     "task.completed",
			Object:   "task",
			ObjectID: task.TaskID,
			TaskID:   task.TaskID,
			Data: map[string]any{
				"status":  "completed",
				"summary": "Reviewed already.",
			},
		},
	} {
		if err := appendLedgerEvent(ledgerDir, event); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MNM_RUN_DIR", outputDir)
	t.Setenv(ledgerDirEnv, ledgerDir)

	var stdout, stderr bytes.Buffer
	if err := run([]string{
		"verdict", "record",
		"--finding", "finding_auth",
		"--phase", "review",
		"--value", "accepted",
		"--reason", "Accepted already.",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("verdict record failed: %v\nstderr: %s", err, stderr.String())
	}
	if got := strings.TrimSpace(stdout.String()); got != "verdict_existing_review" {
		t.Fatalf("verdict id = %q, want snapshot id", got)
	}
	outputEvents, err := readLedgerEventsFile(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputEvents) != 0 {
		t.Fatalf("output events = %#v, want none", outputEvents)
	}
}

func TestLedgerOverlayReadsSnapshotReadOnly(t *testing.T) {
	ledgerDir := t.TempDir()
	outputDir := t.TempDir()
	t.Setenv(ledgerDirEnv, ledgerDir)

	if _, err := readLedgerEvents(outputDir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ledgerDir, ".events.lock")); !os.IsNotExist(err) {
		t.Fatalf("snapshot read should not create lock file, stat err = %v", err)
	}

	missingDir := filepath.Join(t.TempDir(), "missing-ledger")
	t.Setenv(ledgerDirEnv, missingDir)
	if _, err := readLedgerEvents(outputDir); err == nil {
		t.Fatal("expected missing snapshot directory error")
	}
	if _, err := os.Stat(missingDir); !os.IsNotExist(err) {
		t.Fatalf("missing snapshot dir should not be created, stat err = %v", err)
	}
}

func TestLedgerOverlayTreatsSymlinkEquivalentDirsAsSameLedger(t *testing.T) {
	ledgerDir := t.TempDir()
	if err := appendLedgerEvent(ledgerDir, LedgerEvent{
		ID:        "event_started",
		RunID:     "run_overlay",
		Type:      "task.started",
		Object:    "task",
		ObjectID:  "task_recon",
		TaskID:    "task_recon",
		Timestamp: "2026-01-01T00:00:00Z",
		Data: map[string]any{
			"phase": "recon",
		},
	}); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(t.TempDir(), "ledger-link")
	if err := os.Symlink(ledgerDir, linkPath); err != nil {
		t.Skipf("symlink not available: %v", err)
	}
	t.Setenv(ledgerDirEnv, ledgerDir)

	events, err := readLedgerEvents(linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("event count = %d, want 1", len(events))
	}
}

func TestLedgerOverlayRequiresTaskArtifactsInOutputDir(t *testing.T) {
	ledgerDir := t.TempDir()
	outputDir := t.TempDir()
	outsideDir := t.TempDir()
	task := TaskRecord{
		RunID:  "run_overlay",
		TaskID: "task_investigate_lead_auth",
		Phase:  "investigate",
	}
	if err := appendLedgerEvent(ledgerDir, LedgerEvent{
		ID:        "event_lead",
		RunID:     task.RunID,
		Type:      "lead.created",
		Object:    "lead",
		ObjectID:  "lead_auth",
		TaskID:    "task_recon",
		Timestamp: "2026-01-01T00:00:00Z",
		Data: map[string]any{
			"title":     "Investigate auth",
			"category":  "authz",
			"priority":  "high",
			"body_path": "evidence/lead-auth.md",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeTaskFile(filepath.Join(outputDir, currentTaskFile), task); err != nil {
		t.Fatal(err)
	}
	outsideFinding := filepath.Join(outsideDir, "finding-auth.md")
	if err := os.WriteFile(outsideFinding, []byte("# Finding\n"), filePerm); err != nil {
		t.Fatal(err)
	}

	t.Setenv("MNM_RUN_DIR", outputDir)
	t.Setenv(ledgerDirEnv, ledgerDir)

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"finding", "create",
		"--lead", "lead_auth",
		"--title", "Auth bypass",
		"--category", "authz",
		"--severity", "high",
		"--confidence", "medium",
		"--body-file", outsideFinding,
	}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected output path rejection")
	}
	if !strings.Contains(err.Error(), "path must be inside run directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}
