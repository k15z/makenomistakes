package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func taskCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("task requires a subcommand")
	}
	switch args[0] {
	case "current":
		flags := flag.NewFlagSet("task current", flag.ContinueOnError)
		flags.SetOutput(stderr)
		runDirFlag := flags.String("run-dir", "", "run directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		runDir, err := resolveRunDir(*runDirFlag)
		if err != nil {
			return err
		}
		task, err := readCurrentTask(runDir)
		if err != nil {
			return err
		}
		b, err := json.MarshalIndent(task, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(b))
		return nil
	case "complete":
		flags := flag.NewFlagSet("task complete", flag.ContinueOnError)
		flags.SetOutput(stderr)
		runDirFlag := flags.String("run-dir", "", "run directory")
		status := flags.String("status", "completed", "completed|failed")
		summary := flags.String("summary", "", "summary")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if !oneOf(*status, "completed", "failed") {
			return fmt.Errorf("invalid task status %q", *status)
		}
		summaryText, err := requiredTextFlag("--summary", *summary)
		if err != nil {
			return err
		}
		runDir, task, err := currentTaskForCommand(*runDirFlag)
		if err != nil {
			return err
		}
		event, err := prepareLedgerEvent(runDir, LedgerEvent{
			RunID:    task.RunID,
			Type:     "task.completed",
			Object:   "task",
			ObjectID: task.TaskID,
			TaskID:   task.TaskID,
			Data: map[string]any{
				"status":  *status,
				"summary": summaryText,
			},
		})
		if err != nil {
			return err
		}
		unlock, err := lockRunDir(runDir)
		if err != nil {
			return err
		}
		defer unlock()
		currentStatus, exists, err := ledgerTaskCompletionStatusForOutputUnlocked(runDir, task.TaskID)
		if err != nil {
			return err
		}
		if exists {
			if currentStatus == *status {
				return nil
			}
			return fmt.Errorf("task %s is already completed with status %q", task.TaskID, currentStatus)
		}
		return appendLedgerEventUnlocked(runDir, event)
	default:
		return fmt.Errorf("unknown task subcommand %q", args[0])
	}
}

func ledgerTaskCompletionStatus(runDir, taskID string) (string, bool, error) {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return "", false, err
	}
	return taskCompletionStatusFromEvents(events, taskID)
}

func ledgerTaskCompletionStatusUnlocked(runDir, taskID string) (string, bool, error) {
	events, err := readLedgerEventsOverlayUnlocked(runDir)
	if err != nil {
		return "", false, err
	}
	return taskCompletionStatusFromEvents(events, taskID)
}

func ledgerTaskCompletionStatusForOutputUnlocked(runDir, taskID string) (string, bool, error) {
	events, err := readLedgerEventsUnlocked(runDir)
	if err != nil {
		return "", false, err
	}
	return taskCompletionStatusFromEvents(events, taskID)
}

func taskCompletionStatusFromEvents(events []LedgerEvent, taskID string) (string, bool, error) {
	status := ""
	found := false
	for _, event := range events {
		if event.Type != "task.completed" || event.Object != "task" || event.ObjectID != taskID {
			continue
		}
		status = stringData(event.Data, "status")
		found = true
	}
	return status, found, nil
}

func leadCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("lead requires a subcommand")
	}
	switch args[0] {
	case "create":
		flags := flag.NewFlagSet("lead create", flag.ContinueOnError)
		flags.SetOutput(stderr)
		runDirFlag := flags.String("run-dir", "", "run directory")
		title := flags.String("title", "", "lead title")
		category := flags.String("category", "general", "lead category")
		priority := flags.String("priority", "medium", "high|medium|low")
		bodyFile := flags.String("body-file", "", "lead body file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		titleText, err := requiredTextFlag("--title", *title)
		if err != nil {
			return err
		}
		categoryText, err := requiredTextFlag("--category", *category)
		if err != nil {
			return err
		}
		if !oneOf(*priority, "high", "medium", "low") {
			return fmt.Errorf("invalid lead priority %q", *priority)
		}
		runDir, task, err := currentTaskForCommand(*runDirFlag)
		if err != nil {
			return err
		}
		if err := requireCurrentTaskPhase(task, "lead create", "recon", "investigate"); err != nil {
			return err
		}
		bodyPath, err := requirePathInsideRunDir(runDir, *bodyFile)
		if err != nil {
			return err
		}
		if err := requireNonEmptyRunFile(runDir, bodyPath, "lead body file"); err != nil {
			return err
		}
		leadID := newLedgerID("lead")
		event, err := prepareLedgerEvent(runDir, LedgerEvent{
			RunID:    task.RunID,
			Type:     "lead.created",
			Object:   "lead",
			ObjectID: leadID,
			TaskID:   task.TaskID,
			Data: map[string]any{
				"title":     titleText,
				"category":  categoryText,
				"priority":  *priority,
				"body_path": bodyPath,
			},
		})
		if err != nil {
			return err
		}
		unlock, err := lockRunDir(runDir)
		if err != nil {
			return err
		}
		defer unlock()
		existing, exists, err := ledgerTaskLeadBodyPathUnlocked(runDir, task.TaskID, bodyPath)
		if err != nil {
			return err
		}
		if exists {
			if existing.Title == titleText && existing.Category == categoryText && existing.Priority == *priority {
				fmt.Fprintln(stdout, existing.ID)
				return nil
			}
			return fmt.Errorf("task %s already created lead from body path %s with different metadata", task.TaskID, bodyPath)
		}
		if err := appendLedgerEventUnlocked(runDir, event); err != nil {
			return err
		}
		fmt.Fprintln(stdout, leadID)
		return nil
	case "close":
		flags := flag.NewFlagSet("lead close", flag.ContinueOnError)
		flags.SetOutput(stderr)
		runDirFlag := flags.String("run-dir", "", "run directory")
		id := flags.String("id", "", "lead id")
		status := flags.String("status", "", "closed_no_finding|promoted_to_finding|superseded|inconclusive")
		reason := flags.String("reason", "", "reason")
		negativeBoundary := flags.String("negative-boundary", "", "exact trust, network, auth, data-flow, or deployment boundary that makes the lead unreachable or non-impacting")
		negativeEnforcement := flags.String("negative-enforcement", "", "specific enforcement point, guard, policy, or code path that blocks the issue")
		negativeExposure := flags.String("negative-exposure", "", "deployment exposure conclusion, including whether the path is external, internal-only, disabled, or unreachable")
		negativeEdgeCases := flags.String("negative-edge-cases", "", "relevant bypasses, role variants, alternate routes, or edge cases checked")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *status == "" {
			return errors.New("lead close requires --id and --status")
		}
		if !oneOf(*status, "closed_no_finding", "promoted_to_finding", "superseded", "inconclusive") {
			return fmt.Errorf("invalid lead close status %q", *status)
		}
		runDir, task, err := currentTaskForCommand(*runDirFlag)
		if err != nil {
			return err
		}
		if err := requireCurrentTaskPhase(task, "lead close", "investigate"); err != nil {
			return err
		}
		event, err := prepareLedgerEvent(runDir, LedgerEvent{
			RunID:    task.RunID,
			Type:     "lead.closed",
			Object:   "lead",
			ObjectID: *id,
			TaskID:   task.TaskID,
			Data: map[string]any{
				"status": *status,
				"reason": *reason,
			},
		})
		if err != nil {
			return err
		}
		unlock, err := lockRunDir(runDir)
		if err != nil {
			return err
		}
		defer unlock()
		currentStatus, exists, err := ledgerLeadStatusOverlayUnlocked(runDir, *id)
		if err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("lead %s does not exist in ledger", *id)
		}
		reasonText, err := requiredTextFlag("--reason", *reason)
		if err != nil {
			return err
		}
		if *status == "closed_no_finding" {
			negativeProof, err := requiredNegativeProofData(*negativeBoundary, *negativeEnforcement, *negativeExposure, *negativeEdgeCases)
			if err != nil {
				return err
			}
			for key, value := range negativeProof {
				event.Data[key] = value
			}
		}
		if currentStatus != "open" {
			if currentStatus == *status {
				return nil
			}
			return fmt.Errorf("lead %s is already closed with status %q", *id, currentStatus)
		}
		event.Data["reason"] = reasonText
		return appendLedgerEventUnlocked(runDir, event)
	default:
		return fmt.Errorf("unknown lead subcommand %q", args[0])
	}
}

func ledgerLeadStatus(runDir, leadID string) (string, bool, error) {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return "", false, err
	}
	return leadStatusFromEvents(events, leadID)
}

func ledgerLeadStatusUnlocked(runDir, leadID string) (string, bool, error) {
	events, err := readLedgerEventsUnlocked(runDir)
	if err != nil {
		return "", false, err
	}
	return leadStatusFromEvents(events, leadID)
}

func ledgerLeadStatusOverlayUnlocked(runDir, leadID string) (string, bool, error) {
	events, err := readLedgerEventsOverlayUnlocked(runDir)
	if err != nil {
		return "", false, err
	}
	return leadStatusFromEvents(events, leadID)
}

func leadStatusFromEvents(events []LedgerEvent, leadID string) (string, bool, error) {
	status := ""
	exists := false
	for _, event := range events {
		if event.Object != "lead" || event.ObjectID != leadID {
			continue
		}
		switch event.Type {
		case "lead.created":
			status = "open"
			exists = true
		case "lead.closed":
			status = stringData(event.Data, "status")
			exists = true
		}
	}
	return status, exists, nil
}

func requiredNegativeProofData(boundary, enforcement, exposure, edgeCases string) (map[string]any, error) {
	values := []struct {
		key   string
		flag  string
		value string
	}{
		{key: "negative_proof_boundary", flag: "--negative-boundary", value: strings.TrimSpace(boundary)},
		{key: "negative_proof_enforcement", flag: "--negative-enforcement", value: strings.TrimSpace(enforcement)},
		{key: "negative_proof_exposure", flag: "--negative-exposure", value: strings.TrimSpace(exposure)},
		{key: "negative_proof_edge_cases", flag: "--negative-edge-cases", value: strings.TrimSpace(edgeCases)},
	}
	data := make(map[string]any, len(values))
	for _, item := range values {
		if item.value == "" {
			return nil, fmt.Errorf("closed_no_finding requires %s", item.flag)
		}
		data[item.key] = item.value
	}
	return data, nil
}

func validateClosedNoFindingNegativeProof(event LedgerEvent) error {
	if stringData(event.Data, "status") != "closed_no_finding" {
		return nil
	}
	for _, key := range []string{
		"negative_proof_boundary",
		"negative_proof_enforcement",
		"negative_proof_exposure",
		"negative_proof_edge_cases",
	} {
		if strings.TrimSpace(stringData(event.Data, key)) == "" {
			return fmt.Errorf("closed_no_finding lead close requires data.%s", key)
		}
	}
	return nil
}

func ledgerTaskLeadBodyPath(runDir, taskID, bodyPath string) (LeadRecord, bool, error) {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return LeadRecord{}, false, err
	}
	return taskLeadBodyPathFromEvents(events, taskID, bodyPath)
}

func ledgerTaskLeadBodyPathUnlocked(runDir, taskID, bodyPath string) (LeadRecord, bool, error) {
	events, err := readLedgerEventsOverlayUnlocked(runDir)
	if err != nil {
		return LeadRecord{}, false, err
	}
	return taskLeadBodyPathFromEvents(events, taskID, bodyPath)
}

func taskLeadBodyPathFromEvents(events []LedgerEvent, taskID, bodyPath string) (LeadRecord, bool, error) {
	leads, err := leadsFromEvents(events)
	if err != nil {
		return LeadRecord{}, false, err
	}
	for _, lead := range leads {
		if lead.TaskID == taskID && lead.BodyPath == bodyPath {
			return lead, true, nil
		}
	}
	return LeadRecord{}, false, nil
}

func findingCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "create" {
		return errors.New("finding supports: create")
	}
	flags := flag.NewFlagSet("finding create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runDirFlag := flags.String("run-dir", "", "run directory")
	title := flags.String("title", "", "finding title")
	leadID := flags.String("lead", "", "source lead id")
	category := flags.String("category", "other", "finding category")
	severity := flags.String("severity", "medium", "critical|high|medium|low|info")
	confidence := flags.String("confidence", "medium", "high|medium|low")
	bodyFile := flags.String("body-file", "", "finding body file")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	titleText, err := requiredTextFlag("--title", *title)
	if err != nil {
		return err
	}
	categoryText, err := requiredTextFlag("--category", *category)
	if err != nil {
		return err
	}
	if !oneOf(*severity, "critical", "high", "medium", "low", "info") {
		return fmt.Errorf("invalid finding severity %q", *severity)
	}
	if !oneOf(*confidence, "high", "medium", "low") {
		return fmt.Errorf("invalid finding confidence %q", *confidence)
	}
	runDir, task, err := currentTaskForCommand(*runDirFlag)
	if err != nil {
		return err
	}
	if err := requireCurrentTaskPhase(task, "finding create", "investigate"); err != nil {
		return err
	}
	if *leadID != "" {
		if err := requireLedgerObject(runDir, "lead", *leadID); err != nil {
			return err
		}
	}
	bodyPath, err := requirePathInsideRunDir(runDir, *bodyFile)
	if err != nil {
		return err
	}
	if err := requireNonEmptyRunFile(runDir, bodyPath, "finding body file"); err != nil {
		return err
	}
	findingID := newLedgerID("finding")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    task.RunID,
		Type:     "finding.created",
		Object:   "finding",
		ObjectID: findingID,
		TaskID:   task.TaskID,
		Data: map[string]any{
			"title":      titleText,
			"lead_id":    *leadID,
			"category":   categoryText,
			"severity":   *severity,
			"confidence": *confidence,
			"body_path":  bodyPath,
		},
	}); err != nil {
		return err
	}
	fmt.Fprintln(stdout, findingID)
	return nil
}

func evidenceCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("evidence supports: add, write")
	}
	switch args[0] {
	case "add":
		return evidenceAddCommand(args[1:], stdout, stderr)
	case "write":
		return evidenceWriteCommand(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown evidence subcommand %q; expected add or write", args[0])
	}
}

func evidenceAddCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("evidence add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runDirFlag := flags.String("run-dir", "", "run directory")
	kind := flags.String("kind", "", "evidence kind")
	title := flags.String("title", "", "evidence title")
	path := flags.String("path", "", "evidence file path")
	leadID := flags.String("lead", "", "lead id")
	findingID := flags.String("finding", "", "finding id")
	if err := flags.Parse(args); err != nil {
		return err
	}

	runDir, registration, err := evidenceRegistrationForCommand(*runDirFlag, *kind, *title, *path, *leadID, *findingID, "evidence add")
	if err != nil {
		return err
	}
	evidenceID, err := registerTaskEvidence(runDir, registration)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, evidenceID)
	return nil
}

func evidenceWriteCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("evidence write", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runDirFlag := flags.String("run-dir", "", "run directory")
	kind := flags.String("kind", "", "evidence kind")
	title := flags.String("title", "", "evidence title")
	path := flags.String("path", "", "evidence file path")
	leadID := flags.String("lead", "", "lead id")
	findingID := flags.String("finding", "", "finding id")
	input := flags.String("input", "", "input file; reads stdin when omitted")
	if err := flags.Parse(args); err != nil {
		return err
	}
	runDir, registration, err := evidenceRegistrationForWriteCommand(*runDirFlag, *kind, *title, *path, *leadID, *findingID, "evidence write")
	if err != nil {
		return err
	}
	data, err := readCommandInput(*input)
	if err != nil {
		return err
	}
	if err := writeEvidenceFileIfAbsentOrSame(runDir, registration.Path, data); err != nil {
		return err
	}
	evidenceID, err := registerTaskEvidence(runDir, registration)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, evidenceID)
	return nil
}

func evidenceRegistrationForCommand(runDirFlag, kind, title, path, leadID, findingID, command string) (string, taskEvidenceRegistration, error) {
	kindText, err := requiredTextFlag("--kind", kind)
	if err != nil {
		return "", taskEvidenceRegistration{}, err
	}
	titleText, err := requiredTextFlag("--title", title)
	if err != nil {
		return "", taskEvidenceRegistration{}, err
	}
	if leadID != "" && findingID != "" {
		return "", taskEvidenceRegistration{}, fmt.Errorf("%s accepts at most one of --lead or --finding", command)
	}
	runDir, task, err := currentTaskForCommand(runDirFlag)
	if err != nil {
		return "", taskEvidenceRegistration{}, err
	}
	if err := requireEvidenceOwnerPhase(task, command, leadID, findingID); err != nil {
		return "", taskEvidenceRegistration{}, err
	}
	if leadID != "" {
		if err := requireLedgerObject(runDir, "lead", leadID); err != nil {
			return "", taskEvidenceRegistration{}, err
		}
	}
	if findingID != "" {
		if err := requireLedgerObject(runDir, "finding", findingID); err != nil {
			return "", taskEvidenceRegistration{}, err
		}
	}
	relPath, err := requirePathInsideRunDir(runDir, path)
	if err != nil {
		return "", taskEvidenceRegistration{}, err
	}
	return runDir, taskEvidenceRegistration{
		RunID:     task.RunID,
		TaskID:    task.TaskID,
		Kind:      kindText,
		Title:     titleText,
		Path:      relPath,
		LeadID:    leadID,
		FindingID: findingID,
	}, nil
}

func evidenceRegistrationForWriteCommand(runDirFlag, kind, title, path, leadID, findingID, command string) (string, taskEvidenceRegistration, error) {
	kindText, err := requiredTextFlag("--kind", kind)
	if err != nil {
		return "", taskEvidenceRegistration{}, err
	}
	titleText, err := requiredTextFlag("--title", title)
	if err != nil {
		return "", taskEvidenceRegistration{}, err
	}
	if leadID != "" && findingID != "" {
		return "", taskEvidenceRegistration{}, fmt.Errorf("%s accepts at most one of --lead or --finding", command)
	}
	runDir, task, err := currentTaskForCommand(runDirFlag)
	if err != nil {
		return "", taskEvidenceRegistration{}, err
	}
	if err := requireEvidenceOwnerPhase(task, command, leadID, findingID); err != nil {
		return "", taskEvidenceRegistration{}, err
	}
	if leadID != "" {
		if err := requireLedgerObject(runDir, "lead", leadID); err != nil {
			return "", taskEvidenceRegistration{}, err
		}
	}
	if findingID != "" {
		if err := requireLedgerObject(runDir, "finding", findingID); err != nil {
			return "", taskEvidenceRegistration{}, err
		}
	}
	relPath, err := writableRunRelPath(runDir, path)
	if err != nil {
		return "", taskEvidenceRegistration{}, err
	}
	return runDir, taskEvidenceRegistration{
		RunID:     task.RunID,
		TaskID:    task.TaskID,
		Kind:      kindText,
		Title:     titleText,
		Path:      relPath,
		LeadID:    leadID,
		FindingID: findingID,
	}, nil
}

func verdictCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "record" {
		return errors.New("verdict supports: record")
	}
	flags := flag.NewFlagSet("verdict record", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runDirFlag := flags.String("run-dir", "", "run directory")
	findingID := flags.String("finding", "", "finding id")
	phase := flags.String("phase", "", "review|deduplicate|validate")
	value := flags.String("value", "", "verdict value")
	reason := flags.String("reason", "", "reason")
	canonicalFindingID := flags.String("canonical-finding", "", "canonical finding id for deduplicate duplicate verdicts")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *findingID == "" || *phase == "" || *value == "" {
		return errors.New("verdict record requires --finding, --phase, and --value")
	}
	if !oneOf(*phase, "review", "deduplicate", "validate") {
		return fmt.Errorf("invalid verdict phase %q", *phase)
	}
	if !validVerdictValue(*phase, *value) {
		return fmt.Errorf("invalid %s verdict value %q; expected one of: %s", *phase, *value, verdictValues(*phase))
	}
	if *canonicalFindingID != "" && (*phase != "deduplicate" || *value != "duplicate") {
		return errors.New("--canonical-finding is only valid for deduplicate duplicate verdicts")
	}
	if *phase == "deduplicate" && *value == "duplicate" && *canonicalFindingID == "" {
		return errors.New("deduplicate duplicate verdicts require --canonical-finding")
	}
	if *canonicalFindingID == *findingID {
		return errors.New("--canonical-finding must be different from --finding")
	}
	reasonText, err := requiredTextFlag("--reason", *reason)
	if err != nil {
		return err
	}
	runDir, task, err := currentTaskForCommand(*runDirFlag)
	if err != nil {
		return err
	}
	if task.Phase != *phase {
		return fmt.Errorf("current task phase %q cannot record %q verdicts", task.Phase, *phase)
	}
	if err := requireLedgerObject(runDir, "finding", *findingID); err != nil {
		return err
	}
	if *phase == "deduplicate" {
		if err := requireReviewAcceptedFinding(runDir, *findingID); err != nil {
			return err
		}
	}
	if *canonicalFindingID != "" {
		if err := requireLedgerObject(runDir, "finding", *canonicalFindingID); err != nil {
			return err
		}
		if err := requireReviewAcceptedFinding(runDir, *canonicalFindingID); err != nil {
			return err
		}
	}
	event, err := prepareLedgerEvent(runDir, LedgerEvent{
		RunID:    task.RunID,
		Type:     "verdict.recorded",
		Object:   "verdict",
		ObjectID: newLedgerID("verdict"),
		TaskID:   task.TaskID,
		Data: map[string]any{
			"finding_id":           *findingID,
			"phase":                *phase,
			"value":                *value,
			"reason":               reasonText,
			"canonical_finding_id": *canonicalFindingID,
		},
	})
	if err != nil {
		return err
	}
	unlock, err := lockRunDir(runDir)
	if err != nil {
		return err
	}
	defer unlock()
	existing, exists, err := existingVerdictForCommandUnlocked(runDir, task.TaskID, *findingID, *phase)
	if err != nil {
		return err
	}
	if exists {
		if existing.Value == *value && existing.CanonicalFindingID == *canonicalFindingID {
			fmt.Fprintln(stdout, existing.ID)
			return nil
		}
		return fmt.Errorf("finding %s already has %s verdict %q", *findingID, *phase, existing.Value)
	}
	if err := appendLedgerEventUnlocked(runDir, event); err != nil {
		return err
	}
	fmt.Fprintln(stdout, event.ObjectID)
	return nil
}

func existingVerdictForCommandUnlocked(runDir, taskID, findingID, phase string) (VerdictRecord, bool, error) {
	events, err := readLedgerEventsOverlayUnlocked(runDir)
	if err != nil {
		return VerdictRecord{}, false, err
	}
	verdicts := verdictsFromEvents(events)
	var match VerdictRecord
	found := false
	for _, verdict := range verdicts {
		if verdict.FindingID == findingID && verdict.Phase == phase && ledgerVerdictCompleteFromEvents(runDir, events, verdict) {
			match = verdict
			found = true
		}
	}
	if found {
		return match, true, nil
	}
	for _, verdict := range verdicts {
		if verdict.TaskID == taskID && verdict.FindingID == findingID && verdict.Phase == phase {
			match = verdict
			found = true
		}
	}
	return match, found, nil
}

func requireReviewAcceptedFinding(runDir, findingID string) error {
	verdict, ok, err := ledgerFindingVerdict(runDir, findingID, "review")
	if err != nil {
		return err
	}
	if !ok || verdict.Value != "accepted" {
		return fmt.Errorf("finding %s must have a completed accepted review verdict", findingID)
	}
	return nil
}

func validVerdictValue(phase, value string) bool {
	switch phase {
	case "review":
		return oneOf(value, "accepted", "rejected")
	case "deduplicate":
		return oneOf(value, "canonical", "duplicate")
	case "validate":
		return oneOf(value, "proven", "failed", "inconclusive")
	default:
		return false
	}
}

func verdictValues(phase string) string {
	switch phase {
	case "review":
		return "accepted, rejected"
	case "deduplicate":
		return "canonical, duplicate"
	case "validate":
		return "proven, failed, inconclusive"
	default:
		return ""
	}
}

func reportCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("report supports: finalize, show")
	}
	switch args[0] {
	case "finalize":
		return reportFinalizeCommand(args[1:], stdout, stderr)
	case "show":
		return reportShowCommand(args[1:], stdout, stderr)
	default:
		return errors.New("report supports: finalize, show")
	}
}

func reportFinalizeCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("report finalize", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runDirFlag := flags.String("run-dir", "", "run directory")
	markdownPath := flags.String("markdown", "", "markdown report path")
	jsonPath := flags.String("json", "", "json report path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	runDir, task, err := currentTaskForCommand(*runDirFlag)
	if err != nil {
		return err
	}
	if err := requireCurrentTaskPhase(task, "report finalize", "finalize"); err != nil {
		return err
	}
	markdownRel, err := requirePathInsideRunDir(runDir, *markdownPath)
	if err != nil {
		return err
	}
	jsonRel, err := requirePathInsideRunDir(runDir, *jsonPath)
	if err != nil {
		return err
	}
	if err := validateReportArtifacts(runDir, task, markdownRel, jsonRel); err != nil {
		return err
	}
	event, err := prepareLedgerEvent(runDir, LedgerEvent{
		RunID:    task.RunID,
		Type:     "report.finalized",
		Object:   "report",
		ObjectID: newLedgerID("report"),
		TaskID:   task.TaskID,
		Data: map[string]any{
			"markdown_path": markdownRel,
			"json_path":     jsonRel,
		},
	})
	if err != nil {
		return err
	}
	unlock, err := lockRunDir(runDir)
	if err != nil {
		return err
	}
	defer unlock()
	existing, exists, err := ledgerTaskFinalizedReportUnlocked(runDir, task.TaskID)
	if err != nil {
		return err
	}
	if exists {
		if existing.MarkdownPath == markdownRel && existing.JSONPath == jsonRel {
			fmt.Fprintln(stdout, "report finalized")
			return nil
		}
		return fmt.Errorf("task %s already finalized report %s with different paths", task.TaskID, existing.ID)
	}
	if err := appendLedgerEventUnlocked(runDir, event); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "report finalized")
	return nil
}

func ledgerTaskFinalizedReport(runDir, taskID string) (ReportRecord, bool, error) {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return ReportRecord{}, false, err
	}
	return taskFinalizedReportFromEvents(events, taskID), taskFinalizedReportExists(events, taskID), nil
}

func ledgerTaskFinalizedReportUnlocked(runDir, taskID string) (ReportRecord, bool, error) {
	events, err := readLedgerEventsOverlayUnlocked(runDir)
	if err != nil {
		return ReportRecord{}, false, err
	}
	return taskFinalizedReportFromEvents(events, taskID), taskFinalizedReportExists(events, taskID), nil
}

func taskFinalizedReportFromEvents(events []LedgerEvent, taskID string) ReportRecord {
	var report ReportRecord
	for _, event := range events {
		if event.Type != "report.finalized" || event.Object != "report" || event.TaskID != taskID {
			continue
		}
		report = ReportRecord{
			ID:           event.ObjectID,
			MarkdownPath: stringData(event.Data, "markdown_path"),
			JSONPath:     stringData(event.Data, "json_path"),
		}
	}
	return report
}

func taskFinalizedReportExists(events []LedgerEvent, taskID string) bool {
	for _, event := range events {
		if event.Type == "report.finalized" && event.Object == "report" && event.TaskID == taskID {
			return true
		}
	}
	return false
}

func currentTaskForCommand(runDirFlag string) (string, TaskRecord, error) {
	runDir, err := resolveRunDir(runDirFlag)
	if err != nil {
		return "", TaskRecord{}, err
	}
	task, err := readCurrentTask(runDir)
	if err != nil {
		return "", TaskRecord{}, err
	}
	return runDir, task, nil
}

func requireCurrentTaskPhase(task TaskRecord, command string, allowed ...string) error {
	if oneOf(task.Phase, allowed...) {
		return nil
	}
	return fmt.Errorf("current task phase %q cannot run %s; expected one of: %s", task.Phase, command, strings.Join(allowed, ", "))
}

func requireEvidenceOwnerPhase(task TaskRecord, command, leadID, findingID string) error {
	switch {
	case leadID != "":
		return requireCurrentTaskPhase(task, command+" --lead", "investigate")
	case findingID != "":
		return requireCurrentTaskPhase(task, command+" --finding", "investigate", "review", "validate")
	default:
		return requireCurrentTaskPhase(task, command, "recon", "deduplicate")
	}
}

func ledgerTaskEvidenceRegistrationUnlocked(runDir string, registration taskEvidenceRegistration) (EvidenceRecord, bool, error) {
	events, err := readLedgerEventsOverlayUnlocked(runDir)
	if err != nil {
		return EvidenceRecord{}, false, err
	}
	var match EvidenceRecord
	found := false
	for _, item := range evidenceFromEvents(events) {
		if item.TaskID == registration.TaskID &&
			item.Path == registration.Path &&
			item.LeadID == registration.LeadID &&
			item.FindingID == registration.FindingID {
			match = item
			found = true
		}
	}
	return match, found, nil
}

type taskEvidenceRegistration struct {
	RunID              string
	TaskID             string
	Kind               string
	Title              string
	Path               string
	LeadID             string
	FindingID          string
	AllowContentChange bool
}

func registerTaskEvidence(runDir string, registration taskEvidenceRegistration) (string, error) {
	if err := requireNonEmptyRunFile(runDir, registration.Path, "evidence file"); err != nil {
		return "", err
	}
	contentSHA256 := ""
	if info, err := os.Stat(filepath.Join(runDir, filepath.FromSlash(registration.Path))); err == nil && info.Mode().IsRegular() {
		contentSHA256, err = evidenceFileSHA256(runDir, registration.Path)
		if err != nil {
			return "", fmt.Errorf("hash evidence file %s: %w", registration.Path, err)
		}
	} else if err != nil {
		return "", fmt.Errorf("stat evidence path %s: %w", registration.Path, err)
	}
	evidenceID := newLedgerID("evidence")
	event, err := prepareLedgerEvent(runDir, LedgerEvent{
		RunID:    registration.RunID,
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: evidenceID,
		TaskID:   registration.TaskID,
		Data: map[string]any{
			"kind":           registration.Kind,
			"title":          registration.Title,
			"path":           registration.Path,
			"lead_id":        registration.LeadID,
			"finding_id":     registration.FindingID,
			"content_sha256": contentSHA256,
		},
	})
	if err != nil {
		return "", err
	}
	unlock, err := lockRunDir(runDir)
	if err != nil {
		return "", err
	}
	defer unlock()
	if err := requireNoRegisteredEvidenceContentConflictUnlocked(runDir, registration, contentSHA256); err != nil {
		return "", err
	}
	existing, exists, err := ledgerTaskEvidenceRegistrationUnlocked(runDir, registration)
	if err != nil {
		return "", err
	}
	if exists {
		sameMetadata := existing.Kind == registration.Kind && existing.Title == registration.Title && existing.LeadID == registration.LeadID && existing.FindingID == registration.FindingID
		sameContent := existing.ContentSHA256 == contentSHA256
		if sameMetadata && (sameContent || registration.AllowContentChange) {
			return existing.ID, nil
		}
		return "", fmt.Errorf("task %s already registered evidence path %s with different metadata or content", registration.TaskID, registration.Path)
	}
	if err := appendLedgerEventUnlocked(runDir, event); err != nil {
		return "", err
	}
	return evidenceID, nil
}

func requireNoRegisteredEvidenceContentConflictUnlocked(runDir string, registration taskEvidenceRegistration, contentSHA256 string) error {
	if registration.AllowContentChange {
		return nil
	}
	events, err := readLedgerEventsOverlayUnlocked(runDir)
	if err != nil {
		return err
	}
	for _, item := range evidenceFromEvents(events) {
		if item.Path != registration.Path || item.ContentSHA256 == "" {
			continue
		}
		if item.ContentSHA256 != contentSHA256 {
			return fmt.Errorf("evidence path %s is already registered with different content; write a new task-specific artifact path instead", registration.Path)
		}
	}
	return nil
}

func readCommandInput(inputPath string) ([]byte, error) {
	if strings.TrimSpace(inputPath) != "" {
		return os.ReadFile(inputPath)
	}
	return io.ReadAll(os.Stdin)
}

func writableRunRelPath(runDir, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("path is required")
	}
	absRunDir, err := filepath.Abs(runDir)
	if err != nil {
		return "", err
	}
	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(absRunDir, filepath.FromSlash(candidate))
	}
	absPath, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absRunDir, absPath)
	if err != nil {
		return "", err
	}
	rel = filepath.ToSlash(rel)
	if err := validateTaskBundleRelPath(rel); err != nil {
		return "", fmt.Errorf("path: %w", err)
	}
	if _, err := taskBundleArtifactTargetPath(absRunDir, rel); err != nil {
		return "", err
	}
	return rel, nil
}

func writeEvidenceFileIfAbsentOrSame(runDir, relPath string, data []byte) error {
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("evidence input for %s must not be empty", relPath)
	}
	target, err := taskBundleArtifactTargetPath(runDir, relPath)
	if err != nil {
		return err
	}
	if existing, err := os.ReadFile(target); err == nil {
		if bytes.Equal(existing, data) {
			return nil
		}
		return fmt.Errorf("evidence path %s already exists with different content; choose a fresh path", relPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeFileAtomic(target, data, filePerm)
}

func requiredTextFlag(name, value string) (string, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return "", fmt.Errorf("%s must not be empty", name)
	}
	return text, nil
}
