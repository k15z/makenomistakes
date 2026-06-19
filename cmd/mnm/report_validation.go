package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func validateReportArtifacts(runDir string, task TaskRecord, markdownRel, jsonRel string) error {
	normalizedMarkdownRel, err := normalizeReportPath(runDir, markdownRel)
	if err != nil {
		return fmt.Errorf("markdown report path: %w", err)
	}
	if normalizedMarkdownRel != markdownRel {
		return fmt.Errorf("markdown report path = %q, want normalized run-relative path %q", markdownRel, normalizedMarkdownRel)
	}
	normalizedJSONRel, err := normalizeReportPath(runDir, jsonRel)
	if err != nil {
		return fmt.Errorf("JSON report path: %w", err)
	}
	if normalizedJSONRel != jsonRel {
		return fmt.Errorf("JSON report path = %q, want normalized run-relative path %q", jsonRel, normalizedJSONRel)
	}

	markdownPath := filepath.Join(runDir, filepath.FromSlash(markdownRel))
	markdown, err := os.ReadFile(markdownPath)
	if err != nil {
		return fmt.Errorf("read markdown report: %w", err)
	}
	if len(bytes.TrimSpace(markdown)) == 0 {
		return errors.New("markdown report must not be empty")
	}

	jsonPath := filepath.Join(runDir, filepath.FromSlash(jsonRel))
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return fmt.Errorf("read JSON report: %w", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("report JSON must parse as a single object: %w", err)
	}
	if len(root) == 0 {
		return errors.New("report JSON must not be an empty object")
	}
	runID, err := requiredStringField(root, "run_id")
	if err != nil {
		return err
	}
	if runID != task.RunID {
		return fmt.Errorf("report JSON run_id = %q, want %q", runID, task.RunID)
	}
	if err := validateReportPaths(runDir, root, markdownRel, jsonRel); err != nil {
		return err
	}

	state, err := reportKnownState(runDir)
	if err != nil {
		return err
	}
	counts, err := requiredObjectField(root, "counts")
	if err != nil {
		return err
	}

	buckets := []struct {
		name      string
		countName string
	}{
		{name: "proven", countName: "findings_proven"},
		{name: "inconclusive", countName: "findings_inconclusive"},
		{name: "failed", countName: "findings_failed"},
		{name: "rejected", countName: "findings_rejected"},
		{name: "duplicate", countName: "findings_duplicate"},
		{name: "unvalidated", countName: "findings_unvalidated"},
	}
	seenReportIDs := map[string]string{}
	for _, bucket := range buckets {
		items, err := requiredArrayField(root, bucket.name)
		if err != nil {
			return err
		}
		count, err := requiredIntField(counts, bucket.countName)
		if err != nil {
			return err
		}
		if count != len(items) {
			return fmt.Errorf("report JSON counts.%s = %d, want %d", bucket.countName, count, len(items))
		}
		for i, item := range items {
			if err := validateReportFindingItem(runDir, bucket.name, i, item, state, seenReportIDs); err != nil {
				return err
			}
		}
	}
	if err := validateReportCoversAllFindings(state, seenReportIDs); err != nil {
		return err
	}
	return nil
}

type reportLedgerState struct {
	Findings        map[string]FindingRecord
	Leads           map[string]bool
	Verdicts        map[string]map[string]VerdictRecord
	FindingEvidence map[string]map[string]EvidenceRecord
}

func reportKnownState(runDir string) (reportLedgerState, error) {
	leads, err := ledgerLeads(runDir)
	if err != nil {
		return reportLedgerState{}, err
	}
	findings, err := ledgerFindings(runDir)
	if err != nil {
		return reportLedgerState{}, err
	}
	verdicts, err := ledgerVerdicts(runDir)
	if err != nil {
		return reportLedgerState{}, err
	}
	evidence, err := ledgerEvidence(runDir)
	if err != nil {
		return reportLedgerState{}, err
	}
	state := reportLedgerState{
		Findings:        map[string]FindingRecord{},
		Leads:           map[string]bool{},
		Verdicts:        map[string]map[string]VerdictRecord{},
		FindingEvidence: map[string]map[string]EvidenceRecord{},
	}
	for _, lead := range leads {
		state.Leads[lead.ID] = true
	}
	for _, finding := range findings {
		state.Findings[finding.ID] = finding
	}
	for _, verdict := range verdicts {
		if verdict.FindingID == "" || verdict.Phase == "" {
			continue
		}
		if !ledgerVerdictComplete(runDir, verdict) {
			continue
		}
		if state.Verdicts[verdict.FindingID] == nil {
			state.Verdicts[verdict.FindingID] = map[string]VerdictRecord{}
		}
		state.Verdicts[verdict.FindingID][verdict.Phase] = verdict
	}
	for _, item := range evidence {
		if item.FindingID == "" || item.Path == "" {
			continue
		}
		if state.FindingEvidence[item.FindingID] == nil {
			state.FindingEvidence[item.FindingID] = map[string]EvidenceRecord{}
		}
		state.FindingEvidence[item.FindingID][item.Path] = item
	}
	return state, nil
}

func validateReportPaths(runDir string, root map[string]json.RawMessage, markdownRel, jsonRel string) error {
	paths, err := requiredObjectField(root, "report_paths")
	if err != nil {
		return err
	}
	markdownPath, err := requiredStringField(paths, "markdown")
	if err != nil {
		return err
	}
	gotMarkdownRel, err := normalizeReportPath(runDir, markdownPath)
	if err != nil {
		return fmt.Errorf("report_paths.markdown: %w", err)
	}
	if gotMarkdownRel != markdownRel {
		return fmt.Errorf("report_paths.markdown = %q, want %q", gotMarkdownRel, markdownRel)
	}
	jsonPath, err := requiredStringField(paths, "json")
	if err != nil {
		return err
	}
	gotJSONRel, err := normalizeReportPath(runDir, jsonPath)
	if err != nil {
		return fmt.Errorf("report_paths.json: %w", err)
	}
	if gotJSONRel != jsonRel {
		return fmt.Errorf("report_paths.json = %q, want %q", gotJSONRel, jsonRel)
	}
	return nil
}

func validateReportFindingItem(runDir, bucket string, index int, item map[string]json.RawMessage, state reportLedgerState, seenReportIDs map[string]string) error {
	prefix := fmt.Sprintf("%s[%d]", bucket, index)
	id, err := requiredStringField(item, "id")
	if err != nil {
		return fmt.Errorf("%s.%w", prefix, err)
	}
	if _, ok := state.Findings[id]; !ok {
		return fmt.Errorf("%s.id %q does not reference a known finding", prefix, id)
	}
	finding := state.Findings[id]
	if previous, exists := seenReportIDs[id]; exists {
		return fmt.Errorf("%s.id %q duplicates report item in %s", prefix, id, previous)
	}
	seenReportIDs[id] = prefix
	expectedBucket := reportBucketForFinding(state.Verdicts[id])
	if bucket != expectedBucket {
		return fmt.Errorf("%s.id %q is in bucket %q, want %q from ledger verdicts", prefix, id, bucket, expectedBucket)
	}
	for _, field := range []string{"status", "summary"} {
		if _, err := requiredNonEmptyStringField(item, field); err != nil {
			return fmt.Errorf("%s.%w", prefix, err)
		}
	}
	for _, field := range []struct {
		name string
		want string
	}{
		{name: "title", want: finding.Title},
		{name: "category", want: finding.Category},
		{name: "severity", want: finding.Severity},
		{name: "confidence", want: finding.Confidence},
	} {
		got, err := requiredNonEmptyStringField(item, field.name)
		if err != nil {
			return fmt.Errorf("%s.%w", prefix, err)
		}
		if got != field.want {
			return fmt.Errorf("%s.%s = %q, want ledger value %q", prefix, field.name, got, field.want)
		}
	}
	status, _ := requiredStringField(item, "status")
	expectedStatus := reportStatusForFinding(state.Verdicts[id])
	if status != expectedStatus {
		return fmt.Errorf("%s.status = %q, want ledger value %q", prefix, status, expectedStatus)
	}
	sourceLeadID, err := requiredStringField(item, "source_lead_id")
	if err != nil {
		return fmt.Errorf("%s.%w", prefix, err)
	}
	if sourceLeadID != finding.LeadID {
		return fmt.Errorf("%s.source_lead_id = %q, want ledger value %q", prefix, sourceLeadID, finding.LeadID)
	}
	if sourceLeadID != "" && !state.Leads[sourceLeadID] {
		return fmt.Errorf("%s.source_lead_id %q does not reference a known lead", prefix, sourceLeadID)
	}
	for _, field := range []string{"verdicts", "evidence_paths", "affected_paths"} {
		if _, err := requiredStringArrayField(item, field); err != nil {
			return fmt.Errorf("%s.%w", prefix, err)
		}
	}
	evidencePaths, _ := requiredStringArrayField(item, "evidence_paths")
	if bucket == "proven" && len(evidencePaths) == 0 {
		return fmt.Errorf("%s.evidence_paths must include at least one registered evidence path for a proven finding", prefix)
	}
	for _, evidencePath := range evidencePaths {
		evidenceRel, err := normalizeReportPath(runDir, evidencePath)
		if err != nil {
			return fmt.Errorf("%s.evidence_paths contains invalid path %q: %w", prefix, evidencePath, err)
		}
		evidence, ok := state.FindingEvidence[id][evidenceRel]
		if !ok {
			return fmt.Errorf("%s.evidence_paths contains %q, which is not registered ledger evidence for finding %s", prefix, evidencePath, id)
		}
		if err := registeredEvidenceFileError(runDir, evidenceRel, evidence.ContentSHA256, validateNonEmptyEvidenceFile); err != nil {
			return fmt.Errorf("%s.evidence_paths contains unusable evidence %q: %w", prefix, evidencePath, err)
		}
	}
	return nil
}

func validateReportCoversAllFindings(state reportLedgerState, seenReportIDs map[string]string) error {
	ids := make([]string, 0, len(state.Findings))
	for id := range state.Findings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, ok := seenReportIDs[id]; ok {
			continue
		}
		return fmt.Errorf("report JSON missing finding %s from bucket %q", id, reportBucketForFinding(state.Verdicts[id]))
	}
	return nil
}

func reportBucketForFinding(verdicts map[string]VerdictRecord) string {
	review, ok := verdicts["review"]
	if !ok {
		return "unvalidated"
	}
	if review.Value == "rejected" {
		return "rejected"
	}
	if review.Value != "accepted" {
		return "unvalidated"
	}

	deduplicate, ok := verdicts["deduplicate"]
	if !ok {
		return "unvalidated"
	}
	switch deduplicate.Value {
	case "duplicate":
		return "duplicate"
	case "canonical":
	default:
		return "unvalidated"
	}

	validate, ok := verdicts["validate"]
	if !ok {
		return "unvalidated"
	}
	switch validate.Value {
	case "proven":
		return "proven"
	case "inconclusive":
		return "inconclusive"
	case "failed":
		return "failed"
	default:
		return "unvalidated"
	}
}

func reportStatusForFinding(verdicts map[string]VerdictRecord) string {
	review, ok := verdicts["review"]
	if !ok {
		return "candidate"
	}
	if review.Value == "rejected" {
		return "review_rejected"
	}
	if review.Value != "accepted" {
		return "candidate"
	}

	deduplicate, ok := verdicts["deduplicate"]
	if !ok {
		return "reviewed"
	}
	switch deduplicate.Value {
	case "duplicate":
		return "duplicate"
	case "canonical":
	default:
		return "reviewed"
	}

	validate, ok := verdicts["validate"]
	if !ok {
		return "validation_pending"
	}
	switch validate.Value {
	case "proven":
		return "validation_proven"
	case "inconclusive":
		return "validation_inconclusive"
	case "failed":
		return "validation_failed"
	default:
		return "validation_pending"
	}
}

func requiredObjectField(root map[string]json.RawMessage, field string) (map[string]json.RawMessage, error) {
	raw, ok := root[field]
	if !ok {
		return nil, fmt.Errorf("report JSON missing %q object", field)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("report JSON field %q must be an object: %w", field, err)
	}
	if out == nil {
		return nil, fmt.Errorf("report JSON field %q must be an object", field)
	}
	return out, nil
}

func requiredArrayField(root map[string]json.RawMessage, field string) ([]map[string]json.RawMessage, error) {
	raw, ok := root[field]
	if !ok {
		return nil, fmt.Errorf("report JSON missing %q array", field)
	}
	var out []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("report JSON field %q must be an array of objects: %w", field, err)
	}
	if out == nil {
		return nil, fmt.Errorf("report JSON field %q must be an array", field)
	}
	return out, nil
}

func requiredStringField(root map[string]json.RawMessage, field string) (string, error) {
	raw, ok := root[field]
	if !ok {
		return "", fmt.Errorf("missing %q string", field)
	}
	var out *string
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("field %q must be a string: %w", field, err)
	}
	if out == nil {
		return "", fmt.Errorf("field %q must be a string", field)
	}
	return *out, nil
}

func requiredNonEmptyStringField(root map[string]json.RawMessage, field string) (string, error) {
	out, err := requiredStringField(root, field)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("field %q must not be empty", field)
	}
	return out, nil
}

func requiredIntField(root map[string]json.RawMessage, field string) (int, error) {
	raw, ok := root[field]
	if !ok {
		return 0, fmt.Errorf("missing %q integer", field)
	}
	var out *int
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, fmt.Errorf("field %q must be an integer: %w", field, err)
	}
	if out == nil {
		return 0, fmt.Errorf("field %q must be an integer", field)
	}
	return *out, nil
}

func requiredStringArrayField(root map[string]json.RawMessage, field string) ([]string, error) {
	raw, ok := root[field]
	if !ok {
		return nil, fmt.Errorf("missing %q array", field)
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("field %q must be an array of strings: %w", field, err)
	}
	if out == nil {
		return nil, fmt.Errorf("field %q must be an array of strings", field)
	}
	return out, nil
}

func normalizeReportPath(runDir, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is required")
	}
	absRunDir, err := filepath.Abs(runDir)
	if err != nil {
		return "", err
	}
	candidate := filepath.FromSlash(path)
	absPath := candidate
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(absRunDir, absPath)
	}
	absPath, err = filepath.Abs(absPath)
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
	info, err := os.Stat(absPath)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("path must be a regular file: %s", path)
	}
	if err := rejectSymlinkPath(runDir, absPath); err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}
