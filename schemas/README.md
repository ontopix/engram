# Curated schema inventory

Non-normative, ready-to-copy record types. The core standard does not
require this inventory or treat it as one installed profile.

| Logical type | Exact reference | Purpose |
|---|---|---|
| `note` | `note@v1` | Free-form baseline; mirrors normative core Appendix A.3 |
| `fact` | `fact@v2` | Atomic durable claim with validity and provenance |
| `person` | `person@v1` | Living record of a person |
| `project` | `project@v1` | Living record of a line of work |
| `journal-entry` | `journal-entry@v1` | Dated, append-only activity anchor |

Copy only the definitions a store needs, naming each installed file
`.engram/schemas/<type>@v<N>.md` to match its `type` and `version` metadata.
The source filenames in this inventory are package names: for example,
`person.md` installs as `person@v1.md`, and `fact.md` as `fact@v2.md`.
For an existing managed store, `engram --store PATH schema copy person@v1`
creates the correctly named local working-draft file; replace `PATH` with
the store path. Stage and accept the intended change through the managed writer.

A root definition is visible store-wide; a nested definition is visible in
that directory and its descendants. Once accepted, its semantic components
are fixed and its file is retained. A later contract uses a new revision;
records keep their exact references under normal resolution and lifecycle rules.

`note.md` remains byte-identical to
[core Appendix A.3](../docs/spec/README.md#a3-engramschemasnotev1md). The
other files are curated examples, not normative artifacts.

Stores often separate episodic memory (journal, facts, people,
projects) from semantic operating knowledge, sometimes into sibling
stores with different write permissions. That is an optional deployment
pattern, not a conformance rule.
