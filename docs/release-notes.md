# engram 1.0.0-rc.4 release notes

This prerelease completes example compatibility after rc.3. The store format
and command surface are unchanged.

- The quick start and generic agent write handoff now use `new note@v1`.
  Their documented store and write commands run directly in integration tests.
- The directory layout and curated-schema guide now show the versioned names
  required when definitions are installed in a store.
- The [complete journal](../examples/journal/README.md) adds 34 daily entries,
  five linked weekly reviews, and one monthly review. Its records, schema
  filenames, type constants, and templates all select exact revisions.
- Every bundled snapshot is validated in CI. The journal, its local review
  schema, fictional source data, and host-owned routine declaration are
  included in the archives. The CLI validates but does not execute routines.

These examples also work with the published rc.3 CLI. The inherited rc.3
format and upgrade notes follow.

## Explicit schema references from rc.3

Release `1.0.0-rc.3` introduced explicit schema revisions from issue #23,
a breaking change to the draft store format and schema command arguments.

Records now declare an exact reference:

```yaml
type: journal-entry@v2
```

Its definition is `.engram/schemas/journal-entry@v2.md`, with `type:
journal-entry`, `version: 2`, and a direct `schema.properties.type` constant
of `journal-entry@v2`. Multiple revisions coexist; validation uses the
original complete frontmatter and never guesses or falls back.

- Publishing v3 leaves v1 and v2 records valid under their original contracts.
- Published schema semantics cannot be edited in place or removed. A new
  revision must exceed the family's previous maximum; gaps are allowed.
- Append-only and immutable records retain their base protections. Changing
  their type reference is not a permitted migration.
- Typed links can accept any revision of a logical type or one exact revision.
- `schema list` reports every visible revision with its exact reference and
  scope. `init --schema`, `new`, `schema show`, and `schema copy` require exact
  references, for example `engram schema show note@v1`.
- Canonical skills preserve existing references and select the highest numeric
  revision for new records when no revision was explicitly requested.
- Initializing a populated store with asserted date formats no longer reports
  a false concurrent modification from comparing compiled validator functions.

See the [three-revision journal snapshot](../examples/schema-versions/README.md)
and the [CLI contract](cli/README.md). The new diagnostics are E310 (missing
revision), E505 (removed definition), and E506 (non-increasing publication);
E504 now enforces fixed published semantics.

## Upgrading existing draft stores

There is no compatibility mode for unversioned records or schema filenames.
Keep a backup and retain the old store and matching CLI for its historical
lineage. With owner authorization, prepare a converted snapshot in a separate
store: rename definitions, update each matching type constant and template,
and give every record its verified exact reference. Validate that snapshot,
then accept it as a new managed lineage. Do not relabel protected records in
place, rewrite the old history, or claim that old snapshots conform to rc.3.

Re-run `engram setup` to update unchanged installed canonical skills. Bundled
`fact` is `fact@v2`; the other bundled types start at v1. No extra record
`version` field, family descriptor, or write-version setting is needed.

The standard and CLI contract remain drafts until stable `v1.0.0`. This release
includes the unattended workflow improvements from rc.2. Obtain official
archives from the matching GitHub Release and verify its checksums and
provenance. See the [operator guide](operator-guide.md) for operational details.
