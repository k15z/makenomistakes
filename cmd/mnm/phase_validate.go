package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func runValidatePhase(runDir, runID, workspace string, cfg Config, opencodePath string) error {
	findings, err := unvalidatedCanonicalFindings(runDir)
	if err != nil {
		return err
	}
	for _, finding := range findings {
		if err := runValidateTask(runDir, runID, workspace, cfg, opencodePath, finding); err != nil {
			return err
		}
	}
	return nil
}

func runValidateTask(runDir, runID, workspace string, cfg Config, opencodePath string, finding FindingRecord) error {
	safeFindingID := safeFileID(finding.ID)
	task := TaskRecord{
		RunID:       runID,
		TaskID:      "task_validate_" + safeFindingID,
		Phase:       "validate",
		Title:       "Validate: " + finding.Title,
		Instruction: "Attempt an end-to-end reproduction or exploit for one canonical finding and record a validation verdict.",
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
			"phase":      task.Phase,
			"title":      task.Title,
			"finding_id": finding.ID,
		},
	}); err != nil {
		return err
	}

	taskWorkspace, cleanupWorkspace, err := prepareTaskWorkspace(workspace, runID, task.TaskID)
	if err != nil {
		return err
	}
	defer cleanupWorkspace()

	prompt, err := validatePrompt(runDir, taskWorkspace, cfg, finding)
	if err != nil {
		return err
	}
	promptRel := filepath.ToSlash(filepath.Join("evidence", "validate-"+safeFindingID+"-prompt.md"))
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
			"kind":       "markdown",
			"title":      "Validate prompt: " + finding.Title,
			"path":       promptRel,
			"finding_id": finding.ID,
		},
	}); err != nil {
		return err
	}

	logRel := filepath.ToSlash(filepath.Join("evidence", "opencode-validate-"+safeFindingID+".jsonl"))
	logPath := filepath.Join(runDir, filepath.FromSlash(logRel))
	if err := runOpenCodeTask(opencodePath, taskWorkspace, runDir, opencodeTask{
		TaskID:    task.TaskID,
		Phase:     task.Phase,
		FindingID: finding.ID,
		Title:     "mnm validate " + safeFindingID,
		Model:     phaseModel(cfg, "validate"),
		Prompt:    prompt,
		LogPath:   logPath,
		TaskFile:  taskPath,
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
			"kind":       "jsonl",
			"title":      "OpenCode Validate transcript: " + finding.Title,
			"path":       logRel,
			"finding_id": finding.ID,
		},
	}); err != nil {
		return err
	}
	if !ledgerFindingHasVerdict(runDir, finding.ID, "validate") {
		return fmt.Errorf("validate opencode task did not record validation verdict for finding %s", finding.ID)
	}
	if !ledgerTaskCompleted(runDir, task.TaskID) {
		return fmt.Errorf("validate opencode task did not complete task %s", task.TaskID)
	}
	return nil
}

func validatePrompt(runDir, workspace string, cfg Config, finding FindingRecord) (string, error) {
	bodyPath, err := findingBodyPath(runDir, finding)
	if err != nil {
		return "", err
	}
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		return "", fmt.Errorf("read finding body %s: %w", finding.BodyPath, err)
	}
	findingEvidence, err := ledgerEvidenceForFinding(runDir, finding.ID)
	if err != nil {
		return "", err
	}
	reviewText := verdictSummary(runDir, finding.ID, "review")
	dedupText := verdictSummary(runDir, finding.ID, "deduplicate")
	sourceLead := sourceLeadText(runDir, finding.LeadID)
	safeFindingID := safeFileID(finding.ID)

	return fmt.Sprintf(`# makenomistakes Validate

You are running inside an isolated VM. Your job is to make a serious end-to-end attempt to prove or falsify one canonical finding and write durable validation state through the injected mnm CLI.

Workspace: %[1]s
Run directory: %[2]s
Finding ID: %[3]s
Finding title: %[4]s
Finding category: %[5]s
Finding severity: %[6]s
Finding confidence: %[7]s
Source lead ID: %[8]s

Scope instructions:

%[9]s

Recon context files:

- %[2]s/evidence/recon-codebase-map.md
- %[2]s/evidence/recon-risk-register.md

Finding body path: %[10]s

Finding body:

%[11]s

Finding evidence:

%[12]s

Review verdict:

%[13]s

Deduplicate verdict:

%[14]s

Source lead context:

%[15]s

Required actions:

1. Run: mnm task current
2. Read the finding body, prior review and deduplication verdicts, attached evidence, recon context, and relevant source code.
3. Treat the workspace as a disposable per-task copy. Write durable audit artifacts only under the run directory.
4. Keep filesystem searches scoped to the workspace and run directory. Do not run broad host filesystem scans such as find / or inspect host mounts like /Users; use /tmp only for temporary tools or repro files.
5. Attempt the highest-fidelity reproduction or exploit that is feasible inside this VM. Build services, run tests, start dev servers, use Docker/Compose/minikube if available and scoped to this workspace, seed data, send requests, inject malformed inputs, trigger crashes, or write small proof scripts as needed.
6. Write validation notes, commands, observed output, blockers, and any proof artifacts to %[2]s/evidence/validate-%[16]s-notes.md.
7. Register the notes with: mnm evidence add --kind markdown --title "Validation notes: %[4]s" --finding %[3]s --path %[2]s/evidence/validate-%[16]s-notes.md
8. If you observed the claimed failure, exploit path, crash, data exposure, or other concrete impact, record: mnm verdict record --finding %[3]s --phase validate --value proven --reason "..."
9. If focused validation contradicts the finding or shows it is not reachable/applicable, record: mnm verdict record --finding %[3]s --phase validate --value failed --reason "..."
10. If the environment, dependencies, missing services, credentials, or time prevent a fair proof while the finding remains plausible, record: mnm verdict record --finding %[3]s --phase validate --value inconclusive --reason "..."
11. Complete the task with: mnm task complete --status completed --summary "Validated %[3]s"

Validation quality bar:

- Prefer runnable proof commands, request/response captures, logs, stack traces, and minimized reproduction scripts over prose.
- Do not mark proven without observing concrete behavior.
- Do not mark failed merely because full setup is hard; use inconclusive when the environment blocks a fair test.
- Keep any long-running servers, containers, or background processes scoped to this disposable VM task.
`, workspace, runDir, finding.ID, finding.Title, finding.Category, finding.Severity, finding.Confidence, finding.LeadID, scopeText(cfg), bodyPath, string(body), formatEvidenceList(runDir, findingEvidence), reviewText, dedupText, sourceLead, safeFindingID), nil
}

func verdictSummary(runDir, findingID, phase string) string {
	verdict, ok, err := ledgerFindingVerdict(runDir, findingID, phase)
	if err != nil {
		return fmt.Sprintf("Could not read %s verdict: %v", phase, err)
	}
	if !ok {
		return "No " + phase + " verdict was found."
	}
	if verdict.CanonicalFindingID != "" {
		return fmt.Sprintf("Value: %s\nReason: %s\nCanonical finding: %s", verdict.Value, verdict.Reason, verdict.CanonicalFindingID)
	}
	return fmt.Sprintf("Value: %s\nReason: %s", verdict.Value, verdict.Reason)
}
