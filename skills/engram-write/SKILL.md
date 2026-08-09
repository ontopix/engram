---
name: engram-write
description: Add or edit content in an authorized engram managed store. Use when creating or updating records, descriptions, links, maps, or other store content while following placement, schema, contradiction, structural, and managed-write boundaries.
---

# Write engram content

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
3. Complete the content workflow below in one bounded working draft.
4. Regenerate affected catalogs, include structural companions such as a new
   directory's README or a moved record's inbound-link rewrites, and declare
   only that complete authorized operation as the initial candidate.
5. Let the managed transaction prepare and completely validate the final
   candidate. Accept it as one local commit only on a complete result with no E
   finding.
6. Treat a dirty, unstaged, rejected, or indeterminate result as an unaccepted
   working draft. Preserve it for correction and surface the failure. Never
   infer push authorization from a local commit.

## Build the content operation

1. Enter the prospective directory through its README, unread ancestor READMEs,
   and their directly pinned records under core P2. Follow placement guidance;
   surface a genuine gap instead of guessing.
2. Resolve the type from that location and read the selected schema file,
   including its prose. Use note when no more specific type fits. Treat recurring
   same-shaped notes as evidence that a new type may be useful.
3. Apply the schema's creation threshold. When its prose expects recurrence or
   substantial work, consider keeping a first mention in an existing record;
   it may not justify an empty new record.
4. Start from the schema's Template section when present; otherwise use the
   universal labels, declared fields, and schema prose. Write description for
   someone deciding whether to open the file.
5. Put schema-modelled relations in typed frontmatter links and looser
   references in body wikilinks. Link to existing content instead of restating
   it.
6. Handle contradictions under core P5. Use declared superseding semantics or
   surface the conflict; never silently choose a version.
