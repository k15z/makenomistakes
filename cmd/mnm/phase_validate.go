package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func runValidatePhase(runDir, runID, workspace string, cfg Config, opencodePath string) error {
	return runValidatePhaseWithAttemptRunner(runDir, runID, workspace, cfg, directOpenCodeTaskAttemptRunner{opencodePath: opencodePath})
}

func runValidatePhaseWithAttemptRunner(runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner) error {
	return runValidatePhaseWithAttemptRunnerContext(context.Background(), runDir, runID, workspace, cfg, attemptRunner)
}

func runValidatePhaseWithAttemptRunnerContext(ctx context.Context, runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	findings, err := unvalidatedCanonicalFindings(runDir)
	if err != nil {
		return err
	}
	for _, finding := range findings {
		if err := runValidateTaskWithAttemptRunnerContext(ctx, runDir, runID, workspace, cfg, attemptRunner, finding); err != nil {
			return err
		}
	}
	return nil
}

func runValidateTask(runDir, runID, workspace string, cfg Config, opencodePath string, finding FindingRecord) error {
	return runValidateTaskWithAttemptRunner(runDir, runID, workspace, cfg, directOpenCodeTaskAttemptRunner{opencodePath: opencodePath}, finding)
}

func runValidateTaskWithAttemptRunner(runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner, finding FindingRecord) error {
	return runValidateTaskWithAttemptRunnerContext(context.Background(), runDir, runID, workspace, cfg, attemptRunner, finding)
}

func runValidateTaskWithAttemptRunnerContext(ctx context.Context, runDir, runID, workspace string, cfg Config, attemptRunner opencodeTaskAttemptRunner, finding FindingRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
	prompt, err := validatePrompt(runDir, taskWorkspace, cfg, finding, handoffRel)
	if err != nil {
		return err
	}
	promptRel := filepath.ToSlash(filepath.Join("evidence", "validate-"+safeFindingID+"-prompt.md"))
	promptPath := filepath.Join(runDir, filepath.FromSlash(promptRel))
	if err := os.WriteFile(promptPath, []byte(prompt), filePerm); err != nil {
		return err
	}
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:              runID,
		TaskID:             task.TaskID,
		Kind:               "markdown",
		Title:              "Validate prompt: " + finding.Title,
		Path:               promptRel,
		FindingID:          finding.ID,
		AllowContentChange: true,
	}); err != nil {
		return err
	}

	logRel := filepath.ToSlash(filepath.Join("evidence", "opencode-validate-"+safeFindingID+".jsonl"))
	logPath := filepath.Join(runDir, filepath.FromSlash(logRel))
	notesRel := validationNotesRelPath(finding.ID)
	if err := runOpenCodeTaskWithAttemptRunnerContext(ctx, attemptRunner, taskWorkspace, runDir, opencodeTask{
		RunID:     runID,
		TaskID:    task.TaskID,
		Phase:     task.Phase,
		FindingID: finding.ID,
		Title:     "mnm validate " + safeFindingID,
		Model:     phaseModel(cfg, "validate"),
		Prompt:    prompt,
		LogPath:   logPath,
		TaskFile:  taskPath,
		Timeout:   openCodeTaskTimeout(cfg),
		Setup:     cfg.Runner.Setup,
		Verify: func(verifyRunDir string) error {
			if !ledgerFindingHasTaskEvidencePath(verifyRunDir, finding.ID, task.TaskID, notesRel) {
				return fmt.Errorf("validate opencode task did not register validation evidence %s for finding %s", notesRel, finding.ID)
			}
			if err := validateNonEmptyValidationEvidence(verifyRunDir, notesRel); err != nil {
				return err
			}
			if err := validateRequiredTaskHandoff(verifyRunDir, task.Phase, task.TaskID, "", finding.ID); err != nil {
				return err
			}
			verdict, ok, err := ledgerFindingVerdict(verifyRunDir, finding.ID, "validate")
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("validate opencode task did not record validation verdict for finding %s", finding.ID)
			}
			if verdict.Value == "proven" {
				proofEvidence, err := validationProofEvidence(verifyRunDir, finding.ID, task.TaskID, verdict.eventIndex, promptRel, notesRel)
				if err != nil {
					return err
				}
				if len(proofEvidence) == 0 {
					return fmt.Errorf("validate opencode task recorded proven verdict for finding %s without registering proof evidence beyond %s", finding.ID, notesRel)
				}
				for _, evidence := range proofEvidence {
					if err := registeredEvidenceFileError(verifyRunDir, evidence.Path, evidence.ContentSHA256, validateNonEmptyEvidenceFile); err != nil {
						return err
					}
				}
			}
			if !ledgerTaskCompleted(verifyRunDir, task.TaskID) {
				return fmt.Errorf("validate opencode task did not complete task %s", task.TaskID)
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
		Title:     "OpenCode Validate transcript: " + finding.Title,
		Path:      logRel,
		FindingID: finding.ID,
	}); err != nil {
		return err
	}
	return nil
}

func validateNonEmptyValidationEvidence(runDir, relPath string) error {
	path := filepath.Join(runDir, filepath.FromSlash(relPath))
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read validation evidence %s: %w", relPath, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("validation evidence %s must not be empty", relPath)
	}
	return nil
}

func validationProofEvidence(runDir, findingID, taskID string, beforeIndex int, excludedPaths ...string) ([]EvidenceRecord, error) {
	evidence, err := ledgerEvidenceForFinding(runDir, findingID)
	if err != nil {
		return nil, err
	}
	excluded := map[string]bool{}
	for _, path := range excludedPaths {
		excluded[path] = true
	}
	var proof []EvidenceRecord
	for _, item := range evidence {
		if item.TaskID != taskID || item.eventIndex >= beforeIndex || excluded[item.Path] {
			continue
		}
		if !isValidationProofArtifact(findingID, item) {
			continue
		}
		proof = append(proof, item)
	}
	return proof, nil
}

func isValidationProofArtifact(findingID string, item EvidenceRecord) bool {
	safeFindingID := safeFileID(findingID)
	relPath := item.Path
	notesRel := validationNotesRelPath(findingID)
	transcriptRel := filepath.ToSlash(filepath.Join("evidence", "opencode-validate-"+safeFindingID+".jsonl"))
	if strings.HasPrefix(item.Title, "Phase handoff context:") ||
		strings.HasPrefix(item.Title, "Task handoff:") ||
		strings.HasPrefix(item.Title, "Setup hook log:") {
		return false
	}
	return relPath != notesRel &&
		relPath != transcriptRel &&
		!strings.HasPrefix(relPath, "evidence/validate-"+safeFindingID+"-prompt.")
}

func validationNotesRelPath(findingID string) string {
	return filepath.ToSlash(filepath.Join("evidence", "validate-"+safeFileID(findingID)+"-notes.md"))
}

func validatePrompt(runDir, workspace string, cfg Config, finding FindingRecord, handoffRel string) (string, error) {
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

Phase handoff context:

- %[2]s/%[16]s

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
2. Read the finding body, prior review and deduplication verdicts, attached evidence, recon context, phase handoff context, and relevant source code.
3. Reuse setup discoveries, successful commands, failed commands, blocked services, likely leads, and confirmed dead ends from the phase handoff context before rediscovering environment setup.
4. Treat the workspace as a disposable per-task copy. Write durable audit artifacts only under the run directory. Prior evidence files may be present for context; do not overwrite or re-register them.
5. Keep filesystem searches scoped to the workspace and run directory. Do not run broad host filesystem scans such as find / or inspect host mounts like /Users; use /tmp only for temporary tools or repro files.
6. Attempt the highest-fidelity reproduction or exploit that is feasible inside this VM. Build services, run tests, start dev servers, use Docker/Compose/minikube if available and scoped to this workspace, seed data, send requests, inject malformed inputs, trigger crashes, or write small proof scripts as needed.
7. Write validation notes, commands, observed output, blockers, and any proof artifacts to %[2]s/evidence/validate-%[17]s-notes.md.
8. Register the notes with: mnm evidence add --kind markdown --title "Validation notes: %[4]s" --finding %[3]s --path %[2]s/evidence/validate-%[17]s-notes.md
9. Write a structured task handoff JSON file to %[2]s/evidence/handoff-validate-%[17]s.json using this schema:

%[18]s

For blocked validation, include a blocker with the missing dependency, failed command, required service, suspected config gap, and next command whenever known. Register it with: mnm evidence add --kind json --title "Task handoff: %[4]s" --finding %[3]s --path %[2]s/evidence/handoff-validate-%[17]s.json
10. If you observed the claimed failure, exploit path, crash, data exposure, or other concrete impact, write at least one separate proof artifact such as a command log, request/response capture, minimized reproduction script, stack trace, or screenshot under %[2]s/evidence/ and register it with: mnm evidence add --kind log --title "Validation proof: %[4]s" --finding %[3]s --path PROOF_PATH. Use a fresh validation-specific path, especially when rerunning an existing investigation script whose default log path points at prior evidence. Validation notes alone are not enough for a proven verdict.
11. If you observed concrete impact and registered separate proof evidence, record: mnm verdict record --finding %[3]s --phase validate --value proven --reason "..."
12. If focused validation contradicts the finding or shows it is not reachable/applicable, record: mnm verdict record --finding %[3]s --phase validate --value failed --reason "..."
13. If the environment, dependencies, missing services, credentials, or time prevent a fair proof while the finding remains plausible, record: mnm verdict record --finding %[3]s --phase validate --value inconclusive --reason "..."
14. Complete the task with: mnm task complete --status completed --summary "Validated %[3]s"

Validation quality bar:

- Prefer runnable proof commands, request/response captures, logs, stack traces, and minimized reproduction scripts over prose.
- Do not mark proven without observing concrete behavior.
- Do not mark failed merely because full setup is hard; use inconclusive when the environment blocks a fair test.
- Keep any long-running servers, containers, or background processes scoped to this disposable VM task.
`, workspace, runDir, finding.ID, finding.Title, finding.Category, finding.Severity, finding.Confidence, finding.LeadID, scopeText(cfg), bodyPath, string(body), formatEvidenceList(runDir, findingEvidence), reviewText, dedupText, sourceLead, handoffRel, safeFindingID, taskHandoffSchemaText()), nil
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
