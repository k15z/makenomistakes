# Vulnerable Workspace

This workspace is a small multi-repository fixture for manual end-to-end
acceptance runs of `mnm`.

It contains two independent Node.js repositories:

- `repos/file-vault`: a document retrieval service.
- `repos/health-check`: a small status helper.

The fixture is intentionally tiny so the full VM-backed audit pipeline can run
quickly while still having enough behavior for local tests and proof scripts.
