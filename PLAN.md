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
  - `mnm analyze --resume <run_id>`: resume an incomplete run from its saved
    snapshot, config snapshot, ledger, and evidence.
  - `mnm analyze --prepare-only`: create config snapshot, workspace snapshot,
    and run state without launching a VM.
  - `mnm analyze --keep-vm`: stop but do not delete the Lima VM after the runner
    exits, for local debugging.
  - `mnm runs`: list local run IDs, statuses, resumability, update times, and
    run directories for resume and report lookup.
  - `mnm report show <run_id>`: print the latest finalized Markdown or JSON
    report for a local run.
  - `mnm runner`: hidden VM-side runner entrypoint.
  - `mnm task`, `mnm lead`, `mnm finding`, `mnm evidence`, `mnm verdict`, and
    `mnm report`: VM-side ledger commands used by `opencode`.
- Host responsibilities:
  - Validate `mnm.toml`, model environment variables, Lima/QEMU, disk, CPU, and
    RAM.
  - Preflight local runner tooling and host resources before creating run state
    for VM-backed execution.
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
  - Isolate each `opencode` task attempt in its own process group and terminate
    leftover child processes before the next task starts.
  - Write events and evidence files to mounted `.mnm/runs/<run_id>/`.
  - Record `evidence/runner-failure.json` and a `runner.failed` event when the
    VM-side pipeline exits before completion.
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
max_leads = 24
max_investigations = 24
parallel_tasks = 2
```

## Local State

- `.mnm/mnm.sqlite`: host-owned indexed state.
- `.mnm/runs/<run_id>/snapshot.tar.zst`: immutable workspace snapshot.
- `.mnm/runs/<run_id>/events.jsonl`: append-only validated event stream.
- `.mnm/runs/<run_id>/evidence/`: logs, phase outputs, proof of concept files,
  screenshots, and validation bundles.
- `.mnm/runs/<run_id>/evidence/runner-failure.json`: structured diagnostics
  for VM-side bootstrap or phase failures.
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
  transitions, missing references, whitespace-only command fields, empty
  registered artifacts, ambiguous evidence ownership, and evidence paths outside
  the run directory after symlink resolution.
- VM-side `mnm verdict record` is idempotent for repeated identical decisions
  and rejects conflicting verdict rewrites for the same finding and phase.
- Ledger reads reject malformed event envelopes, unknown event types, event
  type/object mismatches, missing required event data fields, and invalid event
  data enum values before downstream phases consume state.
- Recon tasks fail postcondition checks unless they register non-empty codebase
  map and risk register evidence, create at least one lead, and stay within
  `runner.max_leads`.
- Investigate tasks fail postcondition checks unless they register a non-empty
  investigation evidence file for the lead being processed, and promoted leads
  must create at least one finding.
- Review tasks fail postcondition checks unless they register a non-empty review
  evidence file for the finding being assessed.
- Deduplicate tasks fail postcondition checks unless they register a non-empty
  deduplication evidence file explaining canonical and duplicate decisions.
- Validate tasks fail postcondition checks unless they register a non-empty
  validation evidence file for the finding being evaluated.
- Interrupted runs checkpoint to `stopped`; rerunning
  `mnm analyze --resume <run_id>` resumes incomplete tasks.
- Fixture repos cover clean, vulnerable, duplicate-finding,
  malformed-agent-output, and broken-dev-environment cases.
- A manual acceptance fixture under `examples/vulnerable-workspace` exercises a
  multi-repo workspace through the real Lima/OpenCode runner and fails unless
  the final structured report contains at least one proven file-access finding
  with evidence.
- Final reports include proven, inconclusive, failed, rejected, and duplicate
  reviewed findings, with proven findings first.
- Final report validation rejects structured report items whose bucket does not
  match their review, deduplication, and validation verdicts in the ledger.
- Final report validation rejects evidence paths that were not registered as
  ledger evidence for the reported finding.
- Final report validation rejects proven findings that do not cite at least one
  registered evidence path.
- Final report validation rejects report items whose ledger-backed title,
  category, severity, confidence, source lead, or cited evidence contents do not
  match the validated ledger state.
- Final report validation rejects duplicate finding items that do not name the
  canonical finding recorded in the deduplication verdict.
- Final report validation rejects report `verdicts` arrays that do not exactly
  match ledger verdicts in review, deduplicate, validate order.
- Final report validation rejects status fields that do not match the report
  bucket implied by ledger verdicts.
- Final report validation rejects reports that omit any ledger finding.

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
8. Investigate phase:
   - Run one OpenCode task per lead, allow follow-up leads, promote concrete
     findings, and cap investigation growth.
9. Review phase:
   - Run one skeptical OpenCode task per candidate finding and record accepted
     or rejected review verdicts.
10. Deduplicate phase:
   - Cluster review-accepted findings and record canonical or duplicate
     verdicts with structured canonical finding references.
11. Validate phase:
   - Run a heavier VM-side reproduction or exploit attempt for each canonical
     finding and record proven, failed, or inconclusive verdicts.
12. Finalize phase:
   - Render final Markdown and JSON reports from the ledger and evidence, then
     register them with `mnm report finalize`.
13. End-to-end acceptance:
   - Run `mnm analyze` against one or more real open-source repos in a scratch
     workspace and commit only reusable fixtures/docs, never secrets or scratch
     outputs.
   - Add a reusable vulnerable multi-repo fixture and a manual acceptance script
     that runs the real VM-backed pipeline and checks for a proven finding in
     `report.json`.

## Deferred

- Host webserver and React UI.
- SaaS auth, billing, and multi-user collaboration.
- GitHub import.
- CI mode.
- Cloud VM provisioning.
- Kubernetes deployment.
