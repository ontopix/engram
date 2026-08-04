# The engram standard — Specification v1 (draft)

**Version:** v1
**Status:** Draft
**Revision:** 2026-08-04

## Table of contents

1. [Introduction and conformance](#1-introduction-and-conformance)
2. [Store identity and layout](#2-store-identity-and-layout)
3. [The root manifest](#3-the-root-manifest)
4. [Records](#4-records)
5. [Directory READMEs](#5-directory-readmes)
6. [Types and schemas](#6-types-and-schemas)
7. [Links](#7-links)
8. [Changesets and hooks](#8-changesets-and-hooks)
9. [Validation](#9-validation)
10. [Derived state](#10-derived-state)
11. [Agent Protocol](#11-agent-protocol)
12. [Profiles](#12-profiles)
13. [Adoption](#13-adoption)
14. [Versioning](#14-versioning)

[Appendix A — Canonical skeletons](#appendix-a--canonical-skeletons-normative)
[Appendix B — Check catalog](#appendix-b--check-catalog-normative)

---

## 1. Introduction and conformance

### 1.1 Purpose

This specification defines the **engram store**: a directory tree of
markdown records that serves as persistent memory for AI agents and for
the humans who work with them.

A store is plain files under version-control-friendly layout. It
requires no database, no embedding pipeline, and no specific runtime.
Everything an agent needs in order to navigate, write, and validate the
store correctly travels inside the store itself.

### 1.2 Requirements language

The key words "MUST", "MUST NOT", "REQUIRED", "SHALL", "SHALL NOT",
"SHOULD", "SHOULD NOT", "RECOMMENDED", "NOT RECOMMENDED", "MAY", and
"OPTIONAL" in this document are to be interpreted as described in BCP 14
(RFC 2119, RFC 8174) when, and only when, they appear in all capitals,
as shown here.

### 1.3 Design principles

The requirements of this specification derive from four principles.
They are stated here once and referenced throughout.

- **D1 — Files are the source of truth.** The store's markdown files
  are the authoritative state. Anything computed from them — an index,
  a cache, a catalog — is derived state and is subordinate
  ([§10](#10-derived-state)).
- **D2 — The store is self-describing.** Every directory documents
  itself ([§5](#5-directory-readmes)), every record declares its type
  ([§4](#4-records)), every type is defined by a schema file that lives
  in the tree ([§6](#6-types-and-schemas)). An agent with filesystem
  access and no prior knowledge can operate the store correctly by
  reading it.
- **D3 — Integrity is checkable deterministically.** Whether a store
  conforms is decided by mechanical rules with stable identifiers
  ([§9](#9-validation), [Appendix B](#appendix-b--check-catalog-normative)),
  never by model judgment.
- **D4 — Reading is lazy.** Navigation descends through one-line
  descriptions; content is opened only when relevant. Nothing in this
  specification may require bulk-reading the tree.

### 1.4 Conformance targets

This specification defines three independent conformance targets:

- **Store conformance** — a directory tree satisfies the structural and
  content rules of [§2](#2-store-identity-and-layout) through
  [§7](#7-links), which is exactly: no findings with an `E` code from
  the static rules of [Appendix B](#appendix-b--check-catalog-normative).
- **Tool conformance** — software that reads, writes, or validates
  stores honors the obligations marked for consumers and executors.
- **Agent conformance** — an agent working in a store follows the Agent
  Protocol ([§11](#11-agent-protocol)).

A conformance claim MUST identify its target. Passing a partial set of
checks MUST NOT be described as full conformance.

### 1.5 Scope

This specification is declarative. It defines what exists in a store,
what it means, and what obligations consumers and agents have. It does
not define who executes hooks, how synchronization into any runtime
works, or how conformance is enforced. It references no specific
implementation, and conformance MUST NOT depend on any particular
implementation.

### 1.6 Terminology

- **store** — a directory tree whose root contains `.engram/root.yaml`.
- **record** — a markdown file inside a store that carries frontmatter
  and a resolved `type`. The unit of memory.
- **asset** — any non-markdown regular file inside a store.
- **map** — a `README.md` file; describes its directory, not knowledge
  content itself.
- **schema file** — a markdown file under a `.engram/schemas/`
  directory that defines a record type.
- **scope** — the subtree rooted at the directory that contains a given
  `.engram/` configuration directory.
- **changeset** — an ordered set of additions, modifications, and
  deletions applied to a store as one transaction
  ([§8.1](#81-changesets)).
- **consumer** — any software that reads or processes a store.
- **executor** — software that runs hooks at changeset boundaries.

---

## 2. Store identity and layout

### 2.1 Root marker

A directory is a **store root** if and only if it contains the file
`.engram/root.yaml` ([§3](#3-the-root-manifest)).

The store is the entire tree rooted there. A store MAY be a whole
repository, a subdirectory of one, or a directory outside any
repository: the standard is location-neutral and version-control
neutral. One repository MAY contain several stores.

### 2.2 Configuration directories

Any directory in a store — the root included — MAY contain an
`.engram/` directory holding local configuration:

- `.engram/schemas/` — type definitions valid in this scope
  ([§6](#6-types-and-schemas));
- `.engram/hooks/` — hook declarations for this scope
  ([§8](#8-changesets-and-hooks));
- `.engram/cache/` — RESERVED for derived state
  ([§10](#10-derived-state)); at the root only.

At the root, `.engram/` additionally contains `root.yaml`. Entries
inside `.engram/` other than the above are tool-specific; consumers
MUST ignore entries they do not recognize.

`.engram/` directories hold configuration, not content: nothing inside
them is a record, an asset, or a map, and the record rules of
[§4](#4-records) do not apply there.

### 2.3 No nested roots

A store MUST NOT contain another store: `.engram/root.yaml` MUST NOT
exist in any directory of a store other than its root. Trees that need
several stores place them as siblings or otherwise disjoint trees, and
each store is validated independently.

### 2.4 Reserved entries and path safety

Entries whose name begins with a dot (`.`), at any depth in a store,
are RESERVED for tooling state and are not content — with the single
exception of `.engram/`, which is defined by this specification. A
consumer MUST ignore reserved entries when reading content, and a
reserved entry MUST NOT carry normative content.

Symbolic links MUST NOT appear at any depth inside a store. Every
directory MUST be a real directory and every file a regular file.
Consumers MUST NOT follow a symbolic link found inside a store and MUST
report it as invalid structure. Special filesystem entries other than
regular files and directories MUST NOT appear inside a store.

### 2.5 Records, assets, maps

Every regular non-reserved file in a store is exactly one of:

- a **map** — a file named `README.md` ([§5](#5-directory-readmes));
- a **record** — any other `.md` file ([§4](#4-records));
- an **asset** — any non-markdown file.

Assets carry no frontmatter and their content is outside the scope of
validation. Assets do not participate in the type system. Profiles MAY
define sidecar conventions for describing assets; this core does not.

### 2.6 Filenames and encoding

Record and asset filenames:

- MUST NOT contain the characters `[`, `]`, `|`, `#`, newline, or any
  character forbidden by the host filesystem;
- MUST NOT begin with a dot (that would make them reserved);
- SHOULD be lowercase kebab-case slugs (`[a-z0-9]` and single hyphens)
  plus extension, because filenames are link targets and identity
  ([§7.4](#74-identity-and-renames)).

Two entries in the same directory MUST NOT differ only by character
case: stores are required to remain usable on case-insensitive
filesystems.

All markdown files MUST be encoded in UTF-8. LF line endings are
RECOMMENDED.

---

## 3. The root manifest

`.engram/root.yaml` is the root marker and the store's manifest.

The file MUST be a YAML 1.2 mapping. YAML content normed by this
specification — manifests, frontmatter, hook declarations alike — MUST
have string keys at every depth and MUST NOT use duplicate keys,
anchors, or aliases. Required string fields MUST be non-empty after
trimming whitespace.

Fields:

| Field | Requirement | Meaning |
|---|---|---|
| `engram` | REQUIRED integer | Major version of this specification that the store targets |
| `profiles` | OPTIONAL list of strings | Installed profiles, `<name>@<major>` each ([§12](#12-profiles)) |

Unknown root-level keys are tool-specific; consumers MUST ignore keys
they do not recognize.

A consumer built for specification major version *N* MUST NOT silently
process a store that declares `engram: M` with *M* > *N*; it MUST
surface the mismatch.

Example (non-normative):

```yaml
engram: 1
profiles:
  - base@1
```

---

## 4. Records

A record is a markdown file that begins with YAML frontmatter followed
by a markdown body. The body MAY be empty; the frontmatter MUST NOT be
absent.

### 4.1 Frontmatter rules

Frontmatter is delimited by `---` lines, starts at the first byte of
the file, and parses under the YAML rules of [§3](#3-the-root-manifest).

Frontmatter keys beginning with `engram-` are RESERVED for future
versions of this specification. Schemas MUST NOT define them and
records MUST NOT carry them.

### 4.2 Universal labels

Every record MUST carry:

- `type` — string; MUST resolve to a schema visible from the record's
  directory ([§6.1](#61-resolution)).
- `description` — string, 1–200 characters, single line. The record's
  catalog entry: what a reader learns about this record without opening
  it ([§5.2](#52-the-catalog), principle D4).

Every record MAY carry:

- `pinned` — boolean. A portable hot/cold signal: a runtime that
  preloads store content SHOULD prefer records with `pinned: true`.
  This specification defines no further pinning semantics.

No other universal label exists. Creation and modification timestamps
are deliberately not labels: the store's version-control history is
their single source of truth, and duplicating them in frontmatter
creates a second truth that rots. Types that need dates carry
domain-meaningful date fields in their own schemas (for example
`valid_until`), never bookkeeping copies of VCS metadata.

All other frontmatter is governed by the record's schema.

---

## 5. Directory READMEs

Every directory in a store — the root included, `.engram/` and
reserved entries excluded — MUST contain a `README.md`. It is the map
of that directory.

### 5.1 Contract

A README MUST begin with YAML frontmatter containing:

- `description` — string, 1–200 characters, single line: what this
  directory holds, phrased for a reader deciding whether to descend.

and MAY contain:

- `catalog` — `all` | `dirs` | `none` (default `all`;
  [§5.2](#52-the-catalog)).

Unknown README frontmatter keys are tool-specific; consumers MUST
ignore keys they do not recognize. READMEs do not carry `type`: maps
are not records and do not participate in the type system.

The body MUST state the directory's purpose and, when the directory
accepts records, its **placement rules** — what belongs here and what
does not, phrased so an agent deciding where to write can decide
without asking. The `## Placement` heading is RECOMMENDED for that
section.

A README describes its own directory. It MUST NOT describe the internal
content of descendant directories beyond their one-line descriptions:
each child README is the authority on its own directory, and duplicated
descriptions are staleness waiting to happen.

### 5.2 The catalog

Unless `catalog: none`, the README body MUST contain exactly one
catalog block delimited by these markers:

```markdown
<!-- engram:catalog -->
<!-- /engram:catalog -->
```

The block between the markers is **machine-owned**: it is generated
from the directory's contents and the `description` fields of its
children, and a conforming store is one where the block is byte-exact
equal to its regeneration. It is never edited by hand and never carries
prose.

Catalog content, in order:

1. one line per child directory, alphabetical:
   `- [<name>/](<name>/README.md) — <that README's description>`
2. unless `catalog: dirs`: one line per record directly in this
   directory, alphabetical:
   `- [<name>](<name>.md) — <that record's description>`

Assets are not cataloged; when they matter, the README's prose mentions
them. `catalog: dirs` exists for large homogeneous collections
(a daily journal directory does not need 365 catalog lines — its
records are addressed by name); `catalog: none` additionally drops the
directory lines and is appropriate only when the README's prose fully
replaces the map.

The catalog is what makes lazy navigation work (D4): a reader descends
by descriptions alone, opening only what is relevant. Because the block
is generated, descriptions live in exactly one place — the described
file — and the map cannot drift from the territory without
[§9](#9-validation) noticing.

---

## 6. Types and schemas

### 6.1 Resolution

The `type` of a record resolves by ascending search: first
`.engram/schemas/` of the record's own directory, then of each ancestor
up to and including the store root. The first schema file named
`<type>.md` found is the definition.

Consequently a type defined at depth *d* is visible in the whole
subtree of *d*, and a type defined at the root is visible everywhere.
A record whose `type` resolves to no schema file is invalid.

### 6.2 Shadowing forbidden

A schema file MUST NOT define a type name that is already visible from
its scope's parent. If `project` means something in a subtree, it means
exactly that everywhere in the subtree: a name that changed meaning
with depth would silently corrupt every cross-cutting query. Scoped
vocabularies choose new names instead (`meeting-note`, not a second
`note`).

### 6.3 The note baseline

The store root MUST define the type `note`: a minimal, low-ceremony
record type whose only required frontmatter is the universal labels.
The canonical definition appears in
[Appendix A.3](#a3-engramschemasnotemd); a store MAY extend it but MUST
NOT make `note` more demanding than the universal labels plus OPTIONAL
fields.

This is a deliberate floor. The value of a markdown store is low
write friction: there is always a valid type to write, and ceremony is
opt-in per type — never the entry price of remembering something.

### 6.4 Schema files

A schema file lives at `.engram/schemas/<type>.md` and defines one
type. It MUST begin with YAML frontmatter with these fields:

| Field | Requirement | Meaning |
|---|---|---|
| `type` | REQUIRED string | The type name; slug syntax (1–64 chars, `[a-z0-9-]`, no leading/trailing/double hyphen); MUST equal the filename minus `.md` |
| `version` | REQUIRED integer | Bumped on any change that can invalidate existing records |
| `description` | REQUIRED string | One line: what this type is for |
| `schema` | REQUIRED mapping | JSON Schema for the record's frontmatter ([§6.5](#65-the-json-schema-profile)) |
| `body` | OPTIONAL mapping | Body requirements ([§6.6](#66-body-requirements)) |
| `policy` | OPTIONAL mapping | Lifecycle policies ([§6.8](#68-policies)) |

The markdown body of a schema file is documentation for the writer —
typically an agent deciding whether and how to create a record. It
SHOULD state: when to create a record of this type and when not to,
how to name it, what each non-obvious field means and why it is shaped
that way, and a canonical example. It SHOULD contain a `## Template`
section whose first fenced code block is a ready-to-fill skeleton;
tooling MAY extract it.

One file, two consumers: validators read the frontmatter, writers read
the prose, and the two cannot drift apart without the diff showing it.

### 6.5 The JSON Schema profile

The `schema` mapping is a JSON Schema (draft 2020-12) that validates
the record's **entire frontmatter**, expressed in YAML syntax — the
same object model, a friendlier serialization.

Profile restrictions:

- `$ref` MAY target only local definitions under `#/$defs`; remote and
  cross-file references MUST NOT be used. A schema file is
  self-contained ([§1.3](#13-design-principles), D2).
- `$dynamicRef`, `$dynamicAnchor`, and `$vocabulary` MUST NOT be used.
- `format` is an annotation, not an assertion, except for `date` and
  `date-time`, which validators MUST assert.
- Unknown keywords are ignored, per JSON Schema semantics — except the
  `x-engram-` prefix, which is RESERVED for this specification's
  extensions ([§6.7](#67-link-fields)).

Universal-label validation ([§4.2](#42-universal-labels)) always runs,
before and independently of the schema. A schema therefore does not
need to re-declare `type` and `description` — but a schema that sets
`additionalProperties: false` MUST declare every universal label it
permits (including `pinned` if records may carry it), or conforming
records would be impossible.

### 6.6 Body requirements

The OPTIONAL `body` mapping supports:

- `required-sections` — list of strings; the record's body MUST contain
  a `## <string>` heading for each entry.

This is deliberately not JSON Schema: it is a distinct, tiny assertion
engine over the markdown body, kept separate so that no reader is
misled about what validates what. Future versions MAY extend it;
unknown `body` keys MUST be ignored by consumers.

### 6.7 Link fields

A string-valued schema position (a property, or the `items` of an
array) MAY carry the extension keyword:

```yaml
x-engram-link:
  types: [person]        # REQUIRED, non-empty list of type names
  must-exist: true       # OPTIONAL, default true
```

A value at that position MUST be a wikilink (`[[<target>]]`,
[§7](#7-links)). Validation asserts, beyond syntax: the target resolves
to a record in this store; and the target record's `type` is one of
`types`. With `must-exist: false` only syntax and store-internal form
are asserted — for references that may be created later.

Typed frontmatter links are the store's relational layer: relations an
agent can traverse and a validator can prove, without a graph database
(D1, D3). Free wikilinks in bodies remain available for everything
looser.

### 6.8 Policies

The OPTIONAL `policy` mapping declares lifecycle guarantees for all
records of the type:

- `immutable: true` — after the changeset that creates it, a record of
  this type MUST NOT be modified, renamed, or deleted.
- `append-only: true` — a modification MUST leave the previous content
  a byte-exact prefix of the new content; renames and deletions are
  forbidden.

The two are mutually exclusive. Policies are **changeset rules**
([§8.1](#81-changesets)): they constrain transitions, not states, so
only changeset-aware tooling can enforce them (E5xx findings,
[Appendix B](#appendix-b--check-catalog-normative)); a static check of
a working tree cannot prove them. Where version-control history exists,
an auditor MAY verify policies retroactively over it.

Policies make guarantees like "the journal is append-only" mechanical
instead of aspirational — the difference between a convention an agent
is asked to respect and an invariant a gate enforces.

### 6.9 Schema evolution

Schemas are edited like any other truth, but never alone: a change that
can invalidate existing records MUST bump `version` and MUST land
together with the migration of every affected record, so that the store
never points at itself in a broken state. Widening changes (adding an
OPTIONAL field, relaxing a constraint) need no migration; narrowing
changes do. The evolve discipline for agents is normed in the skills
annex.

---

## 7. Links

### 7.1 Wikilinks

A wikilink is `[[<target>]]` or `[[<target>|<label>]]`.

`<target>` is the store-root-relative path of a record, without the
`.md` extension: `[[people/jane-doe]]`, `[[projects/acme/decisions]]`.
It MUST NOT begin with `/`, MUST NOT contain `.` or `..` path segments,
and MUST resolve inside the store. One form everywhere — bodies and
frontmatter alike; there is no context-dependent short form, because a
link that means different things in different places cannot be checked
or rewritten mechanically.

A wikilink in a record body MUST resolve to an existing record.
Wikilinks target records only — not READMEs, not assets, not
directories.

### 7.2 Frontmatter link values

Frontmatter fields hold wikilinks as plain YAML strings, quoted:
`people: ["[[people/jane-doe]]"]`. Fields whose schema position carries
`x-engram-link` are validated per [§6.7](#67-link-fields); a wikilink
in a frontmatter position without `x-engram-link` is validated for
resolution like a body link.

### 7.3 Asset and external links

Assets and directories are referenced with standard markdown links
whose destination is a path relative to the containing document:
`[the contract](contracts/2026-msa.pdf)`. Such a destination MUST
exist and MUST remain inside the store after normalization. Absolute
URIs (`https:`, `mailto:`, …) are external references and are not
validated by this specification.

### 7.4 Identity and renames

A record's identity is its store-root-relative path. Renaming or moving
a record is therefore an act with consequences: every inbound wikilink
MUST be rewritten to the new target in the same changeset. Tooling
SHOULD provide this as an atomic operation; an agent without tooling
performs the rewrite by search, in the same changeset
([§11](#11-agent-protocol), P7).

Stable opaque identifiers (surviving renames without rewrites) are
deliberately absent from v1: they require a resolver index to be
useful, and an index the store cannot be read without would violate D1.

---

## 8. Changesets and hooks

### 8.1 Changesets

A **changeset** is the unit of store mutation: an ordered set of
`(added | modified | deleted, path)` entries applied as one
transaction. What delimits a changeset is owned by the environment —
a git commit, a runtime's write transaction, a batch import. Consumers
that enforce transition rules (policies, hooks) define their changeset
boundary explicitly.

Store conformance ([§1.4](#14-conformance-targets)) is a property of
states; policies and hooks are properties of transitions between
states.

### 8.2 Hook declarations

A hook is declared by a YAML file at
`.engram/hooks/<event>/<NN>-<slug>.yaml`, where `<NN>` is a two-digit
ordering prefix and `<event>` is one of the events of
[§8.3](#83-events).

| Field | Requirement | Meaning |
|---|---|---|
| `name` | REQUIRED string | Slug syntax; MUST equal `<slug>` in the filename |
| `stage` | REQUIRED string | `fix` \| `gate` \| `derive` ([§8.4](#84-stages-and-write-boundaries)) |
| `action` | REQUIRED string | Opaque instruction for the executor; this specification does not interpret it |
| `on` | OPTIONAL mapping | Filters: `paths` (list of globs, relative to the hook's scope) and/or `types` (list of type names) |

A hook's **scope** is the subtree of the directory whose `.engram/`
declares it. Absent `on`, the hook applies to every changed record and
asset in its scope.

### 8.3 Events

Four events exist:

| Event | When | Support |
|---|---|---|
| `pre-write` | Before a single file mutation is applied | OPTIONAL |
| `post-write` | After a single file mutation is applied | OPTIONAL |
| `pre-commit` | Before a changeset is sealed | REQUIRED of executors |
| `post-commit` | After a changeset is sealed | REQUIRED of executors |

The commit pair is the integrity boundary; the write pair exists for
environments that can intercept individual writes and want early
feedback. An executor that cannot intercept writes simply never fires
the write pair — nothing in a conforming store may depend on the write
pair for correctness.

### 8.4 Stages and write boundaries

The stage declares what a hook is allowed to do, and the boundaries are
what make the machinery loop-free:

- **`fix`** — pre-\* events only. MAY modify files, but only inside its
  own scope; its modifications join the current changeset. Catalog
  regeneration and formatting live here.
- **`gate`** — pre-\* events only. MUST NOT write anything. Evaluates
  the changeset and MAY reject it; a rejected changeset MUST NOT be
  sealed. Validation lives here.
- **`derive`** — post-\* events only. MUST NOT write inside the store.
  Rebuilds external derived state — indexes, mirrors, notifications
  ([§10](#10-derived-state)).

### 8.5 Applicability and ordering

For a given changeset and event, the applicable hooks are those whose
scope contains at least one changed path matching their `on` filters
(for `types`, the record's resolved type). Each applicable hook is
invoked once per changeset, with the matching sublist.

Execution order is deterministic:

1. by stage: all `fix` hooks, then all `gate` hooks (pre-\* events);
   `derive` hooks (post-\* events);
2. within a stage: by scope depth, root first;
3. within a depth: by filename, lexicographic.

The cascade is single-pass: modifications made by `fix` hooks extend
the changeset and are visible to subsequent hooks, but they MUST NOT
re-trigger the cascade. Gates therefore always evaluate the post-fix
state.

### 8.6 Executors

Who runs hooks is outside this specification ([§1.5](#15-scope)). An
executor that claims conformance MUST implement the commit pair, the
stage write-boundaries, the ordering, and rejection semantics of gates.
A store remains conforming — and fully validatable via
[§9](#9-validation) — with no executor present at all: hooks automate
integrity, they are never its definition (D3).

---

## 9. Validation

### 9.1 check

**check** is the deterministic validation function of the standard. Its
findings — each `(code, path, detail)` — are normed in
[Appendix B](#appendix-b--check-catalog-normative); codes are stable
forever and never reused with a different meaning.

- **Static rules** (E1xx–E4xx) evaluate a store state. Zero static `E`
  findings is exactly store conformance
  ([§1.4](#14-conformance-targets)).
- **Changeset rules** (E5xx) evaluate a transition and require a
  changeset as input ([§6.8](#68-policies)).
- **Warnings** (W9xx) never affect conformance.

check reads only the store (and, for E5xx, the changeset). It performs
no network access and consults no state outside the store. Output
ordering is deterministic: by path, then code.

### 9.2 Advisory diagnostics

Tooling MAY offer heuristic, model-assisted diagnostics — duplicate
candidates, staleness suspicion, description quality, orphan analysis.
Such diagnostics are advisory ("doctor"-class), MUST be clearly
distinguished from check findings, and MUST NOT be described as
conformance. Determinism is the boundary: what a model flags, a human
or agent triages; what check flags, is broken.

---

## 10. Derived state

Anything computed from the store — full-text or vector indexes,
backlink tables, search caches, published mirrors — is **derived
state**.

- Derived state MUST be reconstructible from the store alone; the
  rebuild is a pure function of store content (D1).
- When derived state and store content disagree, the store is right;
  consumers MUST NOT treat derived state as authoritative.
- Derived state lives outside the store, or under the reserved
  `.engram/cache/` at the root — which SHOULD be excluded from version
  control and MUST survive deletion at any moment without loss of
  truth.
- Records and maps MUST NOT depend on derived state to be
  interpretable.

This is the standard's answer to retrieval infrastructure: build any
index you want — a `derive` hook rebuilding it at every commit is the
intended pattern ([§8.4](#84-stages-and-write-boundaries)) — but the
files remain the memory.

---

## 11. Agent Protocol

Obligations of an agent working in a store. They bind the agent's
behavior; nothing here authorizes mutations the agent's task or host
policy does not authorize.

- **P1 — Enter through the map.** At first contact with a store in a
  session, read the root `README.md` before anything else. MUST NOT
  bulk-read the tree (D4).
- **P2 — Discovery.** Before reading or writing under a directory, read
  its `README.md` and those of its unread ancestors. The maps are the
  navigation authority; trust them over assumptions, and keep them true.
- **P3 — Find discipline.** Retrieval uses both navigation (catalog
  descent by descriptions) and content search (grep-class, with at
  least one reformulation of terms). Absence is claimed only after both
  paths have been exhausted; before that, the honest answer is "not
  found so far".
- **P4 — Write path.** Before creating a record: resolve the type and
  read its schema file — prose included, that is where "when not to
  create one" lives; place per the READMEs' placement rules; follow the
  type's template. After writing: validate (run check where tooling
  exists; verify against the schema by reading where it does not) and
  regenerate affected catalogs. A write that leaves the store
  non-conforming is not a completed write.
- **P5 — Contradiction.** On discovering that new information
  contradicts an existing record: never silently overwrite. Follow the
  type's superseding semantics where defined; otherwise surface the
  conflict to the human or runtime. Both versions beat a silent pick.
- **P6 — Provenance is never invented.** Where a type defines
  provenance fields, record an identifier or permalink only if it was
  obtained from a tool result in the current session; otherwise state
  the absence explicitly. Never construct, guess, or complete a
  reference.
- **P7 — Structure is maintained at write time.** Creating a directory
  includes creating its conforming README in the same changeset. Moving
  or renaming a record includes rewriting every inbound link in the
  same changeset ([§7.4](#74-identity-and-renames)).
- **P8 — Maps carry no state.** Descriptions in READMEs and records are
  stable descriptors — what a thing is and what is at stake. Mutable
  status, counters, and anything relative to "today" live inside
  records, never in maps, where they rot silently.

---

## 12. Profiles

A **profile** is a named, versioned set of schema files — optionally
with protocol guidance and skills — that gives stores a shared
vocabulary. Profiles are how interoperability happens without the core
freezing semantics: the core norms the mechanism, profiles norm
meanings, and adopting one is OPTIONAL.

- Installing a profile copies its schema files into the root
  `.engram/schemas/` and records `<name>@<major>` in `root.yaml`. The
  store remains self-contained: validation never fetches a profile from
  anywhere (D2).
- Installation is all-or-nothing: profiles are coherent vocabularies
  (their types cross-reference), and partial installs would break that
  coherence.
- Installed schema files MAY be edited locally; the store is then a
  fork of the profile and upgrade tooling MUST surface the drift rather
  than overwrite it.
- A profile's types obey all core rules — shadowing included
  ([§6.2](#62-shadowing-forbidden)).

The **base profile** — common entity types for agent memory (`note`,
`fact`, `person`, `project`, `journal-entry`) and the episodic/semantic
store pattern — is normed in
[annex-base-profile.md](annex-base-profile.md), versioned independently
of this core.

---

## 13. Adoption

A project that keeps engram stores SHOULD declare them in its agent
entrypoint (`AGENTS.md`, `CLAUDE.md`, or equivalent), naming each store
root and pointing at its README:

```markdown
<!-- engram:adoption -->
Agent memory lives in engram stores (spec v1): `memory/` and
`knowledge/`. Before touching a store, read its root `README.md` and
follow the Agent Protocol it carries.
<!-- /engram:adoption -->
```

The HTML-comment markers are an OPTIONAL convention for mechanical
detection; wording MAY vary. The declaration matters because store
roots — unlike a fixed `.agents/` location — are wherever the project
put them.

A store also stands alone: its root README carries enough of the Agent
Protocol ([Appendix A.1](#a1-root-readmemd)) that an agent encountering
the store with no prior context still operates it correctly (D2). The
adoption block reduces friction; it is not what makes the store work.

---

## 14. Versioning

This document is specification v1, at draft status.

While it is a draft, every normative change MUST update the revision
date and the repository changelog. After stabilization, a breaking
structural or semantic change MUST bump the major version, and a
backward-compatible normative addition MUST bump the minor version.

Stores declare the major version they target in `root.yaml`
([§3](#3-the-root-manifest)). Annexes — the base profile, the skills
annex — version independently; an annex's version MUST NOT be inferred
from this document's.

The check catalog is append-only across versions: a code, once
assigned, keeps its meaning forever.

---

## Appendix A — Canonical skeletons (normative)

Adopting stores SHOULD start from these skeletons; a store that does so
satisfies the corresponding contracts. Adopters MAY extend them.

### A.1 Root `README.md`

```markdown
---
description: "<one line: what this store remembers and for whom>"
---
# <store name>

<2–4 lines: what this store is, what belongs in it as a whole, and the
first thing a newcomer should know.>

This store follows the engram standard (v1): every directory carries a
README map, every record declares a `type` resolved against schemas in
`.engram/schemas/`, and the store validates deterministically.

## Map

<!-- engram:catalog -->
<!-- /engram:catalog -->

## Placement

<What belongs at this level and what must descend into a subdirectory.>

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
```

### A.2 `.engram/root.yaml`

```yaml
engram: 1
```

### A.3 `.engram/schemas/note.md`

The canonical `note` definition — byte-identical to
[`profiles/base/schemas/note.md`](../../profiles/base/schemas/note.md).

````markdown
---
type: note
version: 1
description: "Free-form note; the universal low-ceremony baseline type."
schema:
  type: object
  properties:
    type: {const: note}
    description: {type: string, minLength: 1, maxLength: 200}
    pinned: {type: boolean}
    tags:
      type: array
      items: {type: string}
  additionalProperties: true
---
# note

The floor of the store: a typed record with nothing required beyond the
universal labels. Deliberately cheap — the value of a markdown store is
low write friction, so remembering something must never wait for
ceremony.

## When to use it — and when not

Use `note` when no schema-bearing type fits. If you find yourself
writing the third `note` about the same kind of thing, that is a
missing type, not a reason to force structure into notes.

## Fields

`description` is the record's catalog line: write it for someone who
does not know the file exists. `tags` are OPTIONAL free labels; prefer
links to records over tags when the target deserves existence.

## Template

```markdown
---
type: note
description: "<one line for the catalog>"
---
# <Title>

<Self-contained content; first sentence stands alone.>
```
````

---

## Appendix B — Check catalog (normative)

Codes are stable: assigned once, never reused. `E` findings in the
static ranges (E1xx–E4xx) decide store conformance; E5xx require a
changeset; `W` findings are advisory.

### E1xx — Structure

| Code | Finding |
|---|---|
| E101 | Directory without `README.md` |
| E102 | Nested store root (`.engram/root.yaml` below the root) |
| E103 | Symbolic link inside the store |
| E104 | Special filesystem entry inside the store |
| E105 | Invalid or unparsable `root.yaml` |
| E106 | Entries in one directory differing only by case |
| E107 | Filename contains a forbidden character ([§2.6](#26-filenames-and-encoding)) |
| E108 | Markdown file not valid UTF-8 |

### E2xx — Records and maps

| Code | Finding |
|---|---|
| E201 | Record without frontmatter, or frontmatter unparsable under [§3](#3-the-root-manifest) YAML rules |
| E202 | Missing or empty `type` |
| E203 | `type` resolves to no visible schema |
| E204 | Missing, empty, multi-line, or over-long `description` |
| E205 | Frontmatter key with reserved `engram-` prefix |
| E206 | README missing required frontmatter (`description`) |
| E207 | Invalid `catalog` value in README frontmatter |

### E3xx — Schemas and typed content

| Code | Finding |
|---|---|
| E301 | Record frontmatter violates its type's `schema` (detail carries the JSON Pointer) |
| E302 | Record body missing a `required-sections` heading |
| E303 | Schema file invalid (frontmatter fields, slug/filename mismatch, JSON Schema profile violation) |
| E304 | Type shadowing ([§6.2](#62-shadowing-forbidden)) |
| E305 | Schema sets `additionalProperties: false` without declaring permitted universal labels |
| E306 | Mutually exclusive policies both set |
| E307 | Root does not define the `note` baseline, or `note` violates [§6.3](#63-the-note-baseline) |

### E4xx — Links and catalogs

| Code | Finding |
|---|---|
| E401 | Wikilink does not resolve to a record |
| E402 | Typed link target's type not in `x-engram-link.types` |
| E403 | Link target escapes the store, or wikilink form invalid |
| E404 | Relative markdown link destination does not exist |
| E405 | Catalog block missing, duplicated, or not byte-exact with its regeneration |

### E5xx — Changeset and policy

| Code | Finding |
|---|---|
| E501 | `immutable` record modified, renamed, or deleted |
| E502 | `append-only` record edited non-appendingly, renamed, or deleted |
| E503 | Deletion or move left inbound links dangling (same-changeset rewrite missing) |

### W9xx — Warnings

| Code | Finding |
|---|---|
| W901 | Filename not a kebab-case slug |
| W902 | Record with no inbound links and older than the store's median record (orphan candidate) |
| W903 | `description` duplicated verbatim across records |
