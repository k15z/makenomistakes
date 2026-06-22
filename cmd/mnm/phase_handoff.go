package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const phaseHandoffVersion = 1

const (
	phaseHandoffMaxListItems = 20
	phaseHandoffMaxTextRunes = 1600
)

type phaseHandoffContext struct {
	Version           int                     `json:"version"`
	RunID             string                  `json:"run_id"`
	TargetPhase       string                  `json:"target_phase"`
	GeneratedAt       string                  `json:"generated_at"`
	OpenLeads         []phaseHandoffLead      `json:"open_leads"`
	ConfirmedDeadEnds []phaseHandoffLeadClose `json:"confirmed_dead_ends"`
	InconclusiveLeads []phaseHandoffLeadClose `json:"inconclusive_leads"`
	Findings          []phaseHandoffFinding   `json:"findings"`
	SetupLogs         []phaseHandoffEvidence  `json:"setup_logs"`
	TaskHandoffs      []taskHandoffFile       `json:"task_handoffs"`
}

type phaseHandoffLead struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Priority string `json:"priority"`
	BodyPath string `json:"body_path"`
}

type phaseHandoffLeadClose struct {
	ID                       string `json:"id"`
	Title                    string `json:"title"`
	Category                 string `json:"category"`
	Status                   string `json:"status"`
	Reason                   string `json:"reason"`
	TaskID                   string `json:"task_id"`
	NegativeProofBoundary    string `json:"negative_proof_boundary,omitempty"`
	NegativeProofEnforcement string `json:"negative_proof_enforcement,omitempty"`
	NegativeProofExposure    string `json:"negative_proof_exposure,omitempty"`
	NegativeProofEdgeCases   string `json:"negative_proof_edge_cases,omitempty"`
}

type phaseHandoffFinding struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	LeadID     string   `json:"lead_id"`
	Category   string   `json:"category"`
	Severity   string   `json:"severity"`
	Confidence string   `json:"confidence"`
	BodyPath   string   `json:"body_path"`
	Verdicts   []string `json:"verdicts"`
}

type phaseHandoffEvidence struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	TaskID    string `json:"task_id"`
	LeadID    string `json:"lead_id,omitempty"`
	FindingID string `json:"finding_id,omitempty"`
}

type taskHandoffFile struct {
	Version           int                  `json:"version"`
	Phase             string               `json:"phase"`
	TaskID            string               `json:"task_id"`
	LeadID            string               `json:"lead_id,omitempty"`
	FindingID         string               `json:"finding_id,omitempty"`
	AttemptedCommands []string             `json:"attempted_commands"`
	SetupDiscoveries  []string             `json:"setup_discoveries"`
	Blockers          []taskHandoffBlocker `json:"blockers"`
	LikelyLeads       []string             `json:"likely_leads"`
	ConfirmedDeadEnds []taskHandoffDeadEnd `json:"confirmed_dead_ends"`
	Notes             string               `json:"notes,omitempty"`
	SourcePath        string               `json:"source_path,omitempty"`
}

type taskHandoffBlocker struct {
	Summary            string `json:"summary"`
	MissingDependency  string `json:"missing_dependency,omitempty"`
	FailedCommand      string `json:"failed_command,omitempty"`
	RequiredService    string `json:"required_service,omitempty"`
	SuspectedConfigGap string `json:"suspected_config_gap,omitempty"`
	NextCommand        string `json:"next_command,omitempty"`
}

type taskHandoffDeadEnd struct {
	Summary                  string `json:"summary"`
	NegativeProofBoundary    string `json:"negative_proof_boundary"`
	NegativeProofEnforcement string `json:"negative_proof_enforcement"`
	NegativeProofExposure    string `json:"negative_proof_exposure"`
	NegativeProofEdgeCases   string `json:"negative_proof_edge_cases"`
	Legacy                   bool   `json:"legacy,omitempty"`
}

func preparePhaseHandoffContext(runDir, runID string, task TaskRecord, leadID, findingID string) (string, error) {
	relPath := phaseHandoffContextRelPath(task.TaskID)
	context, err := buildPhaseHandoffContext(runDir, runID, task.Phase)
	if err != nil {
		return "", err
	}
	context = compactPhaseHandoffContext(context)
	if err := writeJSON(filepath.Join(runDir, filepath.FromSlash(relPath)), context); err != nil {
		return "", err
	}
	if _, err := registerTaskEvidence(runDir, taskEvidenceRegistration{
		RunID:              runID,
		TaskID:             task.TaskID,
		Kind:               "json",
		Title:              "Phase handoff context: " + task.Phase,
		Path:               relPath,
		LeadID:             leadID,
		FindingID:          findingID,
		AllowContentChange: true,
	}); err != nil {
		return "", err
	}
	return relPath, nil
}

func phaseHandoffContextRelPath(taskID string) string {
	return filepath.ToSlash(filepath.Join("evidence", "phase-handoff-"+safeFileID(taskID)+".json"))
}

func taskHandoffRelPath(phase, subjectID string) string {
	if phase == "deduplicate" {
		return filepath.ToSlash(filepath.Join("evidence", "handoff-deduplicate.json"))
	}
	return filepath.ToSlash(filepath.Join("evidence", "handoff-"+phase+"-"+safeFileID(subjectID)+".json"))
}

func validateRequiredTaskHandoff(runDir, phase, taskID, leadID, findingID string) error {
	subjectID := leadID
	if subjectID == "" {
		subjectID = findingID
	}
	relPath := taskHandoffRelPath(phase, subjectID)
	evidence, ok := requiredTaskHandoffEvidence(runDir, taskID, leadID, findingID, relPath)
	if !ok {
		return fmt.Errorf("%s opencode task did not register task handoff %s", phase, relPath)
	}
	if evidence.Kind != "json" || !strings.HasPrefix(evidence.Title, "Task handoff:") {
		return fmt.Errorf("%s task handoff %s must be registered as json evidence titled Task handoff", phase, relPath)
	}
	if err := registeredEvidenceFileError(runDir, relPath, evidence.ContentSHA256, validateNonEmptyEvidenceFile); err != nil {
		return err
	}
	handoff, err := readTaskHandoffFile(runDir, relPath)
	if err != nil {
		return err
	}
	if err := validateTaskHandoffFile(handoff, evidence, false); err != nil {
		return fmt.Errorf("validate task handoff %s: %w", relPath, err)
	}
	if handoff.Phase != phase {
		return fmt.Errorf("validate task handoff %s: phase = %q, want %q", relPath, handoff.Phase, phase)
	}
	return nil
}

func validateBlockedValidationHandoff(runDir, findingID string) error {
	relPath := taskHandoffRelPath("validate", findingID)
	handoff, err := readTaskHandoffFile(runDir, relPath)
	if err != nil {
		return err
	}
	if len(handoff.Blockers) == 0 {
		return fmt.Errorf("inconclusive validation for finding %s requires at least one task handoff blocker", findingID)
	}
	for i, blocker := range handoff.Blockers {
		if strings.TrimSpace(blocker.Summary) == "" {
			return fmt.Errorf("validate task handoff %s blockers[%d].summary is required", relPath, i)
		}
		if strings.TrimSpace(blocker.NextCommand) == "" {
			return fmt.Errorf("validate task handoff %s blockers[%d].next_command is required for inconclusive validation", relPath, i)
		}
		if strings.TrimSpace(blocker.MissingDependency) == "" &&
			strings.TrimSpace(blocker.FailedCommand) == "" &&
			strings.TrimSpace(blocker.RequiredService) == "" &&
			strings.TrimSpace(blocker.SuspectedConfigGap) == "" {
			return fmt.Errorf("validate task handoff %s blockers[%d] must include a missing dependency, failed command, required service, or suspected config gap", relPath, i)
		}
	}
	return nil
}

func requiredTaskHandoffEvidence(runDir, taskID, leadID, findingID, relPath string) (EvidenceRecord, bool) {
	switch {
	case leadID != "":
		return ledgerLeadTaskEvidence(runDir, leadID, taskID, relPath)
	case findingID != "":
		return ledgerFindingTaskEvidenceBefore(runDir, findingID, taskID, relPath, maxInt)
	default:
		return ledgerTaskEvidence(runDir, taskID, relPath)
	}
}

func buildPhaseHandoffContext(runDir, runID, targetPhase string) (phaseHandoffContext, error) {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return phaseHandoffContext{}, err
	}
	leads, err := leadsFromEvents(events)
	if err != nil {
		return phaseHandoffContext{}, err
	}
	findings, err := ledgerFindings(runDir)
	if err != nil {
		return phaseHandoffContext{}, err
	}
	evidence := evidenceFromEvents(events)
	context := phaseHandoffContext{
		Version:     phaseHandoffVersion,
		RunID:       runID,
		TargetPhase: targetPhase,
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		SetupLogs:   setupLogEvidence(evidence),
	}
	leadByID := map[string]LeadRecord{}
	for _, lead := range leads {
		leadByID[lead.ID] = lead
		if lead.Status == "open" {
			context.OpenLeads = append(context.OpenLeads, phaseHandoffLead{
				ID:       lead.ID,
				Title:    lead.Title,
				Category: lead.Category,
				Priority: lead.Priority,
				BodyPath: lead.BodyPath,
			})
		}
	}
	context.ConfirmedDeadEnds = confirmedDeadEndsFromEvents(events, leadByID)
	context.InconclusiveLeads = inconclusiveLeadsFromEvents(events, leadByID)
	for _, finding := range findings {
		context.Findings = append(context.Findings, phaseHandoffFinding{
			ID:         finding.ID,
			Title:      finding.Title,
			LeadID:     finding.LeadID,
			Category:   finding.Category,
			Severity:   finding.Severity,
			Confidence: finding.Confidence,
			BodyPath:   finding.BodyPath,
			Verdicts:   findingVerdictLabelsFromEvents(events, finding.ID),
		})
	}
	context.TaskHandoffs, err = taskHandoffsFromEvidence(runDir, evidence)
	if err != nil {
		return phaseHandoffContext{}, err
	}
	return context, nil
}

func compactPhaseHandoffContext(context phaseHandoffContext) phaseHandoffContext {
	for i := range context.ConfirmedDeadEnds {
		compactPhaseHandoffLeadClose(&context.ConfirmedDeadEnds[i])
	}
	for i := range context.InconclusiveLeads {
		compactPhaseHandoffLeadClose(&context.InconclusiveLeads[i])
	}
	for i := range context.TaskHandoffs {
		context.TaskHandoffs[i] = compactTaskHandoffFile(context.TaskHandoffs[i])
	}
	return context
}

func compactPhaseHandoffLeadClose(item *phaseHandoffLeadClose) {
	item.Reason = compactPhaseHandoffText(item.Reason)
	item.NegativeProofBoundary = compactPhaseHandoffText(item.NegativeProofBoundary)
	item.NegativeProofEnforcement = compactPhaseHandoffText(item.NegativeProofEnforcement)
	item.NegativeProofExposure = compactPhaseHandoffText(item.NegativeProofExposure)
	item.NegativeProofEdgeCases = compactPhaseHandoffText(item.NegativeProofEdgeCases)
}

func compactTaskHandoffFile(handoff taskHandoffFile) taskHandoffFile {
	handoff.AttemptedCommands = compactStringList(handoff.AttemptedCommands, phaseHandoffMaxListItems, phaseHandoffMaxTextRunes)
	handoff.SetupDiscoveries = compactStringList(handoff.SetupDiscoveries, phaseHandoffMaxListItems, phaseHandoffMaxTextRunes)
	handoff.LikelyLeads = compactStringList(handoff.LikelyLeads, phaseHandoffMaxListItems, phaseHandoffMaxTextRunes)
	handoff.Notes = compactPhaseHandoffText(handoff.Notes)
	if len(handoff.Blockers) > phaseHandoffMaxListItems {
		handoff.Blockers = handoff.Blockers[:phaseHandoffMaxListItems]
	}
	for i := range handoff.Blockers {
		handoff.Blockers[i].Summary = compactPhaseHandoffText(handoff.Blockers[i].Summary)
		handoff.Blockers[i].MissingDependency = compactPhaseHandoffText(handoff.Blockers[i].MissingDependency)
		handoff.Blockers[i].FailedCommand = compactPhaseHandoffText(handoff.Blockers[i].FailedCommand)
		handoff.Blockers[i].RequiredService = compactPhaseHandoffText(handoff.Blockers[i].RequiredService)
		handoff.Blockers[i].SuspectedConfigGap = compactPhaseHandoffText(handoff.Blockers[i].SuspectedConfigGap)
		handoff.Blockers[i].NextCommand = compactPhaseHandoffText(handoff.Blockers[i].NextCommand)
	}
	if len(handoff.ConfirmedDeadEnds) > phaseHandoffMaxListItems {
		handoff.ConfirmedDeadEnds = handoff.ConfirmedDeadEnds[:phaseHandoffMaxListItems]
	}
	for i := range handoff.ConfirmedDeadEnds {
		handoff.ConfirmedDeadEnds[i].Summary = compactPhaseHandoffText(handoff.ConfirmedDeadEnds[i].Summary)
		handoff.ConfirmedDeadEnds[i].NegativeProofBoundary = compactPhaseHandoffText(handoff.ConfirmedDeadEnds[i].NegativeProofBoundary)
		handoff.ConfirmedDeadEnds[i].NegativeProofEnforcement = compactPhaseHandoffText(handoff.ConfirmedDeadEnds[i].NegativeProofEnforcement)
		handoff.ConfirmedDeadEnds[i].NegativeProofExposure = compactPhaseHandoffText(handoff.ConfirmedDeadEnds[i].NegativeProofExposure)
		handoff.ConfirmedDeadEnds[i].NegativeProofEdgeCases = compactPhaseHandoffText(handoff.ConfirmedDeadEnds[i].NegativeProofEdgeCases)
	}
	return handoff
}

func compactPhaseHandoffText(text string) string {
	return previewRunes(text, phaseHandoffMaxTextRunes, " [truncated]")
}

func setupLogEvidence(evidence []EvidenceRecord) []phaseHandoffEvidence {
	var items []phaseHandoffEvidence
	for _, item := range evidence {
		if !strings.HasPrefix(item.Title, "Setup hook log") {
			continue
		}
		items = append(items, phaseHandoffEvidence{
			Path:      item.Path,
			Kind:      item.Kind,
			Title:     item.Title,
			TaskID:    item.TaskID,
			LeadID:    item.LeadID,
			FindingID: item.FindingID,
		})
	}
	return items
}

func confirmedDeadEndsFromEvents(events []LedgerEvent, leadByID map[string]LeadRecord) []phaseHandoffLeadClose {
	var items []phaseHandoffLeadClose
	for _, event := range events {
		if event.Type != "lead.closed" || event.Object != "lead" {
			continue
		}
		status := stringData(event.Data, "status")
		if status != "closed_no_finding" && status != "superseded" {
			continue
		}
		if status == "closed_no_finding" && !leadCloseHasNegativeProof(event) {
			continue
		}
		lead := leadByID[event.ObjectID]
		items = append(items, phaseHandoffLeadClose{
			ID:                       event.ObjectID,
			Title:                    lead.Title,
			Category:                 lead.Category,
			Status:                   status,
			Reason:                   stringData(event.Data, "reason"),
			TaskID:                   event.TaskID,
			NegativeProofBoundary:    stringData(event.Data, "negative_proof_boundary"),
			NegativeProofEnforcement: stringData(event.Data, "negative_proof_enforcement"),
			NegativeProofExposure:    stringData(event.Data, "negative_proof_exposure"),
			NegativeProofEdgeCases:   stringData(event.Data, "negative_proof_edge_cases"),
		})
	}
	return items
}

func inconclusiveLeadsFromEvents(events []LedgerEvent, leadByID map[string]LeadRecord) []phaseHandoffLeadClose {
	var items []phaseHandoffLeadClose
	for _, event := range events {
		if event.Type != "lead.closed" || event.Object != "lead" {
			continue
		}
		status := stringData(event.Data, "status")
		if status != "inconclusive" {
			continue
		}
		lead := leadByID[event.ObjectID]
		items = append(items, phaseHandoffLeadClose{
			ID:       event.ObjectID,
			Title:    lead.Title,
			Category: lead.Category,
			Status:   status,
			Reason:   stringData(event.Data, "reason"),
			TaskID:   event.TaskID,
		})
	}
	return items
}

func leadCloseHasNegativeProof(event LedgerEvent) bool {
	return strings.TrimSpace(stringData(event.Data, "negative_proof_boundary")) != "" &&
		strings.TrimSpace(stringData(event.Data, "negative_proof_enforcement")) != "" &&
		strings.TrimSpace(stringData(event.Data, "negative_proof_exposure")) != "" &&
		strings.TrimSpace(stringData(event.Data, "negative_proof_edge_cases")) != ""
}

func findingVerdictLabelsFromEvents(events []LedgerEvent, findingID string) []string {
	var verdicts []VerdictRecord
	for _, verdict := range verdictsFromEvents(events) {
		if verdict.FindingID == findingID {
			verdicts = append(verdicts, verdict)
		}
	}
	sort.Slice(verdicts, func(i, j int) bool {
		return verdicts[i].eventIndex < verdicts[j].eventIndex
	})
	labels := make([]string, 0, len(verdicts))
	for _, verdict := range verdicts {
		if verdict.Phase == "validate" {
			labels = append(labels, "validation "+verdict.Value)
			continue
		}
		labels = append(labels, verdict.Phase+" "+verdict.Value)
	}
	return labels
}

func taskHandoffsFromEvidence(runDir string, evidence []EvidenceRecord) ([]taskHandoffFile, error) {
	var handoffs []taskHandoffFile
	for _, item := range evidence {
		if item.Kind != "json" || !strings.HasPrefix(item.Title, "Task handoff:") {
			continue
		}
		if err := registeredEvidenceFileError(runDir, item.Path, item.ContentSHA256, validateNonEmptyEvidenceFile); err != nil {
			return nil, fmt.Errorf("validate task handoff %s: %w", item.Path, err)
		}
		handoff, err := readTaskHandoffFile(runDir, item.Path)
		if err != nil {
			return nil, err
		}
		if err := validateTaskHandoffFile(handoff, item, true); err != nil {
			return nil, fmt.Errorf("validate task handoff %s: %w", item.Path, err)
		}
		handoff.SourcePath = item.Path
		handoffs = append(handoffs, handoff)
	}
	return handoffs, nil
}

func readTaskHandoffFile(runDir, relPath string) (taskHandoffFile, error) {
	path := filepath.Join(runDir, filepath.FromSlash(relPath))
	data, err := os.ReadFile(path)
	if err != nil {
		return taskHandoffFile{}, fmt.Errorf("read task handoff %s: %w", relPath, err)
	}
	var handoff taskHandoffFile
	if err := json.Unmarshal(data, &handoff); err != nil {
		return taskHandoffFile{}, fmt.Errorf("parse task handoff %s: %w", relPath, err)
	}
	return handoff, nil
}

func (deadEnd *taskHandoffDeadEnd) UnmarshalJSON(data []byte) error {
	var summary string
	if err := json.Unmarshal(data, &summary); err == nil {
		deadEnd.Summary = summary
		deadEnd.Legacy = true
		return nil
	}
	type taskHandoffDeadEndAlias taskHandoffDeadEnd
	var decoded taskHandoffDeadEndAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*deadEnd = taskHandoffDeadEnd(decoded)
	return nil
}

func validateTaskHandoffFile(handoff taskHandoffFile, evidence EvidenceRecord, allowLegacyDeadEnds bool) error {
	if handoff.Version != phaseHandoffVersion {
		return fmt.Errorf("version = %d, want %d", handoff.Version, phaseHandoffVersion)
	}
	if handoff.Phase == "" || handoff.TaskID == "" {
		return fmt.Errorf("phase and task_id are required")
	}
	if handoff.TaskID != evidence.TaskID {
		return fmt.Errorf("task_id = %q, want %q", handoff.TaskID, evidence.TaskID)
	}
	if evidence.LeadID != "" && handoff.LeadID != evidence.LeadID {
		return fmt.Errorf("lead_id = %q, want %q", handoff.LeadID, evidence.LeadID)
	}
	if evidence.FindingID != "" && handoff.FindingID != evidence.FindingID {
		return fmt.Errorf("finding_id = %q, want %q", handoff.FindingID, evidence.FindingID)
	}
	for i, blocker := range handoff.Blockers {
		if strings.TrimSpace(blocker.Summary) == "" {
			return fmt.Errorf("blockers[%d].summary is required", i)
		}
	}
	for i, deadEnd := range handoff.ConfirmedDeadEnds {
		if deadEnd.Legacy && allowLegacyDeadEnds {
			if strings.TrimSpace(deadEnd.Summary) == "" {
				return fmt.Errorf("confirmed_dead_ends[%d].summary is required", i)
			}
			continue
		}
		if strings.TrimSpace(deadEnd.Summary) == "" {
			return fmt.Errorf("confirmed_dead_ends[%d].summary is required", i)
		}
		if strings.TrimSpace(deadEnd.NegativeProofBoundary) == "" {
			return fmt.Errorf("confirmed_dead_ends[%d].negative_proof_boundary is required", i)
		}
		if strings.TrimSpace(deadEnd.NegativeProofEnforcement) == "" {
			return fmt.Errorf("confirmed_dead_ends[%d].negative_proof_enforcement is required", i)
		}
		if strings.TrimSpace(deadEnd.NegativeProofExposure) == "" {
			return fmt.Errorf("confirmed_dead_ends[%d].negative_proof_exposure is required", i)
		}
		if strings.TrimSpace(deadEnd.NegativeProofEdgeCases) == "" {
			return fmt.Errorf("confirmed_dead_ends[%d].negative_proof_edge_cases is required", i)
		}
	}
	return nil
}

func taskHandoffSchemaText() string {
	return `{
  "version": 1,
  "phase": "investigate|review|deduplicate|validate",
  "task_id": "current task id",
  "lead_id": "lead id when investigating a lead",
  "finding_id": "finding id when reviewing or validating a finding",
  "attempted_commands": ["important commands or scripts run, with short outcomes"],
  "setup_discoveries": ["toolchain, service, fixture, auth, or environment facts later phases should reuse"],
  "blockers": [
    {
      "summary": "what blocked progress",
      "missing_dependency": "optional dependency/tool",
      "failed_command": "optional exact command that failed",
      "required_service": "optional service or endpoint needed",
      "suspected_config_gap": "optional config/env likely missing",
      "next_command": "optional concrete next command to try"
    }
  ],
  "likely_leads": ["under-covered follow-up areas, same-class sibling instances, or adjacent risk classes that deserve another pass"],
  "confirmed_dead_ends": [
    {
      "summary": "area ruled out and the concrete reason",
      "negative_proof_boundary": "exact trust/network/auth/data-flow/deployment boundary",
      "negative_proof_enforcement": "specific guard, policy, middleware, check, or code path",
      "negative_proof_exposure": "deployment exposure conclusion",
      "negative_proof_edge_cases": "bypasses, roles, alternate routes, and edge cases checked"
    }
  ],
  "notes": "brief extra context"
}`
}
