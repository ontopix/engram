# Contributing

Until v1 is declared stable, the spec is a draft and may change without
migration guarantees. Even so, changes follow these rules:

## Normative changes

A normative change is any edit that alters what a conforming snapshot,
tool, or agent must do — in the core spec or in a normative annex.

Every normative change MUST:

1. update the `Revision` date of the affected document;
2. add a `CHANGELOG.md` entry describing the change and its impact;
3. keep `schemas/` and `examples/` conforming in the same commit, and
   keep `schemas/note.md` byte-identical to core Appendix A.3 — the
   repository never points at itself in a broken state.

## Non-normative changes

Rationale, skills and adapters guidance, CLI contract, curated schema
documentation, README, and examples' prose: editorial review only. Keep
the boundary visible — non-normative documents must not smuggle in RFC
2119 requirements that the normative core and annexes do not state.

## Versioning discipline

- Core spec: major version bumps on breaking structural or semantic
  change after stabilization; minor for backward-compatible additions.
- Annexes version independently. An annex's version is never inferred
  from the core's.
- Before the first release, check codes may change without compatibility
  guarantees. After the first release, a published code never changes
  meaning and is never reused.
