# Acceptance Runs

This repository includes a manual end-to-end acceptance fixture:

```sh
OPENROUTER_API_KEY=... scripts/acceptance-vulnerable-workspace.sh
```

The script copies `examples/vulnerable-workspace` to a temporary workspace,
runs `mnm analyze` through the real Lima/OpenCode path, and inspects the final
`report.json`.

Acceptance passes only if the final structured report contains at least one
`proven` finding with evidence paths and language consistent with a file-access
vulnerability, such as path traversal or arbitrary file read.

Why this is manual:

- It launches a Lima VM.
- It uses a real model provider.
- It can take several minutes.
- It depends on local machine resources and network access.

The fixture is intentionally small, but it is not a fake runner. The audit still
needs to inspect code, run local tests or proof commands, record ledger events,
validate a finding, and generate a final report through the normal pipeline.

Set `MNM_ACCEPTANCE_WORKSPACE=/path/to/workspace` to choose the scratch
workspace. By default the script leaves the workspace in place for inspection.
Set `MNM_ACCEPTANCE_CLEANUP=1` to remove it when the script exits.
