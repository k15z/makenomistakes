package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

func runInvestigatePhase(runDir, runID, workspace string, cfg Config, opencodePath string) error {
	processed, invalidClosed, err := completedInvestigationLeads(runDir)
	if err != nil {
		return err
	}
	if len(invalidClosed) > 0 {
		return fmt.Errorf("investigate phase has closed leads with incomplete investigation tasks: %s", strings.Join(invalidClosed, ", "))
	}
	limit := maxInvestigations(cfg)
	for {
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
		if err := runInvestigateBatch(runDir, runID, workspace, cfg, opencodePath, batch); err != nil {
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
	parallelism := investigateParallelism(cfg)
	jobs := make(chan LeadRecord)
	errs := make(chan error, len(leads))
	var wg sync.WaitGroup
	for i := 0; i < parallelism; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for lead := range jobs {
				if err := runInvestigateTask(runDir, runID, workspace, cfg, opencodePath, lead); err != nil {
					errs <- err
				}
			}
		}()
	}
	for _, lead := range leads {
		jobs <- lead
	}
	close(jobs)
	wg.Wait()
	close(errs)

	var joined error
	for err := range errs {
		joined = errors.Join(joined, err)
	}
	return joined
}

func runInvestigateTask(runDir, runID, workspace string, cfg Config, opencodePath string, lead LeadRecord) error {
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

	prompt, err := investigatePrompt(runDir, taskWorkspace, cfg, lead)
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
	if err := runOpenCodeTask(opencodePath, taskWorkspace, runDir, opencodeTask{
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
		Verify: func() error {
			evidence, ok := ledgerLeadTaskEvidence(runDir, lead.ID, task.TaskID, notesRel)
			if !ok {
				return fmt.Errorf("investigate opencode task did not register investigation evidence %s for lead %s", notesRel, lead.ID)
			}
			if err := registeredEvidenceFileError(runDir, notesRel, evidence.ContentSHA256, validateNonEmptyEvidenceFile); err != nil {
				return err
			}
			status, exists, err := ledgerLeadStatus(runDir, lead.ID)
			if err != nil {
				return err
			}
			if !exists || status == "open" {
				return fmt.Errorf("investigate opencode task did not close lead %s", lead.ID)
			}
			if status == "promoted_to_finding" && !ledgerLeadHasFinding(runDir, lead.ID) {
				return fmt.Errorf("investigate opencode task closed lead %s as promoted_to_finding without creating a finding", lead.ID)
			}
			if !ledgerTaskCompleted(runDir, task.TaskID) {
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

func investigatePrompt(runDir, workspace string, cfg Config, lead LeadRecord) (string, error) {
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
- %[8]s

Lead body:

%[9]s

Required actions:

1. Run: mnm task current
2. Read the recon context and inspect the workspace with local tools. Run focused tests, dependency checks, repro scripts, or small proof commands when they would materially answer the lead.
3. Treat the workspace as a disposable per-task copy. Write durable audit artifacts only under the run directory.
4. Keep filesystem searches scoped to the workspace and run directory. Do not run broad host filesystem scans such as find / or inspect host mounts like /Users; use /tmp only for temporary tools or repro files.
5. Write investigation notes, commands, observed output, code references, and decision rationale to %[2]s/evidence/investigate-%[10]s-notes.md, then register them with: mnm evidence add --kind markdown --title "Investigation notes: %[4]s" --lead %[3]s --path %[2]s/evidence/investigate-%[10]s-notes.md
6. If the lead is not a real issue, close the lead with: mnm lead close --id %[3]s --status closed_no_finding --reason "..."
7. If the lead is a real candidate issue, write a finding body to %[2]s/evidence/finding-%[10]s.md with impact, affected paths, evidence, reproduction notes, and confidence limits. Create it with: mnm finding create --lead %[3]s --title "Specific issue title" --category security --severity medium --confidence medium --body-file %[2]s/evidence/finding-%[10]s.md, then close this lead with: mnm lead close --id %[3]s --status promoted_to_finding --reason "..."
8. Attach any additional logs, command output, traces, or proof files with mnm evidence add. Tie finding evidence to the finding ID returned by mnm finding create.
9. If investigation reveals a separate follow-up area, create a new lead with mnm lead create. Still close the current lead as promoted_to_finding, closed_no_finding, or superseded.
10. Complete the task with: mnm task complete --status completed --summary "Investigated %[3]s"

Finding quality bar:

- Create a finding only when you have concrete code references and a plausible failure or exploit path.
- Do not promote vague risk, missing best practices, or style concerns to findings.
- Prefer proof commands and short reproduction notes over speculation.
- Record uncertainty in the finding body rather than overstating the result.
`, workspace, runDir, lead.ID, lead.Title, lead.Category, lead.Priority, scopeText(cfg), bodyPath, string(body), safeLeadID), nil
}

func maxInvestigations(cfg Config) int {
	if cfg.Runner.MaxInvestigations > 0 {
		return cfg.Runner.MaxInvestigations
	}
	return cfg.Runner.MaxLeads
}

func investigateParallelism(cfg Config) int {
	if cfg.Runner.ParallelTasks > 0 {
		return cfg.Runner.ParallelTasks
	}
	if cfg.Runner.CPUs > 1 {
		return 2
	}
	return 1
}

func scopeText(cfg Config) string {
	if cfg.Instructions.Scope == "" {
		return "No additional scope instructions were provided."
	}
	return cfg.Instructions.Scope
}
