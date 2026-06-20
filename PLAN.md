# CLI-First Local MVP Plan

## Summary

Build `mnm` as a single Go CLI for local, resumable audits. Users run
`mnm init` to create `mnm.toml`, then `mnm analyze` to snapshot the workspace,
and execute the audit pipeline through disposable Lima/QEMU task VMs.

Every audit phase, from Recon through Finalize, is performed by one or more
non-interactive `opencode` instances running inside task-scoped VMs. The host
never runs `opencode`. The host only manages config, snapshots, task scheduling,
VM lifecycle, output ingestion, and local report access.

The `mnm` binary is injected into each task VM and used by `opencode` as the
structured audit ledger interface. Agents do not write durable audit state
freehand; they call `mnm` commands that validate schemas and append normalized
events into a task output bundle that the host validates and ingests.

## Key Components

- CLI commands:
  - `mnm init`: create `mnm.toml` and `.mnmignore`.
  - `mnm analyze`: read config, create/resume a run, schedule task VMs, execute
    the pipeline, and collect results.
  - `mnm analyze --resume <run_id>`: resume an incomplete run from its saved
    snapshot, config snapshot, ledger, and evidence.
  - `mnm analyze --prepare-only`: create config snapshot, workspace snapshot,
    and run state without launching a VM.
  - `mnm analyze --keep-vm`: keep task VMs after failure or checkpointed stop,
    for local debugging.
  - `mnm analyze --stop-after <phase>`: run through a completed phase
    (`recon`, `investigate`, `review`, `deduplicate`, or `validate`), checkpoint
    the run as `stopped`, and resume later.
  - `mnm runs`: list local run IDs, statuses, resumability, update times, and
    run directories for resume and report lookup.
  - `mnm report show <run_id>`: print the latest finalized Markdown or JSON
    report for a local run.
  - `mnm runner`: hidden task VM runner entrypoint.
  - `mnm task`, `mnm lead`, `mnm finding`, `mnm evidence`, `mnm verdict`, and
    `mnm report`: ledger commands used by `opencode` inside task VMs.
- Host responsibilities:
  - Validate `mnm.toml`, model environment variables, Lima/QEMU, disk, CPU, and
    RAM.
  - Preflight local runner tooling and aggregate host resources before creating
    run state for VM-backed execution.
  - Create `.mnm/` and host-owned SQLite state.
  - Build an immutable workspace snapshot.
  - Write `evidence/runner-manifest.json` from the captured snapshot before the
    first task is scheduled.
  - Schedule phase tasks from the validated ledger state.
  - Start one fresh Lima/QEMU VM per task attempt; parallel phases start
    parallel VMs up to `runner.parallel_tasks`.
  - Inject the matching `mnm` binary, task file, config snapshot, workspace
    snapshot, relevant run context, and pinned `opencode` bootstrap into each
    task VM.
  - Copy back each task VM's output bundle, validate its JSONL events and
    evidence, and atomically ingest it into the central run ledger.
  - Delete each task VM after its output bundle is collected unless debugging
    options require keeping it.
  - Expose finalized Markdown and JSON reports from validated ledger state.
- Task VM runner responsibilities:
  - Bootstrap pinned `opencode`.
  - Unpack the workspace snapshot into `/workspace`.
  - Restore the task file, config snapshot, current ledger snapshot, and
    evidence files needed by that task.
  - Invoke exactly one `opencode run --format json` task attempt.
  - Provide the `opencode` instance with phase prompt, scope, prior ledger state,
    and required `mnm` output commands.
  - Time-box each `opencode` task attempt and terminate its process group on
    timeout so hung proof commands do not consume the whole run deadline.
  - Reject malformed outputs through `mnm` schema validation.
  - Run validation commands, Docker/Compose, dev servers, tests, and proof of
    concept scripts inside the task VM only.
  - Isolate the `opencode` process group and terminate leftover child processes
    before the task VM exits.
  - Write task-local JSONL events, evidence files, transcripts, and diagnostics
    to an output bundle for host ingestion.
  - Record `runner-failure.json` in the task output bundle and emit a
    `runner.failed` event when the task VM exits before completion.
  - Never write host SQLite or the central run event stream directly.

## Ledger Model

- `Run`: one execution of `mnm analyze` against one immutable workspace
  snapshot.
- `Task`: one scheduled unit of work for a single `opencode` instance in a
  disposable task VM.
- `Lead`: a question, risk area, or suspected path worth investigating.
- `Finding`: a possible defect or vulnerability that may survive review,
  deduplication, and validation.
- `Evidence`: a file, command output, log, screenshot, trace, code reference, or
  proof of concept attached to a task, lead, finding, verdict, or report.
- `Verdict`: a phase decision about a finding, such as review accepted,
  duplicate, validation proven, validation failed, or validation inconclusive.
- `Report`: the final human-readable and machine-readable audit output.

## Task VM Command Contract

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

Only these task VM commands emit task-local ledger events eligible for host
validation and ingestion. Direct files created by `opencode` are scratch files
until they are registered through `mnm evidence add` or `mnm report finalize`.

## Audit Pipeline

- `Recon`: inspect `/workspace` and produce a codebase map, scope
  interpretation, risk register, and leads through `mnm evidence add` and
  `mnm lead create`.
- `Investigate`: run one task VM per lead with bounded parallelism. Each task
  must close the lead, create follow-up leads, or create findings through
  `mnm lead close`, `mnm lead create`, and `mnm finding create`.
- `Review`: run one task VM per candidate finding. Each task records a review
  verdict through `mnm verdict record`.
- `Deduplicate`: run a task VM that clusters reviewed findings and records
  canonical or duplicate verdicts through `mnm verdict record`.
- `Validate`: run one task VM per canonical reviewed finding. Each task attempts
  end-to-end reproduction or exploitation inside its VM and must record a
  validation verdict through `mnm verdict record`.
- `Finalize`: run a final task VM that consumes the ledger and evidence files,
  then calls `mnm report finalize`.

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
opencode_task_timeout_minutes = 30
max_leads = 24
max_investigations = 24
parallel_tasks = 2
```

`cpus`, `memory_gb`, and `disk_gb` are per-task VM requests. `parallel_tasks`
controls the maximum number of task VMs that may run at once; host preflight
checks the aggregate CPU, memory, and disk demand for that maximum local
parallelism.

## Local State

- `.mnm/mnm.sqlite`: host-owned indexed state.
- `.mnm/runs/<run_id>/snapshot.tar.zst`: immutable workspace snapshot.
- `.mnm/runs/<run_id>/events.jsonl`: append-only validated event stream.
- `.mnm/runs/<run_id>/task-bundles/`: staged per-task output bundles copied
  back from task VMs before host ingestion.
- `.mnm/runs/<run_id>/evidence/`: logs, phase outputs, proof of concept files,
  screenshots, and validation bundles.
- `.mnm/runs/<run_id>/task-bundles/<task_id>/runner-failure.json`: structured
  diagnostics for task VM bootstrap or phase failures before ingestion.
- `.mnm/runs/<run_id>/report.md`
- `.mnm/runs/<run_id>/report.json`

SQLite is host-owned. Task VMs never write directly to SQLite or the central
`events.jsonl`. VM-to-host state sync uses staged task bundles containing JSONL
events, evidence files, transcripts, and diagnostics; the host validates and
ingests each bundle atomically.

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
- Host starts a fresh Lima task VM for each task attempt, injects `mnm`,
  bootstraps `opencode` inside it, collects an output bundle, and ingests
  validated events.
- Parallel phases start no more than `runner.parallel_tasks` task VMs at once,
  and host preflight checks aggregate local resources for that maximum.
- Every pipeline phase is verified to run `opencode` inside task VMs; tests fail
  if any phase executes `opencode` on the host or if distinct tasks share a VM
  instance.
- `mnm` inside task VMs rejects malformed leads, findings, verdicts, invalid
  status transitions, missing references, whitespace-only command fields, empty
  registered artifacts, ambiguous evidence ownership, and evidence paths
  outside the task output bundle's evidence root after symlink resolution.
- `mnm lead create`, `mnm lead close`, and `mnm finding create` inside task VMs
  reject attempts from current task phases that are not allowed to perform that
  lifecycle action.
- `mnm evidence add` inside task VMs rejects owner/phase mismatches: only Recon
  and Deduplicate can register unowned evidence, only Investigate can attach
  evidence to leads, and only Investigate, Review, and Validate can attach
  evidence to findings.
- `mnm verdict record` inside task VMs rejects attempts to record a Review,
  Deduplicate, or Validate verdict from any other current task phase.
- `mnm report finalize` inside task VMs rejects attempts from any current task phase
  other than Finalize.
- `mnm verdict record` inside task VMs is idempotent for repeated identical
  decisions and rejects conflicting verdict rewrites for the same finding and
  phase.
- `mnm lead create` inside task VMs is idempotent for repeated identical task/body
  registrations and rejects conflicting metadata for the same task/body path.
- `mnm evidence add` inside task VMs is idempotent for repeated identical
  task/path/owner registrations, can associate the same artifact with both a
  lead and a finding, and rejects conflicting metadata for the same
  task/path/owner.
- `mnm task complete` inside task VMs is idempotent for repeated identical terminal
  status and rejects conflicting task completion status rewrites.
- `mnm report finalize` inside task VMs is idempotent for repeated identical
  report paths and rejects conflicting final report path rewrites for the same
  task.
- Runner-owned lifecycle and failure evidence is idempotent for repeated
  identical run/path registrations.
- `opencode` task attempts have a configurable per-attempt timeout, do not retry
  after that local timeout, and clean up child processes before the task VM is
  deleted.
- Task VM bundles are rejected unless their event stream, evidence paths,
  current task identity, and terminal task status match the scheduled task.
- Ledger reads reject malformed event envelopes, unknown event types, event
  type/object mismatches, missing required event data fields, and invalid event
  data enum values before downstream phases consume state.
- Recon tasks fail postcondition checks unless they register non-empty codebase
  map and risk register evidence, create at least one lead, and stay within
  `runner.max_leads`.
- Recon prompts steer agents to map the workspace and create focused leads
  promptly, leaving proof, exploitation, and falsification work to Investigate
  and Validate.
- Investigate tasks fail postcondition checks unless they register a non-empty
  investigation evidence file for the lead being processed, and promoted leads
  must create at least one finding.
- Review tasks fail postcondition checks unless they register a non-empty review
  evidence file for the finding being assessed.
- Deduplicate tasks fail postcondition checks unless they register a non-empty
  deduplication evidence file explaining canonical and duplicate decisions.
- Validate tasks fail postcondition checks unless they register a non-empty
  validation evidence file for the finding being evaluated.
- Validate tasks that record a `proven` verdict fail postcondition checks unless
  they register at least one additional non-empty proof artifact beyond the
  required validation notes.
- Interrupted runs checkpoint to `stopped`; rerunning
  `mnm analyze --resume <run_id>` resumes incomplete tasks.
- `mnm analyze --stop-after recon` completes Recon through the real task
  VM/`opencode` path, copies back registered map/risk/lead artifacts, marks the
  run `stopped`, and can later resume into Investigate.
- Fixture repos cover clean, vulnerable, duplicate-finding,
  malformed-agent-output, and broken-dev-environment cases.
- A manual acceptance fixture under `examples/vulnerable-workspace` exercises a
  multi-repo workspace through the real Lima/`opencode` runner and fails unless
  the final structured report contains at least one proven file-access finding
  with evidence.
- Final reports include proven, inconclusive, failed, rejected, and duplicate
  reviewed findings, with proven findings first.
- Final report validation rejects structured report items whose bucket does not
  match their review, deduplication, and validation verdicts in the ledger.
- Final report validation rejects evidence paths that were not registered as
  ledger evidence for the reported finding.
- Final report validation rejects evidence paths that do not exactly match the
  registered run-relative ledger path.
- Final report validation rejects affected paths that are empty, absolute,
  unclean, use backslashes, or traverse outside the workspace.
- Final report validation rejects affected paths that are absent from the
  runner workspace manifest when that manifest is available.
- Final report validation rejects proven findings that do not cite at least one
  registered evidence path.
- Final report validation rejects proven findings that do not cite at least one
  validation proof artifact registered by the finding's Validate task.
- Final report validation rejects report items whose ledger-backed title,
  category, severity, confidence, source lead, or cited evidence contents do not
  match the validated ledger state.
- Final report validation rejects duplicate finding items that do not name the
  canonical finding recorded in the deduplication verdict.
- Final report validation rejects report `verdicts` arrays that do not exactly
  match ledger verdicts in review, deduplicate, validate order.
- Final report validation rejects Markdown reports that omit any ledger finding
  ID.
- Final report validation rejects Markdown reports that omit any evidence path
  cited by the structured report.
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
   - Implement JSONL events and task VM `mnm task`, `lead`, `finding`,
     `evidence`, `verdict`, and `report` command contracts.
4. Snapshotter:
   - Build immutable workspace snapshots with `.mnmignore`, default excludes,
     symlink safety, and tests.
5. Task VM lifecycle:
   - Create a fresh VM per task attempt, inject the Linux `mnm` runner payload,
     copy inputs, collect the output bundle, and shut the VM down.
6. Task VM `opencode` bootstrap and task runner:
   - Install or unpack pinned `opencode` inside the task VM and verify the host
     does not need local `opencode`.
7. Recon phase:
   - Run Recon through `opencode` inside a task VM, require ledger writes
     through `mnm`, and generate codebase map, risk register, and leads.
8. Investigate phase:
   - Run one task VM per lead, allow follow-up leads, promote concrete findings,
     and cap investigation growth.
9. Review phase:
   - Run one skeptical task VM per candidate finding and record accepted or
     rejected review verdicts.
10. Deduplicate phase:
   - Cluster review-accepted findings and record canonical or duplicate
     verdicts with structured canonical finding references.
11. Validate phase:
   - Run a heavier task-VM reproduction or exploit attempt for each canonical
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
