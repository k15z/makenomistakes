#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
nodegoat_repo="${MNM_NODEGOAT_REPO:-https://github.com/OWASP/NodeGoat.git}"
nodegoat_ref="${MNM_NODEGOAT_REF:-c5cb68a7084e4ae7dcc60e6a98768720a81841e8}"

if [[ -z "${OPENROUTER_API_KEY:-}" ]]; then
  echo "OPENROUTER_API_KEY must be set for model-backed benchmark runs" >&2
  exit 2
fi

for command in go git limactl python3 tar; do
  if ! command -v "$command" >/dev/null 2>&1; then
    echo "required command not found: $command" >&2
    exit 2
  fi
done

workspace="${MNM_NODEGOAT_WORKSPACE:-}"
if [[ -z "$workspace" ]]; then
  workspace="$(mktemp -d "${TMPDIR:-/tmp}/mnm-benchmark-nodegoat.XXXXXX")"
else
  mkdir -p "$workspace"
  if [[ -n "$(find "$workspace" -mindepth 1 -maxdepth 1 -print -quit)" ]]; then
    echo "MNM_NODEGOAT_WORKSPACE must be empty: $workspace" >&2
    exit 2
  fi
fi

if [[ "${MNM_NODEGOAT_CLEANUP:-0}" = "1" ]]; then
  trap 'rm -rf "$workspace"' EXIT
fi

mkdir -p "$workspace/repos"
git init "$workspace/repos/NodeGoat" >/dev/null
git -C "$workspace/repos/NodeGoat" remote add origin "$nodegoat_repo"
git -C "$workspace/repos/NodeGoat" fetch --depth 1 origin "$nodegoat_ref"
git -C "$workspace/repos/NodeGoat" checkout --detach FETCH_HEAD >/dev/null

cat >"$workspace/.mnmignore" <<'EOF'
.mnm/
.git/
node_modules/
vendor/
dist/
build/
coverage/
.cache/
.next/
target/
tmp/
temp/
*.log
.DS_Store
EOF

cat >"$workspace/mnm.toml" <<'EOF'
version = 1

[instructions]
scope = """
Audit the NodeGoat repository under repos/NodeGoat as if it were a production
Express/MongoDB application. Focus on vulnerabilities that a reviewer could
act on: authentication and authorization bypasses, injection, unsafe redirects
or SSRF, XSS/template injection, sensitive data exposure, insecure session or
CSRF handling, unsafe dependency/runtime configuration, file access, and
container/deployment risks.

Do not rely on the project being intentionally vulnerable as evidence. Inspect
the code, build or run targeted checks when feasible, and validate findings with
concrete proof artifacts. Keep findings specific to reachable code paths and
include affected paths that exist in this workspace.

Out of scope: purely stylistic code quality issues, speculative supply-chain
claims without a vulnerable reachable usage, and tutorial prose unless it maps
to executable application behavior.
"""

[workspace]
root = "repos"
exclude = []

[models]
api_key_env = "OPENROUTER_API_KEY"
default = "openrouter/deepseek/deepseek-v4-pro"
recon = "openrouter/deepseek/deepseek-v4-pro"
investigate = "openrouter/deepseek/deepseek-v4-pro"
review = "openrouter/deepseek/deepseek-v4-pro"
deduplicate = "openrouter/deepseek/deepseek-v4-pro"
validate = "openrouter/deepseek/deepseek-v4-pro"
finalize = "openrouter/deepseek/deepseek-v4-pro"

[runner]
cpus = 4
memory_gb = 8
disk_gb = 80
timeout_minutes = 240
opencode_task_timeout_minutes = 25
max_leads = 8
max_investigations = 8
parallel_tasks = 2
EOF

echo "benchmark workspace: $workspace"
echo "nodegoat ref: $nodegoat_ref"
echo "running mnm analyze through the real Lima/OpenCode path"

(
  cd "$repo_root"
  MNM_SOURCE_DIR="$repo_root" go run ./cmd/mnm analyze "$workspace"
) 2>&1 | tee "$workspace/mnm-analyze.log"

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

python3 "$repo_root/scripts/validate-nodegoat-report.py" "$run_dir"

echo "run dir: $run_dir"
