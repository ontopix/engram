# Changelog

## Unreleased

### Documentation architecture

- Closed the implementation-facing ambiguities found in the final baseline
  review: E603 now covers only grammatically valid raw entries that core prunes
  without another `E` finding; the final candidate exists before final
  validation; hook programs receive their absolute base-materialization path;
  the core base-state definition is binding-neutral; and E503 explicitly
  includes required frontmatter wikilinks. CLI and plan wording now consistently
  distinguishes working drafts, initial and final candidates, accepted state,
  and the accepted ref.
- Simplified the v1 documentation around one authority per layer: the
  core owns snapshot semantics, transitions, validation, and the Agent
  Protocol; the Git annex owns managed history and acceptance; the CLI
  contract owns observable command behavior; the implementation plan
  owns sequencing and release gates.
- Reduced the core glossary to `snapshot`, `store`, `managed store`, and
  `consumer`. File kinds are now defined with layout, schema files with
  the type system, and all write-lifecycle terms together in §8.1.
- Consolidated traversal precedence and finding evaluability instead of
  restating them in schema, hook, and validation sections. The exact
  preparation-hook machinery remains normative in core Appendix C.
- Reorganized the Git annex around a short binding and phased
  transaction. Raw Git projection remains normative in Appendix A;
  locking, fingerprints, durable recovery, ABA handling, and idempotent
  reconciliation remain normative in Appendix B.
- Made the canonical skills annex non-normative guidance. Independent
  bootstrap trust and map-plus-`pinned` co-reading now live in the core
  Agent Protocol, so agent conformance has one source.
- Replaced duplicated adapter, CLI, and roadmap algorithms with links to
  their normative owners. The public CLI commands, JSON v1 shapes,
  statuses, error classes, safety guarantees, and milestone gates remain
  explicit.
- Corrected cross-document drift around path identity, provenance,
  temporal facts, journal granularity, optional catalogs, root README
  content, and universal versus domain timestamps. The minimal example's
  embedded protocol now matches the canonical skeleton.

### V1 draft assembled

- Defined portable, self-describing snapshots with directory maps,
  typed markdown records, lexical schema resolution, the mandatory
  low-ceremony `note` baseline, checked links, deterministic catalogs,
  derived-state boundaries, and an Agent Protocol.
- Closed deterministic behavior for Unicode names, UTF-8/LF text, YAML
  1.2.2 values and exact numbers, the JSON Schema profile, CommonMark
  parsing, link resolution, catalog serialization, finding identity,
  ordering, causal suppression, and complete versus indeterminate
  transition evaluation.
- Added the runtime-neutral `prepare-changeset` protocol with base-state
  selection, ordered exact hook sets, external trust, disposable
  materializations, stable private capture, one executor, and final
  validation.
- Added the normative Git-managed-store binding: exact root ownership,
  byte-transparent worktrees, linear accepted history, complete lineage
  audits, staged initial candidates, compare-and-swap acceptance,
  preservation of unrelated drafts, crash recovery, and exact linear
  synchronization without merge or automatic text conflict resolution.
- Added E1xx–E6xx conformance findings and W9xx warnings. W902 was
  retired because portable validation has no stable age input; age-based
  orphan analysis is advisory. Codes remain draft identifiers until v1.
- Added the non-normative reference CLI contract, phased Go
  implementation plan, curated schema inventory, runtime adapters,
  canonical skills guidance, minimal example, and seed conformance
  fixtures.
- Removed the earlier profile mechanism from v1. Curated schemas are
  copied individually and become ordinary local schemas; only the root
  `note` type is mandatory.
