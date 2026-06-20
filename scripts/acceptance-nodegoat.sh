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

security_keywords = (
    "access control",
    "authorization",
    "authentication",
    "csrf",
    "cross-site",
    "file disclosure",
    "injection",
    "nosql",
    "open redirect",
    "prototype pollution",
    "redos",
    "sensitive",
    "session",
    "ssrf",
    "xss",
)


def strings(value):
    if isinstance(value, str):
        yield value
    elif isinstance(value, list):
        for item in value:
            yield from strings(item)
    elif isinstance(value, dict):
        for item in value.values():
            yield from strings(item)


def verify_evidence_paths(item, index):
    evidence_paths = item.get("evidence_paths")
    if not isinstance(evidence_paths, list) or not evidence_paths:
        raise SystemExit(f"proven[{index}] is missing evidence_paths")
    for evidence_path in evidence_paths:
        if not isinstance(evidence_path, str):
            raise SystemExit(f"proven[{index}] has a non-string evidence path")
        candidate = (run_dir / evidence_path).resolve()
        try:
            candidate.relative_to(run_dir.resolve())
        except ValueError:
            raise SystemExit(f"evidence path escapes run dir: {evidence_path}")
        if not candidate.is_file():
            raise SystemExit(f"evidence path is missing: {evidence_path}")


matched_nodegoat_security_finding = False
for index, item in enumerate(proven):
    verify_evidence_paths(item, index)
    if item.get("status") != "validation_proven":
        raise SystemExit(f"proven[{index}].status = {item.get('status')!r}, want 'validation_proven'")
    affected_paths = item.get("affected_paths")
    if not isinstance(affected_paths, list) or not affected_paths:
        raise SystemExit(f"proven[{index}] is missing affected_paths")
    nodegoat_paths = [
        path for path in affected_paths
        if isinstance(path, str) and path.startswith("NodeGoat/")
    ]
    if not nodegoat_paths:
        continue
    structured_text = "\n".join(strings({
        "title": item.get("title"),
        "category": item.get("category"),
        "summary": item.get("summary"),
        "affected_paths": affected_paths,
        "verdicts": item.get("verdicts"),
    })).lower()
    if any(keyword in structured_text for keyword in security_keywords):
        matched_nodegoat_security_finding = True

if not matched_nodegoat_security_finding:
    raise SystemExit(
        "no proven finding appeared to describe a concrete NodeGoat security issue; "
        f"inspect {report_json}"
    )

print(f"benchmark passed: {len(proven)} proven finding(s)")
for item in proven:
    print(f"- {item.get('id')}: {item.get('title')}")
print(f"report: {report_md}")
PY

echo "run dir: $run_dir"
