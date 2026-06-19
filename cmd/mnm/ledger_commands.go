package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
				"summary": *summary,
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
		if *title == "" {
			return errors.New("lead create requires --title")
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
		leadID := newLedgerID("lead")
		if err := appendLedgerEvent(runDir, LedgerEvent{
			RunID:    task.RunID,
			Type:     "lead.created",
			Object:   "lead",
			ObjectID: leadID,
			TaskID:   task.TaskID,
			Data: map[string]any{
				"title":     *title,
				"category":  *category,
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
		if currentStatus != "open" {
			if currentStatus == *status {
				return nil
			}
			return fmt.Errorf("lead %s is already closed with status %q", *id, currentStatus)
		}
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
	if *title == "" {
		return errors.New("finding create requires --title")
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
	findingID := newLedgerID("finding")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    task.RunID,
		Type:     "finding.created",
		Object:   "finding",
		ObjectID: findingID,
		TaskID:   task.TaskID,
		Data: map[string]any{
			"title":      *title,
			"lead_id":    *leadID,
			"category":   *category,
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
	if *kind == "" || *title == "" {
		return errors.New("evidence add requires --kind and --title")
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
	evidenceID := newLedgerID("evidence")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    task.RunID,
		Type:     "evidence.added",
		Object:   "evidence",
		ObjectID: evidenceID,
		TaskID:   task.TaskID,
		Data: map[string]any{
			"kind":       *kind,
			"title":      *title,
			"path":       relPath,
			"lead_id":    *leadID,
			"finding_id": *findingID,
		},
	}); err != nil {
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
	verdictID := newLedgerID("verdict")
	if err := appendLedgerEvent(runDir, LedgerEvent{
		RunID:    task.RunID,
		Type:     "verdict.recorded",
		Object:   "verdict",
		ObjectID: verdictID,
		TaskID:   task.TaskID,
		Data: map[string]any{
			"finding_id":           *findingID,
			"phase":                *phase,
			"value":                *value,
			"reason":               *reason,
			"canonical_finding_id": *canonicalFindingID,
		},
	}); err != nil {
		return err
	}
	fmt.Fprintln(stdout, verdictID)
	return nil
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
	if len(args) == 0 || args[0] != "finalize" {
		return errors.New("report supports: finalize")
	}
	flags := flag.NewFlagSet("report finalize", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runDirFlag := flags.String("run-dir", "", "run directory")
	markdownPath := flags.String("markdown", "", "markdown report path")
	jsonPath := flags.String("json", "", "json report path")
	if err := flags.Parse(args[1:]); err != nil {
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
