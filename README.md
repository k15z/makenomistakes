# makenomistakes

`makenomistakes` is an experimental AI audit runner for codebases and software
inputs. The goal is to produce defensible findings, not just scanner-style
warnings: findings should be reviewed, deduplicated, and validated with concrete
evidence whenever possible.

This repository is currently in the local prototype stage. The CLI, run state,
workspace snapshotting, Lima runner lifecycle, VM-side OpenCode bootstrap,
Recon, Investigate, Review, Deduplicate, Validate, and Finalize phases are being
built as a stack of reviewable PRs.

## MVP Direction

The first usable version is planned as a local CLI-first product:

```sh
mnm init
mnm analyze
mnm runs
mnm report show RUN_ID
```

`mnm init` will create a project-level configuration file. `mnm analyze` will
snapshot the configured workspace, launch a disposable local VM, run the audit
pipeline, and generate local reports.
Interrupting `mnm analyze` asks the runner to stop and records the run as
`stopped`; configured runner deadlines are recorded as `timed_out`.
Use `mnm analyze --resume RUN_ID` to continue a prepared, stopped, timed out, or
failed run from its saved snapshot and ledger. Use `mnm runs` to rediscover run
IDs, statuses, resumability, update times, and run directories.
Use `mnm report show RUN_ID` to print the latest finalized Markdown report, or
`mnm report show --json RUN_ID` for the structured report.
If a VM-side run fails before Finalize, `mnm runs` shows the failed stage and
`mnm report show RUN_ID` points to the persisted runner failure evidence.

The default runner target is macOS with Lima/QEMU. Future runner targets may
include cloud VMs. Before it creates run state, `mnm analyze` checks for the
local VM tooling plus requested CPU, memory, and Lima disk capacity.
`mnm analyze --prepare-only` only snapshots local inputs and does not require
Lima.

The VM runner owns all model execution. It installs `opencode` inside the VM,
injects the `mnm` CLI as the structured ledger interface, and bootstraps a
pinned Node.js toolchain when the snapshot contains `package.json` files. It
also ensures `ripgrep` is available so agents have a reliable fast search tool
inside the disposable audit environment.
The extracted snapshot is kept as a pristine base; each OpenCode task receives a
disposable workspace copy so build artifacts, package installs, and repro files
do not leak between agents. The runner also terminates leftover child processes
from each task attempt so background dev servers do not bleed into later work.

## Audit Pipeline

Every stage of the audit pipeline runs inside the VM through non-interactive
`opencode` instances:

1. Recon
2. Investigate
3. Review
4. Deduplicate
5. Validate
6. Finalize

The host machine does not run `opencode`. The VM receives the matching `mnm`
binary, and agents use that CLI as the structured interface for creating and
validating audit ledger entries:

- tasks: scheduled units of agent work.
- leads: questions or areas worth investigating.
- findings: possible defects or vulnerabilities.
- evidence: logs, code references, screenshots, proof scripts, traces, and other
  support material.
- verdicts: review, deduplication, and validation decisions.
- reports: final rendered audit output.

Recon creates bounded leads. Investigate runs OpenCode tasks for open leads,
allows follow-up leads, and records `investigate.limit_reached` if the configured
investigation cap is exhausted before all open leads are consumed. Review runs
one skeptical OpenCode task per candidate finding and records an `accepted` or
`rejected` review verdict. Deduplicate compares review-accepted findings and
records each one as `canonical` or `duplicate`. Validate makes a heavier
end-to-end reproduction or exploit attempt for each canonical finding and
records `proven`, `failed`, or `inconclusive`. Finalize renders `report.md` and
`report.json` from the ledger and evidence.

## Reports

The planned output is:

- `report.md` for human review.
- `report.json` for structured consumption.
- proof evidence such as logs, reproduction scripts, screenshots, request and
  response captures, and validation notes.

Reports should distinguish proven findings from inconclusive, failed, rejected,
and duplicate findings.

## Manual Acceptance

A reusable vulnerable workspace fixture lives at
`examples/vulnerable-workspace`. To run a real end-to-end acceptance audit
through Lima and OpenCode:

```sh
OPENROUTER_API_KEY=... scripts/acceptance-vulnerable-workspace.sh
```

The script copies the fixture to a scratch workspace, runs `mnm analyze`, and
fails unless the final structured report contains at least one proven
file-access finding with evidence. See `docs/acceptance.md` for details.

## Status

Implementation is in progress. The current stack is intentionally CLI-first and
local-only while the audit pipeline is proven end to end.
