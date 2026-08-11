---
name: engram-evolve
description: Add, change, narrow, or deprecate an engram record type or schema. Use for schema evolution that must choose scope, avoid shadowing, bump versions, migrate affected records in the same candidate, and preserve local explanatory guidance.
---

# Evolve engram schemas

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
3. Complete the schema workflow below in one bounded working draft.
4. Regenerate affected catalogs, include structural companions such as a new
   directory's README or a moved record's inbound-link rewrites, and declare
   only that complete authorized operation as the initial candidate.
5. Let the managed transaction prepare and completely validate the final
   candidate. Accept it as one local commit only on a complete result with no E
   finding.
6. Treat a dirty, unstaged, rejected, or indeterminate result as an unaccepted
   working draft. Preserve it for correction and surface the failure. Never
   infer push authorization from a local commit.

## Evolve the schema

1. Enter the target area through its maps and pinned context. Keep schema edits
   and record migration in the same candidate.
2. Classify the change. Bump version even for widening. For narrowing, also
   migrate every affected record. Enumerate records by resolved type before
   editing.
3. Define a new type at the deepest directory covering every intended use,
   avoid shadowing, and update placement guidance so writers can find it.
4. Deprecate a type by removing placement guidance and migrating or closing
   remaining records. Remove the schema only when no record resolves to it.
5. Treat a copied curated schema as local once installed. Evolve its version,
   records, and explanatory prose like any other local schema.
