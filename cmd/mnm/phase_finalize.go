package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
)

func runFinalizeTask(runDir, runID, workspace string, cfg Config, opencodePath string) error {
	return runFinalizeTaskWithAttemptRunner(runDir, runID, workspace, cfg, directOpenCodeTaskAttemptRunner{opencodePath: opencodePath})
}

func runFinalizeTaskWithAttemptRunner(runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner) error {
	return runFinalizeTaskWithAttemptRunnerContext(context.Background(), runDir, runID, workspace, cfg, attemptRunner)
}

func runFinalizeTaskWithAttemptRunnerContext(ctx context.Context, runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	task := TaskRecord{
		RunID:       runID,
		TaskID:      "task_finalize",
		Phase:       "finalize",
		Title:       "Finalize report",
		Instruction: "Render the final Markdown and JSON audit reports from the ledger and evidence files.",
	}
	if report, ok, err := latestFinalizedReportForTask(runDir, task.TaskID); err != nil {
		return err
	} else if ok && ledgerTaskCompleted(runDir, task.TaskID) {
		return validateFinalizedReport(runDir, task, report)
	} else if ok {
		if err := removeIncompleteFinalizeArtifacts(runDir, report); err != nil {
			return err
		}
	}
	taskPath := filepath.Join(runDir, "tasks", task.TaskID+".json")
	if err := writeTaskFile(taskPath, task); err != nil {
		return err
	}
	if err := registerTaskStarted(runDir, task, nil); err != nil {
		return err
	}

	contextRel := filepath.ToSlash(filepath.Join("evidence", "finalize-context.json"))
	contextPath := filepath.Join(runDir, filepath.FromSlash(contextRel))
	contextJSON, err := buildFinalizeContext(runDir, runID)
	if err != nil {
		return err
	}
	if err := os.WriteFile(contextPath, contextJSON, filePerm); err != nil {
		return err
	}
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:              runID,
		TaskID:             task.TaskID,
		Kind:               "json",
		Title:              "Finalize compact context",
		Path:               contextRel,
		AllowContentChange: true,
	}); err != nil {
		return err
	}

	handoffRel, err := preparePhaseHandoffContext(runDir, runID, task, "", "")
	if err != nil {
		return err
	}

	taskWorkspace, cleanupWorkspace, err := prepareTaskWorkspace(workspace, runID, task.TaskID)
	if err != nil {
		return err
	}
	defer cleanupWorkspace()

	prompt, err := finalizePrompt(runDir, taskWorkspace, cfg, handoffRel)
	if err != nil {
		return err
	}
	promptRel := filepath.ToSlash(filepath.Join("evidence", "finalize-prompt.md"))
	promptPath := filepath.Join(runDir, filepath.FromSlash(promptRel))
	if err := os.WriteFile(promptPath, []byte(prompt), filePerm); err != nil {
		return err
	}
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:              runID,
		TaskID:             task.TaskID,
		Kind:               "markdown",
		Title:              "Finalize prompt",
		Path:               promptRel,
		AllowContentChange: true,
	}); err != nil {
		return err
	}

	logRel := filepath.ToSlash(filepath.Join("evidence", "opencode-finalize.jsonl"))
	logPath := filepath.Join(runDir, filepath.FromSlash(logRel))
	if err := runOpenCodeTaskWithAttemptRunnerContext(ctx, attemptRunner, taskWorkspace, runDir, opencodeTask{
		RunID:    runID,
		TaskID:   task.TaskID,
		Phase:    task.Phase,
		Title:    "mnm finalize",
		Model:    phaseModel(cfg, "finalize"),
		Prompt:   prompt,
		LogPath:  logPath,
		TaskFile: taskPath,
		Timeout:  openCodeTaskTimeout(cfg),
		Setup:    cfg.Runner.Setup,
		Verify: func(verifyRunDir string) error {
			report, ok, err := latestFinalizedReportForTask(verifyRunDir, task.TaskID)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("finalize opencode task did not register report outputs")
			}
			if err := validateFinalizedReport(verifyRunDir, task, report); err != nil {
				return err
			}
			if !ledgerTaskCompleted(verifyRunDir, task.TaskID) {
				return fmt.Errorf("finalize opencode task did not complete task %s", task.TaskID)
			}
			return nil
		},
	}); err != nil {
		return err
	}
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:  runID,
		TaskID: task.TaskID,
		Kind:   "jsonl",
		Title:  "OpenCode Finalize transcript",
		Path:   logRel,
	}); err != nil {
		return err
	}
	return nil
}

func latestFinalizedReportForTask(runDir, taskID string) (ReportRecord, bool, error) {
	reports, err := ledgerReports(runDir)
	if err != nil {
		return ReportRecord{}, false, err
	}
	for i := len(reports) - 1; i >= 0; i-- {
		if reports[i].TaskID == taskID {
			return reports[i], true, nil
		}
	}
	return ReportRecord{}, false, nil
}

func validateFinalizedReport(runDir string, task TaskRecord, report ReportRecord) error {
	if report.MarkdownPath == "" || report.JSONPath == "" {
		return fmt.Errorf("finalized report %s is missing markdown_path or json_path", report.ID)
	}
	if err := validateReportArtifacts(runDir, task, report.MarkdownPath, report.JSONPath); err != nil {
		return fmt.Errorf("finalized report %s failed validation: %w", report.ID, err)
	}
	return nil
}

func removeIncompleteFinalizeArtifacts(runDir string, report ReportRecord) error {
	for _, relPath := range []string{report.MarkdownPath, report.JSONPath} {
		if strings.TrimSpace(relPath) == "" {
			continue
		}
		if err := validateTaskBundleRelPath(relPath); err != nil {
			continue
		}
		path, err := taskBundleArtifactTargetPath(runDir, relPath)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove incomplete finalize artifact %s: %w", relPath, err)
		}
	}
	return nil
}

func finalizePrompt(runDir, workspace string, cfg Config, handoffRel string) (string, error) {
	leads, err := ledgerLeads(runDir)
	if err != nil {
		return "", err
	}
	findings, err := ledgerFindings(runDir)
	if err != nil {
		return "", err
	}
	verdicts, err := ledgerVerdicts(runDir)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`# makenomistakes Finalize

You are running inside an isolated VM. Your job is to turn the validated audit ledger and evidence files into final durable reports through the injected mnm CLI.

Workspace: %[1]s
Run directory: %[2]s
Ledger path: %[2]s/events.jsonl
Evidence directory: %[2]s/evidence
Markdown report path: %[2]s/report.md
JSON report path: %[2]s/report.json
Lead count: %[3]d
Finding count: %[4]d
Verdict count: %[5]d

Scope instructions:

%[6]s

Important context files:

- %[2]s/evidence/finalize-context.json
- %[2]s/%[7]s
- %[2]s/evidence/recon-codebase-map.md
- %[2]s/evidence/recon-risk-register.md
- %[2]s/events.jsonl

Required actions:

1. Run: mnm task current
2. Read %[2]s/evidence/finalize-context.json first. It is a compact, host-generated view of the ledger containing every finding, its bucket/status, exact verdict labels, recommended evidence paths, validation proof paths, and affected path candidates that pass the workspace manifest check.
3. Read %[2]s/%[7]s for structured phase handoff context including setup logs, task handoffs, confirmed dead ends, open leads, and blockers. Use it to preserve nuance about blocked validation and plausible-but-unproven areas.
4. Use the compact context as the source of truth for JSON shape, buckets, statuses, verdict labels, evidence paths, and affected paths. For prose, treat validation notes and verdict details as higher authority than the original finding body because review and validation may narrow or retract earlier claims.
5. Do not read opencode-*.jsonl transcripts or the full raw events.jsonl unless report validation fails and the compact context is insufficient to debug it. These transcript files are large and not needed for the final report.
6. Write a human-readable Markdown report to %[2]s/report.md.
7. Write a structured JSON report to %[2]s/report.json.
8. Register both reports with: mnm report finalize --markdown %[2]s/report.md --json %[2]s/report.json
9. Complete the task with: mnm task complete --status completed --summary "Finalized report"

Markdown report requirements:

- Start with a concise executive summary.
- List proven findings first, then inconclusive findings, failed validations, rejected findings, and duplicate findings.
- For each finding, include ID, title, severity, confidence, status, affected paths when known, evidence paths, reproduction or validation summary, and limits of confidence.
- Mention every ledger finding ID exactly as written in the final Markdown report.
- Mention every cited evidence path exactly as written in the final Markdown report.
- Every path listed in JSON "evidence_paths" must also appear literally in the Markdown report.
- If there are no findings, say that clearly and summarize what phases ran.
- Preserve nuance. A rejected, failed, duplicate, or inconclusive finding must not be presented as proven.
- Do not upgrade a configuration weakness into a stronger exploit claim unless the validation evidence proves that exact exploit. For example, a hardcoded session signing secret is not by itself proof of forged authenticated sessions when the application uses a server-side session store.

JSON report requirements:

- Produce a single JSON object.
- Include "run_id", "counts", "report_paths", and arrays named "proven", "inconclusive", "failed", "rejected", "duplicate", and "unvalidated".
- "counts" must include integer fields "findings_proven", "findings_inconclusive", "findings_failed", "findings_rejected", "findings_duplicate", and "findings_unvalidated"; each count must match the corresponding array length.
- "report_paths.markdown" must be exactly "report.md" and "report_paths.json" must be exactly "report.json". Do not use absolute VM paths such as "%[2]s/report.md" in the JSON; the reports must stay portable after the task bundle is copied back to the host.
- For each finding object, include id, title, category, severity, confidence, source_lead_id, status, verdicts, evidence_paths, summary, and affected_paths when known.
- For each duplicate finding object, also include canonical_finding_id matching the deduplication verdict.
- "verdicts" must exactly match ledger verdicts in phase order using strings like "review accepted", "deduplicate canonical", and "validation proven".
- Use empty arrays instead of null for absent lists.
- "affected_paths" entries must be clean slash-separated relative workspace paths, never absolute paths, empty strings, or paths containing ".." traversal.
- The JSON must parse with standard JSON parsers.
- "id" must be the real ledger ID of a finding, and each "evidence_paths" entry must exactly match a run-relative evidence path registered for that finding through mnm evidence add.
- Every finding in "proven" must include at least one evidence_paths entry for a validation proof artifact registered by that finding's Validate task before the proven verdict.
- "status" must match exact ledger progress: no review uses candidate, accepted review before deduplication uses reviewed, canonical deduplication before validation uses validation_pending, proven uses validation_proven, inconclusive uses validation_inconclusive, failed uses validation_failed, rejected uses review_rejected, and duplicate uses duplicate.
- Place each finding in the bucket proven by the ledger verdicts. A finding with validation failed belongs in "failed", not "proven"; a review-rejected finding belongs in "rejected"; a deduplicate duplicate belongs in "duplicate".
- If %[2]s/evidence/runner-manifest.json is present, every affected_paths entry must exist in its workspace_files list.
- Every ledger finding must appear in exactly one report bucket.
`, workspace, runDir, len(leads), len(findings), len(verdicts), scopeText(cfg), handoffRel), nil
}

type finalizeContextFile struct {
	RunID      string                    `json:"run_id"`
	ReportPath finalizeContextReportPath `json:"report_paths"`
	Counts     map[string]int            `json:"counts"`
	Buckets    map[string][]string       `json:"buckets"`
	Findings   []finalizeFindingContext  `json:"findings"`
}

type finalizeContextReportPath struct {
	Markdown string `json:"markdown"`
	JSON     string `json:"json"`
}

type finalizeFindingContext struct {
	ID                           string                    `json:"id"`
	Title                        string                    `json:"title"`
	Category                     string                    `json:"category"`
	Severity                     string                    `json:"severity"`
	Confidence                   string                    `json:"confidence"`
	SourceLeadID                 string                    `json:"source_lead_id"`
	Status                       string                    `json:"status"`
	Bucket                       string                    `json:"bucket"`
	CanonicalFindingID           string                    `json:"canonical_finding_id,omitempty"`
	FindingBodyPath              string                    `json:"finding_body_path"`
	FindingBodyExcerpt           string                    `json:"finding_body_excerpt,omitempty"`
	ValidationNotesPath          string                    `json:"validation_notes_path,omitempty"`
	ValidationNotesExcerpt       string                    `json:"validation_notes_excerpt,omitempty"`
	Verdicts                     []string                  `json:"verdicts"`
	VerdictDetails               []finalizeVerdictContext  `json:"verdict_details"`
	RecommendedEvidencePaths     []string                  `json:"recommended_evidence_paths"`
	ValidationProofEvidencePaths []string                  `json:"validation_proof_evidence_paths"`
	RegisteredEvidence           []finalizeEvidenceContext `json:"registered_evidence"`
	AffectedPathCandidates       []string                  `json:"affected_path_candidates"`
}

type finalizeVerdictContext struct {
	Phase              string `json:"phase"`
	Value              string `json:"value"`
	Reason             string `json:"reason"`
	CanonicalFindingID string `json:"canonical_finding_id,omitempty"`
}

type finalizeEvidenceContext struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	TaskID string `json:"task_id"`
}

func buildFinalizeContext(runDir, runID string) ([]byte, error) {
	state, err := reportKnownState(runDir)
	if err != nil {
		return nil, err
	}
	findings, err := ledgerFindings(runDir)
	if err != nil {
		return nil, err
	}
	workspaceFiles := sortedMapKeys(state.WorkspaceFiles)
	context := finalizeContextFile{
		RunID: runID,
		ReportPath: finalizeContextReportPath{
			Markdown: "report.md",
			JSON:     "report.json",
		},
		Counts: map[string]int{
			"findings_proven":       0,
			"findings_inconclusive": 0,
			"findings_failed":       0,
			"findings_rejected":     0,
			"findings_duplicate":    0,
			"findings_unvalidated":  0,
		},
		Buckets: map[string][]string{
			"proven":       {},
			"inconclusive": {},
			"failed":       {},
			"rejected":     {},
			"duplicate":    {},
			"unvalidated":  {},
		},
	}

	for _, finding := range findings {
		verdicts := state.Verdicts[finding.ID]
		bucket := reportBucketForFinding(verdicts)
		status := reportStatusForFinding(verdicts)
		context.Counts["findings_"+bucket]++
		context.Buckets[bucket] = append(context.Buckets[bucket], finding.ID)

		item, err := buildFinalizeFindingContext(runDir, finding, verdicts, state.FindingEvidence[finding.ID], workspaceFiles, bucket, status)
		if err != nil {
			return nil, err
		}
		context.Findings = append(context.Findings, item)
	}
	data, err := json.MarshalIndent(context, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	return data, nil
}

func buildFinalizeFindingContext(runDir string, finding FindingRecord, verdicts map[string]VerdictRecord, evidence map[string]EvidenceRecord, workspaceFiles []string, bucket, status string) (finalizeFindingContext, error) {
	item := finalizeFindingContext{
		ID:                 finding.ID,
		Title:              finding.Title,
		Category:           finding.Category,
		Severity:           finding.Severity,
		Confidence:         finding.Confidence,
		SourceLeadID:       finding.LeadID,
		Status:             status,
		Bucket:             bucket,
		FindingBodyPath:    finding.BodyPath,
		Verdicts:           reportVerdictLabels(verdicts),
		RegisteredEvidence: []finalizeEvidenceContext{},
	}
	if dedup, ok := verdicts["deduplicate"]; ok && dedup.Value == "duplicate" {
		item.CanonicalFindingID = dedup.CanonicalFindingID
	}
	body, err := readRunFileExcerpt(runDir, finding.BodyPath, 2500)
	if err != nil {
		return item, err
	}
	item.FindingBodyExcerpt = body

	if validateVerdict, ok := verdicts["validate"]; ok {
		notesRel := validationNotesRelPath(finding.ID)
		if notes, ok := evidence[notesRel]; ok && notes.TaskID == validateVerdict.TaskID {
			item.ValidationNotesPath = notesRel
			notesExcerpt, err := readRunFileExcerpt(runDir, notesRel, 5000)
			if err != nil {
				return item, err
			}
			item.ValidationNotesExcerpt = notesExcerpt
		}
	}

	for _, phase := range []string{"review", "deduplicate", "validate"} {
		verdict, ok := verdicts[phase]
		if !ok {
			continue
		}
		item.VerdictDetails = append(item.VerdictDetails, finalizeVerdictContext{
			Phase:              verdict.Phase,
			Value:              verdict.Value,
			Reason:             verdict.Reason,
			CanonicalFindingID: verdict.CanonicalFindingID,
		})
	}

	evidenceItems := sortedEvidenceRecords(evidence)
	validateVerdict, hasValidateVerdict := verdicts["validate"]
	recommended := map[string]bool{}
	for _, evidenceItem := range evidenceItems {
		item.RegisteredEvidence = append(item.RegisteredEvidence, finalizeEvidenceContext{
			Path:   evidenceItem.Path,
			Kind:   evidenceItem.Kind,
			Title:  evidenceItem.Title,
			TaskID: evidenceItem.TaskID,
		})
		if hasValidateVerdict &&
			evidenceItem.TaskID == validateVerdict.TaskID &&
			evidenceItem.eventIndex < validateVerdict.eventIndex &&
			isValidationProofArtifact(finding.ID, evidenceItem) {
			item.ValidationProofEvidencePaths = append(item.ValidationProofEvidencePaths, evidenceItem.Path)
			recommended[evidenceItem.Path] = true
		}
	}
	if item.ValidationNotesPath != "" {
		recommended[item.ValidationNotesPath] = true
	}
	for _, evidenceItem := range evidenceItems {
		if recommended[evidenceItem.Path] || strings.HasPrefix(filepath.Base(evidenceItem.Path), "opencode-") {
			continue
		}
		if strings.Contains(evidenceItem.Path, "-prompt.") {
			continue
		}
		if len(recommended) >= 4 {
			break
		}
		recommended[evidenceItem.Path] = true
	}
	item.RecommendedEvidencePaths = sortedMapKeys(recommended)

	searchText := item.FindingBodyExcerpt + "\n" + item.ValidationNotesExcerpt
	for _, detail := range item.VerdictDetails {
		searchText += "\n" + detail.Reason
	}
	item.AffectedPathCandidates = affectedPathCandidates(searchText, workspaceFiles)
	return item, nil
}

func readRunFileExcerpt(runDir, relPath string, limit int) (string, error) {
	if strings.TrimSpace(relPath) == "" {
		return "", nil
	}
	path := filepath.Join(runDir, filepath.FromSlash(relPath))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", relPath, err)
	}
	text := strings.TrimSpace(string(data))
	if len(text) <= limit {
		return text, nil
	}
	headLimit := limit / 2
	tailLimit := limit - headLimit
	head := strings.TrimSpace(text[:headLimit])
	tail := strings.TrimSpace(text[len(text)-tailLimit:])
	return head + "\n\n[...snip...]\n\n" + tail, nil
}

func affectedPathCandidates(text string, workspaceFiles []string) []string {
	if strings.TrimSpace(text) == "" || len(workspaceFiles) == 0 {
		return nil
	}
	basenameCounts := map[string]int{}
	for _, path := range workspaceFiles {
		basenameCounts[pathpkg.Base(path)]++
	}
	seen := map[string]bool{}
	for _, path := range workspaceFiles {
		basename := pathpkg.Base(path)
		if strings.Contains(text, path) ||
			strings.Contains(text, stripFirstPathComponent(path)) ||
			(basenameCounts[basename] == 1 && containsPathToken(text, basename)) {
			seen[path] = true
		}
	}
	return sortedMapKeys(seen)
}

func containsPathToken(text, token string) bool {
	for offset := 0; ; {
		index := strings.Index(text[offset:], token)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(token)
		if isPathTokenBoundary(text, start-1) && isPathTokenBoundary(text, end) {
			return true
		}
		offset = end
	}
}

func isPathTokenBoundary(text string, index int) bool {
	if index < 0 || index >= len(text) {
		return true
	}
	ch := text[index]
	return !((ch >= 'a' && ch <= 'z') ||
		(ch >= 'A' && ch <= 'Z') ||
		(ch >= '0' && ch <= '9') ||
		ch == '_' ||
		ch == '-' ||
		ch == '.' ||
		ch == '/')
}

func stripFirstPathComponent(path string) string {
	if index := strings.Index(path, "/"); index >= 0 && index+1 < len(path) {
		return path[index+1:]
	}
	return path
}

func sortedMapKeys[V any](items map[string]V) []string {
	keys := make([]string, 0, len(items))
	for key := range items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedEvidenceRecords(items map[string]EvidenceRecord) []EvidenceRecord {
	records := make([]EvidenceRecord, 0, len(items))
	for _, item := range items {
		records = append(records, item)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].eventIndex != records[j].eventIndex {
			return records[i].eventIndex < records[j].eventIndex
		}
		return records[i].Path < records[j].Path
	})
	return records
}
