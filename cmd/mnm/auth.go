package main

import (
	"encoding/json"
	"os"
)

func writeOpenCodeAuthFile(providerKeys map[string]string) (string, func(), error) {
	file, err := os.CreateTemp("", "mnm-opencode-auth-*.json")
	if err != nil {
		return "", nil, err
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	auth := map[string]any{}
	for provider, apiKey := range providerKeys {
		auth[provider] = map[string]any{
			"type": "api",
			"key":  apiKey,
		}
	}
	data, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		cleanup()
		return "", nil, err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}
