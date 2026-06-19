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
