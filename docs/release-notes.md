# engram 1.0.0-rc.2 release notes

These notes describe `1.0.0-rc.2`, the first published prerelease following the
`rc.1` development baseline. A build is an
official release only when it comes from the matching GitHub Release and its
published checksums and provenance have been verified.

This release improves unattended memory workflows without adding a daemon,
runtime dependency, configuration format, or CLI command:

- The generic integration reads a fixed accepted commit, keeping working drafts
  and caches out of retrieved memory even during concurrent acceptance.
- Write skills distinguish rejection from acceptance with recovery pending,
  avoiding blind retries of an operation that may already have been recorded.
- A shared owner-adopted policy enables bounded unattended capture and local
  commits without per-write prompts. Setup itself still grants no write authority.
- Exact record references can be read directly with map context. Discovery,
  contradictions, superseding, and absence retain broader retrieval checks.
- Curated `fact` version 2 interprets open-ended validity as no known end, rather
  than a fresh verification of present truth.

The core Agent Protocol revision is `2026-11-11`; snapshot layouts and CLI JSON
shapes are unchanged. Re-run `engram setup` to update unchanged installed
canonical skills. Adopt the unattended policy only through owner-approved host
instructions. Existing local `fact` schemas are not overwritten; adopting the
version 2 prose is an explicit local schema evolution with the normal version
discipline. Existing records need no new fields.

The first reference implementation covers the complete v1 draft command
surface and the M0–M6 delivery gates:

- deterministic portable snapshot, schema, Markdown, link, catalog, finding,
  and changeset validation;
- raw managed-Git discovery, projection, lineage audit, status, diff, and log;
- working-draft, staging, `MEMORY.md` attachment, declarative `engram.yaml`
  editing and acquisition, verified agent-harness setup, schema, and hook-trust
  workflows;
- crash-recoverable managed commit, inverse revert, initialization, and
  verified clone;
- conditional push and exact linear pull replay with explicit conflict,
  continue, abort, and recovery state; and
- advisory diagnostics, canonical skills, cross-platform CI, reproducible
  archives, checksums, licenses, provenance, and operator guidance.

The reference CLI validates static `.engram/routines/` declarations and their
managed Git projection. It does not bind, schedule, or execute routines;
those responsibilities remain external to the CLI.

The standard and CLI contract remain drafts until the project publishes stable
`v1.0.0`. Finding identities and protocol v1 shapes are therefore
release-candidate interfaces, not a retroactive stability promise for older
draft builds.

See [the operator guide](operator-guide.md) for installation, trust,
synchronization, recovery, backup, and upgrade procedures.
