package main

import "testing"

func TestLedgerLeadsTracksOpenAndClosedLeads(t *testing.T) {
	runDir := newLedgerTestRun(t)
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "lead.created",
		Object:   "lead",
		ObjectID: "lead_one",
		TaskID:   "task_recon",
		Data: map[string]any{
			"title":     "First lead",
			"category":  "security",
			"priority":  "high",
			"body_path": "evidence/lead-one.md",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "lead.created",
		Object:   "lead",
		ObjectID: "lead_two",
		TaskID:   "task_recon",
		Data: map[string]any{
			"title":     "Second lead",
			"category":  "correctness",
			"priority":  "medium",
			"body_path": "evidence/lead-two.md",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "lead.closed",
		Object:   "lead",
		ObjectID: "lead_one",
		TaskID:   "task_investigate_lead_one",
		Data: map[string]any{
			"status": "closed_no_finding",
			"reason": "Not reproducible.",
		},
	}); err != nil {
		t.Fatal(err)
	}

	open, err := openLedgerLeads(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].ID != "lead_two" {
		t.Fatalf("unexpected open leads: %#v", open)
	}
	if !ledgerLeadClosed(runDir, "lead_one") {
		t.Fatal("expected lead_one to be closed")
	}
	if ledgerLeadClosed(runDir, "lead_two") {
		t.Fatal("expected lead_two to remain open")
	}
}

func TestLedgerFindingsEvidenceAndVerdicts(t *testing.T) {
	runDir := newLedgerTestRun(t)
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "finding.created",
		Object:   "finding",
		ObjectID: "finding_one",
		TaskID:   "task_investigate_lead_one",
		Data: map[string]any{
			"title":      "Missing authorization",
			"lead_id":    "lead_one",
			"category":   "authz",
			"severity":   "high",
			"confidence": "medium",
			"body_path":  "evidence/finding-one.md",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: "evidence_one",
		TaskID:   "task_investigate_lead_one",
		Data: map[string]any{
			"kind":       "markdown",
			"title":      "Proof notes",
			"path":       "evidence/proof.md",
			"lead_id":    "lead_one",
			"finding_id": "finding_one",
		},
	}); err != nil {
		t.Fatal(err)
	}

	findings, err := unreviewedLedgerFindings(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 || findings[0].ID != "finding_one" {
		t.Fatalf("unexpected unreviewed findings: %#v", findings)
	}
	evidence, err := ledgerEvidenceForFinding(runDir, "finding_one")
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].Title != "Proof notes" {
		t.Fatalf("unexpected finding evidence: %#v", evidence)
	}

	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    "run_test",
		Type:     "verdict.recorded",
		Object:   "verdict",
		ObjectID: "verdict_one",
		TaskID:   "task_review_finding_one",
		Data: map[string]any{
			"finding_id": "finding_one",
			"phase":      "review",
			"value":      "accepted",
			"reason":     "Specific and supported.",
		},
	}); err != nil {
		t.Fatal(err)
	}

	if !ledgerFindingHasVerdict(runDir, "finding_one", "review") {
		t.Fatal("expected finding_one to have a review verdict")
	}
	findings, err = unreviewedLedgerFindings(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("expected no unreviewed findings, got %#v", findings)
	}
}
