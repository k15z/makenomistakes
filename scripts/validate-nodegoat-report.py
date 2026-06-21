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

FORBIDDEN_REPORT_CLAIMS = (
    "enabling session forgery",
    "session forgery on any nodegoat deployment",
    "forged authenticated session",
    "forge an authenticated session",
    "steal document.cookie",
    "document.cookie theft",
    "node 12 implicit",
    "res.write(object) exploitation",
)

BENCHMARK_EXPECTATIONS = (
    {
        "name": "benefits authorization and allocations IDOR",
        "required_terms": (
            "/benefits",
            "/allocations/:userid",
        ),
        "affected_paths": {
            "NodeGoat/app/routes/allocations.js",
            "NodeGoat/app/routes/benefits.js",
        },
    },
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


def structured_item_text(item):
    return "\n".join(strings({
        "title": item.get("title"),
        "category": item.get("category"),
        "summary": item.get("summary"),
        "verdicts": item.get("verdicts"),
    })).lower()


def matched_benchmark_expectations(item):
    item_text = structured_item_text(item)
    for expectation in BENCHMARK_EXPECTATIONS:
        if all(term in item_text for term in expectation["required_terms"]):
            yield expectation


def validate_nodegoat_report(run_dir):
    report_json = run_dir / "report.json"
    report_md = run_dir / "report.md"

    with report_json.open() as fh:
        report = json.load(fh)
    markdown_text = report_md.read_text()
    combined_text = json.dumps(report, sort_keys=True).lower() + "\n" + markdown_text.lower()
    for phrase in FORBIDDEN_REPORT_CLAIMS:
        if phrase in combined_text:
            raise SystemExit(f"report contains overclaim: {phrase!r}")

    proven = report.get("proven")
    if not isinstance(proven, list):
        raise SystemExit(f"{report_json} is missing a proven findings array")
    if not proven:
        raise SystemExit(f"{report_json} contains no proven findings")

    matched_nodegoat_security_finding = False
    matched_expectations = set()
    for index, item in enumerate(proven):
        verify_evidence_paths(run_dir, item, index)
        if item.get("status") != "validation_proven":
            raise SystemExit(
                f"proven[{index}].status = {item.get('status')!r}, want 'validation_proven'"
            )
        if is_nodegoat_security_finding(item):
            matched_nodegoat_security_finding = True
        for expectation in matched_benchmark_expectations(item):
            matched_expectations.add(expectation["name"])
            expected_paths = expectation["affected_paths"]
            affected_paths = set(item.get("affected_paths", []))
            missing = sorted(expected_paths - affected_paths)
            if missing:
                raise SystemExit(
                    f"{expectation['name']} finding is missing expected affected path(s): "
                    f"{', '.join(missing)}"
                )

    if not matched_nodegoat_security_finding:
        raise SystemExit(
            "no proven finding appeared to describe a concrete NodeGoat security issue; "
            f"inspect {report_json}"
        )
    missing_expectations = sorted(
        expectation["name"]
        for expectation in BENCHMARK_EXPECTATIONS
        if expectation["name"] not in matched_expectations
    )
    if missing_expectations:
        raise SystemExit(
            "no proven finding matched expected benchmark check(s): "
            + ", ".join(missing_expectations)
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
