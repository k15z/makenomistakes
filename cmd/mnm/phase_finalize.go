package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func runFinalizeTask(runDir, runID, workspace string, cfg Config, opencodePath string) error {
	if ledgerReportFinalized(runDir) && ledgerTaskCompleted(runDir, "task_finalize") {
		return nil
	}
	task := TaskRecord{
		RunID:       runID,
		TaskID:      "task_finalize",
		Phase:       "finalize",
		Title:       "Finalize report",
		Instruction: "Render the final Markdown and JSON audit reports from the ledger and evidence files.",
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
		TaskID:   task.TaskID,
		Phase:    task.Phase,
		Title:    "mnm finalize",
		Model:    phaseModel(cfg, "finalize"),
		Prompt:   prompt,
		LogPath:  logPath,
		TaskFile: taskPath,
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
	if !ledgerReportFinalized(runDir) {
		return fmt.Errorf("finalize opencode task did not register report outputs")
	}
	if !ledgerTaskCompleted(runDir, task.TaskID) {
		return fmt.Errorf("finalize opencode task did not complete task %s", task.TaskID)
	}
	if err := validateFinalReport(runDir); err != nil {
		return err
	}
	return nil
}

func validateFinalReport(runDir string) error {
	reports, err := ledgerReports(runDir)
	if err != nil {
		return err
	}
	if len(reports) == 0 {
		return fmt.Errorf("no finalized report registered")
	}
	report := reports[len(reports)-1]
	if report.MarkdownPath == "" {
		return fmt.Errorf("finalized report is missing markdown path")
	}
	if report.JSONPath == "" {
		return fmt.Errorf("finalized report is missing JSON path")
	}
	if _, err := os.Stat(filepath.Join(runDir, filepath.FromSlash(report.MarkdownPath))); err != nil {
		return fmt.Errorf("read finalized Markdown report: %w", err)
	}
	jsonPath := filepath.Join(runDir, filepath.FromSlash(report.JSONPath))
	jsonBytes, err := os.ReadFile(jsonPath)
	if err != nil {
		return fmt.Errorf("read finalized JSON report: %w", err)
	}
	if !json.Valid(jsonBytes) {
		return fmt.Errorf("finalized report JSON is not valid JSON: %s", report.JSONPath)
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
- If there are no findings, say that clearly and summarize what phases ran.
- Preserve nuance. A rejected, failed, duplicate, or inconclusive finding must not be presented as proven.

JSON report requirements:

- Produce a single JSON object.
- Include run metadata, counts, generated report paths, and arrays for proven, inconclusive, failed, rejected, duplicate, and unvalidated findings.
- For each finding object, include id, title, category, severity, confidence, source_lead_id, status, verdicts, evidence_paths, summary, and affected_paths when known.
- Use empty arrays instead of null for absent lists.
- The JSON must parse with standard JSON parsers.
`, workspace, runDir, len(leads), len(findings), len(verdicts), scopeText(cfg)), nil
}
