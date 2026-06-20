package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func runFinalizeTask(runDir, runID, workspace string, cfg Config, opencodePath string) error {
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
	}
	taskPath := filepath.Join(runDir, "tasks", task.TaskID+".json")
	if err := writeTaskFile(taskPath, task); err != nil {
		return err
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    runID,
		Type:     "task.started",
		Object:   "task",
		ObjectID: task.TaskID,
		TaskID:   task.TaskID,
		Data: map[string]any{
			"phase": task.Phase,
			"title": task.Title,
		},
	}); err != nil {
		return err
	}

	taskWorkspace, cleanupWorkspace, err := prepareTaskWorkspace(workspace, runID, task.TaskID)
	if err != nil {
		return err
	}
	defer cleanupWorkspace()

	prompt, err := finalizePrompt(runDir, taskWorkspace, cfg)
	if err != nil {
		return err
	}
	promptRel := filepath.ToSlash(filepath.Join("evidence", "finalize-prompt.md"))
	promptPath := filepath.Join(runDir, filepath.FromSlash(promptRel))
	if err := os.WriteFile(promptPath, []byte(prompt), filePerm); err != nil {
		return err
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    runID,
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: newLedgerID("evidence"),
		TaskID:   task.TaskID,
		Data: map[string]any{
			"kind":  "markdown",
			"title": "Finalize prompt",
			"path":  promptRel,
		},
	}); err != nil {
		return err
	}

	logRel := filepath.ToSlash(filepath.Join("evidence", "opencode-finalize.jsonl"))
	logPath := filepath.Join(runDir, filepath.FromSlash(logRel))
	if err := runOpenCodeTask(opencodePath, taskWorkspace, runDir, opencodeTask{
		RunID:    runID,
		TaskID:   task.TaskID,
		Phase:    task.Phase,
		Title:    "mnm finalize",
		Model:    phaseModel(cfg, "finalize"),
		Prompt:   prompt,
		LogPath:  logPath,
		TaskFile: taskPath,
		Verify: func() error {
			report, ok, err := latestFinalizedReportForTask(runDir, task.TaskID)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("finalize opencode task did not register report outputs")
			}
			if err := validateFinalizedReport(runDir, task, report); err != nil {
				return err
			}
			if !ledgerTaskCompleted(runDir, task.TaskID) {
				return fmt.Errorf("finalize opencode task did not complete task %s", task.TaskID)
			}
			return nil
		},
	}); err != nil {
		return err
	}
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    runID,
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: newLedgerID("evidence"),
		TaskID:   task.TaskID,
		Data: map[string]any{
			"kind":  "jsonl",
			"title": "OpenCode Finalize transcript",
			"path":  logRel,
		},
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

func finalizePrompt(runDir, workspace string, cfg Config) (string, error) {
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

- %[2]s/evidence/recon-codebase-map.md
- %[2]s/evidence/recon-risk-register.md
- %[2]s/events.jsonl

Required actions:

1. Run: mnm task current
2. Read the ledger and the evidence files needed to explain findings accurately. Do not invent evidence, commands, files, or impact that is not present in the run directory.
3. Write a human-readable Markdown report to %[2]s/report.md.
4. Write a structured JSON report to %[2]s/report.json.
5. Register both reports with: mnm report finalize --markdown %[2]s/report.md --json %[2]s/report.json
6. Complete the task with: mnm task complete --status completed --summary "Finalized report"

Markdown report requirements:

- Start with a concise executive summary.
- List proven findings first, then inconclusive findings, failed validations, rejected findings, and duplicate findings.
- For each finding, include ID, title, severity, confidence, status, affected paths when known, evidence paths, reproduction or validation summary, and limits of confidence.
- Mention every ledger finding ID exactly as written in the final Markdown report.
- If there are no findings, say that clearly and summarize what phases ran.
- Preserve nuance. A rejected, failed, duplicate, or inconclusive finding must not be presented as proven.

JSON report requirements:

- Produce a single JSON object.
- Include "run_id", "counts", "report_paths", and arrays named "proven", "inconclusive", "failed", "rejected", "duplicate", and "unvalidated".
- "counts" must include integer fields "findings_proven", "findings_inconclusive", "findings_failed", "findings_rejected", "findings_duplicate", and "findings_unvalidated"; each count must match the corresponding array length.
- "report_paths.markdown" and "report_paths.json" must point to the same files passed to "mnm report finalize".
- For each finding object, include id, title, category, severity, confidence, source_lead_id, status, verdicts, evidence_paths, summary, and affected_paths when known.
- For each duplicate finding object, also include canonical_finding_id matching the deduplication verdict.
- "verdicts" must exactly match ledger verdicts in phase order using strings like "review accepted", "deduplicate canonical", and "validation proven".
- Use empty arrays instead of null for absent lists.
- "affected_paths" entries must be clean slash-separated relative workspace paths, never absolute paths, empty strings, or paths containing ".." traversal.
- The JSON must parse with standard JSON parsers.
- "id" must be the real ledger ID of a finding, and each "evidence_paths" entry must exactly match a run-relative evidence path registered for that finding through mnm evidence add.
- Every finding in "proven" must include at least one evidence_paths entry.
- "status" must match exact ledger progress: no review uses candidate, accepted review before deduplication uses reviewed, canonical deduplication before validation uses validation_pending, proven uses validation_proven, inconclusive uses validation_inconclusive, failed uses validation_failed, rejected uses review_rejected, and duplicate uses duplicate.
- Place each finding in the bucket proven by the ledger verdicts. A finding with validation failed belongs in "failed", not "proven"; a review-rejected finding belongs in "rejected"; a deduplicate duplicate belongs in "duplicate".
- Every ledger finding must appear in exactly one report bucket.
`, workspace, runDir, len(leads), len(findings), len(verdicts), scopeText(cfg)), nil
}
