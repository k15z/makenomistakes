package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func runDeduplicatePhase(runDir, runID, workspace string, cfg Config, opencodePath string) error {
	return runDeduplicatePhaseWithAttemptRunner(runDir, runID, workspace, cfg, directOpenCodeTaskAttemptRunner{opencodePath: opencodePath})
}

func runDeduplicatePhaseWithAttemptRunner(runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner) error {
	return runDeduplicatePhaseWithAttemptRunnerContext(context.Background(), runDir, runID, workspace, cfg, attemptRunner)
}

func runDeduplicatePhaseWithAttemptRunnerContext(ctx context.Context, runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	allFindings, err := reviewAcceptedLedgerFindings(runDir)
	if err != nil {
		return err
	}
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
	if err := registerTaskStarted(runDir, task, nil); err != nil {
		return err
	}

	taskWorkspace, cleanupWorkspace, err := prepareTaskWorkspace(workspace, runID, task.TaskID)
	if err != nil {
		return err
	}
	defer cleanupWorkspace()

	handoffRel, err := preparePhaseHandoffContext(runDir, runID, task, "", "")
	if err != nil {
		return err
	}
	prompt, err := deduplicatePrompt(runDir, taskWorkspace, cfg, allFindings, findings, handoffRel)
	if err != nil {
		return err
	}
	promptRel := filepath.ToSlash(filepath.Join("evidence", "deduplicate-prompt.md"))
	promptPath := filepath.Join(runDir, filepath.FromSlash(promptRel))
	if err := os.WriteFile(promptPath, []byte(prompt), filePerm); err != nil {
		return err
	}
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:              runID,
		TaskID:             task.TaskID,
		Kind:               "markdown",
		Title:              "Deduplicate prompt",
		Path:               promptRel,
		AllowContentChange: true,
	}); err != nil {
		return err
	}

	logRel := filepath.ToSlash(filepath.Join("evidence", "opencode-deduplicate.jsonl"))
	logPath := filepath.Join(runDir, filepath.FromSlash(logRel))
	notesRel := deduplicateNotesRelPath()
	if err := runOpenCodeTaskWithAttemptRunnerContext(ctx, attemptRunner, taskWorkspace, runDir, opencodeTask{
		RunID:    runID,
		TaskID:   task.TaskID,
		Phase:    task.Phase,
		Title:    "mnm deduplicate",
		Model:    phaseModel(cfg, "deduplicate"),
		Prompt:   prompt,
		LogPath:  logPath,
		TaskFile: taskPath,
		Timeout:  openCodeTaskTimeout(cfg),
		Setup:    cfg.Runner.Setup,
		Verify: func(verifyRunDir string) error {
			evidence, ok := ledgerTaskEvidence(verifyRunDir, task.TaskID, notesRel)
			if !ok {
				return fmt.Errorf("deduplicate opencode task did not register deduplication evidence %s", notesRel)
			}
			if err := registeredEvidenceFileError(verifyRunDir, notesRel, evidence.ContentSHA256, validateNonEmptyEvidenceFile); err != nil {
				return err
			}
			if err := validateRequiredTaskHandoff(verifyRunDir, task.Phase, task.TaskID, "", ""); err != nil {
				return err
			}
			var missing []string
			for _, finding := range findings {
				if !ledgerFindingHasVerdict(verifyRunDir, finding.ID, "deduplicate") {
					missing = append(missing, finding.ID)
				}
			}
			if len(missing) > 0 {
				return fmt.Errorf("deduplicate opencode task did not record verdicts for findings: %s", strings.Join(missing, ", "))
			}
			if !ledgerTaskCompleted(verifyRunDir, task.TaskID) {
				return errors.New("deduplicate opencode task did not complete task_deduplicate")
			}
			if err := validateDeduplicateGraph(verifyRunDir, findings); err != nil {
				return err
			}
			pending, err := undeduplicatedLedgerFindings(verifyRunDir)
			if err != nil {
				return err
			}
			if len(pending) > 0 {
				return fmt.Errorf("deduplicate opencode task left findings pending deduplication: %s", strings.Join(findingIDs(pending), ", "))
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
		Title:  "OpenCode Deduplicate transcript",
		Path:   logRel,
	}); err != nil {
		return err
	}

	return nil
}

func deduplicateNotesRelPath() string {
	return filepath.ToSlash(filepath.Join("evidence", "deduplicate-notes.md"))
}

func deduplicatePrompt(runDir, workspace string, cfg Config, allFindings, pendingFindings []FindingRecord, handoffRel string) (string, error) {
	findingText, err := formatDeduplicateFindings(runDir, allFindings, findingIDSet(pendingFindings))
	if err != nil {
		return "", err
	}
	pendingIDs := strings.Join(findingIDs(pendingFindings), ", ")
	return fmt.Sprintf(`# makenomistakes Deduplicate

You are running inside an isolated VM. Your job is to cluster review-accepted candidate findings and write durable deduplication verdicts through the injected mnm CLI.

Workspace: %[1]s
Run directory: %[2]s
Review-accepted finding count: %[3]d
Findings requiring deduplicate verdicts: %[6]s

Scope instructions:

%[4]s

Recon context files:

- %[2]s/evidence/recon-codebase-map.md
- %[2]s/evidence/recon-risk-register.md

Phase handoff context:

- %[2]s/%[7]s

Review-accepted findings:

%[5]s

Required actions:

1. Run: mnm task current
2. Read the finding bodies, review notes, attached evidence, recon context, phase handoff context, and relevant source code when needed to decide whether two findings describe the same root defect.
3. Use the handoff context to preserve prior setup discoveries, blockers, likely follow-up leads, and confirmed dead ends while clustering.
4. Treat the workspace as a disposable per-task copy. Write durable audit artifacts only under the run directory.
5. Keep filesystem searches scoped to the workspace and run directory. Do not run broad host filesystem scans such as find / or inspect host mounts like /Users; use /tmp only for temporary tools or repro files.
6. Write clustering notes, canonical selections, duplicate rationale, and any uncertainty to %[2]s/evidence/deduplicate-notes.md.
7. Register the notes with: mnm evidence add --kind markdown --title "Deduplication notes" --path %[2]s/evidence/deduplicate-notes.md
8. Write a structured task handoff JSON file to %[2]s/evidence/handoff-deduplicate.json using this schema:

%[8]s

Register it with: mnm evidence add --kind json --title "Task handoff: Deduplication" --path %[2]s/evidence/handoff-deduplicate.json
9. For every finding listed as "Pending deduplicate verdict", record exactly one deduplicate verdict.
10. For a unique issue, record: mnm verdict record --finding FINDING_ID --phase deduplicate --value canonical --reason "..."
11. For a duplicate issue, record: mnm verdict record --finding FINDING_ID --phase deduplicate --value duplicate --canonical-finding CANONICAL_FINDING_ID --reason "Duplicate of CANONICAL_FINDING_ID because ..."
12. Complete the task with: mnm task complete --status completed --summary "Deduplicated reviewed findings"

Deduplication quality bar:

- Only mark duplicate when findings share the same root defect and would be fixed by substantially the same patch.
- Findings in the same file, framework, category, or symptom family are not automatically duplicates.
- Do not reject findings in Deduplicate. If a reviewed finding seems weak, mark it canonical and leave proof or failure to Validate.
- Prefer the clearest, most complete finding as the canonical finding for a duplicate cluster.
- Preserve clustering commands, setup discoveries, blockers, likely follow-up leads, and confirmed duplicate dead ends in the structured handoff.
`, workspace, runDir, len(allFindings), scopeText(cfg), findingText, pendingIDs, handoffRel, taskHandoffSchemaText()), nil
}

func formatDeduplicateFindings(runDir string, findings []FindingRecord, pending map[string]struct{}) (string, error) {
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
		status := "Pending deduplicate verdict"
		if _, isPending := pending[finding.ID]; !isPending {
			status = "Existing deduplicate verdict"
			if dedup, ok, err := ledgerFindingVerdict(runDir, finding.ID, "deduplicate"); err != nil {
				return "", err
			} else if ok {
				status = fmt.Sprintf("Existing deduplicate verdict: %s", dedup.Value)
				if dedup.CanonicalFindingID != "" {
					status += " of " + dedup.CanonicalFindingID
				}
			}
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
Deduplicate status: %s
Body path: %s
Review reason: %s

Evidence:

%s
Finding body:

%s

`, finding.ID, finding.Title, finding.Category, finding.Severity, finding.Confidence, status, bodyPath, reviewReason, formatEvidenceList(runDir, evidence), string(body))
	}
	return builder.String(), nil
}

func validateDeduplicateGraph(runDir string, findings []FindingRecord) error {
	accepted, err := reviewAcceptedLedgerFindings(runDir)
	if err != nil {
		return err
	}
	acceptedIDs := findingIDSet(accepted)
	for _, finding := range findings {
		verdict, ok, err := ledgerFindingVerdict(runDir, finding.ID, "deduplicate")
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("finding %s is missing deduplicate verdict", finding.ID)
		}
		if verdict.Value != "duplicate" {
			continue
		}
		if _, ok := acceptedIDs[verdict.CanonicalFindingID]; !ok {
			return fmt.Errorf("deduplicate duplicate %s points at non-review-accepted finding %s", finding.ID, verdict.CanonicalFindingID)
		}
		canonical, ok, err := ledgerFindingVerdict(runDir, verdict.CanonicalFindingID, "deduplicate")
		if err != nil {
			return err
		}
		if !ok || canonical.Value != "canonical" {
			return fmt.Errorf("deduplicate duplicate %s points at non-canonical finding %s", finding.ID, verdict.CanonicalFindingID)
		}
	}
	return nil
}

func findingIDSet(findings []FindingRecord) map[string]struct{} {
	ids := make(map[string]struct{}, len(findings))
	for _, finding := range findings {
		ids[finding.ID] = struct{}{}
	}
	return ids
}

func findingIDs(findings []FindingRecord) []string {
	ids := make([]string, 0, len(findings))
	for _, finding := range findings {
		ids = append(ids, finding.ID)
	}
	return ids
}
