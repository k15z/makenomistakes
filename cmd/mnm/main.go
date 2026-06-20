package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const (
	filePerm = 0o644
	dirPerm  = 0o755
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "mnm:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}

	switch args[0] {
	case "help", "-h", "--help":
		printUsage(stdout)
		return nil
	case "init":
		return initCommand(args[1:], stdout, stderr)
	case "analyze":
		return analyzeCommand(args[1:], stdout, stderr)
	case "runs":
		return runsCommand(args[1:], stdout, stderr)
	case "task":
		return taskCommand(args[1:], stdout, stderr)
	case "lead":
		return leadCommand(args[1:], stdout, stderr)
	case "finding":
		return findingCommand(args[1:], stdout, stderr)
	case "evidence":
		return evidenceCommand(args[1:], stdout, stderr)
	case "verdict":
		return verdictCommand(args[1:], stdout, stderr)
	case "report":
		return reportCommand(args[1:], stdout, stderr)
	case "runner":
		return runnerCommand(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `mnm

Usage:
  mnm init [--force] [path]
  mnm analyze [--prepare-only] [--keep-vm] [--resume RUN_ID] [path]
  mnm runs [--json] [path]
  mnm report show [--json] RUN_ID [path]
  mnm task|lead|finding|evidence|verdict|report ...

Commands:
  init       Create mnm.toml and .mnmignore in a workspace.
  analyze    Prepare, run, or resume a durable local audit.
  runs       List local audit runs, statuses, and resumability.
  task       Read or complete the current VM-side task.
  lead       Create or close audit leads.
  finding    Create candidate findings.
  evidence   Register evidence files.
  verdict    Record review, deduplication, or validation decisions.
  report     Finalize or show generated reports.
  runner     Internal VM-side runner entrypoint.
`)
}

func initCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(stderr)
	force := flags.Bool("force", false, "overwrite existing files")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if flags.NArg() > 1 {
		return errors.New("init accepts at most one path")
	}

	target := "."
	if flags.NArg() == 1 {
		target = flags.Arg(0)
	}

	workspace, err := filepath.Abs(target)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(workspace, dirPerm); err != nil {
		return err
	}

	configPath := filepath.Join(workspace, "mnm.toml")
	ignorePath := filepath.Join(workspace, ".mnmignore")
	if !*force {
		for _, path := range []string{configPath, ignorePath} {
			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("%s already exists; pass --force to overwrite", filepath.Base(path))
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}

	if err := os.WriteFile(configPath, []byte(defaultConfig()), filePerm); err != nil {
		return err
	}
	if err := os.WriteFile(ignorePath, []byte(defaultIgnore()), filePerm); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "created %s\n", configPath)
	fmt.Fprintf(stdout, "created %s\n", ignorePath)
	return nil
}

func defaultConfig() string {
	return `version = 1

[instructions]
scope = """
Describe what is in scope, out of scope, and what the audit should care about.
"""

[workspace]
root = "."
exclude = []

[models]
api_key_env = "OPENROUTER_API_KEY"
default = "openrouter/z-ai/glm-5.2"
recon = "openrouter/z-ai/glm-5.2"
investigate = "openrouter/z-ai/glm-5.2"
review = "openrouter/z-ai/glm-5.2"
deduplicate = "openrouter/z-ai/glm-5.2"
validate = "openrouter/z-ai/glm-5.2"
finalize = "openrouter/z-ai/glm-5.2"

[runner]
cpus = 4
memory_gb = 8
disk_gb = 80
timeout_minutes = 120
opencode_task_timeout_minutes = 30
max_leads = 24
max_investigations = 24
parallel_tasks = 2
`
}

func defaultIgnore() string {
	return `.mnm/
.git/
node_modules/
vendor/
dist/
build/
coverage/
.cache/
.next/
target/
tmp/
temp/
*.log
.DS_Store
`
}
