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

func runReviewPhase(runDir, runID, workspace string, cfg Config, opencodePath string) error {
	return runReviewPhaseWithAttemptRunner(runDir, runID, workspace, cfg, directOpenCodeTaskAttemptRunner{opencodePath: opencodePath})
}

func runReviewPhaseWithAttemptRunner(runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner) error {
	return runReviewPhaseWithAttemptRunnerContext(context.Background(), runDir, runID, workspace, cfg, attemptRunner)
}

func runReviewPhaseWithAttemptRunnerContext(ctx context.Context, runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	findings, err := unreviewedLedgerFindings(runDir)
	if err != nil {
		return err
	}
	if len(findings) == 0 {
		return nil
	}
	return runReviewBatchWithAttemptRunnerContext(ctx, runDir, runID, workspace, cfg, attemptRunner, findings)
}

func runReviewBatch(runDir, runID, workspace string, cfg Config, opencodePath string, findings []FindingRecord) error {
	return runReviewBatchWithAttemptRunner(runDir, runID, workspace, cfg, directOpenCodeTaskAttemptRunner{opencodePath: opencodePath}, findings)
}

func runReviewBatchWithAttemptRunner(runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner, findings []FindingRecord) error {
	return runReviewBatchWithAttemptRunnerContext(context.Background(), runDir, runID, workspace, cfg, attemptRunner, findings)
}

func runReviewBatchWithAttemptRunnerContext(ctx context.Context, runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner, findings []FindingRecord) error {
	parallelism := taskParallelism(cfg)
	jobs := make(chan FindingRecord)
	errs := make(chan error, len(findings))
	var wg sync.WaitGroup
	for i := 0; i < parallelism; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for finding := range jobs {
				if err := runReviewTaskWithAttemptRunnerContext(ctx, runDir, runID, workspace, cfg, attemptRunner, finding); err != nil {
					errs <- err
				}
			}
		}()
	}
	var sendErr error
sendLoop:
	for _, finding := range findings {
		select {
		case jobs <- finding:
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

func runReviewTask(runDir, runID, workspace string, cfg Config, opencodePath string, finding FindingRecord) error {
	return runReviewTaskWithAttemptRunner(runDir, runID, workspace, cfg, directOpenCodeTaskAttemptRunner{opencodePath: opencodePath}, finding)
}

func runReviewTaskWithAttemptRunner(runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner, finding FindingRecord) error {
	return runReviewTaskWithAttemptRunnerContext(context.Background(), runDir, runID, workspace, cfg, attemptRunner, finding)
}

func runReviewTaskWithAttemptRunnerContext(ctx context.Context, runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner, finding FindingRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	safeFindingID := safeFileID(finding.ID)
	task := TaskRecord{
		RunID:       runID,
		TaskID:      "task_review_" + safeFindingID,
		Phase:       "review",
		Title:       "Review: " + finding.Title,
		Instruction: "Independently assess one candidate finding from a skeptical lens and record an accepted or rejected review verdict.",
	}
	taskPath := filepath.Join(runDir, "tasks", task.TaskID+".json")
	if err := writeTaskFile(taskPath, task); err != nil {
		return err
	}
	if err := registerTaskStarted(runDir, task, map[string]any{
		"finding_id": finding.ID,
	}); err != nil {
		return err
	}

	taskWorkspace, cleanupWorkspace, err := prepareTaskWorkspace(workspace, runID, task.TaskID)
	if err != nil {
		return err
	}
	defer cleanupWorkspace()

	handoffRel, err := preparePhaseHandoffContext(runDir, runID, task, "", finding.ID)
	if err != nil {
		return err
	}
	prompt, err := reviewPrompt(runDir, taskWorkspace, cfg, finding, handoffRel)
	if err != nil {
		return err
	}
	promptRel := filepath.ToSlash(filepath.Join("evidence", "review-"+safeFindingID+"-prompt.md"))
	promptPath := filepath.Join(runDir, filepath.FromSlash(promptRel))
	if err := os.WriteFile(promptPath, []byte(prompt), filePerm); err != nil {
		return err
	}
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:              runID,
		TaskID:             task.TaskID,
		Kind:               "markdown",
		Title:              "Review prompt: " + finding.Title,
		Path:               promptRel,
		FindingID:          finding.ID,
		AllowContentChange: true,
	}); err != nil {
		return err
	}

	logRel := filepath.ToSlash(filepath.Join("evidence", "opencode-review-"+safeFindingID+".jsonl"))
	logPath := filepath.Join(runDir, filepath.FromSlash(logRel))
	notesRel := reviewNotesRelPath(finding.ID)
	if err := runOpenCodeTaskWithAttemptRunnerContext(ctx, attemptRunner, taskWorkspace, runDir, opencodeTask{
		RunID:     runID,
		TaskID:    task.TaskID,
		Phase:     task.Phase,
		FindingID: finding.ID,
		Title:     "mnm review " + safeFindingID,
		Model:     phaseModel(cfg, "review"),
		Prompt:    prompt,
		LogPath:   logPath,
		TaskFile:  taskPath,
		Timeout:   openCodeTaskTimeout(cfg),
		Setup:     cfg.Runner.Setup,
		Verify: func(verifyRunDir string) error {
			if !ledgerFindingHasTaskEvidencePath(verifyRunDir, finding.ID, task.TaskID, notesRel) {
				return fmt.Errorf("review opencode task did not register review evidence %s for finding %s", notesRel, finding.ID)
			}
			if err := validateNonEmptyEvidenceFile(verifyRunDir, notesRel); err != nil {
				return err
			}
			if err := validateRequiredTaskHandoff(verifyRunDir, task.Phase, task.TaskID, "", finding.ID); err != nil {
				return err
			}
			if !ledgerFindingHasVerdict(verifyRunDir, finding.ID, "review") {
				return fmt.Errorf("review opencode task did not record review verdict for finding %s", finding.ID)
			}
			if !ledgerTaskCompleted(verifyRunDir, task.TaskID) {
				return fmt.Errorf("review opencode task did not complete task %s", task.TaskID)
			}
			return nil
		},
	}); err != nil {
		return err
	}
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:     runID,
		TaskID:    task.TaskID,
		Kind:      "jsonl",
		Title:     "OpenCode Review transcript: " + finding.Title,
		Path:      logRel,
		FindingID: finding.ID,
	}); err != nil {
		return err
	}
	return nil
}

func reviewNotesRelPath(findingID string) string {
	return filepath.ToSlash(filepath.Join("evidence", "review-"+safeFileID(findingID)+"-notes.md"))
}

func reviewPrompt(runDir, workspace string, cfg Config, finding FindingRecord, handoffRel string) (string, error) {
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
	leadEvidence, err := ledgerEvidenceForLead(runDir, finding.LeadID)
	if err != nil {
		return "", err
	}
	sourceLead := sourceLeadText(runDir, finding.LeadID)
	safeFindingID := safeFileID(finding.ID)

	return fmt.Sprintf(`# makenomistakes Review

You are running inside an isolated VM. Your job is to independently review one candidate finding from a skeptical lens and write durable audit state through the injected mnm CLI.

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

Phase handoff context:

- %[2]s/%[15]s

Finding body path: %[10]s

Finding body:

%[11]s

Finding evidence:

%[12]s

Source lead context:

%[13]s

Source lead evidence:

%[14]s

Required actions:

1. Run: mnm task current
2. Read the finding body, attached evidence, recon context, phase handoff context, and relevant source code. Run focused proof or falsification commands if they materially affect the verdict.
3. Use the handoff context to avoid rechecking confirmed dead ends and to reuse setup or command discoveries from prior tasks.
4. Treat the workspace as a disposable per-task copy. Write durable audit artifacts only under the run directory.
5. Keep filesystem searches scoped to the workspace and run directory. Do not run broad host filesystem scans such as find / or inspect host mounts like /Users; use /tmp only for temporary tools or repro files.
6. Write and register review notes, commands, falsification attempts, code references, and uncertainty with: mnm evidence write --kind markdown --title "Review notes: %[4]s" --finding %[3]s --path %[2]s/evidence/review-%[16]s-notes.md
The mnm evidence write and mnm handoff write commands read artifact content from stdin unless you pass --input /tmp/file; use a heredoc, pipe, or --input to provide content.
7. Write and register a structured task handoff JSON file with: mnm handoff write --finding %[3]s --path %[2]s/evidence/handoff-review-%[16]s.json. Use this schema as the JSON input:

%[17]s

8. If the finding is concrete, in scope, supported by code references, and has a plausible failure or exploit path, record: mnm verdict record --finding %[3]s --phase review --value accepted --reason "..."
9. If the finding is vague, out of scope, unsupported, contradicted by the code, duplicate-style noise, or only a best-practice concern, record: mnm verdict record --finding %[3]s --phase review --value rejected --reason "..."
10. Complete the task with: mnm task complete --status completed --summary "Reviewed %[3]s"

Review quality bar:

- Be cynical. Reject findings that do not survive concrete code inspection.
- Do not create new findings in Review. Record a verdict for this candidate only.
- Preserve important commands, setup discoveries, blockers, under-covered follow-up areas, sibling instances, adjacent risk classes, and confirmed dead ends in the structured handoff.
- If the candidate bundles separable root causes whose proof, remediation, or ownership differs, call that out in review notes and likely_leads instead of accepting it as one broad issue.
- When a candidate survives review, do a bounded sibling-instance check for the same class of bug in nearby files, routes, handlers, templates, configuration blocks, or data flows.
- Record uncertainty in the review notes and reason rather than overstating the result.
`, workspace, runDir, finding.ID, finding.Title, finding.Category, finding.Severity, finding.Confidence, finding.LeadID, scopeText(cfg), bodyPath, string(body), formatEvidenceList(runDir, findingEvidence), sourceLead, formatEvidenceList(runDir, leadEvidence), handoffRel, safeFindingID, taskHandoffSchemaText()), nil
}

func sourceLeadText(runDir, leadID string) string {
	if leadID == "" {
		return "No source lead was recorded for this finding."
	}
	leads, err := ledgerLeads(runDir)
	if err != nil {
		return fmt.Sprintf("Could not read source lead %s: %v", leadID, err)
	}
	for _, lead := range leads {
		if lead.ID != leadID {
			continue
		}
		path, err := leadBodyPath(runDir, lead)
		if err != nil {
			return fmt.Sprintf("Source lead %s has no readable body: %v", leadID, err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Sprintf("Could not read source lead body %s: %v", lead.BodyPath, err)
		}
		return fmt.Sprintf("Lead title: %s\nLead category: %s\nLead priority: %s\nLead body path: %s\n\n%s", lead.Title, lead.Category, lead.Priority, path, string(body))
	}
	return "Source lead " + leadID + " was not found in the ledger."
}

func formatEvidenceList(runDir string, records []EvidenceRecord) string {
	if len(records) == 0 {
		return "- No attached evidence records.\n"
	}
	var builder strings.Builder
	for _, record := range records {
		path, err := evidencePath(runDir, record)
		if err != nil {
			path = record.Path
		}
		fmt.Fprintf(&builder, "- %s (%s): %s\n", record.Title, record.Kind, path)
	}
	return builder.String()
}
