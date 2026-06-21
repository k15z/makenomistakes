package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestWriteOpenCodeAuthFile(t *testing.T) {
	path, cleanup, err := writeOpenCodeAuthFile(map[string]string{
		"anthropic":  "anthropic-key",
		"openai":     "openai-key",
		"openrouter": "openrouter-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("auth file mode = %v, want 0600", got)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var auth map[string]map[string]string
	if err := json.Unmarshal(data, &auth); err != nil {
		t.Fatal(err)
	}
	if auth["openrouter"]["type"] != "api" || auth["openrouter"]["key"] != "openrouter-key" {
		t.Fatalf("unexpected openrouter auth payload: %#v", auth)
	}
	if auth["openai"]["type"] != "api" || auth["openai"]["key"] != "openai-key" {
		t.Fatalf("unexpected openai auth payload: %#v", auth)
	}
	if auth["anthropic"]["type"] != "api" || auth["anthropic"]["key"] != "anthropic-key" {
		t.Fatalf("unexpected auth payload: %#v", auth)
	}
}
