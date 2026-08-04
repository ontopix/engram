---
description: "Smallest conforming engram store: one type, one topic directory, two notes."
---
# minimal

The smallest store that satisfies the engram standard (v1): a root
manifest, the `note` baseline type, README maps with generated
catalogs, and records that link to each other. Copy it, rename it,
start writing.

This store follows the engram standard (v1): every directory carries a
README map, every record declares a `type` resolved against schemas in
`.engram/schemas/`, and the store validates deterministically.

## Map

<!-- engram:catalog -->
- [topics/](topics/README.md) — Standalone notes, one self-contained topic per record.
<!-- /engram:catalog -->

## Placement

Nothing is written at the root level. Topic notes go under `topics/`;
a new kind of content earns a new directory (with its README) — and,
when it recurs, its own type.

## Agent Protocol

- Enter through the maps: read a directory's README (and unread
  ancestors') before working under it. Never bulk-read the tree.
- Find with both catalog descent and content search, reformulating
  terms at least once; claim absence only after both.
- Before writing: read the type's schema file (`.engram/schemas/`),
  including its prose — placement and "when not to" live there. After
  writing: validate, and regenerate affected catalogs.
- Never silently overwrite a contradicted record; supersede or surface.
- Never invent a reference; a provenance field holds a tool-returned
  identifier or an explicit absence.
- New directory ⇒ its README, same changeset. Move ⇒ inbound links
  rewritten, same changeset.
- Maps carry stable descriptors, never mutable state.
