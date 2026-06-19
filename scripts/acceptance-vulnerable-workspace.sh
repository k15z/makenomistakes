#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
fixture="$repo_root/examples/vulnerable-workspace"

if [[ -z "${OPENROUTER_API_KEY:-}" ]]; then
  echo "OPENROUTER_API_KEY must be set for model-backed acceptance runs" >&2
  exit 2
fi

for command in go limactl python3 tar; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "required command not found: $command" >&2
    exit 2
  fi
done

workspace="${MNM_ACCEPTANCE_WORKSPACE:-}"
if [[ -z "$workspace" ]]; then
  workspace="$(mktemp -d "${TMPDIR:-/tmp}/mnm-acceptance-vulnerable.XXXXXX")"
else
  mkdir -p "$workspace"
  if [[ -n "$(find "$workspace" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
    echo "MNM_ACCEPTANCE_WORKSPACE must be empty: $workspace" >&2
    exit 2
  fi
fi

if [[ "${MNM_ACCEPTANCE_CLEANUP:-0}" = "1" ]]; then
  trap 'rm -rf "$workspace"' EXIT
fi

tar -C "$fixture" -cf - . | tar -C "$workspace" -xf -

echo "acceptance workspace: $workspace"
echo "running mnm analyze through the real Lima/OpenCode path"

(
  cd "$repo_root"
  MNM_SOURCE_DIR="$repo_root" go run ./cmd/mnm analyze "$workspace"
) | tee "$workspace/mnm-analyze.log"

run_dir="$(python3 - "$workspace" <<'PY'
import pathlib
import sys

workspace = pathlib.Path(sys.argv[1])
runs_dir = workspace / ".mnm" / "runs"
runs = [path for path in runs_dir.iterdir() if path.is_dir()]
if not runs:
    raise SystemExit(f"no runs found under {runs_dir}")
runs.sort(key=lambda path: path.stat().st_mtime, reverse=True)
print(runs[0])
PY
)"

python3 - "$run_dir" <<'PY'
import json
import pathlib
import sys

run_dir = pathlib.Path(sys.argv[1])
report_json = run_dir / "report.json"
report_md = run_dir / "report.md"

with report_json.open() as fh:
    report = json.load(fh)

proven = report.get("proven")
if not isinstance(proven, list):
    raise SystemExit(f"{report_json} is missing a proven findings array")
if not proven:
    raise SystemExit(f"{report_json} contains no proven findings")

combined = "\n".join(
    " ".join(str(item.get(field, "")) for field in ("title", "summary", "status", "category"))
    for item in proven
).lower()
report_text = report_md.read_text(errors="replace").lower()
keywords = ("path traversal", "directory traversal", "arbitrary file read", "file disclosure")
if not any(keyword in combined or keyword in report_text for keyword in keywords):
    raise SystemExit(
        "proven findings did not appear to include the expected file-access vulnerability; "
        f"inspect {report_json} and {report_md}"
    )

for index, item in enumerate(proven):
    evidence_paths = item.get("evidence_paths")
    if not isinstance(evidence_paths, list) or not evidence_paths:
        raise SystemExit(f"proven[{index}] is missing evidence_paths")

print(f"acceptance passed: {len(proven)} proven finding(s)")
for item in proven:
    print(f"- {item.get('id')}: {item.get('title')}")
print(f"report: {report_md}")
PY

echo "run dir: $run_dir"
