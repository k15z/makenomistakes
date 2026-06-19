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

	knownIDs, knownLeadIDs, err := reportKnownIDs(runDir)
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
			if err := validateReportFindingItem(runDir, bucket.name, i, item, knownIDs, knownLeadIDs, seenReportIDs); err != nil {
				return err
			}
		}
	}
	return nil
}

func reportKnownIDs(runDir string) (map[string]bool, map[string]bool, error) {
	leads, err := ledgerLeads(runDir)
	if err != nil {
		return nil, nil, err
	}
	findings, err := ledgerFindings(runDir)
	if err != nil {
		return nil, nil, err
	}
	knownIDs := map[string]bool{}
	knownLeadIDs := map[string]bool{}
	for _, lead := range leads {
		knownIDs[lead.ID] = true
		knownLeadIDs[lead.ID] = true
	}
	for _, finding := range findings {
		knownIDs[finding.ID] = true
	}
	return knownIDs, knownLeadIDs, nil
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

func validateReportFindingItem(runDir, bucket string, index int, item map[string]json.RawMessage, knownIDs, knownLeadIDs map[string]bool, seenReportIDs map[string]string) error {
	prefix := fmt.Sprintf("%s[%d]", bucket, index)
	id, err := requiredStringField(item, "id")
	if err != nil {
		return fmt.Errorf("%s.%w", prefix, err)
	}
	if !knownIDs[id] {
		return fmt.Errorf("%s.id %q does not reference a known lead or finding", prefix, id)
	}
	if previous, exists := seenReportIDs[id]; exists {
		return fmt.Errorf("%s.id %q duplicates report item in %s", prefix, id, previous)
	}
	seenReportIDs[id] = prefix
	for _, field := range []string{"title", "category", "severity", "confidence", "status", "summary"} {
		if _, err := requiredStringField(item, field); err != nil {
			return fmt.Errorf("%s.%w", prefix, err)
		}
	}
	sourceLeadID, err := requiredStringField(item, "source_lead_id")
	if err != nil {
		return fmt.Errorf("%s.%w", prefix, err)
	}
	if sourceLeadID != "" && !knownLeadIDs[sourceLeadID] {
		return fmt.Errorf("%s.source_lead_id %q does not reference a known lead", prefix, sourceLeadID)
	}
	for _, field := range []string{"verdicts", "evidence_paths", "affected_paths"} {
		if _, err := requiredStringArrayField(item, field); err != nil {
			return fmt.Errorf("%s.%w", prefix, err)
		}
	}
	evidencePaths, _ := requiredStringArrayField(item, "evidence_paths")
	for _, evidencePath := range evidencePaths {
		if _, err := normalizeReportPath(runDir, evidencePath); err != nil {
			return fmt.Errorf("%s.evidence_paths contains invalid path %q: %w", prefix, evidencePath, err)
		}
	}
	return nil
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
	var out string
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("field %q must be a string: %w", field, err)
	}
	return out, nil
}

func requiredIntField(root map[string]json.RawMessage, field string) (int, error) {
	raw, ok := root[field]
	if !ok {
		return 0, fmt.Errorf("missing %q integer", field)
	}
	var out int
	if err := json.Unmarshal(raw, &out); err != nil {
		return 0, fmt.Errorf("field %q must be an integer: %w", field, err)
	}
	return out, nil
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
