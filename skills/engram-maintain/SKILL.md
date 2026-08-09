---
name: engram-maintain
description: Repair deterministic drift or perform explicit curation in an engram managed store. Use for scheduled upkeep or drift noticed during other work, including stale catalogs, broken links, missing maps, duplicates, orphans, promotion, and policy-safe cleanup.
---

# Maintain an engram store

Treat the engram core specification as the sole authority for vocabulary,
formats, transitions, validation, and the Agent Protocol. This skill adds no
obligations; if it differs from the core, follow the core. At first contact,
apply using-engram before this workflow.

## Respect the managed-write boundary

1. Confirm that the task authorizes a persistent write and local commit and that
   the target is a managed store rather than an exported snapshot.
2. Start from the accepted state. Inspect existing staged and unstaged edits,
   coordinate one automated editor for the worktree, and stop when unrelated
   edits cannot be separated safely. Perform network synchronization only when
   separately authorized and only while the worktree and index are clean.
3. Complete the maintenance workflow below in one bounded working draft.
4. Regenerate affected catalogs, include structural companions such as a new
   directory's README or a moved record's inbound-link rewrites, and declare
   only that complete authorized operation as the initial candidate.
5. Let the managed transaction prepare and completely validate the final
   candidate. Accept it as one local commit only on a complete result with no E
   finding.
6. Treat a dirty, unstaged, rejected, or indeterminate result as an unaccepted
   working draft. Preserve it for correction and surface the failure. Never
   infer push authorization from a local commit.

## Perform maintenance

1. Run deterministic check first. Repair structural and content findings such
   as stale catalogs, broken links, and missing READMEs before
   judgment-based curation.
2. Keep the judgment pass explicit. Merge duplicate candidates through the
   store's superseding model; connect or surface orphans; rewrite descriptions
   that no longer describe their records.
3. When local schemas define promotion layers, move durable claims along them
   while preserving links to source anchors and schema-defined provenance.
4. Respect immutable and append-only policies; maintenance has no special
   exemption. Report both the accepted changes and any decision left to a human.
