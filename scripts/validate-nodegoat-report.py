#!/usr/bin/env python3
import json
import pathlib
import sys


SECURITY_KEYWORDS = (
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


def verify_evidence_paths(run_dir, item, index):
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


def is_nodegoat_security_finding(item):
    affected_paths = item.get("affected_paths")
    if not isinstance(affected_paths, list) or not affected_paths:
        raise SystemExit("proven finding is missing affected_paths")
    for path in affected_paths:
        if not isinstance(path, str):
            raise SystemExit("proven finding has a non-string affected path")
    nodegoat_paths = [
        path for path in affected_paths
        if path.startswith("NodeGoat/")
    ]
    if not nodegoat_paths:
        return False
    structured_text = "\n".join(strings({
        "title": item.get("title"),
        "category": item.get("category"),
        "summary": item.get("summary"),
        "verdicts": item.get("verdicts"),
    })).lower()
    return any(keyword in structured_text for keyword in SECURITY_KEYWORDS)


def validate_nodegoat_report(run_dir):
    report_json = run_dir / "report.json"
    report_md = run_dir / "report.md"

    with report_json.open() as fh:
        report = json.load(fh)

    proven = report.get("proven")
    if not isinstance(proven, list):
        raise SystemExit(f"{report_json} is missing a proven findings array")
    if not proven:
        raise SystemExit(f"{report_json} contains no proven findings")

    matched_nodegoat_security_finding = False
    for index, item in enumerate(proven):
        verify_evidence_paths(run_dir, item, index)
        if item.get("status") != "validation_proven":
            raise SystemExit(
                f"proven[{index}].status = {item.get('status')!r}, want 'validation_proven'"
            )
        if is_nodegoat_security_finding(item):
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


def main(argv):
    if len(argv) != 2:
        raise SystemExit("usage: validate-nodegoat-report.py RUN_DIR")
    validate_nodegoat_report(pathlib.Path(argv[1]))


if __name__ == "__main__":
    main(sys.argv)
