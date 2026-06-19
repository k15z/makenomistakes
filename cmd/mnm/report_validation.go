package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	return nil
}

type reportLedgerState struct {
	Findings        map[string]FindingRecord
	Leads           map[string]bool
	Verdicts        map[string]map[string]VerdictRecord
	FindingEvidence map[string]map[string]bool
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
		FindingEvidence: map[string]map[string]bool{},
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
		if !ledgerTaskCompleted(runDir, verdict.TaskID) {
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
			state.FindingEvidence[item.FindingID] = map[string]bool{}
		}
		state.FindingEvidence[item.FindingID][item.Path] = true
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
	if previous, exists := seenReportIDs[id]; exists {
		return fmt.Errorf("%s.id %q duplicates report item in %s", prefix, id, previous)
	}
	seenReportIDs[id] = prefix
	expectedBucket := reportBucketForFinding(state.Verdicts[id])
	if bucket != expectedBucket {
		return fmt.Errorf("%s.id %q is in bucket %q, want %q from ledger verdicts", prefix, id, bucket, expectedBucket)
	}
	for _, field := range []string{"title", "category", "severity", "confidence", "status", "summary"} {
		if _, err := requiredStringField(item, field); err != nil {
			return fmt.Errorf("%s.%w", prefix, err)
		}
	}
	status, _ := requiredStringField(item, "status")
	if !reportStatusAllowedForBucket(bucket, status) {
		return fmt.Errorf("%s.status = %q is not valid for bucket %q; expected one of: %s", prefix, status, bucket, reportStatusValues(bucket))
	}
	sourceLeadID, err := requiredStringField(item, "source_lead_id")
	if err != nil {
		return fmt.Errorf("%s.%w", prefix, err)
	}
	if sourceLeadID != "" && !state.Leads[sourceLeadID] {
		return fmt.Errorf("%s.source_lead_id %q does not reference a known lead", prefix, sourceLeadID)
	}
	if expectedLeadID := state.Findings[id].LeadID; sourceLeadID != expectedLeadID {
		return fmt.Errorf("%s.source_lead_id = %q, want %q for finding %s", prefix, sourceLeadID, expectedLeadID, id)
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
		if !state.FindingEvidence[id][evidenceRel] {
			return fmt.Errorf("%s.evidence_paths contains %q, which is not registered ledger evidence for finding %s", prefix, evidencePath, id)
		}
		info, err := os.Stat(filepath.Join(runDir, filepath.FromSlash(evidenceRel)))
		if err != nil {
			return fmt.Errorf("%s.evidence_paths contains unreadable path %q: %w", prefix, evidencePath, err)
		}
		if info.IsDir() {
			return fmt.Errorf("%s.evidence_paths contains directory %q; want file", prefix, evidencePath)
		}
	}
	return nil
}

func reportStatusAllowedForBucket(bucket, status string) bool {
	for _, allowed := range reportStatusesForBucket(bucket) {
		if status == allowed {
			return true
		}
	}
	return false
}

func reportStatusValues(bucket string) string {
	values := reportStatusesForBucket(bucket)
	if len(values) == 0 {
		return ""
	}
	out := values[0]
	for _, value := range values[1:] {
		out += ", " + value
	}
	return out
}

func reportStatusesForBucket(bucket string) []string {
	switch bucket {
	case "proven":
		return []string{"validation_proven"}
	case "inconclusive":
		return []string{"validation_inconclusive"}
	case "failed":
		return []string{"validation_failed"}
	case "rejected":
		return []string{"review_rejected"}
	case "duplicate":
		return []string{"duplicate"}
	case "unvalidated":
		return []string{"candidate", "reviewed", "validation_pending"}
	default:
		return nil
	}
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
	candidate := filepath.FromSlash(path)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(runDir, candidate)
	}
	return requirePathInsideRunDir(runDir, candidate)
}
