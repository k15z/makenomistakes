# makenomistakes

`makenomistakes` is an experimental AI audit runner for codebases and software
inputs. The goal is to produce defensible findings, not just scanner-style
warnings: findings should be reviewed, deduplicated, and validated with concrete
evidence whenever possible.

This repository is currently in the planning/prototype stage. It does not yet
contain a working implementation.

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

## Reports

The planned output is:

- `report.md` for human review.
- `report.json` for structured consumption.
- proof evidence such as logs, reproduction scripts, screenshots, request and
  response captures, and validation notes.

Reports should distinguish proven findings from inconclusive, failed, rejected,
and duplicate findings.

## Status

This repository currently contains design documentation only. Implementation
will follow after the MVP plan is reviewed.
