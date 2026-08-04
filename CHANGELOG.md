# Changelog

## Unreleased

- Determinism pass over core v1 after first review: catalog sort order
  fixed to UTF-8 byte order; link extraction ignores fenced/inline
  code; frontmatter wikilinks are exact-value only; CommonMark named
  as the markdown interpretation, with ATX exact-match headings for
  `required-sections`; character counts defined as Unicode code
  points; hook `on.paths` globs fixed to gitignore-style wildmatch;
  BOM forbidden (E108 widened); space, parentheses, and C0 controls
  forbidden in filenames (§2.6).
- Check catalog: new E208 (`pinned` not boolean) and E308 (invalid
  hook declaration; store conformance now includes §8.2); E206
  reworded to mirror E204.
- §5.1 README body obligations downgraded MUST → SHOULD (uncheckable
  MUSTs are vacuous under §1.4's conformance definition).
- `pinned`: generated catalogs mark pinned records (`(pinned)` between
  link and dash); the co-reading discipline (read pinned records of a
  directory and its ancestors alongside its maps) is normed in the
  skills annex (engram-find, engram-write).
- Skills annex: new `using-engram` orientation skill (panorama, store
  location, entry through the map, operation→skill routing, red
  flags); the "four skills, no more" cap dropped — the set is open,
  disciplines earn skills by recurrence (type-authoring noted as a
  future split of engram-evolve); adapters annex updated to match.
- Initial draft of the engram standard: core specification v1 (draft),
  base profile annex v1 (draft), skills annex (draft), adapters annex
  (non-normative, draft), rationale, canonical base-profile schemas,
  minimal example store.
