package main

import "fmt"

func previewRunes(text string, maxRunes int, suffix string) string {
	if maxRunes <= 0 {
		if text == "" {
			return ""
		}
		return suffix
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + suffix
}

func compactStringList(items []string, maxItems, maxRunes int) []string {
	limit := len(items)
	if maxItems > 0 && limit > maxItems {
		limit = maxItems
	}
	compact := make([]string, 0, limit+1)
	for _, item := range items[:limit] {
		compact = append(compact, previewRunes(item, maxRunes, " [truncated]"))
	}
	if limit < len(items) {
		compact = append(compact, fmt.Sprintf("[truncated; %d more items omitted]", len(items)-limit))
	}
	return compact
}
