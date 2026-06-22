package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func handoffCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("handoff supports: write")
	}
	switch args[0] {
	case "write":
		return handoffWriteCommand(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown handoff subcommand %q; expected write", args[0])
	}
}

func handoffWriteCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("handoff write", flag.ContinueOnError)
	flags.SetOutput(stderr)
	runDirFlag := flags.String("run-dir", "", "run directory")
	path := flags.String("path", "", "handoff JSON path")
	leadIDFlag := flags.String("lead", "", "lead id")
	findingIDFlag := flags.String("finding", "", "finding id")
	title := flags.String("title", "", "evidence title")
	input := flags.String("input", "", "input JSON file; reads stdin when omitted")
	if err := flags.Parse(args); err != nil {
		return err
	}

	runDir, task, err := currentTaskForCommand(*runDirFlag)
	if err != nil {
		return err
	}
	data, err := readCommandInput(*input)
	if err != nil {
		return err
	}
	var handoff taskHandoffFile
	if err := json.Unmarshal(data, &handoff); err != nil {
		return fmt.Errorf("parse handoff input: %w", err)
	}
	leadID, findingID, err := handoffOwner(task, handoff, *leadIDFlag, *findingIDFlag)
	if err != nil {
		return err
	}
	if err := requireHandoffPhaseOwner(task, leadID, findingID); err != nil {
		return err
	}
	if leadID != "" {
		if err := requireLedgerObject(runDir, "lead", leadID); err != nil {
			return err
		}
	}
	if findingID != "" {
		if err := requireLedgerObject(runDir, "finding", findingID); err != nil {
			return err
		}
	}
	handoff = normalizeTaskHandoff(handoff, task, leadID, findingID)
	registration := taskEvidenceRegistration{
		RunID:     task.RunID,
		TaskID:    task.TaskID,
		Kind:      "json",
		Title:     handoffEvidenceTitle(*title, task, leadID, findingID),
		LeadID:    leadID,
		FindingID: findingID,
	}
	if err := validateTaskHandoffFile(handoff, EvidenceRecord{
		TaskID:    registration.TaskID,
		LeadID:    registration.LeadID,
		FindingID: registration.FindingID,
	}, false); err != nil {
		return fmt.Errorf("validate handoff input: %w", err)
	}
	relPath, err := writableRunRelPath(runDir, *path)
	if err != nil {
		return err
	}
	registration.Path = relPath
	output, err := json.MarshalIndent(handoff, "", "  ")
	if err != nil {
		return err
	}
	output = append(output, '\n')
	evidenceID, err := writeAndRegisterTaskEvidence(runDir, registration, output)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, evidenceID)
	return nil
}

func handoffOwner(task TaskRecord, handoff taskHandoffFile, leadIDFlag, findingIDFlag string) (string, string, error) {
	leadID := firstNonEmpty(leadIDFlag, os.Getenv("MNM_LEAD_ID"), handoff.LeadID)
	findingID := firstNonEmpty(findingIDFlag, os.Getenv("MNM_FINDING_ID"), handoff.FindingID)
	if leadID != "" && findingID != "" {
		return "", "", errors.New("handoff write accepts at most one of --lead or --finding")
	}
	if leadID != "" && handoff.LeadID != "" && leadID != handoff.LeadID {
		return "", "", fmt.Errorf("handoff lead_id %q does not match owner %q", handoff.LeadID, leadID)
	}
	if findingID != "" && handoff.FindingID != "" && findingID != handoff.FindingID {
		return "", "", fmt.Errorf("handoff finding_id %q does not match owner %q", handoff.FindingID, findingID)
	}
	if task.Phase == "deduplicate" {
		if leadID != "" || findingID != "" {
			return "", "", errors.New("deduplicate handoff must not include --lead, --finding, lead_id, or finding_id")
		}
		return "", "", nil
	}
	return leadID, findingID, nil
}

func requireHandoffPhaseOwner(task TaskRecord, leadID, findingID string) error {
	switch task.Phase {
	case "investigate":
		if leadID == "" || findingID != "" {
			return errors.New("investigate handoff requires --lead")
		}
	case "review", "validate":
		if findingID == "" || leadID != "" {
			return fmt.Errorf("%s handoff requires --finding", task.Phase)
		}
	case "deduplicate":
		if leadID != "" || findingID != "" {
			return errors.New("deduplicate handoff must not include --lead or --finding")
		}
	default:
		return fmt.Errorf("current task phase %q cannot write a handoff; expected one of: investigate, review, deduplicate, validate", task.Phase)
	}
	return nil
}

func normalizeTaskHandoff(handoff taskHandoffFile, task TaskRecord, leadID, findingID string) taskHandoffFile {
	if handoff.Version == 0 {
		handoff.Version = phaseHandoffVersion
	}
	handoff.Phase = task.Phase
	handoff.TaskID = task.TaskID
	handoff.LeadID = leadID
	handoff.FindingID = findingID
	handoff.SourcePath = ""
	return handoff
}

func handoffEvidenceTitle(title string, task TaskRecord, leadID, findingID string) string {
	if strings.TrimSpace(title) != "" {
		return strings.TrimSpace(title)
	}
	switch {
	case task.Phase == "deduplicate":
		return "Task handoff: Deduplication"
	case leadID != "":
		return "Task handoff: Lead " + leadID
	case findingID != "":
		return "Task handoff: " + findingID
	default:
		return "Task handoff: " + task.TaskID
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
