package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	pathpkg "path"
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
	if err := validateReportPaths(root, markdownRel, jsonRel); err != nil {
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
	citedEvidencePaths := map[string]bool{}
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
			if err := validateReportFindingItem(runDir, bucket.name, i, item, state, seenReportIDs, citedEvidencePaths); err != nil {
				return err
			}
		}
	}
	if err := validateReportCoversAllFindings(state, seenReportIDs); err != nil {
		return err
	}
	if err := validateMarkdownReportCoversAllFindings(markdown, state); err != nil {
		return err
	}
	if err := validateMarkdownReportCoversValidationBlockers(markdown, state); err != nil {
		return err
	}
	if err := validateMarkdownReportCoversEvidencePaths(markdown, citedEvidencePaths); err != nil {
		return err
	}
	return nil
}

type reportLedgerState struct {
	Findings           map[string]FindingRecord
	Leads              map[string]bool
	Verdicts           map[string]map[string]VerdictRecord
	FindingEvidence    map[string]map[string]EvidenceRecord
	ValidationBlockers map[string][]validationBlockerContext
	WorkspaceFiles     map[string]bool
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
	workspaceFiles, err := reportWorkspaceFiles(runDir)
	if err != nil {
		return reportLedgerState{}, err
	}
	state := reportLedgerState{
		Findings:           map[string]FindingRecord{},
		Leads:              map[string]bool{},
		Verdicts:           map[string]map[string]VerdictRecord{},
		FindingEvidence:    map[string]map[string]EvidenceRecord{},
		ValidationBlockers: map[string][]validationBlockerContext{},
		WorkspaceFiles:     workspaceFiles,
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
	validationBlockers, err := validationBlockersFromTaskHandoffs(runDir, evidence, state.Verdicts)
	if err != nil {
		return reportLedgerState{}, err
	}
	state.ValidationBlockers = validationBlockers
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

func validationBlockersFromTaskHandoffs(runDir string, evidence []EvidenceRecord, verdicts map[string]map[string]VerdictRecord) (map[string][]validationBlockerContext, error) {
	handoffs, err := taskHandoffsFromEvidence(runDir, evidence)
	if err != nil {
		return nil, err
	}
	blockersByFinding := map[string][]validationBlockerContext{}
	for _, handoff := range handoffs {
		if handoff.Phase != "validate" || handoff.FindingID == "" {
			continue
		}
		verdict, ok := verdicts[handoff.FindingID]["validate"]
		if !ok || verdict.Value != "inconclusive" || verdict.TaskID != handoff.TaskID {
			continue
		}
		for _, blocker := range handoff.Blockers {
			blockersByFinding[handoff.FindingID] = append(blockersByFinding[handoff.FindingID], validationBlockerContext{
				Summary:            blocker.Summary,
				MissingDependency:  blocker.MissingDependency,
				FailedCommand:      blocker.FailedCommand,
				RequiredService:    blocker.RequiredService,
				SuspectedConfigGap: blocker.SuspectedConfigGap,
				NextCommand:        blocker.NextCommand,
				SourcePath:         handoff.SourcePath,
			})
		}
	}
	for findingID, blockers := range blockersByFinding {
		blockersByFinding[findingID] = sortedValidationBlockers(blockers)
	}
	return blockersByFinding, nil
}

func reportWorkspaceFiles(runDir string) (map[string]bool, error) {
	relPath := "evidence/runner-manifest.json"
	evidence, ok := ledgerTaskEvidence(runDir, "", relPath)
	if !ok {
		return nil, nil
	}
	if err := registeredEvidenceFileError(runDir, relPath, evidence.ContentSHA256, validateNonEmptyEvidenceFile); err != nil {
		return nil, fmt.Errorf("registered runner manifest is unusable: %w", err)
	}
	path := filepath.Join(runDir, filepath.FromSlash(relPath))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read runner manifest: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse runner manifest: %w", err)
	}
	workspaceFilesRaw, ok := raw["workspace_files"]
	if !ok || bytes.Equal(bytes.TrimSpace(workspaceFilesRaw), []byte("null")) {
		return nil, errors.New("runner manifest workspace_files is required")
	}
	var workspaceFiles []string
	if err := json.Unmarshal(workspaceFilesRaw, &workspaceFiles); err != nil {
		return nil, fmt.Errorf("parse runner manifest workspace_files: %w", err)
	}
	files := map[string]bool{}
	for _, item := range workspaceFiles {
		files[item] = true
	}
	return files, nil
}

func validateReportPaths(root map[string]json.RawMessage, markdownRel, jsonRel string) error {
	paths, err := requiredObjectField(root, "report_paths")
	if err != nil {
		return err
	}
	markdownPath, err := requiredStringField(paths, "markdown")
	if err != nil {
		return err
	}
	if markdownPath != markdownRel {
		return fmt.Errorf("report_paths.markdown = %q, want %q", markdownPath, markdownRel)
	}
	jsonPath, err := requiredStringField(paths, "json")
	if err != nil {
		return err
	}
	if jsonPath != jsonRel {
		return fmt.Errorf("report_paths.json = %q, want %q", jsonPath, jsonRel)
	}
	return nil
}

func validateReportFindingItem(runDir, bucket string, index int, item map[string]json.RawMessage, state reportLedgerState, seenReportIDs map[string]string, citedEvidencePaths map[string]bool) error {
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
	if bucket == "duplicate" {
		if err := validateDuplicateCanonicalFinding(item, prefix, id, state); err != nil {
			return err
		}
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
	verdicts, err := requiredStringArrayField(item, "verdicts")
	if err != nil {
		return fmt.Errorf("%s.%w", prefix, err)
	}
	expectedVerdicts := reportVerdictLabels(state.Verdicts[id])
	if !stringSlicesEqual(verdicts, expectedVerdicts) {
		return fmt.Errorf("%s.verdicts = %q, want %q from ledger", prefix, verdicts, expectedVerdicts)
	}
	evidencePaths, err := requiredStringArrayField(item, "evidence_paths")
	if err != nil {
		return fmt.Errorf("%s.%w", prefix, err)
	}
	affectedPaths, err := requiredStringArrayField(item, "affected_paths")
	if err != nil {
		return fmt.Errorf("%s.%w", prefix, err)
	}
	if err := validateAffectedPaths(prefix, affectedPaths, state.WorkspaceFiles); err != nil {
		return err
	}
	if bucket == "proven" && len(evidencePaths) == 0 {
		return fmt.Errorf("%s.evidence_paths must include at least one registered evidence path for a proven finding", prefix)
	}
	citedEvidence := make([]EvidenceRecord, 0, len(evidencePaths))
	for _, evidencePath := range evidencePaths {
		evidenceRel, err := normalizeReportPath(runDir, evidencePath)
		if err != nil {
			return fmt.Errorf("%s.evidence_paths contains invalid path %q: %w", prefix, evidencePath, err)
		}
		if evidenceRel != evidencePath {
			return fmt.Errorf("%s.evidence_paths contains %q, want normalized registered path %q", prefix, evidencePath, evidenceRel)
		}
		evidence, ok := state.FindingEvidence[id][evidenceRel]
		if !ok {
			return fmt.Errorf("%s.evidence_paths contains %q, which is not registered ledger evidence for finding %s", prefix, evidencePath, id)
		}
		if err := registeredEvidenceFileError(runDir, evidenceRel, evidence.ContentSHA256, validateNonEmptyEvidenceFile); err != nil {
			return fmt.Errorf("%s.evidence_paths contains unusable evidence %q: %w", prefix, evidencePath, err)
		}
		citedEvidence = append(citedEvidence, evidence)
		citedEvidencePaths[evidenceRel] = true
	}
	if bucket == "proven" && !citesValidationProofEvidence(id, state.Verdicts[id]["validate"], citedEvidence) {
		return fmt.Errorf("%s.evidence_paths must include at least one validation proof artifact for a proven finding", prefix)
	}
	if bucket == "inconclusive" && len(state.ValidationBlockers[id]) == 0 {
		return fmt.Errorf("%s.validation_blockers must include at least one blocker from the inconclusive validation handoff", prefix)
	}
	if err := validateReportValidationBlockers(prefix, item, state.ValidationBlockers[id]); err != nil {
		return err
	}
	return nil
}

func validateReportValidationBlockers(prefix string, item map[string]json.RawMessage, expected []validationBlockerContext) error {
	if len(expected) == 0 {
		raw, ok := item["validation_blockers"]
		if !ok {
			return nil
		}
		var blockers []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &blockers); err != nil {
			return fmt.Errorf("%s.validation_blockers must be an array when present: %w", prefix, err)
		}
		if len(blockers) > 0 {
			return fmt.Errorf("%s.validation_blockers must be empty or omitted because no validation handoff blockers were recorded", prefix)
		}
		return nil
	}
	rawBlockers, err := requiredArrayField(item, "validation_blockers")
	if err != nil {
		return fmt.Errorf("%s.%w", prefix, err)
	}
	if len(rawBlockers) != len(expected) {
		return fmt.Errorf("%s.validation_blockers length = %d, want %d", prefix, len(rawBlockers), len(expected))
	}
	got := make([]validationBlockerContext, 0, len(rawBlockers))
	for i, raw := range rawBlockers {
		blocker, err := reportValidationBlockerFromJSON(raw)
		if err != nil {
			return fmt.Errorf("%s.validation_blockers[%d].%w", prefix, i, err)
		}
		got = append(got, blocker)
	}
	got = sortedValidationBlockers(got)
	expected = sortedValidationBlockers(expected)
	for i := range expected {
		if got[i] != expected[i] {
			return fmt.Errorf("%s.validation_blockers[%d] = %#v, want %#v from validation handoff", prefix, i, got[i], expected[i])
		}
	}
	return nil
}

func reportValidationBlockerFromJSON(item map[string]json.RawMessage) (validationBlockerContext, error) {
	var blocker validationBlockerContext
	var err error
	if blocker.Summary, err = requiredNonEmptyStringField(item, "summary"); err != nil {
		return blocker, err
	}
	if blocker.MissingDependency, err = requiredStringField(item, "missing_dependency"); err != nil {
		return blocker, err
	}
	if blocker.FailedCommand, err = requiredStringField(item, "failed_command"); err != nil {
		return blocker, err
	}
	if blocker.RequiredService, err = requiredStringField(item, "required_service"); err != nil {
		return blocker, err
	}
	if blocker.SuspectedConfigGap, err = requiredStringField(item, "suspected_config_gap"); err != nil {
		return blocker, err
	}
	if blocker.NextCommand, err = requiredNonEmptyStringField(item, "next_command"); err != nil {
		return blocker, err
	}
	if blocker.SourcePath, err = requiredNonEmptyStringField(item, "source_path"); err != nil {
		return blocker, err
	}
	return blocker, nil
}

func validateMarkdownReportCoversValidationBlockers(markdown []byte, state reportLedgerState) error {
	findingIDs := make([]string, 0, len(state.ValidationBlockers))
	for findingID := range state.ValidationBlockers {
		findingIDs = append(findingIDs, findingID)
	}
	sort.Strings(findingIDs)
	for _, findingID := range findingIDs {
		for _, blocker := range sortedValidationBlockers(state.ValidationBlockers[findingID]) {
			for _, item := range []struct {
				name  string
				value string
			}{
				{name: "summary", value: blocker.Summary},
				{name: "missing_dependency", value: blocker.MissingDependency},
				{name: "failed_command", value: blocker.FailedCommand},
				{name: "required_service", value: blocker.RequiredService},
				{name: "suspected_config_gap", value: blocker.SuspectedConfigGap},
				{name: "next_command", value: blocker.NextCommand},
			} {
				if strings.TrimSpace(item.value) == "" {
					continue
				}
				if !bytes.Contains(markdown, []byte(item.value)) {
					return fmt.Errorf("markdown report missing validation blocker %s %q for finding %s", item.name, item.value, findingID)
				}
			}
		}
	}
	return nil
}

func citesValidationProofEvidence(findingID string, verdict VerdictRecord, evidence []EvidenceRecord) bool {
	for _, item := range evidence {
		if item.TaskID != verdict.TaskID || item.eventIndex >= verdict.eventIndex {
			continue
		}
		if isValidationProofArtifact(findingID, item) {
			return true
		}
	}
	return false
}

func validateAffectedPaths(prefix string, paths []string, workspaceFiles map[string]bool) error {
	for i, path := range paths {
		itemPrefix := fmt.Sprintf("%s.affected_paths[%d]", prefix, i)
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("%s must not be empty", itemPrefix)
		}
		if path != strings.TrimSpace(path) {
			return fmt.Errorf("%s must not contain leading or trailing whitespace", itemPrefix)
		}
		if strings.Contains(path, "\\") {
			return fmt.Errorf("%s = %q must use slash-separated relative paths", itemPrefix, path)
		}
		if pathpkg.IsAbs(path) {
			return fmt.Errorf("%s = %q must be a relative workspace path", itemPrefix, path)
		}
		if len(path) >= 2 && path[1] == ':' {
			return fmt.Errorf("%s = %q must be a relative workspace path", itemPrefix, path)
		}
		clean := pathpkg.Clean(path)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return fmt.Errorf("%s = %q must be a relative workspace path", itemPrefix, path)
		}
		if clean != path {
			return fmt.Errorf("%s = %q, want clean relative path %q", itemPrefix, path, clean)
		}
		if workspaceFiles != nil && !workspaceFiles[path] {
			return fmt.Errorf("%s = %q was not found in the runner workspace manifest", itemPrefix, path)
		}
	}
	return nil
}

func reportVerdictLabels(verdicts map[string]VerdictRecord) []string {
	if len(verdicts) == 0 {
		return []string{}
	}
	phases := []struct {
		name  string
		label string
	}{
		{name: "review", label: "review"},
		{name: "deduplicate", label: "deduplicate"},
		{name: "validate", label: "validation"},
	}
	var labels []string
	for _, phase := range phases {
		verdict, ok := verdicts[phase.name]
		if !ok {
			continue
		}
		labels = append(labels, phase.label+" "+verdict.Value)
	}
	return labels
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func validateDuplicateCanonicalFinding(item map[string]json.RawMessage, prefix, findingID string, state reportLedgerState) error {
	canonicalFindingID, err := requiredNonEmptyStringField(item, "canonical_finding_id")
	if err != nil {
		return fmt.Errorf("%s.%w", prefix, err)
	}
	if _, ok := state.Findings[canonicalFindingID]; !ok {
		return fmt.Errorf("%s.canonical_finding_id %q does not reference a known finding", prefix, canonicalFindingID)
	}
	dedup, ok := state.Verdicts[findingID]["deduplicate"]
	if !ok || dedup.Value != "duplicate" {
		return fmt.Errorf("%s.id %q is not backed by a duplicate deduplication verdict", prefix, findingID)
	}
	if dedup.CanonicalFindingID != canonicalFindingID {
		return fmt.Errorf("%s.canonical_finding_id = %q, want ledger value %q", prefix, canonicalFindingID, dedup.CanonicalFindingID)
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

func validateMarkdownReportCoversAllFindings(markdown []byte, state reportLedgerState) error {
	ids := make([]string, 0, len(state.Findings))
	for id := range state.Findings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if markdownContainsFindingID(markdown, id) {
			continue
		}
		return fmt.Errorf("markdown report missing finding %s", id)
	}
	return nil
}

func markdownContainsFindingID(markdown []byte, id string) bool {
	if id == "" {
		return false
	}
	needle := []byte(id)
	for start := 0; start < len(markdown); {
		index := bytes.Index(markdown[start:], needle)
		if index < 0 {
			return false
		}
		index += start
		end := index + len(needle)
		if (index == 0 || !isFindingIDChar(markdown[index-1])) && (end == len(markdown) || !isFindingIDChar(markdown[end])) {
			return true
		}
		start = index + 1
	}
	return false
}

func validateMarkdownReportCoversEvidencePaths(markdown []byte, evidencePaths map[string]bool) error {
	paths := make([]string, 0, len(evidencePaths))
	for path := range evidencePaths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		if markdownContainsEvidencePath(markdown, path) {
			continue
		}
		return fmt.Errorf("markdown report missing evidence path %s", path)
	}
	return nil
}

func markdownContainsEvidencePath(markdown []byte, path string) bool {
	if path == "" {
		return false
	}
	needle := []byte(path)
	for start := 0; start < len(markdown); {
		index := bytes.Index(markdown[start:], needle)
		if index < 0 {
			return false
		}
		index += start
		end := index + len(needle)
		if evidencePathHasStartBoundary(markdown, index) && evidencePathHasEndBoundary(markdown, end) {
			return true
		}
		start = index + 1
	}
	return false
}

func evidencePathHasStartBoundary(markdown []byte, index int) bool {
	return index == 0 || !isEvidencePathChar(markdown[index-1])
}

func evidencePathHasEndBoundary(markdown []byte, end int) bool {
	if end == len(markdown) {
		return true
	}
	if markdown[end] == '.' {
		return end+1 == len(markdown) || !isFindingIDChar(markdown[end+1]) && markdown[end+1] != '/'
	}
	return !isEvidencePathChar(markdown[end])
}

func isFindingIDChar(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' ||
		value == '_' ||
		value == '-'
}

func isEvidencePathChar(value byte) bool {
	return isFindingIDChar(value) ||
		value == '.' ||
		value == '/'
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
