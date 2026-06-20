#!/usr/bin/env python3
import importlib.util
import json
import pathlib
import tempfile
import unittest


SCRIPT = pathlib.Path(__file__).with_name("validate-nodegoat-report.py")
SPEC = importlib.util.spec_from_file_location("validate_nodegoat_report", SCRIPT)
validator = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(validator)


class ValidateNodeGoatReportTest(unittest.TestCase):
    def write_report(self, run_dir, proven):
        (run_dir / "report.md").write_text("# Report\n", encoding="utf-8")
        (run_dir / "report.json").write_text(
            json.dumps({"proven": proven}),
            encoding="utf-8",
        )

    def valid_item(self, **overrides):
        item = {
            "id": "finding_nodegoat_nosql",
            "title": "NoSQL injection in NodeGoat login",
            "category": "injection",
            "severity": "high",
            "confidence": "high",
            "status": "validation_proven",
            "summary": "Reachable NoSQL injection in the NodeGoat login flow.",
            "affected_paths": ["NodeGoat/app/routes/session.js"],
            "evidence_paths": ["evidence/proof.log"],
            "verdicts": [
                "review accepted",
                "deduplicate canonical",
                "validation proven",
            ],
        }
        item.update(overrides)
        return item

    def test_accepts_structured_nodegoat_security_finding(self):
        with tempfile.TemporaryDirectory() as tmp:
            run_dir = pathlib.Path(tmp)
            (run_dir / "evidence").mkdir()
            (run_dir / "evidence/proof.log").write_text("proof\n", encoding="utf-8")
            self.write_report(run_dir, [self.valid_item()])

            validator.validate_nodegoat_report(run_dir)

    def test_rejects_generic_finding_even_if_evidence_mentions_nodegoat(self):
        with tempfile.TemporaryDirectory() as tmp:
            run_dir = pathlib.Path(tmp)
            (run_dir / "evidence").mkdir()
            (run_dir / "evidence/proof.log").write_text(
                "prompt mentioned nodegoat/ and injection\n",
                encoding="utf-8",
            )
            self.write_report(run_dir, [
                self.valid_item(
                    title="Generic defect",
                    category="correctness",
                    summary="A generic proven defect.",
                    affected_paths=["OtherRepo/app.js"],
                )
            ])

            with self.assertRaisesRegex(SystemExit, "concrete NodeGoat security issue"):
                validator.validate_nodegoat_report(run_dir)

    def test_rejects_generic_finding_on_security_named_nodegoat_path(self):
        with tempfile.TemporaryDirectory() as tmp:
            run_dir = pathlib.Path(tmp)
            (run_dir / "evidence").mkdir()
            (run_dir / "evidence/proof.log").write_text("proof\n", encoding="utf-8")
            self.write_report(run_dir, [
                self.valid_item(
                    title="Generic defect",
                    category="correctness",
                    summary="A generic proven defect.",
                    affected_paths=["NodeGoat/app/routes/session.js"],
                )
            ])

            with self.assertRaisesRegex(SystemExit, "concrete NodeGoat security issue"):
                validator.validate_nodegoat_report(run_dir)

    def test_rejects_malformed_proven_item_even_with_valid_finding(self):
        with tempfile.TemporaryDirectory() as tmp:
            run_dir = pathlib.Path(tmp)
            (run_dir / "evidence").mkdir()
            (run_dir / "evidence/proof.log").write_text("proof\n", encoding="utf-8")
            malformed = self.valid_item(
                id="finding_malformed",
                affected_paths=[],
            )
            self.write_report(run_dir, [self.valid_item(), malformed])

            with self.assertRaisesRegex(SystemExit, "missing affected_paths"):
                validator.validate_nodegoat_report(run_dir)

    def test_rejects_evidence_paths_that_escape_run_dir(self):
        with tempfile.TemporaryDirectory() as tmp:
            run_dir = pathlib.Path(tmp)
            self.write_report(run_dir, [
                self.valid_item(evidence_paths=["../proof.log"])
            ])

            with self.assertRaisesRegex(SystemExit, "evidence path escapes run dir"):
                validator.validate_nodegoat_report(run_dir)


if __name__ == "__main__":
    unittest.main()
