package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

type LeadRecord struct {
	ID       string
	Title    string
	Category string
	Priority string
	BodyPath string
	Status   string
}

type FindingRecord struct {
	ID         string
	Title      string
	LeadID     string
	Category   string
	Severity   string
	Confidence string
	BodyPath   string
}

type EvidenceRecord struct {
	ID        string
	Kind      string
	Title     string
	Path      string
	LeadID    string
	FindingID string
}

type VerdictRecord struct {
	ID                 string
	TaskID             string
	FindingID          string
	Phase              string
	Value              string
	Reason             string
	CanonicalFindingID string
}

func ledgerLeads(runDir string) ([]LeadRecord, error) {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return nil, err
	}
	leadsByID := map[string]LeadRecord{}
	var order []string
	for _, event := range events {
		if event.Object != "lead" {
			continue
		}
		switch event.Type {
		case "lead.created":
			lead := LeadRecord{
				ID:       event.ObjectID,
				Title:    stringData(event.Data, "title"),
				Category: stringData(event.Data, "category"),
				Priority: stringData(event.Data, "priority"),
				BodyPath: stringData(event.Data, "body_path"),
				Status:   "open",
			}
			if _, exists := leadsByID[lead.ID]; !exists {
				order = append(order, lead.ID)
			}
			leadsByID[lead.ID] = lead
		case "lead.closed":
			lead, exists := leadsByID[event.ObjectID]
			if !exists {
				continue
			}
			lead.Status = stringData(event.Data, "status")
			leadsByID[event.ObjectID] = lead
		}
	}

	leads := make([]LeadRecord, 0, len(order))
	for _, id := range order {
		leads = append(leads, leadsByID[id])
	}
	return leads, nil
}

func openLedgerLeads(runDir string) ([]LeadRecord, error) {
	leads, err := ledgerLeads(runDir)
	if err != nil {
		return nil, err
	}
	var open []LeadRecord
	for _, lead := range leads {
		if lead.Status == "open" {
			open = append(open, lead)
		}
	}
	return open, nil
}

func ledgerLeadClosed(runDir, leadID string) bool {
	leads, err := ledgerLeads(runDir)
	if err != nil {
		return false
	}
	for _, lead := range leads {
		if lead.ID == leadID {
			return lead.Status != "open"
		}
	}
	return false
}

func ledgerFindings(runDir string) ([]FindingRecord, error) {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return nil, err
	}
	var findings []FindingRecord
	for _, event := range events {
		if event.Type != "finding.created" || event.Object != "finding" {
			continue
		}
		findings = append(findings, FindingRecord{
			ID:         event.ObjectID,
			Title:      stringData(event.Data, "title"),
			LeadID:     stringData(event.Data, "lead_id"),
			Category:   stringData(event.Data, "category"),
			Severity:   stringData(event.Data, "severity"),
			Confidence: stringData(event.Data, "confidence"),
			BodyPath:   stringData(event.Data, "body_path"),
		})
	}
	return findings, nil
}

func unreviewedLedgerFindings(runDir string) ([]FindingRecord, error) {
	findings, err := ledgerFindings(runDir)
	if err != nil {
		return nil, err
	}
	var pending []FindingRecord
	for _, finding := range findings {
		if !ledgerFindingHasVerdict(runDir, finding.ID, "review") {
			pending = append(pending, finding)
		}
	}
	return pending, nil
}

func reviewAcceptedLedgerFindings(runDir string) ([]FindingRecord, error) {
	findings, err := ledgerFindings(runDir)
	if err != nil {
		return nil, err
	}
	var accepted []FindingRecord
	for _, finding := range findings {
		verdict, ok, err := ledgerFindingVerdict(runDir, finding.ID, "review")
		if err != nil {
			return nil, err
		}
		if ok && verdict.Value == "accepted" {
			accepted = append(accepted, finding)
		}
	}
	return accepted, nil
}

func undeduplicatedLedgerFindings(runDir string) ([]FindingRecord, error) {
	findings, err := reviewAcceptedLedgerFindings(runDir)
	if err != nil {
		return nil, err
	}
	var pending []FindingRecord
	for _, finding := range findings {
		if !ledgerFindingHasVerdict(runDir, finding.ID, "deduplicate") {
			pending = append(pending, finding)
		}
	}
	return pending, nil
}

func ledgerEvidence(runDir string) ([]EvidenceRecord, error) {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return nil, err
	}
	var evidence []EvidenceRecord
	for _, event := range events {
		if event.Type != "evidence.added" || event.Object != "evidence" {
			continue
		}
		evidence = append(evidence, EvidenceRecord{
			ID:        event.ObjectID,
			Kind:      stringData(event.Data, "kind"),
			Title:     stringData(event.Data, "title"),
			Path:      stringData(event.Data, "path"),
			LeadID:    stringData(event.Data, "lead_id"),
			FindingID: stringData(event.Data, "finding_id"),
		})
	}
	return evidence, nil
}

func ledgerEvidenceForFinding(runDir, findingID string) ([]EvidenceRecord, error) {
	if findingID == "" {
		return nil, nil
	}
	evidence, err := ledgerEvidence(runDir)
	if err != nil {
		return nil, err
	}
	var matches []EvidenceRecord
	for _, item := range evidence {
		if item.FindingID == findingID {
			matches = append(matches, item)
		}
	}
	return matches, nil
}

func ledgerEvidenceForLead(runDir, leadID string) ([]EvidenceRecord, error) {
	if leadID == "" {
		return nil, nil
	}
	evidence, err := ledgerEvidence(runDir)
	if err != nil {
		return nil, err
	}
	var matches []EvidenceRecord
	for _, item := range evidence {
		if item.LeadID == leadID {
			matches = append(matches, item)
		}
	}
	return matches, nil
}

func ledgerVerdicts(runDir string) ([]VerdictRecord, error) {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return nil, err
	}
	var verdicts []VerdictRecord
	for _, event := range events {
		if event.Type != "verdict.recorded" || event.Object != "verdict" {
			continue
		}
		verdicts = append(verdicts, VerdictRecord{
			ID:                 event.ObjectID,
			TaskID:             event.TaskID,
			FindingID:          stringData(event.Data, "finding_id"),
			Phase:              stringData(event.Data, "phase"),
			Value:              stringData(event.Data, "value"),
			Reason:             stringData(event.Data, "reason"),
			CanonicalFindingID: stringData(event.Data, "canonical_finding_id"),
		})
	}
	return verdicts, nil
}

func ledgerFindingVerdict(runDir, findingID, phase string) (VerdictRecord, bool, error) {
	verdicts, err := ledgerVerdicts(runDir)
	if err != nil {
		return VerdictRecord{}, false, err
	}
	var match VerdictRecord
	found := false
	for _, verdict := range verdicts {
		if verdict.FindingID == findingID && verdict.Phase == phase && ledgerTaskCompleted(runDir, verdict.TaskID) {
			match = verdict
			found = true
		}
	}
	return match, found, nil
}

func ledgerFindingHasVerdict(runDir, findingID, phase string) bool {
	_, ok, err := ledgerFindingVerdict(runDir, findingID, phase)
	return err == nil && ok
}

func leadBodyPath(runDir string, lead LeadRecord) (string, error) {
	if lead.BodyPath == "" {
		return "", fmt.Errorf("lead %s is missing body_path", lead.ID)
	}
	return filepath.Join(runDir, filepath.FromSlash(lead.BodyPath)), nil
}

func findingBodyPath(runDir string, finding FindingRecord) (string, error) {
	if finding.BodyPath == "" {
		return "", fmt.Errorf("finding %s is missing body_path", finding.ID)
	}
	return filepath.Join(runDir, filepath.FromSlash(finding.BodyPath)), nil
}

func evidencePath(runDir string, evidence EvidenceRecord) (string, error) {
	if evidence.Path == "" {
		return "", fmt.Errorf("evidence %s is missing path", evidence.ID)
	}
	return filepath.Join(runDir, filepath.FromSlash(evidence.Path)), nil
}

func stringData(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	value, ok := data[key]
	if !ok || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func safeFileID(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			builder.WriteRune(r)
			continue
		}
		builder.WriteByte('_')
	}
	if builder.Len() == 0 {
		return "item"
	}
	return builder.String()
}
