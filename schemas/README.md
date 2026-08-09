# Curated schema inventory

Non-normative, ready-to-copy record types. The core standard does not
require this inventory or treat it as one installed profile.

| Type | Purpose |
|---|---|
| `note` | Free-form baseline; mirrors normative core Appendix A.3 |
| `fact` | Atomic durable claim with validity and provenance |
| `person` | Living record of a person |
| `project` | Living record of a line of work |
| `journal-entry` | Dated, append-only activity anchor |

Copy only the files a store needs into `.engram/schemas/`. A root copy
is visible store-wide; a nested copy is visible in that directory and
its descendants. Once copied, it is an ordinary local schema governed
by normal resolution, shadowing, and evolution rules.

`note.md` remains byte-identical to
[core Appendix A.3](../docs/spec/README.md#a3-engramschemasnotemd). The
other files are curated examples, not normative artifacts.

Stores often separate episodic memory (journal, facts, people,
projects) from semantic operating knowledge, sometimes into sibling
stores with different write permissions. That is an optional deployment
pattern, not a conformance rule.
