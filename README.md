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
```

`mnm init` will create a project-level configuration file. `mnm analyze` will
snapshot the configured workspace, launch a disposable local VM, run the audit
pipeline, and generate local reports.

The default runner target is macOS with Lima/QEMU. Future runner targets may
include cloud VMs.

The VM runner owns all model execution. It installs `opencode` inside the VM,
injects the `mnm` CLI as the structured ledger interface, and bootstraps a
pinned Node.js toolchain when the snapshot contains `package.json` files.
The extracted snapshot is kept as a pristine base; each OpenCode task receives a
disposable workspace copy so build artifacts, package installs, and repro files
do not leak between agents.

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

## Status

Implementation is in progress. The current stack is intentionally CLI-first and
local-only while the audit pipeline is proven end to end.
