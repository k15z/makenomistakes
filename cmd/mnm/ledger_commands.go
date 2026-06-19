package main

import (
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
		return appendLedgerEvent(runDir, LedgerEvent{
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
	default:
		return fmt.Errorf("unknown task subcommand %q", args[0])
	}
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
		bodyPath, err := requirePathInsideRunDir(runDir, *bodyFile)
		if err != nil {
			return err
		}
		if err := requireNonEmptyRunFile(runDir, bodyPath, "lead body file"); err != nil {
			return err
		}
		leadID := newLedgerID("lead")
		if err := appendLedgerEvent(runDir, LedgerEvent{
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
		}); err != nil {
			return err
		}
		fmt.Fprintln(stdout, leadID)
		return nil
	case "close":
		flags := flag.NewFlagSet("lead close", flag.ContinueOnError)
		flags.SetOutput(stderr)
		runDirFlag := flags.String("run-dir", "", "run directory")
		id := flags.String("id", "", "lead id")
		status := flags.String("status", "", "closed_no_finding|promoted_to_finding|superseded")
		reason := flags.String("reason", "", "reason")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" || *status == "" {
			return errors.New("lead close requires --id and --status")
		}
		if !oneOf(*status, "closed_no_finding", "promoted_to_finding", "superseded") {
			return fmt.Errorf("invalid lead close status %q", *status)
		}
		runDir, task, err := currentTaskForCommand(*runDirFlag)
		if err != nil {
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
		currentStatus, exists, err := ledgerLeadStatusUnlocked(runDir, *id)
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
	if len(args) == 0 || args[0] != "add" {
		return errors.New("evidence supports: add")
	}
	flags := flag.NewFlagSet("evidence add", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runDirFlag := flags.String("run-dir", "", "run directory")
	kind := flags.String("kind", "", "evidence kind")
	title := flags.String("title", "", "evidence title")
	path := flags.String("path", "", "evidence file path")
	leadID := flags.String("lead", "", "lead id")
	findingID := flags.String("finding", "", "finding id")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	kindText, err := requiredTextFlag("--kind", *kind)
	if err != nil {
		return err
	}
	titleText, err := requiredTextFlag("--title", *title)
	if err != nil {
		return err
	}
	if *leadID != "" && *findingID != "" {
		return errors.New("evidence add accepts at most one of --lead or --finding")
	}
	runDir, task, err := currentTaskForCommand(*runDirFlag)
	if err != nil {
		return err
	}
	if *leadID != "" {
		if err := requireLedgerObject(runDir, "lead", *leadID); err != nil {
			return err
		}
	}
	if *findingID != "" {
		if err := requireLedgerObject(runDir, "finding", *findingID); err != nil {
			return err
		}
	}
	relPath, err := requirePathInsideRunDir(runDir, *path)
	if err != nil {
		return err
	}
	if err := requireNonEmptyRunFile(runDir, relPath, "evidence file"); err != nil {
		return err
	}
	contentSHA256 := ""
	if info, err := os.Stat(filepath.Join(runDir, filepath.FromSlash(relPath))); err == nil && info.Mode().IsRegular() {
		contentSHA256, err = evidenceFileSHA256(runDir, relPath)
		if err != nil {
			return fmt.Errorf("hash evidence file %s: %w", relPath, err)
		}
	} else if err != nil {
		return fmt.Errorf("stat evidence path %s: %w", relPath, err)
	}
	evidenceID := newLedgerID("evidence")
	event, err := prepareLedgerEvent(runDir, LedgerEvent{
		RunID:    task.RunID,
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: evidenceID,
		TaskID:   task.TaskID,
		Data: map[string]any{
			"kind":           kindText,
			"title":          titleText,
			"path":           relPath,
			"lead_id":        *leadID,
			"finding_id":     *findingID,
			"content_sha256": contentSHA256,
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
	existing, exists, err := ledgerTaskEvidencePathUnlocked(runDir, task.TaskID, relPath)
	if err != nil {
		return err
	}
	if exists {
		if existing.Kind == kindText && existing.Title == titleText && existing.LeadID == *leadID && existing.FindingID == *findingID && existing.ContentSHA256 == contentSHA256 {
			fmt.Fprintln(stdout, existing.ID)
			return nil
		}
		return fmt.Errorf("task %s already registered evidence path %s with different metadata", task.TaskID, relPath)
	}
	if err := appendLedgerEventUnlocked(runDir, event); err != nil {
		return err
	}
	fmt.Fprintln(stdout, evidenceID)
	return nil
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
		return fmt.Errorf("current task phase %q cannot record %s verdict", task.Phase, *phase)
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
	events, err := readLedgerEventsUnlocked(runDir)
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
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    task.RunID,
		Type:     "report.finalized",
		Object:   "report",
		ObjectID: newLedgerID("report"),
		TaskID:   task.TaskID,
		Data: map[string]any{
			"markdown_path": markdownRel,
			"json_path":     jsonRel,
		},
	}); err != nil {
		return err
	}
	fmt.Fprintln(stdout, "report finalized")
	return nil
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

func ledgerTaskEvidencePathUnlocked(runDir, taskID, relPath string) (EvidenceRecord, bool, error) {
	events, err := readLedgerEventsUnlocked(runDir)
	if err != nil {
		return EvidenceRecord{}, false, err
	}
	var match EvidenceRecord
	found := false
	for _, item := range evidenceFromEvents(events) {
		if item.TaskID == taskID && item.Path == relPath {
			match = item
			found = true
		}
	}
	return match, found, nil
}

func requiredTextFlag(name, value string) (string, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return "", fmt.Errorf("%s must not be empty", name)
	}
	return text, nil
}
