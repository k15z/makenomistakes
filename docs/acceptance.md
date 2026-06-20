# Acceptance Runs

This repository includes manual end-to-end acceptance runs that exercise the
real Lima/OpenCode runner. They are intentionally not part of normal CI because
they launch VMs, use a model provider, and can take several minutes to hours
depending on target size and runner settings.

## Vulnerable Workspace Fixture

Run the small reusable fixture with:

```sh
OPENROUTER_API_KEY=... scripts/acceptance-vulnerable-workspace.sh
```

The script copies `examples/vulnerable-workspace` to a temporary workspace,
runs `mnm analyze` through the real Lima/OpenCode path, and inspects the final
`report.json`.

Acceptance passes only if the final structured report contains at least one
`proven` finding with evidence paths and language consistent with a file-access
vulnerability, such as path traversal or arbitrary file read.

The fixture is intentionally small, but it is not a fake runner. The audit still
needs to inspect code, run local tests or proof commands, record ledger events,
validate a finding, and generate a final report through the normal pipeline.

Set `MNM_ACCEPTANCE_WORKSPACE=/path/to/workspace` to choose the scratch
workspace. By default the script leaves the workspace in place for inspection.
Set `MNM_ACCEPTANCE_CLEANUP=1` to remove it when the script exits.

## NodeGoat Benchmark

Run the OWASP NodeGoat benchmark with:

```sh
OPENROUTER_API_KEY=... scripts/acceptance-nodegoat.sh
```

The script fetches a pinned NodeGoat revision into a scratch workspace,
generates a benchmark-specific `mnm.toml`, runs `mnm analyze` through the real
Lima/OpenCode path, and checks the final structured report for at least one
proven NodeGoat security finding with registered evidence.

To re-check an existing benchmark run without launching new VMs, run:

```sh
python3 scripts/validate-nodegoat-report.py /path/to/.mnm/runs/RUN_ID
```

The default NodeGoat revision is pinned for reproducibility. Override it with
`MNM_NODEGOAT_REF=<ref-or-sha>` or choose a different source repository with
`MNM_NODEGOAT_REPO=<url>`.

Set `MNM_NODEGOAT_WORKSPACE=/path/to/workspace` to choose the scratch
workspace. By default the script leaves the workspace in place for inspection.
Set `MNM_NODEGOAT_CLEANUP=1` to remove it when the script exits.

## Why These Are Manual

- It launches a Lima VM.
- It uses a real model provider.
- It can take several minutes to hours.
- It depends on local machine resources and network access.
- It consumes model-provider quota and may incur provider costs.
