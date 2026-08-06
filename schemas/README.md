# Curated schema inventory

Non-normative, ready-to-copy schemas for common agent-memory records.
The core standard is complete without this inventory, and a store does
not declare, install, or hash it as a unit.

| Type | One line |
|---|---|
| `note` | Free-form baseline; mirrors the core's normative Appendix A.3 |
| `fact` | One atomic durable statement, with temporal validity and provenance |
| `person` | Living record of a person |
| `project` | Living record of a line of work |
| `journal-entry` | One dated, append-only unit of raw activity |

Copy only the files a store needs into an appropriate
`.engram/schemas/` directory. A copy at the store root is visible
throughout the store; a copy in a nested directory is visible only in
that subtree. Once copied, it is an ordinary local schema: lexical
resolution, forbidden shadowing, schema evolution, and all other core
rules apply. There is no compatibility claim beyond the bytes actually
present in the store.

`note.md` is maintained byte-identical to the canonical definition in
[core Appendix A.3](../docs/spec/README.md#a3-engramschemasnotemd). The
other four files are curated examples, not normative artifacts. All five
are kept mechanically conforming with the schema-file format at HEAD.

## Store organization pattern

Memory often splits along one practical axis:

- **episodic** — what happened and who or what exists: dated,
  accumulating journal entries, facts, people, and projects;
- **semantic** — how things work: conventions and operating knowledge
  corrected in place so it stays current, with history in version
  control.

Deployments may keep those roles in sibling stores such as `memory/`
and `knowledge/`, with writability enforced by their environment. This
is an optional organization pattern, not a store mechanism or a
conformance requirement.

## Temporal validity

Version control answers when a file was edited, not until when a claim
was true. The curated `fact` schema keeps that second clock explicitly:

- `valid_from` records when the claim became true;
- `valid_until` records when it stopped holding, or `null` while it
  remains current;
- `supersedes` links to facts it replaces.

The intended discipline is to close and supersede contradicted facts,
not silently rewrite their former truth. Cross-record consistency is
advisory in v1 rather than a check rule.

## Provenance and traction

The `fact` fields `source` and `claim` preserve where a claim came from
and whether it is confirmed, inferred, or hypothetical. References are
recorded only when actually obtained; `no-reference` states absence
without inventing provenance.

Living records are earned rather than pre-created. The prose in
`person.md` and `project.md` asks writers to wait for meaningful
recurrence or real work instead of creating empty stubs. This is writer
guidance, not something a validator attempts to measure.

## Journal discipline

`journal-entry` is the raw append-only layer. Corrections arrive as new
entries, and durable records link back to the entry that introduced the
information. A journal directory can use `catalog: dirs` when listing
every dated record would add churn without helping navigation.

This inventory deliberately stops at five well-understood shapes. A
store adds scoped local types when its own recurring content earns them.
