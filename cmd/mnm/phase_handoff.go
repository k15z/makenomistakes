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

type phaseHandoffContext struct {
	Version           int                     `json:"version"`
	RunID             string                  `json:"run_id"`
	TargetPhase       string                  `json:"target_phase"`
	GeneratedAt       string                  `json:"generated_at"`
	OpenLeads         []phaseHandoffLead      `json:"open_leads"`
	ConfirmedDeadEnds []phaseHandoffLeadClose `json:"confirmed_dead_ends"`
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
	ID       string `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Status   string `json:"status"`
	Reason   string `json:"reason"`
	TaskID   string `json:"task_id"`
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
	ConfirmedDeadEnds []string             `json:"confirmed_dead_ends"`
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

func preparePhaseHandoffContext(runDir, runID string, task TaskRecord, leadID, findingID string) (string, error) {
	relPath := phaseHandoffContextRelPath(task.TaskID)
	context, err := buildPhaseHandoffContext(runDir, runID, task.Phase)
	if err != nil {
		return "", err
	}
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
	if err := validateTaskHandoffFile(handoff, evidence); err != nil {
		return fmt.Errorf("validate task handoff %s: %w", relPath, err)
	}
	if handoff.Phase != phase {
		return fmt.Errorf("validate task handoff %s: phase = %q, want %q", relPath, handoff.Phase, phase)
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
			return nil, err
		}
		handoff, err := readTaskHandoffFile(runDir, item.Path)
		if err != nil {
			return nil, err
		}
		if err := validateTaskHandoffFile(handoff, item); err != nil {
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

func validateTaskHandoffFile(handoff taskHandoffFile, evidence EvidenceRecord) error {
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
  "likely_leads": ["follow-up areas or leads that still look plausible"],
  "confirmed_dead_ends": ["areas ruled out and the concrete reason"],
  "notes": "brief extra context"
}`
}
