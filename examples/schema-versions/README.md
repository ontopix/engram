---
description: Three append-only journal revisions coexist without changing historical records.
---
# Schema versions

This conforming snapshot demonstrates one family with three contracts.
Revision 1 requires a date; revision 2 adds a source; revision 3 also requires
a category. Each record selects exactly its declared revision. Old records
remain valid without invented metadata or frontmatter changes.

## Map

<!-- engram:catalog -->
- [2026-11-01](2026-11-01.md) — Entry created under revision 1.
- [2026-11-02](2026-11-02.md) — Entry created under revision 2.
- [2026-11-03](2026-11-03.md) — Entry created under revision 3.
<!-- /engram:catalog -->

## Placement

Use the root for dated journal entries. For a new entry without an explicitly
requested revision, select journal-entry@v3. Read its exact schema before
writing. Existing entries allow only byte-preserving body appends. Preserve
all published schema files; a future semantic change gets a new revision.
