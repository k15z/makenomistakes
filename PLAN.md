# CLI-First Local MVP Plan

## Summary

Build `mnm` as a single Go CLI for local, resumable audits. Users run
`mnm init` to create `mnm.toml`, then `mnm analyze` to snapshot the workspace,
launch a disposable Lima/QEMU VM, and run the entire audit pipeline inside that
VM.

Every audit phase, from Recon through Finalize, is performed by one or more
non-interactive `opencode` instances running inside the VM. The host never runs
`opencode`. The host only manages config, snapshots, VM lifecycle, state
ingestion, and local report access.

The `mnm` binary is injected into the VM and used by `opencode` as the
structured audit ledger interface. Agents do not write durable audit state
freehand; they call `mnm` commands that validate schemas and append normalized
events.

## Key Components

- CLI commands:
  - `mnm init`: create `mnm.toml` and `.mnmignore`.
  - `mnm analyze`: read config, create/resume a run, launch VM, execute the
    pipeline, and collect results.
  - `mnm runner`: hidden VM-side runner entrypoint.
  - `mnm task`, `mnm lead`, `mnm finding`, `mnm evidence`, `mnm verdict`, and
    `mnm report`: VM-side ledger commands used by `opencode`.
- Host responsibilities:
  - Validate `mnm.toml`, model environment variables, Lima/QEMU, disk, CPU, and
    RAM.
  - Create `.mnm/` and host-owned SQLite state.
  - Build an immutable workspace snapshot.
  - Start one fresh Lima VM per audit run.
  - Inject the matching `mnm` binary, runner config, schemas, prompts, and
    pinned `opencode` bootstrap into the VM.
  - Ingest JSONL events and evidence manifests after or during execution.
  - Render final Markdown and JSON reports from validated ledger state.
- VM runner responsibilities:
  - Bootstrap pinned `opencode`.
  - Unpack the workspace snapshot into `/workspace`.
  - Orchestrate every phase by invoking `opencode run --format json`.
  - Provide each `opencode` instance with phase prompt, scope, prior ledger state,
    and required `mnm` output commands.
  - Reject malformed outputs through `mnm` schema validation.
  - Run validation commands, Docker/Compose, dev servers, tests, and proof of
    concept scripts inside the VM only.
  - Write events and evidence files to mounted `.mnm/runs/<run_id>/`.
  - Shut down the VM after completion or checkpointed stop.

## Ledger Model

- `Run`: one execution of `mnm analyze` against one immutable workspace
  snapshot.
- `Task`: one scheduled unit of work for a single `opencode` instance.
- `Lead`: a question, risk area, or suspected path worth investigating.
- `Finding`: a possible defect or vulnerability that may survive review,
  deduplication, and validation.
- `Evidence`: a file, command output, log, screenshot, trace, code reference, or
  proof of concept attached to a task, lead, finding, verdict, or report.
- `Verdict`: a phase decision about a finding, such as review accepted,
  duplicate, validation proven, validation failed, or validation inconclusive.
- `Report`: the final human-readable and machine-readable audit output.

## VM-Side Command Contract

- `mnm task current`: show the current scheduled task and required output
  contract.
- `mnm task complete`: close the current task after its required ledger events
  have been written.
- `mnm lead create`: create a lead from Recon or from follow-up work discovered
  during Investigate.
- `mnm lead close`: close a lead as no-finding, promoted-to-finding, or
  superseded.
- `mnm finding create`: create a candidate finding from an investigated lead.
- `mnm evidence add`: register logs, code references, screenshots, traces,
  request captures, proof scripts, and other files as evidence.
- `mnm verdict record`: record Review, Deduplicate, or Validate decisions for a
  finding.
- `mnm report finalize`: register the final Markdown and JSON reports.

Only these VM-side commands append durable ledger events. Direct files created
by `opencode` are scratch files until they are registered through
`mnm evidence add` or `mnm report finalize`.

## Audit Pipeline

- `Recon`: inspect `/workspace` and produce a codebase map, scope
  interpretation, risk register, and leads through `mnm evidence add` and
  `mnm lead create`.
- `Investigate`: run one `opencode` instance per lead with bounded parallelism.
  Each task must close the lead, create follow-up leads, or create findings
  through `mnm lead close`, `mnm lead create`, and `mnm finding create`.
- `Review`: run one `opencode` instance per candidate finding. Each task records
  a review verdict through `mnm verdict record`.
- `Deduplicate`: cluster reviewed findings and record canonical or duplicate
  verdicts through `mnm verdict record`.
- `Validate`: run one `opencode` instance per canonical reviewed finding. Each
  task attempts end-to-end reproduction or exploitation inside the VM and must
  record a validation verdict through `mnm verdict record`.
- `Finalize`: run a final `opencode` instance that consumes the ledger and
  evidence files, then calls `mnm report finalize`.

## Configuration

Minimum `mnm.toml` shape:

```toml
version = 1

[instructions]
scope = """
Describe what is in scope, out of scope, and what the audit should care about.
"""

[workspace]
root = "."
exclude = []

[models]
base_url_env = "OPENAI_BASE_URL"
api_key_env = "OPENAI_API_KEY"
default = "openai/gpt-5"
recon = "openai/gpt-5"
investigate = "openai/gpt-5"
review = "openai/gpt-5"
deduplicate = "openai/gpt-5"
validate = "openai/gpt-5"
finalize = "openai/gpt-5"

[runner]
cpus = 4
memory_gb = 8
disk_gb = 80
timeout_minutes = 120
max_leads = 24
```

## Local State

- `.mnm/mnm.sqlite`: host-owned indexed state.
- `.mnm/runs/<run_id>/snapshot.tar.zst`: immutable workspace snapshot.
- `.mnm/runs/<run_id>/events.jsonl`: append-only validated event stream.
- `.mnm/runs/<run_id>/evidence/`: logs, phase outputs, proof of concept files,
  screenshots, and validation bundles.
- `.mnm/runs/<run_id>/report.md`
- `.mnm/runs/<run_id>/report.json`

SQLite is host-owned. The VM never writes directly to SQLite. VM-to-host state
sync uses mounted evidence files plus append-only JSONL events.

## Statuses

Run statuses:

- `created`
- `prepared`
- `snapshotting`
- `vm_starting`
- `running`
- `stopping`
- `stopped`
- `completed`
- `failed`
- `timed_out`

Lead statuses:

- `open`
- `closed_no_finding`
- `promoted_to_finding`
- `superseded`

Finding statuses:

- `candidate`
- `review_rejected`
- `reviewed`
- `duplicate`
- `validation_pending`
- `validation_proven`
- `validation_failed`
- `validation_inconclusive`
- `finalized`

## Test Plan

- `mnm init` creates valid `mnm.toml` and `.mnmignore`.
- `mnm analyze` fails clearly if config, Lima/QEMU, host resources, or model
  environment variables are missing.
- Host snapshot excludes `.mnm`, `.git`, dependency/cache folders, and
  `.mnmignore` entries.
- Host starts a fresh Lima VM, injects `mnm`, bootstraps VM-side `opencode`, and
  receives validated events.
- Every pipeline phase is verified to run through VM-side `opencode`; tests fail
  if any phase executes `opencode` on the host.
- VM-side `mnm` rejects malformed leads, findings, verdicts, invalid status
  transitions, missing references, and evidence paths outside the run directory.
- Interrupted runs checkpoint to `stopped`; rerunning `mnm analyze` resumes
  incomplete tasks.
- Fixture repos cover clean, vulnerable, duplicate-finding,
  malformed-agent-output, and broken-dev-environment cases.
- Final reports include proven, inconclusive, failed, rejected, and duplicate
  reviewed findings, with proven findings first.

## PR Sequence

Implementation should land as small reviewable PRs. Each PR must include tests
and pass GitHub Actions before later work builds on it.

1. CLI bootstrap and CI:
   - Add the Go module, `mnm init`, unit tests, and GitHub Actions for
     formatting, tests, and `go vet`.
2. Config and run state:
   - Parse `mnm.toml`, create `.mnm/`, define run metadata, and persist
     host-owned state.
3. Ledger core:
   - Implement JSONL events and VM-side `mnm task`, `lead`, `finding`,
     `evidence`, `verdict`, and `report` command contracts.
4. Snapshotter:
   - Build immutable workspace snapshots with `.mnmignore`, default excludes,
     symlink safety, and tests.
5. Lima runner lifecycle:
   - Create a fresh VM per run, inject the Linux `mnm` runner payload, copy
     inputs, collect outputs, and shut the VM down.
6. VM-side `opencode` bootstrap:
   - Install or unpack pinned `opencode` inside the VM and verify the host does
     not need local `opencode`.
7. Recon phase:
   - Run Recon through VM-side `opencode`, require ledger writes through `mnm`,
     and generate codebase map, risk register, and leads.
8. Investigate, Review, Deduplicate, Validate, and Finalize:
   - Add one phase per PR, with fixture coverage and report-quality checks.
9. End-to-end acceptance:
   - Run `mnm analyze` against one or more real open-source repos in a scratch
     workspace and commit only reusable fixtures/docs, never secrets or scratch
     outputs.

## Deferred

- Host webserver and React UI.
- SaaS auth, billing, and multi-user collaboration.
- GitHub import.
- CI mode.
- Cloud VM provisioning.
- Kubernetes deployment.
