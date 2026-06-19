package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunnerCommandExtractsSnapshotAndWritesLifecycleEvents(t *testing.T) {
	prependFakeOpenCode(t, opencodeVersion+"\n")
	source := t.TempDir()
	writeWorkspaceFile(t, source, "repo/app.go", "package main")
	snapshot := filepath.Join(t.TempDir(), "snapshot.tar.zst")
	if err := createWorkspaceSnapshot(SnapshotOptions{
		WorkspaceRoot: source,
		WorkspaceDir:  source,
		OutputPath:    snapshot,
	}); err != nil {
		t.Fatal(err)
	}

	runDir := t.TempDir()
	configPath := filepath.Join(runDir, "mnm.toml")
	if err := os.WriteFile(configPath, []byte(defaultConfig()), filePerm); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	err := run([]string{
		"runner",
		"--run-id", "run_test",
		"--run-dir", runDir,
		"--snapshot", snapshot,
		"--config", configPath,
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("runner failed: %v\nstderr: %s", err, stderr.String())
	}

	events, err := readLedgerEvents(runDir)
	if err != nil {
		t.Fatal(err)
	}
	types := eventTypes(events)
	for _, want := range []string{"runner.started", "evidence.added", "runner.completed"} {
		if !contains(types, want) {
			t.Fatalf("missing event type %q in %#v", want, types)
		}
	}
	for _, want := range []string{"task.started", "lead.created", "task.completed"} {
		if !contains(types, want) {
			t.Fatalf("missing recon event type %q in %#v", want, types)
		}
	}
	for _, want := range []string{"finding.created", "lead.closed", "verdict.recorded"} {
		if !contains(types, want) {
			t.Fatalf("missing audit event type %q in %#v", want, types)
		}
	}
	manifest := readFile(t, filepath.Join(runDir, "evidence", "runner-manifest.json"))
	if !strings.Contains(manifest, "repo/app.go") {
		t.Fatalf("manifest missing unpacked workspace file:\n%s", manifest)
	}
	if strings.Contains(manifest, "mutated-by-opencode") {
		t.Fatalf("manifest included task-local mutation:\n%s", manifest)
	}
	if !strings.Contains(manifest, `"opencode_version": "`+opencodeVersion+`"`) {
		t.Fatalf("manifest missing opencode version:\n%s", manifest)
	}
}

func TestLimaRunnerCommandSequence(t *testing.T) {
	runDir := t.TempDir()
	payload := filepath.Join(runDir, "mnm-linux-test")
	if err := os.WriteFile(payload, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MNM_LINUX_RUNNER_PAYLOAD", payload)
	snapshot := filepath.Join(runDir, "snapshot.tar.zst")
	if err := os.WriteFile(snapshot, []byte("snapshot"), filePerm); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(runDir, "mnm.toml")
	if err := os.WriteFile(configPath, []byte(defaultConfig()), filePerm); err != nil {
		t.Fatal(err)
	}

	executor := &recordingExecutor{}
	runner := LimaRunner{Executor: executor, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}
	err := runner.Run(context.Background(), RunnerRequest{
		Run: RunRecord{
			ID:                 "run_abc",
			RunDir:             runDir,
			SnapshotPath:       snapshot,
			ConfigSnapshotPath: configPath,
		},
		Config: Config{Runner: RunnerConfig{CPUs: 2, MemoryGB: 4, DiskGB: 20}},
	})
	if err != nil {
		t.Fatalf("runner failed: %v", err)
	}

	joined := strings.Join(executor.commands, "\n")
	for _, want := range []string{
		"limactl create --tty=false --name mnm-run-abc --cpus 2 --memory 4 --disk 20 template:docker",
		"limactl start --tty=false mnm-run-abc",
		"limactl copy --backend=scp " + payload + " mnm-run-abc:/tmp/mnm",
		"limactl shell mnm-run-abc bash -lc",
		"limactl stop --tty=false mnm-run-abc",
		"limactl delete --force --tty=false mnm-run-abc",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing command %q in:\n%s", want, joined)
		}
	}
}

func TestReconPromptIncludesLeadBodyFileCommand(t *testing.T) {
	cfg := Config{
		Runner: RunnerConfig{MaxLeads: 3},
		Instructions: InstructionConfig{
			Scope: "Focus on parser bugs.",
		},
	}
	prompt := reconPrompt("/tmp/run", "/tmp/workspace", cfg)
	for _, want := range []string{
		"Maximum leads: 3",
		"Focus on parser bugs.",
		"mnm lead create --title",
		"--body-file /tmp/run/evidence/lead-specific-name.md",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

type recordingExecutor struct {
	commands []string
}

func (executor *recordingExecutor) Run(_ context.Context, name string, args ...string) error {
	executor.commands = append(executor.commands, name+" "+strings.Join(args, " "))
	if name == "limactl" && len(args) >= 5 && args[0] == "copy" && args[len(args)-2] == "mnm-run-abc:/tmp/mnm-run" {
		dst := args[len(args)-1]
		outDir := filepath.Join(dst, "mnm-run")
		if err := os.MkdirAll(filepath.Join(outDir, "evidence"), dirPerm); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outDir, eventsFile), []byte(""), filePerm); err != nil {
			return err
		}
	}
	return nil
}

func prependFakeOpenCode(t *testing.T, version string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode")
	body := `#!/bin/sh
set -eu
if [ "${1:-}" = "--version" ]; then
  printf '` + version + `'
  exit 0
fi
if [ "${1:-}" = "run" ]; then
  : "${MNM_RUN_DIR:?MNM_RUN_DIR is required}"
  : "${MNM_TASK_ID:?MNM_TASK_ID is required}"
  prompt=""
  workspace=""
  while [ "$#" -gt 0 ]; do
    if [ "$1" = "--dir" ]; then
      shift
      workspace="${1:-}"
    fi
    prompt="$1"
    shift
  done
  : "${workspace:?workspace is required}"
  printf '%s\n' "$MNM_TASK_ID" > "$workspace/mutated-by-opencode"
  mkdir -p "$MNM_RUN_DIR/evidence"
  if printf '%s' "$prompt" | grep -q 'makenomistakes Review'; then
    : "${MNM_FINDING_ID:?MNM_FINDING_ID is required}"
    cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_fake_review_$MNM_FINDING_ID","run_id":"run_test","type":"verdict.recorded","object":"verdict","object_id":"verdict_fake_$MNM_FINDING_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:07Z","data":{"finding_id":"$MNM_FINDING_ID","phase":"review","value":"accepted","reason":"Accepted by fake review."}}
{"id":"event_fake_review_done_$MNM_FINDING_ID","run_id":"run_test","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:08Z","data":{"status":"completed","summary":"Reviewed $MNM_FINDING_ID"}}
EOF
    printf '{"type":"done"}\n'
    exit 0
  fi
  if printf '%s' "$prompt" | grep -q 'makenomistakes Investigate'; then
    : "${MNM_LEAD_ID:?MNM_LEAD_ID is required}"
    cat > "$MNM_RUN_DIR/evidence/finding-$MNM_LEAD_ID.md" <<'EOF'
# Finding

Fake finding for tests.
EOF
    cat >> "$MNM_RUN_DIR/events.jsonl" <<EOF
{"id":"event_fake_finding_$MNM_LEAD_ID","run_id":"run_test","type":"finding.created","object":"finding","object_id":"finding_fake_$MNM_LEAD_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:04Z","data":{"title":"Fake candidate finding","lead_id":"$MNM_LEAD_ID","category":"authz","severity":"high","confidence":"medium","body_path":"evidence/finding-$MNM_LEAD_ID.md"}}
{"id":"event_fake_lead_closed_$MNM_LEAD_ID","run_id":"run_test","type":"lead.closed","object":"lead","object_id":"$MNM_LEAD_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:05Z","data":{"status":"promoted_to_finding","reason":"Promoted by fake investigate."}}
{"id":"event_fake_investigate_done_$MNM_LEAD_ID","run_id":"run_test","type":"task.completed","object":"task","object_id":"$MNM_TASK_ID","task_id":"$MNM_TASK_ID","timestamp":"2026-01-01T00:00:06Z","data":{"status":"completed","summary":"Investigated $MNM_LEAD_ID"}}
EOF
    printf '{"type":"done"}\n'
    exit 0
  fi
  cat > "$MNM_RUN_DIR/evidence/recon-codebase-map.md" <<'EOF'
# Codebase Map

Fake map for tests.
EOF
  cat > "$MNM_RUN_DIR/evidence/recon-risk-register.md" <<'EOF'
# Risk Register

Fake risk register for tests.
EOF
  cat > "$MNM_RUN_DIR/evidence/lead-auth.md" <<'EOF'
# Lead

Investigate authentication boundaries.
EOF
  cat >> "$MNM_RUN_DIR/events.jsonl" <<'EOF'
{"id":"event_fake_map","run_id":"run_test","type":"evidence.added","object":"evidence","object_id":"evidence_fake_map","task_id":"task_recon","timestamp":"2026-01-01T00:00:00Z","data":{"kind":"markdown","title":"Recon codebase map","path":"evidence/recon-codebase-map.md"}}
{"id":"event_fake_risk","run_id":"run_test","type":"evidence.added","object":"evidence","object_id":"evidence_fake_risk","task_id":"task_recon","timestamp":"2026-01-01T00:00:01Z","data":{"kind":"markdown","title":"Recon risk register","path":"evidence/recon-risk-register.md"}}
{"id":"event_fake_lead","run_id":"run_test","type":"lead.created","object":"lead","object_id":"lead_fake_auth","task_id":"task_recon","timestamp":"2026-01-01T00:00:02Z","data":{"title":"Investigate authentication boundaries","category":"authz","priority":"high","body_path":"evidence/lead-auth.md"}}
{"id":"event_fake_done","run_id":"run_test","type":"task.completed","object":"task","object_id":"task_recon","task_id":"task_recon","timestamp":"2026-01-01T00:00:03Z","data":{"status":"completed","summary":"Recon completed"}}
EOF
  printf '{"type":"done"}\n'
  exit 0
fi
printf 'fake opencode\n'
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}
