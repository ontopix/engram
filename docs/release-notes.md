# engram 1.0.0-rc.1 release notes

These notes describe the source targeted for `1.0.0-rc.1`. A build is an
official release only when it comes from the matching GitHub Release and its
published checksums and provenance have been verified.

The first reference implementation covers the complete v1 draft command
surface and the M0–M6 delivery gates:

- deterministic portable snapshot, schema, Markdown, link, catalog, finding,
  and changeset validation;
- raw managed-Git discovery, projection, lineage audit, status, diff, and log;
- working-draft, staging, `MEMORY.md` attachment, verified agent-harness setup,
  schema, and hook-trust workflows;
- crash-recoverable managed commit, inverse revert, initialization, and
  verified clone;
- conditional push and exact linear pull replay with explicit conflict,
  continue, abort, and recovery state; and
- advisory diagnostics, canonical skills, cross-platform CI, reproducible
  archives, checksums, licenses, provenance, and operator guidance.

The standard and CLI contract remain drafts until the project publishes stable
`v1.0.0`. Finding identities and protocol v1 shapes are therefore
release-candidate interfaces, not a retroactive stability promise for older
draft builds.

See [the operator guide](operator-guide.md) for installation, trust,
synchronization, recovery, backup, and upgrade procedures.
