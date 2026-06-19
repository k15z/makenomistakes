package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func runDeduplicatePhase(runDir, runID, workspace string, cfg Config, opencodePath string) error {
	findings, err := undeduplicatedLedgerFindings(runDir)
	if err != nil {
		return err
	}
	if len(findings) == 0 {
		return nil
	}

	task := TaskRecord{
		RunID:       runID,
		TaskID:      "task_deduplicate",
		Phase:       "deduplicate",
		Title:       "Deduplicate reviewed findings",
		Instruction: "Cluster review-accepted findings and record canonical or duplicate deduplication verdicts.",
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
			"phase":    task.Phase,
			"title":    task.Title,
			"findings": findingIDs(findings),
		},
	}); err != nil {
		return err
	}

	taskWorkspace, cleanupWorkspace, err := prepareTaskWorkspace(workspace, runID, task.TaskID)
	if err != nil {
		return err
	}
	defer cleanupWorkspace()

	prompt, err := deduplicatePrompt(runDir, taskWorkspace, cfg, findings)
	if err != nil {
		return err
	}
	promptRel := filepath.ToSlash(filepath.Join("evidence", "deduplicate-prompt.md"))
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
			"title": "Deduplicate prompt",
			"path":  promptRel,
		},
	}); err != nil {
		return err
	}

	logRel := filepath.ToSlash(filepath.Join("evidence", "opencode-deduplicate.jsonl"))
	logPath := filepath.Join(runDir, filepath.FromSlash(logRel))
	if err := runOpenCodeTask(opencodePath, taskWorkspace, runDir, opencodeTask{
		RunID:    runID,
		TaskID:   task.TaskID,
		Phase:    task.Phase,
		Title:    "mnm deduplicate",
		Model:    phaseModel(cfg, "deduplicate"),
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
			"title": "OpenCode Deduplicate transcript",
			"path":  logRel,
		},
	}); err != nil {
		return err
	}

	var missing []string
	for _, finding := range findings {
		if !ledgerFindingHasVerdict(runDir, finding.ID, "deduplicate") {
			missing = append(missing, finding.ID)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("deduplicate opencode task did not record verdicts for findings: %s", strings.Join(missing, ", "))
	}
	if !ledgerTaskCompleted(runDir, task.TaskID) {
		return errors.New("deduplicate opencode task did not complete task_deduplicate")
	}
	return nil
}

func deduplicatePrompt(runDir, workspace string, cfg Config, findings []FindingRecord) (string, error) {
	findingText, err := formatDeduplicateFindings(runDir, findings)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`# makenomistakes Deduplicate

You are running inside an isolated VM. Your job is to cluster review-accepted candidate findings and write durable deduplication verdicts through the injected mnm CLI.

Workspace: %[1]s
Run directory: %[2]s
Reviewed finding count: %[3]d

Scope instructions:

%[4]s

Recon context files:

- %[2]s/evidence/recon-codebase-map.md
- %[2]s/evidence/recon-risk-register.md

Review-accepted findings:

%[5]s

Required actions:

1. Run: mnm task current
2. Read the finding bodies, review notes, attached evidence, recon context, and relevant source code when needed to decide whether two findings describe the same root defect.
3. Treat the workspace as a disposable per-task copy. Write durable audit artifacts only under the run directory.
4. Keep filesystem searches scoped to the workspace and run directory. Do not run broad host filesystem scans such as find / or inspect host mounts like /Users; use /tmp only for temporary tools or repro files.
5. For every listed finding, record exactly one deduplicate verdict.
6. For a unique issue, record: mnm verdict record --finding FINDING_ID --phase deduplicate --value canonical --reason "..."
7. For a duplicate issue, record: mnm verdict record --finding FINDING_ID --phase deduplicate --value duplicate --canonical-finding CANONICAL_FINDING_ID --reason "Duplicate of CANONICAL_FINDING_ID because ..."
8. Complete the task with: mnm task complete --status completed --summary "Deduplicated reviewed findings"

Deduplication quality bar:

- Only mark duplicate when findings share the same root defect and would be fixed by substantially the same patch.
- Findings in the same file, framework, category, or symptom family are not automatically duplicates.
- Do not reject findings in Deduplicate. If a reviewed finding seems weak, mark it canonical and leave proof or failure to Validate.
- Prefer the clearest, most complete finding as the canonical finding for a duplicate cluster.
`, workspace, runDir, len(findings), scopeText(cfg), findingText), nil
}

func formatDeduplicateFindings(runDir string, findings []FindingRecord) (string, error) {
	var builder strings.Builder
	for _, finding := range findings {
		bodyPath, err := findingBodyPath(runDir, finding)
		if err != nil {
			return "", err
		}
		body, err := os.ReadFile(bodyPath)
		if err != nil {
			return "", fmt.Errorf("read finding body %s: %w", finding.BodyPath, err)
		}
		review, ok, err := ledgerFindingVerdict(runDir, finding.ID, "review")
		if err != nil {
			return "", err
		}
		reviewReason := "No review verdict was found."
		if ok {
			reviewReason = review.Reason
		}
		evidence, err := ledgerEvidenceForFinding(runDir, finding.ID)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&builder, `## %s

Title: %s
Category: %s
Severity: %s
Confidence: %s
Body path: %s
Review reason: %s

Evidence:

%s
Finding body:

%s

`, finding.ID, finding.Title, finding.Category, finding.Severity, finding.Confidence, bodyPath, reviewReason, formatEvidenceList(runDir, evidence), string(body))
	}
	return builder.String(), nil
}

func findingIDs(findings []FindingRecord) []string {
	ids := make([]string, 0, len(findings))
	for _, finding := range findings {
		ids = append(ids, finding.ID)
	}
	return ids
}
