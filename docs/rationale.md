# Rationale

Non-normative. This document explains the design choices; the
[specification](spec/README.md) governs whenever they differ.

## Why files

Agents and humans already know how to read, search, diff, and repair
files. A markdown store keeps memory inspectable with those familiar
tools and avoids making a database, service, or retrieval pipeline a
prerequisite for access.

Two properties matter as much as retrieval quality:

- **Auditability.** A person can inspect and correct what the agent
  believes.
- **Visible failure.** Incorrect content and incomplete writes appear in
  ordinary files and diffs rather than behind an extraction pipeline.

This does not imply that plain files solve retrieval. It means they are
the durable source from which better retrieval can be rebuilt.

## Where files need structure

Three important weaknesses shape the standard:

1. **Placement and retrieval degrade with scale.** Directory READMEs
   provide local maps, and deterministic catalogs do so when enabled.
   Agents combine map descent with reformulated content search while
   keeping unrelated files out of model context.
2. **Claims change over time.** Domain schemas can record validity and
   superseding relations alongside the store's Git history. The curated
   `fact` type demonstrates closing an old claim and linking its
   replacement; cross-record consistency remains advisory in v1.
3. **Some useful recall has no obvious query.** Full-text, vector, graph,
   or ranking indexes are welcome as derived state. They remain
   replaceable because disagreements are resolved in favor of the
   files.

## Why a standard

Memory should outlive the runtime that first wrote it. Engram therefore
defines a portable format and mechanically testable behavior rather
than a memory service. The store carries its maps, types, schemas, and
operating protocol; no product-specific API is required to understand a
snapshot.

The design combines several established ideas: filesystem navigation,
typed markdown frontmatter, wikilinks, generated catalogs, schema-backed
validity, and version-controlled history. Engram's contribution is to
make their boundaries explicit and checkable as one format.

## Why managed writes use Git

A logical memory change often touches several files: a record, its map,
links, and perhaps a schema migration. Treating each filesystem write as
immediately accepted exposes partial states and gives transition rules
no stable base.

Git already supplies immutable trees, parent relationships,
content-addressed commits, and atomic ref updates. A managed store uses
those primitives to distinguish:

- an editable working draft;
- the candidate selected for one operation; and
- the accepted snapshot at the store's branch.

Preparation and validation happen before the accepted ref advances.
The normative [Git annex](spec/annex-git.md) defines the required safety
properties; it does not make Git metadata part of snapshot content.
Consequently an exported tree remains readable and statically
checkable, but becomes writable memory only under a managed-store
boundary.

Giving a reusable memory its own repository also separates ownership
from the projects that consume it. Several projects may attach the same
store without placing its commits in their code histories.

## Deliberate boundaries

- **No graph database.** Typed links form the portable relational layer;
  richer graphs can be derived.
- **No opaque record IDs.** Paths are identities in v1, so renames also
  rewrite inbound links.
- **No embedded memory service.** The format chooses neither a daemon,
  scheduler, database, nor runtime.
- **No universal bookkeeping timestamps.** Git records file history;
  schemas add dates only when time has domain meaning.
- **No mandated retrieval stack.** Retrieval improves downstream of the
  authoritative files.

## The bet

Keep truth in files, accepted history in Git, discipline in the Agent
Protocol, and every acceleration layer rebuildable.
