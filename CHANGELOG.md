# Changelog

## Unreleased — target 1.0.0-rc.1

### Standard

- Added the normative routine-declarations annex: each optional routine is a
  Markdown file in a local `.engram/routines/` directory with a closed UTC
  five-field cron profile and instructions in its body. Declarations are
  runtime-neutral intent, never authority, hooks, or runtime state.
- Added deterministic hierarchical `prepare-changeset` hooks: hooks may live
  under any logical directory, activate from the frozen initial changeset's
  affected subtrees, execute by ordering band then full logical path, and bind
  trust to the exact applicable base set. Cache remains root-only.
- Added the non-normative project setup manifest `engram.yaml`, separating
  versioned repository and harness intent from the materialized local paths in
  `MEMORY.md`. Declarative stores use the ignored `.memory/` namespace while
  external imperative attachments remain independent.
- Standardized project-level `MEMORY.md` attachment registries so one
  runtime-neutral manifest can discover several independent stores while
  retaining the Agent Protocol's trust and authority boundary. Runtime
  entrypoints now point to that registry instead of duplicating store paths.
- Clarified that draft check-code identities remain changeable through
  prereleases and become append-only at the first stable release (`v1.0.0`),
  matching the repository's stated release-candidate compatibility policy.
- Generalized adapter skill verification to independently trusted distribution
  digests rather than assuming that a public release already exists.

### Public project

- Reworked the README around distinct user, agent-integrator, and standard-
  implementer paths, with a tested end-to-end quick start and visible project
  status badges.
- Added real Codex, Claude Code, and generic filesystem-agent integrations,
  including bounded retrieval, JSON feedback, attachment, trusted-skill, and
  managed-write examples.
- Added a security policy, support guide, contribution workflow, structured
  issue forms, a pull-request checklist, and Dependabot configuration.
- Included the public project policies and agent-integration examples in
  release archives so links from the packaged README remain self-contained.
- Recast the completed implementation roadmap as a delivery record, removed
  stale references to a future CLI, and distinguished release readiness and
  deferred shell completions from published state.

### Reference implementation

- Added local `config attachment add/remove`, `config harness`, and
  `config show` commands for idempotent, comment-preserving edits to project
  `engram.yaml`. Configuration remains separate from explicit `setup`: these
  commands never acquire stores, reconcile attachments, or install skills.
- Extended `setup` to read strict project-root `engram.yaml`, acquire missing
  verified stores below `.memory/<name>`, maintain the root ignore rule,
  reconcile only declarative attachments, and install the selected harness in
  one idempotent flow. Existing clones are verified without fetching, CLI
  `--harness` overrides the manifest without editing it, and removed entries
  are detached without deleting their repositories.
- Changed `attach` and `detach` to manage the versioned attachment block in
  project `MEMORY.md`, retaining an empty registry after the last detach, and
  added idempotent `setup --harness codex|claude-code` to install the embedded,
  digest-verified canonical skills plus a bounded runtime-entrypoint pointer.
  Setup migrates the exact previously CLI-owned adoption block without treating
  similar project prose as owned state.
- Made human CLI discovery follow Git-style conventions: an empty invocation
  shows root help, root commands are grouped by workflow with short
  descriptions, group help describes its subcommands, and incomplete known
  commands print contextual usage. Close command misspellings receive bounded,
  deterministic suggestions while JSON protocol output remains unchanged.
- Hardened initialization and clone recovery against concurrent lifecycle,
  target, stage, and publication-plan replacement. Recovery now seals the
  approved plan bytes, binds observations to descriptor-derived physical
  identities, and revalidates the published root before rollback.
- Made productive Git invocations opt into long-path support and materialized
  retained filesystem identities before mutable boundaries, including draft,
  attachment, staging, guard, journal, rendezvous, and pull observations. This
  closes Windows-specific identity races and long-path failures.
- Added deterministic regression coverage for disappearance, replacement, and
  rollback races, including Windows amd64 and arm64 build coverage.

### Initial reference implementation

- Completed implementation milestones M0–M6: deterministic portable
  conformance, raw managed-Git reads, safe draft helpers, managed acceptance
  and recovery, exact linear synchronization, advisory diagnostics, and the
  complete documented command surface.
- Added deterministic cross-platform release archives for macOS, Linux, and
  Windows on amd64 and arm64, with checksums, machine-readable provenance,
  canonical skills, operator documentation, compiled-dependency licenses, the
  Go license, and Unicode generated-data provenance.
- Added compatibility CI across Go 1.25 and 1.26 on macOS, Linux, and Windows,
  plus race, offline, vet, fuzz-smoke, formatting, module, shell, and repository
  gates. Tagged releases re-run that workflow before a least-privilege publish
  job can create a GitHub release.
- Declared the embedded default CLI version `1.0.0-rc.1`. The standard,
  finding identities, and JSON v1 protocol remain release-candidate interfaces
  until the final `v1.0.0` publication.

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
