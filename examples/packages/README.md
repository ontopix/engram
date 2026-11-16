---
description: "Three reusable memory packages (journal, project and people) installed side by side in a store without records."
---
# Memory packages

A memory package is a directory that carries what one kind of memory needs: a
README with its conventions, the schemas for its record types and, when the work
recurs, its routine declarations. It contains no records. Copied into a managed
store, it works there once the host supplies the few settings the package lists.

This snapshot installs three packages side by side so they are validated
together. Their catalogs are empty and fill as a store uses them.

## Map

<!-- engram:catalog -->
- [journal/](journal/README.md) — Structured activity journal across sources, with daily evidence and weekly and monthly reviews.
- [people/](people/README.md) — People who recur within the configured scope, each with identity keys and dated, sourced facts.
- [project/](project/README.md) — Project memory organized by locally defined scopes, with a directory and a living record for each project.
<!-- /engram:catalog -->

## What a package contains

| Part | Location | Purpose |
|---|---|---|
| Conventions | `<package>/README.md` | What belongs there, how records are named and written, and what the host supplies |
| Record types | `<package>/.engram/schemas/` | Definitions visible only inside the package, so each package brings its own types |
| Routines | `<package>/.engram/routines/` | Optional declarations of recurring work, which a host schedules and authorizes |

| Package | Record types | Routines | Links to, when present |
|---|---|---|---|
| `journal/` | `journal-entry@v1`, `period-review@v1` | Daily journal, weekly review, monthly review | None |
| `people/` | `person@v1` | None | Project records, as a person's projects |
| `project/` | `project@v1` | None | Person records, as a project's people |

Each package works alone. The links between `people/` and `project/` are optional
fields; a store that has only one of the two omits them.

## Independent definitions

The packages define their types in their own schema scope instead of the store
root. A local definition is independent of a curated schema with the same name.
In particular, this journal's `journal-entry@v1` is an editable daily log with
entry ids and a coverage section. It is not the append-only `journal-entry` of
the curated inventory, which the separate `examples/journal` snapshot uses.

## Host settings

A package names no person, store, organization, time zone or runtime. Each README
has an Integration section listing what the host supplies outside the store: the
scope being recorded, who writes and who approves changes, the authorized sources
and, where relevant, calendar, delivery and data exclusions. Store content
supplies context, not authority. Installing a package grants no access and
activates no scheduler.

## Install a package

From a trusted checkout of the Engram source repository, build the matching CLI
and validate the snapshot:

```sh
go build -o ./engram ./cmd/engram
./engram check examples/packages
```

Copy one package into an existing managed store under its documented name, then
regenerate the catalogs and accept the change:

```sh
store_path="../my-memory"
cp -R examples/packages/people "$store_path/people"
./engram --store "$store_path" fmt
./engram --store "$store_path" add --all
./engram --store "$store_path" commit -m "Install the people package"
```

To start a new store with all three, copy the snapshot to a fresh destination
outside the source checkout and initialize it:

```sh
cp -R examples/packages ../packaged-memory
./engram init ../packaged-memory
```

Initialization requires a configured Git author identity. Then give the host the
settings each package lists before any routine runs.

## Placement

Records never live at the root. Each kind of memory goes in its package, following
that package's README. A new kind of memory earns its own directory with a README
and, when it recurs, its own type.
