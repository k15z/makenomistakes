package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func runInvestigatePhase(runDir, runID, workspace string, cfg Config, opencodePath string) error {
	return runInvestigatePhaseWithAttemptRunner(runDir, runID, workspace, cfg, directOpenCodeTaskAttemptRunner{opencodePath: opencodePath})
}

func runInvestigatePhaseWithAttemptRunner(runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner) error {
	return runInvestigatePhaseWithAttemptRunnerContext(context.Background(), runDir, runID, workspace, cfg, attemptRunner)
}

func runInvestigatePhaseWithAttemptRunnerContext(ctx context.Context, runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner) error {
	processed, invalidClosed, err := completedInvestigationLeads(runDir)
	if err != nil {
		return err
	}
	if len(invalidClosed) > 0 {
		return fmt.Errorf("investigate phase has closed leads with incomplete investigation tasks: %s", strings.Join(invalidClosed, ", "))
	}
	limit := maxInvestigations(cfg)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		leads, err := openLedgerLeads(runDir)
		if err != nil {
			return err
		}
		var batch []LeadRecord
		for _, lead := range leads {
			if !processed[lead.ID] {
				batch = append(batch, lead)
			}
		}
		if len(batch) == 0 {
			return nil
		}
		remaining := limit - len(processed)
		if remaining <= 0 {
			return appendInvestigateLimitReached(runDir, runID, limit, len(processed))
		}
		if len(batch) > remaining {
			batch = batch[:remaining]
		}
		if err := runInvestigateBatchWithAttemptRunnerContext(ctx, runDir, runID, workspace, cfg, attemptRunner, batch); err != nil {
			return err
		}
		for _, lead := range batch {
			processed[lead.ID] = true
		}
	}
}

func completedInvestigationLeads(runDir string) (map[string]bool, []string, error) {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return nil, nil, err
	}
	leadByTaskID := map[string]string{}
	seenLead := map[string]bool{}
	var leadOrder []string
	for _, event := range events {
		if event.Object != "task" || event.ObjectID == "" || !strings.HasPrefix(event.ObjectID, "task_investigate_") {
			continue
		}
		if event.Type == "task.started" {
			leadID, _ := event.Data["lead_id"].(string)
			if leadID != "" {
				if !seenLead[leadID] {
					seenLead[leadID] = true
					leadOrder = append(leadOrder, leadID)
				}
				leadByTaskID[event.ObjectID] = leadID
			}
		}
	}
	processed := map[string]bool{}
	for taskID, leadID := range leadByTaskID {
		if investigationTaskComplete(runDir, leadID, taskID) {
			processed[leadID] = true
		}
	}
	var invalidClosed []string
	for _, leadID := range leadOrder {
		if processed[leadID] {
			continue
		}
		status, exists, err := ledgerLeadStatus(runDir, leadID)
		if err != nil {
			return nil, nil, err
		}
		if exists && status != "open" {
			invalidClosed = append(invalidClosed, leadID)
		}
	}
	return processed, invalidClosed, nil
}

func investigationTaskComplete(runDir, leadID, taskID string) bool {
	if !ledgerTaskCompleted(runDir, taskID) {
		return false
	}
	if !ledgerLeadHasValidInvestigationEvidence(runDir, leadID, taskID) {
		return false
	}
	status, exists, err := ledgerLeadStatus(runDir, leadID)
	if err != nil || !exists || status == "open" {
		return false
	}
	return status != "promoted_to_finding" || ledgerLeadHasFinding(runDir, leadID)
}

func appendInvestigateLimitReached(runDir, runID string, limit, processed int) error {
	open, err := openLedgerLeads(runDir)
	if err != nil {
		return err
	}
	leadIDs := make([]string, 0, len(open))
	for _, lead := range open {
		leadIDs = append(leadIDs, lead.ID)
	}
	return appendLedgerEvent(runDir, LedgerEvent{
		RunID:    runID,
		Type:     "investigate.limit_reached",
		Object:   "phase",
		ObjectID: "investigate",
		Data: map[string]any{
			"limit":         limit,
			"processed":     processed,
			"open_leads":    len(open),
			"open_lead_ids": leadIDs,
		},
	})
}

func runInvestigateBatch(runDir, runID, workspace string, cfg Config, opencodePath string, leads []LeadRecord) error {
	return runInvestigateBatchWithAttemptRunner(runDir, runID, workspace, cfg, directOpenCodeTaskAttemptRunner{opencodePath: opencodePath}, leads)
}

func runInvestigateBatchWithAttemptRunner(runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner, leads []LeadRecord) error {
	return runInvestigateBatchWithAttemptRunnerContext(context.Background(), runDir, runID, workspace, cfg, attemptRunner, leads)
}

func runInvestigateBatchWithAttemptRunnerContext(ctx context.Context, runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner, leads []LeadRecord) error {
	parallelism := taskParallelism(cfg)
	jobs := make(chan LeadRecord)
	errs := make(chan error, len(leads))
	var wg sync.WaitGroup
	for i := 0; i < parallelism; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for lead := range jobs {
				if err := runInvestigateTaskWithAttemptRunnerContext(ctx, runDir, runID, workspace, cfg, attemptRunner, lead); err != nil {
					errs <- err
				}
			}
		}()
	}
	var sendErr error
sendLoop:
	for _, lead := range leads {
		select {
		case jobs <- lead:
		case <-ctx.Done():
			sendErr = ctx.Err()
			break sendLoop
		}
	}
	close(jobs)
	wg.Wait()
	close(errs)

	var joined error
	for err := range errs {
		joined = errors.Join(joined, err)
	}
	return errors.Join(sendErr, joined)
}

func runInvestigateTask(runDir, runID, workspace string, cfg Config, opencodePath string, lead LeadRecord) error {
	return runInvestigateTaskWithAttemptRunner(runDir, runID, workspace, cfg, directOpenCodeTaskAttemptRunner{opencodePath: opencodePath}, lead)
}

func runInvestigateTaskWithAttemptRunner(runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner, lead LeadRecord) error {
	return runInvestigateTaskWithAttemptRunnerContext(context.Background(), runDir, runID, workspace, cfg, attemptRunner, lead)
}

func runInvestigateTaskWithAttemptRunnerContext(ctx context.Context, runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner, lead LeadRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	safeLeadID := safeFileID(lead.ID)
	task := TaskRecord{
		RunID:       runID,
		TaskID:      "task_investigate_" + safeLeadID,
		Phase:       "investigate",
		Title:       "Investigate: " + lead.Title,
		Instruction: "Investigate one lead and either close it without a finding, promote it to a candidate finding, or create follow-up leads.",
	}
	taskPath := filepath.Join(runDir, "tasks", task.TaskID+".json")
	if err := writeTaskFile(taskPath, task); err != nil {
		return err
	}
	if err := registerTaskStarted(runDir, task, map[string]any{
		"lead_id": lead.ID,
	}); err != nil {
		return err
	}

	taskWorkspace, cleanupWorkspace, err := prepareTaskWorkspace(workspace, runID, task.TaskID)
	if err != nil {
		return err
	}
	defer cleanupWorkspace()

	handoffRel, err := preparePhaseHandoffContext(runDir, runID, task, lead.ID, "")
	if err != nil {
		return err
	}
	prompt, err := investigatePrompt(runDir, taskWorkspace, cfg, lead, handoffRel)
	if err != nil {
		return err
	}
	promptRel := filepath.ToSlash(filepath.Join("evidence", "investigate-"+safeLeadID+"-prompt.md"))
	promptPath := filepath.Join(runDir, filepath.FromSlash(promptRel))
	if err := os.WriteFile(promptPath, []byte(prompt), filePerm); err != nil {
		return err
	}
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:              runID,
		TaskID:             task.TaskID,
		Kind:               "markdown",
		Title:              "Investigate prompt: " + lead.Title,
		Path:               promptRel,
		LeadID:             lead.ID,
		AllowContentChange: true,
	}); err != nil {
		return err
	}

	logRel := filepath.ToSlash(filepath.Join("evidence", "opencode-investigate-"+safeLeadID+".jsonl"))
	logPath := filepath.Join(runDir, filepath.FromSlash(logRel))
	notesRel := investigationNotesRelPath(lead.ID)
	if err := runOpenCodeTaskWithAttemptRunnerContext(ctx, attemptRunner, taskWorkspace, runDir, opencodeTask{
		RunID:    runID,
		TaskID:   task.TaskID,
		Phase:    task.Phase,
		LeadID:   lead.ID,
		Title:    "mnm investigate " + safeLeadID,
		Model:    phaseModel(cfg, "investigate"),
		Prompt:   prompt,
		LogPath:  logPath,
		TaskFile: taskPath,
		Timeout:  openCodeTaskTimeout(cfg),
		Setup:    cfg.Runner.Setup,
		Verify: func(verifyRunDir string) error {
			evidence, ok := ledgerLeadTaskEvidence(verifyRunDir, lead.ID, task.TaskID, notesRel)
			if !ok {
				return fmt.Errorf("investigate opencode task did not register investigation evidence %s for lead %s", notesRel, lead.ID)
			}
			if err := registeredEvidenceFileError(verifyRunDir, notesRel, evidence.ContentSHA256, validateNonEmptyEvidenceFile); err != nil {
				return err
			}
			if err := validateRequiredTaskHandoff(verifyRunDir, task.Phase, task.TaskID, lead.ID, ""); err != nil {
				return err
			}
			status, exists, err := ledgerLeadStatus(verifyRunDir, lead.ID)
			if err != nil {
				return err
			}
			if !exists || status == "open" {
				return fmt.Errorf("investigate opencode task did not close lead %s", lead.ID)
			}
			if status == "promoted_to_finding" && !ledgerLeadHasFinding(verifyRunDir, lead.ID) {
				return fmt.Errorf("investigate opencode task closed lead %s as promoted_to_finding without creating a finding", lead.ID)
			}
			if !ledgerTaskCompleted(verifyRunDir, task.TaskID) {
				return fmt.Errorf("investigate opencode task did not complete task %s", task.TaskID)
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
		Title:  "OpenCode Investigate transcript: " + lead.Title,
		Path:   logRel,
		LeadID: lead.ID,
	}); err != nil {
		return err
	}
	return nil
}

func investigationNotesRelPath(leadID string) string {
	return filepath.ToSlash(filepath.Join("evidence", "investigate-"+safeFileID(leadID)+"-notes.md"))
}

func investigatePrompt(runDir, workspace string, cfg Config, lead LeadRecord, handoffRel string) (string, error) {
	bodyPath, err := leadBodyPath(runDir, lead)
	if err != nil {
		return "", err
	}
	body, err := os.ReadFile(bodyPath)
	if err != nil {
		return "", fmt.Errorf("read lead body %s: %w", lead.BodyPath, err)
	}
	safeLeadID := safeFileID(lead.ID)
	return fmt.Sprintf(`# makenomistakes Investigate

You are running inside an isolated VM. Your job is to deeply investigate one Recon lead and write durable audit state through the injected mnm CLI.

Workspace: %[1]s
Run directory: %[2]s
Lead ID: %[3]s
Lead title: %[4]s
Lead category: %[5]s
Lead priority: %[6]s

Scope instructions:

%[7]s

Recon context files:

- %[2]s/evidence/recon-codebase-map.md
- %[2]s/evidence/recon-risk-register.md

Phase handoff context:

- %[2]s/%[8]s

Lead body:

%[9]s

Required actions:

1. Run: mnm task current
2. Read the recon context and inspect the workspace with local tools. Run focused tests, dependency checks, repro scripts, or small proof commands when they would materially answer the lead.
3. Read the phase handoff context for prior setup discoveries, confirmed dead ends, open leads, candidate findings, and task handoff entries from earlier work.
4. Treat the workspace as a disposable per-task copy. Write durable audit artifacts only under the run directory.
5. Keep filesystem searches scoped to the workspace and run directory. Do not run broad host filesystem scans such as find / or inspect host mounts like /Users; use /tmp only for temporary tools or repro files.
6. Write investigation notes, commands, observed output, code references, and decision rationale to %[2]s/evidence/investigate-%[10]s-notes.md, then register them with: mnm evidence add --kind markdown --title "Investigation notes: %[4]s" --lead %[3]s --path %[2]s/evidence/investigate-%[10]s-notes.md
7. Write a structured task handoff JSON file to %[2]s/evidence/handoff-investigate-%[10]s.json using this schema:

%[11]s

Register it with: mnm evidence add --kind json --title "Task handoff: %[4]s" --lead %[3]s --path %[2]s/evidence/handoff-investigate-%[10]s.json
8. If the lead is not a real issue, close the lead with: mnm lead close --id %[3]s --status closed_no_finding --reason "..."
9. If the lead is a real candidate issue, write a finding body to %[2]s/evidence/finding-%[10]s.md with impact, affected paths, evidence, reproduction notes, and confidence limits. Create it with: mnm finding create --lead %[3]s --title "Specific issue title" --category security --severity medium --confidence medium --body-file %[2]s/evidence/finding-%[10]s.md, then close this lead with: mnm lead close --id %[3]s --status promoted_to_finding --reason "..."
10. Attach any additional logs, command output, traces, or proof files with mnm evidence add. Tie finding evidence to the finding ID returned by mnm finding create.
11. If investigation reveals a separate follow-up area, create a new lead with mnm lead create. Still close the current lead as promoted_to_finding, closed_no_finding, or superseded.
12. Complete the task with: mnm task complete --status completed --summary "Investigated %[3]s"

Finding quality bar:

- Create a finding only when you have concrete code references and a plausible failure or exploit path.
- Do not promote vague risk, missing best practices, or style concerns to findings.
- Prefer proof commands and short reproduction notes over speculation.
- Record uncertainty in the finding body rather than overstating the result.
`, workspace, runDir, lead.ID, lead.Title, lead.Category, lead.Priority, scopeText(cfg), handoffRel, string(body), safeLeadID, taskHandoffSchemaText()), nil
}

func maxInvestigations(cfg Config) int {
	if cfg.Runner.MaxInvestigations > 0 {
		return cfg.Runner.MaxInvestigations
	}
	return cfg.Runner.MaxLeads
}

func taskParallelism(cfg Config) int {
	return effectiveParallelTasks(cfg.Runner)
}

func scopeText(cfg Config) string {
	if cfg.Instructions.Scope == "" {
		return "No additional scope instructions were provided."
	}
	return cfg.Instructions.Scope
}
