package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

type LeadRecord struct {
	ID       string
	Title    string
	Category string
	Priority string
	BodyPath string
	Status   string
}

func ledgerLeads(runDir string) ([]LeadRecord, error) {
	events, err := readLedgerEvents(runDir)
	if err != nil {
		return nil, err
	}
	leadsByID := map[string]LeadRecord{}
	var order []string
	for _, event := range events {
		if event.Object != "lead" {
			continue
		}
		switch event.Type {
		case "lead.created":
			lead := LeadRecord{
				ID:       event.ObjectID,
				Title:    stringData(event.Data, "title"),
				Category: stringData(event.Data, "category"),
				Priority: stringData(event.Data, "priority"),
				BodyPath: stringData(event.Data, "body_path"),
				Status:   "open",
			}
			if _, exists := leadsByID[lead.ID]; !exists {
				order = append(order, lead.ID)
			}
			leadsByID[lead.ID] = lead
		case "lead.closed":
			lead, exists := leadsByID[event.ObjectID]
			if !exists {
				continue
			}
			lead.Status = stringData(event.Data, "status")
			leadsByID[event.ObjectID] = lead
		}
	}

	leads := make([]LeadRecord, 0, len(order))
	for _, id := range order {
		leads = append(leads, leadsByID[id])
	}
	return leads, nil
}

func openLedgerLeads(runDir string) ([]LeadRecord, error) {
	leads, err := ledgerLeads(runDir)
	if err != nil {
		return nil, err
	}
	var open []LeadRecord
	for _, lead := range leads {
		if lead.Status == "open" {
			open = append(open, lead)
		}
	}
	return open, nil
}

func ledgerLeadClosed(runDir, leadID string) bool {
	leads, err := ledgerLeads(runDir)
	if err != nil {
		return false
	}
	for _, lead := range leads {
		if lead.ID == leadID {
			return lead.Status != "open"
		}
	}
	return false
}

func leadBodyPath(runDir string, lead LeadRecord) (string, error) {
	if lead.BodyPath == "" {
		return "", fmt.Errorf("lead %s is missing body_path", lead.ID)
	}
	return filepath.Join(runDir, filepath.FromSlash(lead.BodyPath)), nil
}

func stringData(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	value, ok := data[key]
	if !ok || value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func safeFileID(value string) string {
	var builder strings.Builder
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			builder.WriteRune(r)
			continue
		}
		builder.WriteByte('_')
	}
	if builder.Len() == 0 {
		return "item"
	}
	return builder.String()
}
