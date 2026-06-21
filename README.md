# makenomistakes

`makenomistakes` is an experimental AI audit runner for codebases and software
inputs. The goal is to produce defensible findings, not just scanner-style
warnings: findings should be reviewed, deduplicated, and validated with concrete
evidence whenever possible.

This repository is currently in the local prototype stage. The CLI, run state,
workspace snapshotting, task VM runner lifecycle, task VM `opencode` bootstrap,
Recon, Investigate, Review, Deduplicate, Validate, and Finalize phases are
being built as a stack of reviewable PRs.

## MVP Direction

The first usable version is planned as a local CLI-first product:

```sh
mnm init
mnm analyze
mnm runs
mnm report show RUN_ID
```

`mnm init` will create a project-level configuration file. `mnm analyze` will
snapshot the configured workspace, schedule disposable local task VMs, run the
audit pipeline, and generate local reports.
Interrupting `mnm analyze` asks the runner to stop and records the run as
`stopped`; configured runner deadlines are recorded as `timed_out`.
Each task VM `opencode` attempt also has its own configurable timeout, so a hung
proof command fails that task without consuming the entire run deadline.
Use `mnm analyze --resume RUN_ID` to continue a prepared, stopped, timed out, or
failed run from its saved snapshot and ledger. Use `mnm runs` to rediscover run
IDs, statuses, resumability, update times, and run directories.
Use `mnm analyze --stop-after recon` to checkpoint after Recon while iterating
on audit setup or reviewing generated leads before resuming.
Set `instructions.risk_areas` in `mnm.toml` for narrower campaigns such as
authorization, data exposure, state consistency, transaction validation, input
parsing, or deployment boundaries. Recon uses those areas to create focused
leads instead of spreading effort across a generic audit pass.
Use `mnm report show RUN_ID` to print the latest finalized Markdown report, or
`mnm report show --json RUN_ID` for the structured report.
If a task VM or host orchestration fails before Finalize, `mnm runs` shows the
failed stage and `mnm report show RUN_ID` points to the persisted runner
failure evidence.

The default runner target is macOS with Lima/QEMU. Future runner targets may
include cloud VMs. Before it creates run state, `mnm analyze` checks for the
local VM tooling plus requested aggregate CPU, memory, and Lima disk capacity
for the configured local task parallelism.
`mnm analyze --prepare-only` only snapshots local inputs and does not require
Lima.

The target runner model owns all model execution by scheduling one disposable
task VM per `opencode` task attempt. Each task VM installs or bootstraps
`opencode`, receives the matching `mnm` CLI as the structured ledger interface,
and bootstraps a pinned Node.js toolchain when the snapshot contains
`package.json` files. It also ensures `ripgrep` is available so agents have a
reliable fast search tool inside the disposable audit environment.
Each task VM receives the immutable workspace snapshot plus the ledger and
evidence context needed for its task. Build artifacts, package installs, dev
servers, containers, and repro files disappear with that VM after the host has
collected and validated its output bundle.

Projects can declare a first-class per-task setup hook in `mnm.toml`:

```toml
[runner.setup]
script = "audit/setup-vm.sh"
timeout_minutes = 15
mode = "fail" # or "warn"
```

The script path is relative to the workspace root and must be included in the
snapshot. The runner sources it inside every task workspace after snapshot
extraction and before `opencode` starts, so exported environment variables are
passed to the task process. The hook also receives `MNM_RUN_DIR`,
`MNM_TASK_ID`, `MNM_PHASE`, `MNM_WORKSPACE`, and `MNM_SETUP_LOG`. Setup stdout
and stderr are captured under each task output bundle as
`evidence/setup-TASK-attempt-N.log`.

Model execution uses provider-prefixed `opencode` model ids. OpenRouter,
OpenAI, and Anthropic are first-class providers:

```toml
[models]
default = "openrouter/deepseek/deepseek-v4-pro"
openrouter_api_key_env = "OPENROUTER_API_KEY"
openai_api_key_env = "OPENAI_API_KEY"
anthropic_api_key_env = "ANTHROPIC_API_KEY"
```

If phase-specific models use multiple provider prefixes, `mnm` injects each
corresponding API key into the task VM's OpenCode auth file. The older
`api_key_env` setting remains supported as a single-provider fallback. For
unprefixed single-provider model ids, set `provider` to `openrouter`, `openai`,
or `anthropic`.

## Audit Pipeline

Every stage of the audit pipeline runs inside task VMs through non-interactive
`opencode` instances:

1. Recon
2. Investigate
3. Review
4. Deduplicate
5. Validate
6. Finalize

The host machine does not run `opencode`. Each task VM receives the matching
`mnm` binary, and agents use that CLI as the structured interface for creating
and validating audit ledger entries:

- tasks: scheduled units of agent work.
- leads: questions or areas worth investigating.
- findings: possible defects or vulnerabilities.
- evidence: logs, code references, screenshots, proof scripts, traces, and other
  support material.
- verdicts: review, deduplication, and validation decisions.
- reports: final rendered audit output.

Recon creates bounded leads. Investigate runs one task VM per open lead, allows
follow-up leads, and records `investigate.limit_reached` if the configured
investigation cap is exhausted before all open leads are consumed. Review runs
one skeptical task VM per candidate finding and records an `accepted` or
`rejected` review verdict. Deduplicate compares review-accepted findings and
records each one as `canonical` or `duplicate`. Validate makes a heavier
end-to-end reproduction or exploit attempt in a task VM for each canonical
finding and records `proven`, `failed`, or `inconclusive`. Inconclusive
validation must include an actionable blocker: missing dependency, failed
command, required service, suspected config gap, and next command where known.
Finalize renders `report.md` and `report.json` from the ledger and evidence.

Each post-recon task also receives a compact `phase-handoff-*.json` context
artifact. It summarizes open leads, confirmed dead ends, findings and verdicts,
setup logs, and task-authored handoff JSON so later phases can reuse prior
commands, setup discoveries, blockers, and likely follow-up areas instead of
rediscovering them in a fresh VM.

Leads dismissed as not real issues must carry structured negative proof when
they are closed as `closed_no_finding`: the exact boundary, enforcement point,
deployment exposure, and edge cases checked. Leads that remain plausible but
lack enough positive evidence or negative proof should be closed as
`inconclusive`, preserving uncertainty instead of turning it into a confirmed
dead end.

## Reports

The planned output is:

- `report.md` for human review.
- `report.json` for structured consumption.
- proof evidence such as logs, reproduction scripts, screenshots, request and
  response captures, and validation notes.

Reports should distinguish proven findings from inconclusive, failed, rejected,
and duplicate findings. When validation is blocked, structured reports must
preserve the remediation checklist as `validation_blockers` so a later run can
resume from the precise missing setup step instead of rediscovering it.

## Manual Acceptance

A reusable vulnerable workspace fixture lives at
`examples/vulnerable-workspace`. To run a real end-to-end acceptance audit
through Lima and `opencode`:

```sh
OPENROUTER_API_KEY=... scripts/acceptance-vulnerable-workspace.sh
```

Set `OPENAI_API_KEY` or `ANTHROPIC_API_KEY` instead when the configured models
use `openai/...` or `anthropic/...` provider prefixes.

The script copies the fixture to a scratch workspace, runs `mnm analyze`, and
fails unless the final structured report contains at least one proven
file-access finding with evidence. See `docs/acceptance.md` for details.

A second manual benchmark script runs the same real pipeline against a pinned
OWASP NodeGoat revision:

```sh
OPENROUTER_API_KEY=... scripts/acceptance-nodegoat.sh
```

This benchmark fails unless the final structured report contains at least one
proven NodeGoat security finding with registered evidence. It can take
substantially longer than the small fixture and consumes model-provider quota.

## Status

The current prototype is intentionally CLI-first and local-only while the audit
pipeline is proven end to end. It runs the full Recon, Investigate, Review,
Deduplicate, Validate, and Finalize flow through task-scoped Lima VMs, with the
host responsible for orchestration, bundle validation, and report access.
