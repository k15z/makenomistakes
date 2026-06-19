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
	eventIndex    int
	ID            string
	TaskID        string
	Kind          string
	Title         string
	Path          string
	ContentSHA256 string
	LeadID        string
	FindingID     string
}

type VerdictRecord struct {
	eventIndex         int
	ID                 string
	TaskID             string
	FindingID          string
	Phase              string
	Value              string
	Reason             string
	CanonicalFindingID string
}

type ReportRecord struct {
	ID           string
	TaskID       string
	MarkdownPath string
	JSONPath     string
}

type RunnerFailureRecord struct {
	Stage string
	Error string
	Path  string
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

func ledgerLeadHasFinding(runDir, leadID string) bool {
	if leadID == "" {
		return false
	}
	findings, err := ledgerFindings(runDir)
	if err != nil {
		return false
	}
	for _, finding := range findings {
		if finding.LeadID == leadID {
			return true
		}
	}
	return false
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

func unvalidatedCanonicalFindings(runDir string) ([]FindingRecord, error) {
	findings, err := reviewAcceptedLedgerFindings(runDir)
	if err != nil {
		return nil, err
	}
	var pending []FindingRecord
	for _, finding := range findings {
		dedup, ok, err := ledgerFindingVerdict(runDir, finding.ID, "deduplicate")
		if err != nil {
			return nil, err
		}
		if !ok || dedup.Value != "canonical" {
			continue
		}
		if !ledgerFindingHasVerdict(runDir, finding.ID, "validate") {
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
	for i, event := range events {
		if event.Type != "evidence.added" || event.Object != "evidence" {
			continue
		}
		evidence = append(evidence, EvidenceRecord{
			eventIndex:    i,
			ID:            event.ObjectID,
			TaskID:        event.TaskID,
			Kind:          stringData(event.Data, "kind"),
			Title:         stringData(event.Data, "title"),
			Path:          stringData(event.Data, "path"),
			ContentSHA256: stringData(event.Data, "content_sha256"),
			LeadID:        stringData(event.Data, "lead_id"),
			FindingID:     stringData(event.Data, "finding_id"),
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

func ledgerFindingHasTaskEvidencePath(runDir, findingID, taskID, relPath string) bool {
	_, ok := ledgerFindingTaskEvidenceBefore(runDir, findingID, taskID, relPath, maxInt)
	return ok
}

func ledgerFindingTaskEvidenceBefore(runDir, findingID, taskID, relPath string, beforeIndex int) (EvidenceRecord, bool) {
	evidence, err := ledgerEvidenceForFinding(runDir, findingID)
	if err != nil {
		return EvidenceRecord{}, false
	}
	var match EvidenceRecord
	found := false
	for _, item := range evidence {
		if item.TaskID == taskID && item.Path == relPath && item.eventIndex < beforeIndex {
			match = item
			found = true
		}
	}
	return match, found
}

func ledgerFindingHasValidValidationEvidence(runDir, findingID, taskID string, beforeIndex int) bool {
	notesRel := validationNotesRelPath(findingID)
	item, ok := ledgerFindingTaskEvidenceBefore(runDir, findingID, taskID, notesRel, beforeIndex)
	if !ok {
		return false
	}
	return validateRegisteredEvidenceFile(runDir, notesRel, item.ContentSHA256, validateNonEmptyValidationEvidence)
}

func ledgerFindingHasValidReviewEvidence(runDir, findingID, taskID string, beforeIndex int) bool {
	notesRel := reviewNotesRelPath(findingID)
	item, ok := ledgerFindingTaskEvidenceBefore(runDir, findingID, taskID, notesRel, beforeIndex)
	if !ok {
		return false
	}
	return validateRegisteredEvidenceFile(runDir, notesRel, item.ContentSHA256, validateNonEmptyEvidenceFile)
}

func validateRegisteredEvidenceFile(runDir, relPath, contentSHA256 string, validate func(string, string) error) bool {
	return registeredEvidenceFileError(runDir, relPath, contentSHA256, validate) == nil
}

func registeredEvidenceFileError(runDir, relPath, contentSHA256 string, validate func(string, string) error) error {
	if err := validate(runDir, relPath); err != nil {
		return err
	}
	if contentSHA256 == "" {
		return fmt.Errorf("evidence file %s is missing registered content hash", relPath)
	}
	currentSHA256, err := evidenceFileSHA256(runDir, relPath)
	if err != nil {
		return fmt.Errorf("hash evidence file %s: %w", relPath, err)
	}
	if currentSHA256 != contentSHA256 {
		return fmt.Errorf("evidence file %s changed after registration", relPath)
	}
	return nil
}

const maxInt = int(^uint(0) >> 1)

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

func ledgerLeadTaskEvidence(runDir, leadID, taskID, relPath string) (EvidenceRecord, bool) {
	evidence, err := ledgerEvidenceForLead(runDir, leadID)
	if err != nil {
		return EvidenceRecord{}, false
	}
	var match EvidenceRecord
	found := false
	for _, item := range evidence {
		if item.TaskID == taskID && item.Path == relPath {
			match = item
			found = true
		}
	}
	return match, found
}

func ledgerLeadHasValidInvestigationEvidence(runDir, leadID, taskID string) bool {
	notesRel := investigationNotesRelPath(leadID)
	item, ok := ledgerLeadTaskEvidence(runDir, leadID, taskID, notesRel)
	if !ok {
		return false
	}
	return validateRegisteredEvidenceFile(runDir, notesRel, item.ContentSHA256, validateNonEmptyEvidenceFile)
}

func ledgerVerdicts(runDir string) ([]VerdictRecord, error) {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return nil, err
	}
	var verdicts []VerdictRecord
	for i, event := range events {
		if event.Type != "verdict.recorded" || event.Object != "verdict" {
			continue
		}
		verdicts = append(verdicts, VerdictRecord{
			eventIndex:         i,
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
		if verdict.FindingID == findingID && verdict.Phase == phase && ledgerVerdictComplete(runDir, verdict) {
			match = verdict
			found = true
		}
	}
	return match, found, nil
}

func ledgerVerdictComplete(runDir string, verdict VerdictRecord) bool {
	if !ledgerTaskCompletedAfter(runDir, verdict.TaskID, verdict.eventIndex) {
		return false
	}
	switch verdict.Phase {
	case "review":
		return ledgerFindingHasValidReviewEvidence(runDir, verdict.FindingID, verdict.TaskID, verdict.eventIndex)
	case "validate":
		return ledgerFindingHasValidValidationEvidence(runDir, verdict.FindingID, verdict.TaskID, verdict.eventIndex)
	default:
		return true
	}
}

func ledgerTaskCompletedAfter(runDir, taskID string, afterIndex int) bool {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return false
	}
	status := ""
	for i, event := range events {
		if i <= afterIndex {
			continue
		}
		if event.Type == "task.completed" && event.Object == "task" && event.ObjectID == taskID {
			status, _ = event.Data["status"].(string)
		}
	}
	return status == "completed"
}

func ledgerFindingHasVerdict(runDir, findingID, phase string) bool {
	_, ok, err := ledgerFindingVerdict(runDir, findingID, phase)
	return err == nil && ok
}

func ledgerReports(runDir string) ([]ReportRecord, error) {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return nil, err
	}
	var reports []ReportRecord
	for _, event := range events {
		if event.Type != "report.finalized" || event.Object != "report" {
			continue
		}
		reports = append(reports, ReportRecord{
			ID:           event.ObjectID,
			TaskID:       event.TaskID,
			MarkdownPath: stringData(event.Data, "markdown_path"),
			JSONPath:     stringData(event.Data, "json_path"),
		})
	}
	return reports, nil
}

func ledgerReportFinalized(runDir string) bool {
	reports, err := ledgerReports(runDir)
	return err == nil && len(reports) > 0
}

func latestRunnerFailure(runDir string) (RunnerFailureRecord, bool, error) {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return RunnerFailureRecord{}, false, err
	}
	var failure RunnerFailureRecord
	found := false
	for _, event := range events {
		if event.Object != "run" {
			continue
		}
		switch event.Type {
		case "runner.failed":
			failure = RunnerFailureRecord{
				Stage: stringData(event.Data, "stage"),
				Error: stringData(event.Data, "error"),
				Path:  stringData(event.Data, "path"),
			}
			found = true
		case "runner.completed":
			failure = RunnerFailureRecord{}
			found = false
		}
	}
	return failure, found, nil
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
