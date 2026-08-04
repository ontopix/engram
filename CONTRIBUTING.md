# Contributing

Until v1 is declared stable, the spec is a draft and may change without
migration guarantees. Even so, changes follow these rules:

## Normative changes

A normative change is any edit that alters what a conforming store,
tool, or agent must do — in the core spec or in a normative annex.

Every normative change MUST:

1. update the `Revision` date of the affected document;
2. add a `CHANGELOG.md` entry describing the change and its impact;
3. keep `profiles/base/schemas/` and `examples/` conforming in the same
   commit — the repository never points at itself in a broken state.

## Non-normative changes

Rationale, adapters annex, README, examples' prose: editorial review
only. Keep the boundary visible — non-normative documents must not
smuggle in requirements ("MUST", "SHALL") that the core does not state.

## Versioning discipline

- Core spec: major version bumps on breaking structural or semantic
  change after stabilization; minor for backward-compatible additions.
- Annexes version independently. An annex's version is never inferred
  from the core's.
- The base profile versions as a unit: any breaking change to any of
  its schemas bumps the profile version.
