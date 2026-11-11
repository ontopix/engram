# Explicit schema versions in record types

**Status:** Implemented in 1.0.0-rc.3; the core spec and CLI contract are authoritative

**Date:** 2026-11-11

**Normative status:** Non-normative design proposal

**Issue:** [#23](https://github.com/ontopix/engram/issues/23)

**Original design baseline reviewed:** `6bafc72b7070e2b73d9522ff4619f62b146fe118`

## 1. Decision and scope

Every record declares an exact schema reference in its existing `type` field:

```yaml
type: journal-entry@v2
```

Every schema revision lives in its own correspondingly named file:

```text
.engram/schemas/
  note@v1.md
  journal-entry@v1.md
  journal-entry@v2.md
  journal-entry@v3.md
```

The logical type is `journal-entry`; the revision is `2`. They form one
explicit reference, while remaining separately available to consumers.

There is one record format and one schema format. Records without a revision
are invalid. There is no family descriptor, implicit version, current-version
pointer, `legacy-version`, `write-version`, or separate record version field.
A family is simply the set of revisions of one logical type in one scope.

This is a breaking redesign of the pre-stable format. Existing examples and
small stores can be converted deliberately; retaining an old representation
inside the new standard is not a design requirement. The change still
preserves historical records written in the new format when later schema
revisions are introduced.

## 2. Exact reference grammar

The record's `type` is a string with the form `<name>@v<N>`:

- `<name>` uses the existing type-slug grammar: 1–64 lowercase ASCII
  characters from `[a-z0-9-]`, with no leading, trailing, or doubled hyphen.
- `@v` is literal and lowercase.
- `<N>` is canonical positive decimal `[1-9][0-9]*`.
- Revision numbers are exact integers. Comparison and filename construction
  cannot round, overflow, or truncate them to native numeric precision.

| Value | Result |
|---|---|
| `journal-entry@v1` | Logical type `journal-entry`, revision 1 |
| `journal-entry@v12` | Logical type `journal-entry`, revision 12 |
| `journal-entry` | Invalid record type: revision missing |
| `journal-entry@v01` | Invalid: noncanonical revision |
| `journal-entry@v0` | Invalid: revision is not positive |
| `journal-entry@latest` | Invalid: no moving aliases |
| `journal-entry@v1.2` | Invalid: revisions are integers, not SemVer |

The complete reference selects exactly one contract; it is not the logical
name itself. Classification and type-level links use the parsed logical name.
Exact-version operations use the complete reference.

Numbers have meaning within their scoped logical type. They do not select a
core-specification version, annex version, JSON Schema dialect, record edit
counter, or Git revision.

## 3. Schema definition and ordinary validation

A schema file is a direct regular file named `<name>@v<N>.md` inside a
`.engram/schemas/` directory. Its frontmatter retains the existing fields:

```yaml
---
type: journal-entry
version: 2
description: "Dated journal entry with explicit structured source capture."
schema:
  type: object
  required: [type, description, date, sources]
  additionalProperties: false
  properties:
    type: {const: journal-entry@v2}
    description: {type: string, minLength: 1, maxLength: 200}
    date: {type: string, format: date}
    sources:
      type: array
      items:
        type: object
        required: [kind, reference]
        additionalProperties: false
        properties:
          kind: {type: string, minLength: 1}
          reference: {type: string, minLength: 1}
policy:
  append-only: true
---
```

The schema's own `type` is the logical name and `version` is its positive
integer revision. Both match the filename. The frontmatter remains closed:
no descriptor, compatibility, or write-pointer fields are introduced.

Every definition has a direct top-level `schema.properties.type` equal to
`{const: <name>@v<N>}`. This provides a syntactic consistency check among
filename, definition metadata, and record contract. The core independently
requires the universal record labels.

The JSON Schema validates the **entire original parsed record frontmatter**.
There is no removal of fields, substitution of a bare type name, or normalized
validation view. A record containing `type: journal-entry@v2` is checked against
`const: journal-entry@v2`. Additional-property and property-count assertions
operate on exactly the stored data.

Description, optional body requirements, optional policy, YAML/JSON Schema
profiles, Markdown documentation, and templates retain their existing roles.
Revision-specific prose explains that revision and includes an exact-version
template. All schema files, including unused revisions, must conform.

## 4. Scope, discovery, and resolution

All revisions of a logical type belong to one schema scope along any record's
ancestor chain. The no-shadowing rule applies to the logical type, not
independently to each versioned filename. These cannot coexist in one chain:

```text
.engram/schemas/journal-entry@v1.md
projects/.engram/schemas/journal-entry@v2.md
```

A descendant cannot contribute another revision to an ancestor's family.
Independent sibling scopes can define their own types, as today. The resolved
identity is `(scope, logical type, revision)`; equal names and numbers in
unrelated scopes do not establish identical contracts.

Resolution for a record declaring `T@vN` is:

1. Parse and validate the exact reference.
2. Ascend from its directory to the root. The first schema directory containing
   any canonically named `T@vK.md` boundary owns the selected family. Ownership
   does not depend on those files' content validity or kind.
3. Select exactly `T@vN.md` within that directory. If absent, report an
   unavailable revision. Do not search an ancestor or another version.
4. Apply boundary checks and component-by-component evaluability to the
   selected file. Invalid content or kind never triggers a replacement.
5. Validate frontmatter, body, and typed links using that definition. Resolve
   its policy for applicable transition checks.

An uninspectable schema-directory boundary stops the search under the existing
availability rules. An invalid filename is a layout error and is pruned;
no alternative spelling is inferred. Canonical family boundaries occurring
at ancestor and descendant scopes produce shadowing even when revision numbers
differ. Symlinks are never followed.

Resolution is a function of snapshot files. It uses no history lookup,
registry, timestamps, shape inference, validation trial-and-error, database,
cache authority, or network request.

## 5. Choosing a revision for new records

The format and validator accept a new record under any available revision
that it explicitly names. Publishing v3 does not make v1 or v2 invalid for
creation. A missing field in a v3 record fails v3 validation; the consumer
does not relabel it as v1 to make it pass.

The Agent Protocol provides the default authoring choice: when the task does
not specify a revision, the agent selects the highest available numeric
revision in the destination's family and writes its exact reference. It reads
that revision's documentation and template before composing the record.
An authorized request for a particular older revision is honored explicitly.
Missing evidence needed by the chosen contract is surfaced under P6; the
agent never invents values or automatically downgrades to evade a requirement.

This chooses a contract for a new record. It never changes an existing record's
interpretation and never permits an unqualified `type` to be stored. Once
written, the choice is fixed in the record's bytes.

An existing record keeps its revision unless an explicit migration is intended.
The authoring default is not applied to ordinary edits, imports, or renames
of existing version-qualified records.

The design deliberately does not enforce one exclusive version for all new
records. Prohibiting new v1 records is an additional creation-policy
requirement, not an implication of publishing v2. No normative creation-policy
language or permanent write pointer is added here.

Publishing a revision makes it available for the default selection.
Experimental definitions should remain in an unaccepted working draft or
separate test store until ready. Staged releases and enforced deprecation
would require an explicit future design.

## 6. Publication and semantic stability

In a managed lineage, acceptance publishes a schema revision. After that:

- Its path, logical type, version, and parsed `schema`, `body`, and `policy`
  values are fixed.
- Its description and Markdown documentation may receive editorial changes.
- A semantic change creates a new file with a higher revision number.
- New numbers exceed the maximum already present in that scoped family;
  gaps are allowed and comparison is numeric.
- Published files cannot be deleted or moved, even if unreferenced. They
  remain available for resolution and their identifiers cannot be reused.

Semantic equality uses exact parsed-value comparison, not general JSON Schema
equivalence. A logically equivalent rewrite can still require a new revision.
For the explicitly empty initialization base, all supplied conforming
definitions are published together; no earlier publication claim is inferred.

Retention avoids tombstones, reused identities, and history-dependent
interpretation. Its cost is accumulating small files and keeping a published
family's scope stable. Relocation and garbage collection are outside this
first design.

An accidental publication is corrected through a new revision or explicit
selection of an earlier revision. A generic inverse changeset deleting a
published definition is rejected. There is no mutable alias to roll back.

## 7. Record migration and lifecycle policies

Migration changes the record's exact `type` and the data required by the
destination contract in one final candidate. Publishing a definition does
not migrate records implicitly.

```yaml
# Before
type: journal-entry@v1

# After an allowed, explicit migration
type: journal-entry@v2
sources: []
```

The base record's policy is resolved from its base `type` and definition.
Candidate metadata cannot replace that input.

| Base policy | Same-revision update | Change to another revision |
|---|---|---|
| Editable | Allowed when the candidate conforms | Allowed explicitly when the destination conforms |
| Append-only | Only an exact byte-prefix append | Frontmatter modification rejected by E502 |
| Immutable | Rejected by E501 | In-place migration rejected by E501 |

An editable record can adopt a stronger policy, which applies in subsequent
transitions. A protected record cannot weaken its policy by changing type or
revision. Editing its definition's policy first also fails because published
semantic components are fixed.

Late evidence may be appended to an append-only record's body. If it needs a
newer structured format, create a separate record and link to the original.
Immutable records use linked supplements or corrections. Store schemas define
those relationships; no universal supersession field or automatic link
redirection is introduced.

Renames retain today's deletion/addition model and inbound-link requirements.
An editable record can retain its exact version on a move when that revision
is visible at the destination. A move to another scope requires verifying the
destination contract even when the reference string is unchanged. Append-only
and immutable records still reject moves and deletions.

## 8. Typed links and consumers

Allow each entry in `x-engram-link.types` to be either a logical name or an
exact schema reference:

```yaml
x-engram-link:
  types: [journal-entry]
```

This matches logical type `journal-entry` at any valid revision.

```yaml
x-engram-link:
  types: [journal-entry@v2, journal-entry@v3]
```

This admits only the two stated references. Matching is the union of entries.
Duplicate literal entries remain invalid; overlapping logical and exact entries
are allowed but unnecessary. There are no ranges, wildcards, moving aliases,
or additional matching language.

As today, constraints compare names on the actual target; they do not resolve
the source's schema scope on behalf of the target. A versioned constraint does
not imply global schema identity across unrelated scopes. The target resolves
and validates its own exact contract.

Body wikilinks, paths, descriptions, and catalogs retain their formats.
Logical grouping uses the parsed name; exact-version filters use the complete
reference. A link admitting all revisions does not guarantee that every target
contains fields introduced in v3.

Consumers preserve the original `type` string and expose the parsed name,
numeric revision, and resolved schema path when structured resolution metadata
is needed. Those values are derived from files, never independent authorities.

## 9. The note baseline

`note` uses the same explicit format as every other type:

```yaml
---
type: note@v1
description: "A short observation worth preserving."
---
```

The root contains `.engram/schemas/note@v1.md`. The canonical baseline retains
its low-ceremony contract: only `type` and `description` are required; optional
fields remain possible; no required body sections or restrictive lifecycle
policy are added.

Further `note@vN` revisions are allowed, but every note revision satisfies the
baseline normal form, with `properties.type` set to its own exact reference.
A higher revision therefore cannot make basic note capture more demanding.
The root v1 remains available and published versions are retained normally.

Update Appendix A.3 to the installed filename `note@v1.md` and exact constant.
The curated source artifact remains `schemas/note.md`, as required by the
repository, and remains byte-identical to that appendix. Curated repository
artifact names are not installed-store filename rules.

## 10. Worked journal evolution

Initially publish `journal-entry@v1.md` with its exact type constant, required
fields `type`, `description`, and `date`, and the chosen append-only policy.
A record under it declares:

```markdown
---
type: journal-entry@v1
description: "Daily journal, 2026-11-10."
date: 2026-11-10
---
# 2026-11-10

- Example activity recorded without a source reference.
```

Later publish v2 as defined in section 3. Its `sources` field is required; in
this example an empty list explicitly records that no source reference was
captured. A type requiring an observed source can use `minItems: 1` instead.
The agent's next ordinary creation uses:

```markdown
---
type: journal-entry@v2
description: "Daily journal, 2026-11-12."
date: 2026-11-12
sources: []
---
# 2026-11-12

- Example activity recorded without a source reference.
```

Suppose v3 later requires `summary` too. Publish `journal-entry@v3.md`, with
matching metadata and constant, and create subsequent records using
`type: journal-entry@v3` and the required data. Neither publication changes
old record bytes, paths, or applicable rules.

| Record or action | Result |
|---|---|
| Existing v1 entry | Validates against v1 |
| Existing v2 entry | Validates against v2 |
| New v3 entry with required fields | Validates against v3 |
| v2 entry without `sources` | Rejected under v2; no v1 fallback |
| Record declaring unavailable v99 | Resolution error |
| Record declaring bare `journal-entry` | Invalid reference |
| Explicitly requested new v1 entry after v3 publication | Valid when it conforms to v1 |
| Append evidence to old append-only v1 entry | Allowed if old bytes remain an exact prefix |
| Rewrite append-only v1 entry to v2 | Rejected by E502 |
| Link accepting `journal-entry` | Admits v1, v2, and v3 targets |
| Link accepting only `journal-entry@v2` | Rejects v1 and v3 targets |

The implementation example includes complete root metadata, note baseline,
directory maps, schema prose, and base/candidate fixtures. These excerpts are
not claimed to be a complete conforming store under today's specification.

## 11. Git history, initialization, and format adoption

All snapshots in a new-format managed lineage carry explicit references.
Auditors resolve each snapshot with its own files and evaluate each transition
with its own base policies. Tip schemas never reinterpret ancestors.

A portable mixed-version snapshot initializes a store using the existing
empty-base rule. No creation-version exemption is needed: all records already
declare exact versions, and any conforming available revision is valid for
creation. Unavailable history is never an empty base.

Working-tree traversal and raw Git projection admit the same revised flat
grammar. Definitions are normed configuration, not derived or pruned state.
Capture, hooks, final-candidate validation, CAS, and recovery retain their
boundaries. Audit cache identities change with the normative rule set.

Concurrent publication of different contents at one filename creates an
ordinary synchronization conflict. Replay never invents a merge, renumbers
records, or modifies published contracts to make integration succeed. A new
record explicitly using v1 can replay after another writer publishes v2,
because publication does not prohibit explicit older-version creation.
Identical additions retain the annex's already-satisfied behavior.

Adoption is a deliberate format break before stabilization. Update the draft
core's revision and changelog, convert repository schemas and examples in the
same change, and release a clearly identified breaking prerelease. There is
no runtime compatibility mode for bare record types or old schema paths.
Old validators reject the new reference/filename grammar.

For any existing small stores requiring conversion, prepare a converted
snapshot from their currently accepted definitions, qualify the records, and
initialize a new managed lineage with explicit owner authorization. Retain
the original store/history separately when historical evidence matters. This
is an import, not an in-place transaction waiving append-only or immutable
policies, and does not claim the old lineage conforms to the new rules.
The current task designs this boundary; it performs no store conversion.

This delivery assumes a pre-stable specification. If v1 is stable at adoption,
the representation change requires a new core major and the release scope
must be reconsidered. Annex version numbers remain independent.

## 12. Diagnostics

Keep deterministic `(code, path)` findings and existing evaluability. The
normative patch updates these draft meanings and adds the listed codes,
checking their availability when it lands:

| Code | Proposed behavior |
|---|---|
| E202 | Missing, non-string, or empty record `type` |
| E203 | Malformed exact reference or no visible family; record path |
| E310 | Selected family lacks the requested revision; record path |
| E303 | Revised filename/definition grammar, metadata mismatch, or wrong direct type constant; normal schema attribution |
| E304 | Logical family shadowing, including different revisions across ancestor scopes |
| E307 | Missing root `note@v1` or any note revision violating the baseline |
| E402 | Target matches none of the logical/exact entries in a link constraint |
| E504 | Published schema semantic components changed in place; base schema path |
| E505 | Published schema file removed or moved; base schema path |
| E506 | Added revision does not exceed the base family's maximum; new schema path |

E205 is unchanged: every top-level record `engram-*` key remains reserved.
No selector-specific exception is needed. E301, E302, and E501–E503 keep their
roles.

Malformed filenames produce layout errors at their schema directory and are
pruned. A validly named but invalid selected definition emits causal findings,
not E310. An invalid unused definition invalidates the snapshot, but otherwise
evaluable selected contracts are still checked. Invalid reference syntax
suppresses checks needing a parsed reference; invalid definition components
suppress only dependent checks. Link type matching can remain evaluable from
a valid target reference even when another target component is invalid.

Transition rules read required base inputs and completely validate the
candidate. Unavailable required inputs yield `indeterminate`, never success.
Define the candidate-path attribution exception for E506 explicitly. Static
checks do not claim to prove publication history.

## 13. Agent Protocol and reference CLI

The core Agent Protocol owns all agent obligations: exact references, default
choice for new records, reading selected schemas, preserving revisions during
ordinary edits, explicit migration, and honest provenance. Skills and adapters
teach those rules.

Retrieval uses the record's declared revision; evolution adds a definition
instead of rewriting a published contract. Schema prose and newer versions
never expand authority to write, execute hooks, or access the network. Working
draft, initial candidate, final candidate, transaction, and accepted state
keep their established meanings.

The CLI contract changes separately from the normative standard:

```text
engram schema list [--at PATH]
engram schema show TYPE_REF [--at PATH]
engram schema copy TYPE_REF [--to SCOPE] [--dry-run]
engram new TYPE_REF PATH ...
```

- `TYPE_REF` is exact, such as `journal-entry@v2`. `show`, `copy`, and `new`
  reject bare names; they never choose a moving default behind the caller.
- `list` returns every visible revision, ordered by logical name then numeric
  version, with exact reference, scope, path, and description. Discovery remains
  all-or-nothing on invalid selected schema state.
- `inventory` exposes bundled exact references. `copy` installs the selected
  artifact under its versioned filename, rejects overwrite/shadowing, and
  establishes no automatic update relationship.
- `new` retains deterministic generation, required user-supplied domain values,
  and all-or-nothing draft/catalog behavior. It writes the supplied reference;
  `--fields` cannot override `type`.
- A caller resolves a record by reading its exact reference and using `show`
  at its directory. No selector flag, family-query command, or new record-
  resolution command is needed for the initial feature.
- Draft planners capture the selected definition and scope. A changed selected
  definition invalidates a stale plan; adding another revision does not
  invalidate an explicit choice of an existing one.
- Read/search results preserve the stored `type`. Structured type filters and
  grouping use the shared parser; string-prefix guesses are not authority.

Pin changed help, grammar, JSON shapes, ordering, errors, and goldens in the
CLI contract. Commands do not become requirements in the core or Git annex.

## 14. Delivery and verification

Implement one coordinated pre-stable format change, internally sequenced as:

1. Define reference grammar, file layout, scope, full-data validation, note
   baseline, link matching, and publication transitions in the core. Update
   revision dates, diagnostics, and changelog, and clarify history validation
   in the independently versioned Git annex.
2. Implement one shared parser and resolver across traversal, checker, queries,
   links, draft helpers, and audits. Validate original frontmatter; no selector
   projection layer is needed.
3. Update commands, structured results, inventory installation, snapshots,
   fixtures, schemas, examples, and canonical skeletons together.
   `schemas/note.md` and Appendix A.3 stay byte-identical at every commit.
4. Update P4 and applicable retrieval guidance, then skills, manifests/digests,
   embedded copies, adapters, and release/operator docs. Publish the feature
   only when its acceptance evidence is ready.

| Area | Required evidence |
|---|---|
| Grammar | Canonical references; missing suffixes, aliases, leading zeros, malformed numbers, and native-precision overflow cases |
| Definition consistency | Filename, metadata, and direct `properties.type.const` agree |
| Raw validation | Closed schemas and property-count assertions see original frontmatter |
| Coexistence | v1, v2, v3 validate together; adding v3 preserves old record bytes and paths |
| Strict resolution | Missing revisions fail; invalid nearer families never fall back |
| Scope | Same-scope revisions allowed; cross-version ancestor shadowing rejected; siblings permitted |
| Publication | Higher revisions accepted; semantic edits, deletion, relocation, and number reuse rejected |
| Editorial maintenance | Description/Markdown changes allowed without contract replacement |
| New records | Explicit older/current revisions accepted; bare names rejected; missing v2 fields never trigger v1 fallback |
| Agent default | Highest numeric revision for unspecified new work; explicit older requests honored; no automatic downgrade |
| Migration | Editable migrations validate destination; append-only/immutable type edits rejected |
| Late evidence | Exact prefix append accepted; immutable corrections use another record |
| Relationships | Logical constraints span versions; exact constraints match listed references; target scope authoritative |
| Note | Root v1 exists; all revisions preserve baseline; curated skeleton matches Appendix A.3 |
| Imports | Mixed-version snapshots initialize without special exceptions; unavailable history is not empty |
| History | Per-snapshot schemas and base policies; cache rule-set identity updated |
| Synchronization | Conflicting publications rejected; identical additions and explicit older record additions replay normally |
| CLI | Exact-reference commands, numeric ordering, inventory mapping, full-data validation, and stale-plan detection |

Run standard repository checks and the relevant CI matrix when implementing.
This proposal does not claim the current checker supports the format or that
these new acceptance cases have passed.

## 15. Tradeoffs and authority

One visible mechanism governs the design: a record names a contract, and the
contract lives in a matching file. Existing `version` metadata stays in schema
definitions without a second record field or descriptor. Every revision uses
the same model, including `note`.

The costs are a format break, parsing `type` into two components, retaining
published definitions, and explicit version choices. Authoring defaults favor
the latest published revision while permitting explicitly requested older ones.
Exclusive creation versions, staged releases, retirement enforcement, version
ranges, and schema garbage collection are additional requirements rather than
hidden behavior.

Implementation authority remains the accepted [core specification](../spec/README.md),
[Git annex](../spec/annex-git.md), [CLI contract](../cli/README.md),
[AGENTS.md](../../AGENTS.md), and [CONTRIBUTING.md](../../CONTRIBUTING.md).
This proposal becomes binding through their reviewed updates. The source use
case is the [curated journal schema](../../schemas/journal-entry.md) and issue #23.
