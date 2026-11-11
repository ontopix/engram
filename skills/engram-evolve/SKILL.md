---
name: engram-evolve
description: Publish a new engram schema revision, preserve existing contracts, and explicitly migrate eligible records under their base policies.
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
6. Distinguish acceptance from checkout completion. Rejection before the
   accepted ref advances leaves the operation as a working draft. A failure
   after it advances preserves the accepted commit and requires recovery, not
   another attempt to record the same operation. Inspect acceptance and recovery
   evidence; if the outcome is unknown, surface it without assuming either
   success or safe retry. Preserve unrelated draft bytes. A local commit never
   implies push.

## Evolve the schema

1. Enter through maps and pinned context. Resolve the logical family and its
   exact revision files in one scope; do not distribute revisions across
   ancestor and descendant scopes.
2. For a semantic change, add a new `name@vN.md` definition with a numeric
   revision above the published maximum. Keep metadata and the direct
   `schema.properties.type` constant consistent with that filename.
3. Preserve every published definition and its semantic components. Editorial
   descriptions and Markdown may change; retired or unused revisions remain.
4. Existing records retain their exact declared contracts. Migrate only when
   explicitly intended, in one candidate satisfying both the base lifecycle
   policy and destination contract. Never rewrite append-only or immutable
   frontmatter to change its revision.
5. Update guidance and templates for new records. Protocol P4 selects the
   highest available revision when the authorized task specifies none, and
   permits an explicitly requested older revision. Never downgrade to evade
   missing data or validation. Copied curated schemas are ordinary local
   definitions with the same publication guarantees.
