# engram reference CLI — implementation plan

**Status:** Implemented; release-ready `v1.0.0-rc.1` baseline
**Revision:** 2026-08-13
**Normative status:** Non-normative

This plan records how the v1 specification was implemented as the reference
`engram` CLI. The
[core specification](spec/README.md), its normative annexes, and the
[observable CLI contract](cli/README.md) remain authoritative when this plan
disagrees with them.

## 1. Direction

The reference implementation uses Go for parsing, validation, managed Git
operations, workflows, and the command interface. It invokes the system Git
executable for repository storage and plumbing rather than embedding another
Git implementation.

Local Git integration installs a minimal POSIX `sh` `pre-commit` guard. The
guard only recognizes the managed context and rejects unmanaged commits with
guidance to use `engram commit`. The Go process owns preparation, validation,
acceptance, synchronization, and recovery under the Git annex. The guard never
becomes a second acceptance engine.

### Outcomes

The first stable release is ready when:

- every emit-able normative finding has positive and negative fixtures, every
  retired code has a non-emission regression, and every applicable procedural
  writer, executor, attachment, and synchronizer obligation has an integration
  or fault test;
- `engram check` produces identical finding identities, ordering, statuses,
  and JSON bytes on all supported platforms;
- accepted local writes are all-or-nothing at the accepted-ref boundary,
  preserve unrelated staged and unstaged bytes, and leave explicit recoverable
  state after an interrupted post-acceptance reconciliation;
- the complete command surface has black-box coverage for syntax, result
  shape, ordering, exit status, and representative failures;
- the bundled examples and curated schemas pass the implemented checker; and
- local-only commands are proven not to initiate repository network access.

Coverage is tracked in machine-readable fixtures and test manifests under
`testdata/`, not as exhaustive prose in this plan.

### Non-goals

Version v1 deliberately omits schema profiles, MCP, a daemon, a database, a
public transaction handle, a top-level changeset family, an `engram git`
family, a second hook phase, or a second acceptance engine behind raw
`git commit`.
Ordinary file reading, writing, and searching remain filesystem operations.

## 2. Technical baseline

### Runtime and dependencies

- The module targets the Go 1.25 language baseline. CI covers supported patch
  releases of Go 1.25 and Go 1.26.
- Direct non-stdlib dependencies are pinned to `go.yaml.in/yaml/v3` v3.0.5,
  `github.com/santhosh-tekuri/jsonschema/v6` v6.0.2, and
  `github.com/yuin/goldmark` v1.8.5. `go.mod` and `go.sum` pin the complete
  transitive graph.
- The system `git` executable supplies objects, refs, worktrees, indexes, and
  atomic plumbing. Capability probes, not a version-string threshold, decide
  whether the required operations are available.
- After explicit module acquisition, ordinary builds and tests run without
  network access. Runtime standards data, schemas, skills, and launcher
  templates are embedded or checked in.

Library defaults are not conformance authority. The implementation enforces
the engram constraints for YAML 1.2.2 Core scalars, exact numbers, the admitted
JSON Schema subset and formats, CommonMark 0.31.2 parsing, and Unicode 17.0.0
normalization/case folding. Generated Unicode data includes its source,
license, hash, and reproducible generator.

### Architecture

| Layer | Responsibility |
|---|---|
| Interface | Command grammar, discovery, text output, JSON v1 envelopes, exit mapping |
| Portable core | Logical paths and snapshots; YAML, Markdown, Unicode, schemas, catalogs, links, changesets, findings |
| Managed Git | Raw refs/objects/index projection, accepted-history audit, locks, compare-and-swap, reconciliation |
| Workflows | Draft helpers, `MEMORY.md` attachments, harness setup, hooks/trust, init/clone, commit/revert, pull/push, doctor |
| Platform integration | Filesystem capabilities, controller-owned state, process launch, network isolation, build metadata |

Dependencies point inward: portable conformance code imports no Git, CLI,
process, or network layer. Workflow code composes the portable and managed Git
layers; the interface only translates their typed results. Tests may exercise
each layer directly and the compiled binary as a black box.

### Implementation status

| Milestone | Release-candidate status |
|---|---|
| M0 — contract harness and dependency proofs | Complete |
| M1 — portable snapshot engine | Complete |
| M2 — changesets and managed read paths | Complete |
| M3 — draft, staging, and attachment helpers | Complete |
| M4 — hooks, trust, and managed acceptance | Complete |
| M5 — linear synchronization | Complete |
| M6 — operations, packaging, and release readiness | Complete; release workflow re-runs all gates before publication |

The final `v1.0.0` tag remains a publication decision. Until then, finding
identities, the JSON v1 protocol, and other observable interfaces retain
release-candidate rather than stable status.

## 3. Delivery record

Milestones were dependency gates, not date promises. The completed phases below
record the implementation sequence: conformance first, then local authoring and
acceptance, then synchronization and release hardening.

### Phase 1 — conformance kernel and managed reads

#### M0 — Contract harness and dependency proofs

**Depends on:** the v1 draft documents and seed fixtures.

**Deliver:** Go module/process skeleton; one command model; JSON envelope and
build metadata; the versioned fixture manifest; standards subsets; Unicode
tables/generator; dependency spikes for lossless YAML access, Markdown source
ranges, exact JSON numbers, portable regex/formats, and SHA-1/SHA-256 Git
objects; capability probes; and the five canonical skill artifacts promised by
the skills annex.

**Gate:** every external library passes its applicable conformance fixtures
behind an engram adapter or is contained/replaced. No normative ambiguity is
left as an implementation TODO.

#### M1 — Portable snapshot engine

**Depends on:** M0 parser and standards proofs.

**Deliver:** safe logical traversal; path/name/text checks; YAML/frontmatter;
schema resolution and validation; body requirements; Markdown and wikilinks;
typed links; catalogs; finding suppression/ordering; portable `check`; and
`schema inventory/list/show`.

**Gate:** the complete portable fixture corpus is deterministic and green on
all CI platforms; `examples/minimal/` and curated schemas pass; parser fuzzing
cannot crash or escape/prune-follow.

#### M2 — Changesets and managed read paths

**Depends on:** M1 snapshots and the M0 Git object proof.

**Deliver:** deterministic snapshot differences; transition rules and
complete/indeterminate status; raw Git discovery/projection; accepted-lineage
audit; read-only `status`, `diff`, `log`, `check --accepted`, `check --staged`,
and explicit snapshot-pair checking; safe in-process audit reuse keyed by exact
tip and normative rule-set identity.

**Gate:** managed fixtures cover both object formats, root and linear history,
linked worktrees, merge/malformed boundaries, unavailable objects without
fetching, raw-tree pruning, index eligibility, modes, replacements/grafts, and
deterministic inspection results.

### Phase 2 — local authoring and acceptance

#### M3 — Draft, staging, and attachment helpers

**Depends on:** M1 rewrite primitives and M2 index models.

**Deliver:** `add`, `fmt`, `new`, `mv`, and `schema copy`; worktree
coordination; dry-run/check behavior; lossless link/catalog rewrites; local
`attach` and `detach` through project `MEMORY.md`; project-scoped harness
`setup`; declarative project `engram.yaml`, ignored `.memory/` acquisition and
attachment reconciliation; and the bounded `doctor --recover` support required
by CLI-owned helper state.

**Gate:** helpers preserve unrelated bytes, reject collisions and concurrent
changes, publish no partial successful result, never move accepted refs, and
match JSON/text goldens. Attachment tests cover missing, valid, malformed,
duplicate, aliased, and concurrently updated owned blocks. Declarative setup
tests cover strict parsing, dry-run network silence, verified acquisition,
exact reuse without fetch, harness overrides, external attachment preservation,
and non-destructive removal.

#### M4 — Hooks, trust, and managed acceptance

**Depends on:** M2 managed audits and M3 safe local update primitives.

**Deliver:** annex-compatible locks and recovery; external physical-store
bindings and complete-set hook trust; sealed disposable hook execution;
`commit`, `commit --dry-run`, and `revert`; the owned raw-Git guard; `init`,
verified `clone`, and `hooks list/trust/revoke`; plus the corresponding
`doctor` diagnostics and recovery paths.

**Gate:** fault injection across every normative acceptance/recovery boundary
proves that pre-acceptance failures do not move the accepted ref,
post-acceptance failures remain recoverable, unrelated draft bytes survive,
hooks execute once from the trusted base set, and raw/native Git hooks or
inherited Git environment cannot redirect the managed operation. Init and
clone publish only complete verified stores and are safe to retry or recover.

### Phase 3 — synchronization and release readiness

#### M5 — Linear synchronization

**Depends on:** M4 managed transactions and recovery.

**Deliver:** `pull`, `pull --continue`, `pull --abort`, and `push`; separate
network authorization; incoming lineage audit; fast-forward compare-and-swap;
exact divergent replay through managed transactions; explicit resolution
drafts; private replay state; and synchronization-aware `status` and `doctor`.

**Gate:** local integration tests cover up-to-date, fast-forward, divergence,
conflict/rejection, continue/abort, unrelated history, ref and checkout races,
interruption/recovery, missing history, network denial, remote conditional
updates, and unknown publication outcome. No path performs merge, force, or
implicit retry.

#### M6 — Operations, packaging, and release readiness

**Depends on:** all prior milestone gates.

**Deliver:** remaining advisory `doctor` checks; final `version`; canonical
skills packaged through skills-only adapters; cross-platform archives,
checksums, release notes, and operator documentation. Generated shell
completions remain a possible post-v1 convenience and were not part of the
completed command surface.

**Gate:** all commands and global-option combinations have golden text/JSON
and exit tests; unit, race, vet, fuzz-smoke, shell, and repository checks pass;
CI covers supported macOS, Linux, and Windows combinations; licenses and
generated-data provenance ship; examples and schemas validate; local commands
are network-silent; and CLI, plan, changelog, and README describe the same
surface.

## 4. Test strategy

| Test layer | Required evidence |
|---|---|
| Pure unit | Exact scalars/numbers, Unicode/path keys, dates, regex, links, catalogs, changesets, ordering |
| Normative fixtures | Positive/negative cases for each emit-able finding; retired-code non-emission; suppression and attribution |
| Protocol goldens | Complete JSON result families, deterministic arrays, statuses 0–3, stable error kinds, text output |
| Git black box | SHA-1/SHA-256, worktrees, raw objects, modes, sparse/filtered state, malformed indexes, replace/graft isolation |
| Fault injection | Failure around every required lock, hook, journal, object, ref, index, checkout, and publication boundary |
| Procedural conformance | Outcome/error evidence for each writer, executor, attachment, and synchronizer duty |
| Concurrency | Shared refs/worktrees, stale owners, compare-and-swap races, parallel creation/acquisition |
| Security boundary | Exact trusted hook sets, cleared Git/engram environment, no native hooks, base immutability, resource limits |
| Cross-platform | Case/Unicode filesystems, LF and byte preservation, executable/path quoting, Git for Windows guard |
| Fuzzing | YAML, paths, JSON Pointer/regex, Markdown/wikilinks, catalogs, raw Git record parsers |

The fixture manifest under `testdata/conformance/` is the coverage source of
truth. Test results compare structured identities before human detail. No
fixture depends on directory enumeration order, locale, timezone, filesystem
normalization, ambient Git configuration, or network access.

## 5. Main risks and controls

| Risk | Control |
|---|---|
| Libraries accept broader or different standards behavior | Prevalidate the engram subset and gate adapters on pinned standards fixtures |
| Go Unicode tables differ from Unicode 17 | Check in source, hashes, generator, and generated tables; assert the version |
| Markdown parser cannot support lossless rewrites | Maintain a small tested source-range adapter; block helper delivery if preservation fails |
| JSON Schema engine rounds numbers or resolves externally | Preserve exact values, deny external loading, pre-resolve admitted refs, and supply strict regex/formats |
| Git config, attributes, or environment transform bytes | Probe presentation, isolate Git children, capture exact inputs, and use raw projection/reconciliation |
| Crash after accepted-ref update leaves checkout stale | Preserve the accepted commit and explicit recovery state; block later writers until reconciliation |
| Trusted hooks cause external effects | Trust exact complete sets, use sealed disposable trees and finite resources, deny network where possible |
| Pull conflict cleanup loses user work | Require a clean start, reserve explicit replay state, and restore/discard only captured pull-owned bytes |
| CLI and implementation drift | Generate parser/help/protocol tests from one command model and update goldens with every surface change |

## 6. Work sequencing

M0 → M1 → M2 formed the critical path. M3 followed the M1 rewrite primitives
and M2 index models; M4 followed managed audits and safe local updates; M5
followed acceptance fault injection. M6 closed the release gates rather than
deferring conformance defects.

When implementation exposes a normative ambiguity, update the specification
and changelog before code chooses behavior. When only CLI behavior is unclear,
resolve it in the CLI contract and its goldens before implementation.
