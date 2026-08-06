# engram reference CLI — implementation plan

**Status:** Approved direction; implementation not started
**Revision:** 2026-08-06
**Normative status:** Non-normative

This plan turns the closed v1 specification into the reference `engram`
CLI. The [core specification](spec/README.md), its normative annexes,
and the [CLI contract](cli/README.md) remain the authority when this plan
and a contract disagree.

## 1. Direction

The reference implementation will use Go for the CLI, validation engine,
Git transaction engine, and all stateful logic. Local Git integration
will install a minimal POSIX `sh` `pre-commit` guard that delegates to a
private CLI entrypoint and rejects raw `git commit`. The script will not
parse stores, run preparation hooks, alter the index, or create commits.
The Go process will own the complete managed transaction through locking,
hook execution, validation, commit creation, compare-and-swap ref update,
and safe reconciliation.

This split gives one portable executable responsibility for the hard
work while retaining the low-friction Git integration users expect.
POSIX `sh`, rather than Bash-specific syntax, keeps the launcher usable
by standard Git installations on macOS, Linux, and Git for Windows.

### Outcomes

The first stable release is ready when:

- every emit-able cataloged E/W rule has a non-triggering fixture and at
  least one triggering fixture with the expected ordered finding
  identities; each retired code has a regression fixture proving
  non-emission while its historical meaning remains documented; and
  every applicable procedural executor/writer/synchronizer obligation
  has an integration or fault test for its specified outcome and error
  kind;
- `engram check` returns byte-identical JSON and identical `(code, path)`
  sequences on the supported platforms;
- every accepted local write is all-or-nothing at the accepted-ref
  boundary and preserves unrelated staged and unstaged draft bytes;
- the full CLI surface in `docs/cli/README.md` has black-box tests for
  syntax, JSON shape, output ordering, exit status, and failure behavior;
- `examples/minimal/` and every curated schema pass the implemented
  checker; and
- local-only commands perform no repository network operation.

### Non-goals

The implementation will not add profiles, MCP, a daemon, a database, a
public transaction handle, a top-level changeset command, an `engram
git` command family, or another hook phase. It will not make raw Git
commit a second acceptance engine.

## 2. Technical baseline

### Runtime and dependencies

- The module will target the Go 1.25 language baseline and CI will test
  the latest supported Go 1.25 and Go 1.26 patch releases.
- Direct packages will initially be pinned to
  `go.yaml.in/yaml/v3` v3.0.5,
  `github.com/santhosh-tekuri/jsonschema/v6` v6.0.2, and
  `github.com/yuin/goldmark` v1.8.5. `go.mod` and `go.sum` will pin the
  complete transitive graph. M0 will also evaluate the maintained v4
  API, but v1 will not start on a release candidate; the pinned v3 line
  remains API-stable and receives security fixes.
- The system `git` executable will provide repository storage and
  plumbing. Capability probes, not a version-string comparison, will
  decide whether a repository supports the required object format,
  worktree, raw-object, and atomic-ref operations.
- After one explicit module-acquisition step, builds and normal tests
  will run with network access disabled. Standards fixtures, Unicode
  data, schemas, skills, and launcher templates needed at runtime will be
  embedded or checked into this repository.

`yaml.v3` will be used as a syntax tree parser, not as the semantic
authority for YAML scalars. A validation layer will enforce YAML 1.2.2
Core spelling, exact numbers, duplicate/tag/anchor restrictions, and the
closed frontmatter grammars. The JSON Schema library will run only after
engram's JSON Schema-subset prevalidator has rejected forbidden or unknown
keywords; it will receive exact numeric values, asserted strict formats,
and the custom portable regex engine. Goldmark will run in CommonMark
mode without GFM extensions and will be covered by the pinned CommonMark
0.31.2 fixtures relevant to links, images, reference definitions, code
ranges, and ATX headings.

Go's standard Unicode tables are older than the specification's Unicode
17.0.0 boundary. The repository will therefore carry generated,
version-pinned normalization and full default case-fold tables plus the
Unicode source files, licenses, hashes, and generator needed to
reproduce them. Store conformance will never depend on the host or Go
toolchain Unicode version.

### Component boundaries

| Area | Responsibility |
|---|---|
| `cmd/engram` | Process entrypoint and no business logic |
| `internal/cli` | Command grammar, discovery, text rendering, JSON v1 envelope, exit mapping |
| `internal/model` | Logical paths, exact values, snapshots, changesets, findings, deterministic ordering |
| `internal/unicode17` | UTF-8 scalar checks, NFC validation, full default case folding, generated tables |
| `internal/yamlcore` | YAML syntax tree, 1.2.2 Core scalar resolver, exact numbers, closed mappings |
| `internal/markdown` | CommonMark AST adapter, source ranges, wikilink scanner, rewrite primitives |
| `internal/schema` | Schema-file grammar, lexical resolution, JSON Schema-subset prevalidation, `$ref`, regex and formats |
| `internal/store` | Boundary-safe traversal, snapshots, catalogs, links, path attribution and suppression |
| `internal/check` | E1xx–E6xx/W9xx evaluation, aggregation, ordering, complete/indeterminate status |
| `internal/gitstore` | Raw refs/objects, worktrees, index snapshots, E6 audit, temporary indexes, CAS updates |
| `internal/hooks` | Selection, trust-set digest, disposable trees, process environment and execution |
| `internal/transaction` | Shared locks, preflight, preparation, commit creation, failure cleanup, reconciliation |
| `internal/workflow` | Draft helpers, attachment, revert, pull replay, push, and doctor |
| `testdata/` | Normative fixtures, standards subsets, golden CLI output, Git repositories and fault cases |

Pure snapshot and changeset packages will not import Git, CLI, process,
or network packages. The CLI and workflow layers will depend inward on
those pure packages, making the conformance engine independently
testable.

### Managed commit data flow

1. Discover and verify the exact store/worktree relationship and owned
   local guard.
2. Resolve the direct, non-symbolic accepted branch named by symbolic
   `HEAD`, acquire the annex-defined ref and worktree locks, then capture
   two byte-identical observations of `HEAD`, the accepted ref, and the
   exact raw index as the stable base/index.
3. Re-prove local raw ancestry/object completeness, then audit the
   accepted lineage, reusing only a content-addressed validation result
   for an unchanged tip under the identical digested normative rule set.
4. Read base and index blobs into byte-exact virtual snapshots; reject
   unmerged, intent-to-add, pruned, sparse, or transforming state before
   a changeset exists.
5. Materialize disposable base/candidate trees outside every Git
   worktree, select and authorize the complete base hook set, and invoke
   each interpreter directly with the closed environment and canonical
   JSON protocol.
6. Rebuild the final snapshot, compute the definitive changeset, and run
   complete snapshot and transition validation.
7. Prove byte-exact, draft-safe reconciliation, fingerprint every live
   path, index, configuration, attribute, environment, and presentation
   input on which that proof depends, then build the final tree and
   commit object through Git plumbing without moving a ref. Durably write
   the `pending` recovery journal with expected old/new IDs,
   reconciliation plan, and fingerprints, then durably advance the lock
   owner phase to journal-required before CAS.
8. Recheck `HEAD`, the accepted ref, and every safety fingerprint; stop
   on any difference. Otherwise compare-and-swap the accepted ref and
   reconcile the real index and worktree through byte-raw mechanisms
   that consult no uncaptured mutable presentation input. Each actual
   ref, `HEAD`, or index mutation uses Git's short native atomic protocol;
   no native Git lock is held across preparation. After reconciliation,
   durably mark the journal `complete`, release locks, and then remove the
   journal; a crash at any boundary leaves a decidable state for explicit
   recovery without hiding an accepted commit.
9. A rejection before the step-7 journal exists discards only
   transaction-owned temporary state and releases locks through ordinary
   cleanup. Once `pending` is durable, a failed final recheck or a CAS
   that definitively made no update first marks the journal `cancelled`,
   then cleans temporary state, releases locks, and removes the journal.
   An ambiguous local CAS result remains `pending` and returns status 2,
   kind `concurrency`, and the typed recovery result rather than a
   complete command-shaped `indeterminate` response. A cleanup failure
   or process crash leaves the conservative stale lock and phase/journal
   evidence required by the annex and emits the typed recovery result
   when a response remains possible.

The local `pre-commit` file contains only an ownership/version marker and
an exactly quoted absolute executable path. Its private CLI target checks
context and exits with guidance to use `engram commit`. Managed commit
creation uses plumbing that does not invoke that guard recursively.

## 3. Delivery roadmap

Priorities are dependency-based rather than calendar promises. “Now”
establishes the correctness kernel before any writer exists; “Next” adds
local authoring and acceptance; “Later” adds distributed workflows and
release hardening.

### Now — conformance kernel and read-only proof

#### M0 — Contract harness and dependency proofs

Deliver:

- `go.mod`, the process skeleton, deterministic command parser, JSON v1
  envelope, build metadata, and test layout;
- machine-readable cases for every emit-able catalog code, every retired
  code's required non-emission, every suppression and ordering rule, and
  every CLI JSON result family;
- the five canonical `skills/<slug>/SKILL.md` artifacts promised by the
  skills annex, with lint, byte digests, independently trusted
  `using-engram` packaging, and fixtures proving that each annex duty is
  represented in the corresponding artifact;
- vendored, provenance-recorded relevant cases from YAML 1.2.2,
  CommonMark 0.31.2, JSON Schema 2020-12, and Unicode 17.0.0;
- focused spikes proving lossless YAML lexical access, Goldmark source
  mapping, exact-number transfer into JSON Schema, custom regexp-engine
  injection, and SHA-1/SHA-256 raw Git object handling; and
- platform capability probes plus a documented observed Git floor.

The initialization fixture asserts a direct unborn
`refs/heads/main` independent of `init.defaultBranch` and Git templates.

Exit gate: each external library either passes the relevant normative
fixtures behind an engram adapter or is replaced/contained before M1.
There is no unresolved normative behavior encoded as a TODO.

#### M1 — Portable snapshot engine

Deliver:

- safe logical-tree walking and expected-kind/pruning precedence;
- normed-text, Unicode 17, YAML/frontmatter, exact-number, path, and
  catalog engines;
- schema scope/resolution, JSON Schema-subset prevalidation, strict `$ref`, custom
  regex, exact date/date-time, JSON Schema execution, and record-body
  rules;
- CommonMark links/images, deterministic malformed-wikilink scanning,
  typed links, catalog comparison, and precise path attribution;
- finding suppression, aggregation, UTF-8 ordering, and E1xx–E4xx/W9xx;
  and
- `engram check` for portable snapshots plus `schema inventory/list/show`.

Exit gate: the complete portable fixture corpus is green on all CI
platforms, repeated runs are byte-identical, `examples/minimal/` passes,
and fuzzing cannot escape/prune-follow or crash the parsers.

#### M2 — Changesets and managed read paths

Deliver:

- deterministic snapshot differences and E501–E504 with
  complete/indeterminate evaluation;
- raw Git discovery, refs, object/tree/index readers, replacement/graft
  suppression, full-lineage auditing, and E601–E603;
- staging-layer models and read-only `status`, `diff`, `log`,
  `check --accepted`, `check --staged`, and explicit snapshot-pair check;
  and
- in-process audit memoization keyed by the exact tip and digested
  normative rule-set identity. Persistent entries, if added later, must
  live in controller-owned external storage with authenticated
  provenance and integrity; `.engram/cache` remains a non-authoritative
  hint and can never replace an audit.

Exit gate: managed fixtures cover root and multi-commit histories,
SHA-1 and SHA-256 repositories, linked worktrees, shallow/missing
objects, partial-clone/promisor repositories with a connection counter
proving local reads never lazy-fetch, merges, invalid historical
transitions, sparse/filtered presentations, malformed index stages, and
raw pruned tree entries. Raw-object fixtures cover missing objects as
capability separately from the complete admitted/rejected raw commit
header grammar (separator, continuation, unknown/repeated bookkeeping
headers, tree/parent placement and OID form) and malformed tree encodings,
duplicate or noncanonically ordered entries, every admitted and rejected
raw mode spelling, empty-tree directory projection, and wrong-type
references as E601 with causal suppression. Boundary fixtures prove
target exclusion for E102, E103, E104, E106, E107, E109, E110, E303,
E308, and E603 with both absent and locally present targets. Merge fixtures
pair E602 with absent, wrong-type, and malformed tree/parent targets to
prove causal suppression without traversal.
Inspection fixtures cover full SHA-1/SHA-256 and literal-`HEAD`
resolution, rejection of abbreviated/general revision syntax and
out-of-lineage objects, and immunity to replacement/graft overlays.
Index fixtures distinguish both admitted accepted-tree regular modes
from the base-preserving/new-`100644` declaration required for a new
managed transaction, including mode-only drift and `check --staged`'s
logical projection.
`log` fixtures also cover canonical count bounds, root completion,
merge-boundary `issues` output, and capability versus repository object
failures without fetching.

### Next — authoring and safe local acceptance

#### M3 — Draft and staging helpers

Deliver `add`, `fmt`, `new`, `mv`, and `schema copy` as worktree-lock-
serialized, journaled all-or-nothing helper operations over the store
working draft, together with the draft-journal subset of `doctor
--recover`. Link and image rewrites will use CommonMark source ranges,
preserve every source byte outside a byte-deterministically serialized
destination token, regenerate affected catalogs, and never stage their
own edits. Deliver local-path `attach`
and `detach` as separately locked atomic replacements of the consumer
project entrypoint. Attachment fixtures cover exact owned-block
serialization, canonical physical path/identity deduplication,
malformed/duplicate markers, concurrent cooperating CLI updates,
pre-publication external-edit detection, and removal of the last store.
URL acquisition remains the separate `clone` operation delivered in M4.

Exit gate: every helper has the applicable dirty-tree, collision,
partial-failure, JSON, Unicode-path, and unrelated-byte preservation
tests; helper/helper races and a synthetic conforming lock holder
rendezvous on the same worktree lock. Fault injection before and after every per-file/index
publication proves exact rollback of mixed preimage/final states,
including shallow-create/deepest-remove directory ordering, blocking on
an unrecorded child or other third value, idempotent `doctor --recover`, and no accepted-
ref movement. Each command that exposes `--dry-run` also has exact no-
write tests for that mode.
Golden helper cases additionally prove literal/non-recursive `fmt` path
selection, all-catalog selection including `catalog: none`, exact
would-change result arrays, exact `new` fields/body input grammar,
canonical relative link/image destinations including reference
definitions and current-directory directory targets, single-line quoted
frontmatter wikilink rewriting, lossless rejection of unsupported scalar
presentations, and invalid selected-schema discovery without partial
descriptors or ancestor fallback,
quoted universal-field bytes, default-title/required-section bytes,
record-only `new`/`mv` paths, existing-parent and same-path move cases,
and rejection of every ambiguous option or input combination.

#### M4 — Hooks, trust, and the acceptance engine

Deliver:

- annex-compatible shared locks and stale-lock diagnostics;
- complete-set external trust keyed by a controller-owned binding of the
  physical common Git directory and the canonically framed digest of the
  exact ordered hook paths/bytes; copied or duplicated local markers
  confer no trust, and explicit `hooks trust` creates or rotates the
  binding without reusing an old grant;
- disposable base/candidate materialization, direct interpreter launch,
  canonical stdin, closed environment, one-pass ordering, resource/time
  controls, post-hook stable capture into never-exposed trees, fresh
  materialization between hooks, and final revalidation from the sealed
  capture;
- temporary-index tree construction, commit creation, ref CAS, draft-safe
  reconciliation, crash recovery, `commit`, `commit --dry-run`, and
  `revert`;
- CLI-owned lock owner records, durable post-CAS recovery metadata, and
  the conservative `doctor --recover` subset needed to prove a dead
  owner, distinguish pre-journal from journal-required phases, take over
  stale locks, and reconcile without losing draft bytes;
- owned POSIX `sh` pre-commit guard management; and
- suppression tests proving that managed Git plumbing never dispatches
  native `reference-transaction`, checkout, or other Git hooks and that
  inherited `GIT_*` overlays cannot redirect refs, objects, index,
  worktree, configuration, or lazy-fetch behavior; and
- `init`, raw-first `clone`, and `hooks list/trust/revoke`. Clone uses a
  private no-checkout acquisition with controlled templates/hooks/
  filters, audits raw objects before materialization, and publishes only
  the verified byte-transparent checkout.

`init` uses an external durable intent: absent targets publish one
complete sibling tree atomically, while existing targets prebuild Git
administration, journal exact absent/final bootstrap paths, and publish
that administration only after every checkout byte is ready. Retry and
`doctor --recover` exercise every pre/post-publication phase without
overwriting a third value or removing an already published accepted
store.
Clone uses the analogous acquisition intent around its final directory
rename and marker/default-URL bindings; retry and doctor distinguish an
absent target from the exact already-published audited identity and
reject every third value.

Exit gate: fault injection at every transaction and journal-state
boundary — including pre-journal, pending before/after CAS, cancelled,
complete before lock release, and cleanup — proves that no pre-CAS
failure moves accepted memory, no retry reuses prepared output, no
reconciliation overwrites unrelated bytes, the guard never executes
store or Git-native hooks, and concurrent processes rendezvous on the
normative lock paths. Background hook descendants retaining old file
handles cannot change later-hook input or validated/committed bytes.
Every launch and post-launch core §8.3 rejection has a golden `hook`
error-kind case, while causal failures before hook launch retain their
closed common-error class.
Recovery fixtures include an old→new→old ref ABA while the journal is
pending, every mixed preimage/final-image reconciliation state, a
conflicting third worktree/index value, interruption after each
individual replacement, and a non-cooperating write in the documented
last-recheck window; only captured preimages may advance and pending-old
always remains blocked.
Helper/commit and helper/revert races also prove cross-milestone
rendezvous on the same worktree lock.
The same gate covers each marker/binding/grant trust boundary and
concurrent `trust`/`revoke`, plus add/delete/modify revert cases for
postimage match, already-satisfied state, conflict, multi-path
all-or-none behavior, both file-to-directory prefix-collision directions,
and `--dry-run`.
Hook-list/trust goldens reject every invalid selected hook-tree or program
without a partial result or registry mutation and do not require the
selected interpreter to be installed.
Init-specific injection covers every private-build, bootstrap, binding,
directory/`.git` rename, completion, and cleanup boundary for absent and
pre-existing targets, including idempotent same-invocation retry and
doctor rollback/finish behavior.
Commit-object goldens use a controlled clock and identity and cover
the exact tree/optional-parent/author/committer-only header sequence,
base-mode preservation, canonical `100644` new files, ignored
materialization permissions, and exact accepted-tree/index equivalence
for ordinary staged, hook-created, and initialization files;
multiline-message final-LF framing,
rejection of invalid identity/message arguments, replayed raw author and
message preservation, duplicate/continued historical headers, timestamp
range failure, and maximal-subpart U+FFFD decoding.
Clone injection covers intent creation, pending external bindings, final
rename, binding activation, cleanup, exact retry, and both sides of
doctor recovery for explicit and default destinations. Network tests
prove system/global/includes and URL rewrites or command-bearing Git
configuration cannot influence the synthesized transport repository.
Default-destination reuse rejects drift in `origin`, upstream, fetch,
guard, cache-exclusion, or presentation state without fetching or
repairing; after exact prerequisites pass, complete-E and indeterminate
reuse audits retain the clone result with `published: true` and `reused:
true`. Doctor fixtures cover a live, stale, and owner-unprovable
target lock both before and after intent creation, including an absent
target and post-rollback no-store result.
Parallel init/clone attempts for one destination prove serialization on
the shared controller-owned target lock without lost registry updates.

### Later — synchronization and release

#### M5 — Linear synchronization

Deliver `pull`, `pull --continue`, `pull --abort`, and `push` with
separate network authorization, incoming full-lineage validation,
fast-forward CAS, annex-ordered multi-ref/worktree locks, exact-OID and
checkout-fingerprint rechecks around every local ref/`HEAD`/index/
worktree mutation, private-branch replay, explicit conflict drafts, and
safe cleanup limited to pull-owned state.
Every mutating pull form uses one durable phased workflow journal that
incorporates replay transaction subrecords, reaches an intentional active
replay draft only after a complete durable checkpoint, and gives doctor an
exact pre-publication rollback or post-publication completion path.
The journal distinguishes `prepared`, private-ref creation, checkout
switch, `replaying`, original-ref publication, final checkout, and
cleanup. It captures exact present or absent preimages for refs,
metadata, `HEAD`, index, and worktree so interrupted `--continue` and
`--abort` restore an active draft rather than deleting it. The M5
`doctor --recover` subset implements the same per-item preimage/final-
image/third-value checks, pending-old ABA block, and restore-before-
publication versus finish-after-publication boundary as the CLI
contract.

Exit gate: local integration tests cover up-to-date, fast-forward,
divergence, every add/modify/delete exact-preimage and already-satisfied
replay case, simultaneous all-path conflict detection with no partial
application or text merge, both file-to-directory prefix-collision
directions, explicit no-op resolution, concurrent remote movement,
nearest-common-ancestor ordering and unrelated histories,
interruption and races at every branch creation, switch, ref
update, index update, checkout, and replay point, missing history,
credential/network denial, and no merge/force/delete path.
Crash fixtures assert exact rollback before original-ref publication,
finalization after it, blocking for pending-old ABA or any third value,
coherent conflict handoff without recovery, and preservation of an
active conflict draft across interrupted continue/abort recovery. Fault
injection covers entry to and exit from every durable phase above,
including an automatic replay candidate rejection handed off as the
exact unprepared staged draft. The same handoff is exercised for hook
trust/preparation errors, with the typed durable error, and `status`
goldens distinguish `reason: "conflict"` from `reason: "rejected"` while
serializing the historical replay base with a null ref. A rejected
continuation preserves the immutable source-draft reason and any
original exact-conflict list.
Plain-pull, continue, and abort fixtures prove their distinct clean/
active-replay preconditions; continuation accepts only an eligible fully
staged resolution, while abort stably captures and explicitly discards
that replay draft without network or hooks and blocks a post-capture
race. Pull and push goldens serialize the selected remote ref only as a
full `refs/heads/<branch>` name; continue and abort retain the exact
remote and ref captured by the active replay even if configuration
changes.
Fast-forward, completed replay, incoming rejection, and abort fixtures
also prove exact accepted-tree/index path/blob/mode equivalence and a
clean logical worktree; only a persisted intentional replay draft may
depart from its private `HEAD` without requiring recovery.
Private-branch switch fixtures prove that the exact incoming modes and
blob IDs are installed and journaled before the first replay transaction.
Validation/audit-shape goldens distinguish the would-be incoming lineage,
the active private lineage, the completed replay lineage, and the
original lineage restored by abort, including differing warning sets;
audits from another lineage evaluated during the operation never leak
into that result.
Push fixtures separately cover equal tips, explicit branch creation,
fast-forward, preflight non-fast-forward rejection with exact zero
counts, a definitive remote compare-and-swap race, lost-response
indeterminate publication, and explicit re-observation without automatic
retry.

#### M6 — Operations, packaging, and v1 release

Complete the remaining advisory/integration checks in `doctor`; deliver
final `version` output and package/publish the M0-verified canonical
skills through skills-only adapters for Claude Code and Codex,
shell completions only if already derivable from the command grammar,
cross-platform archives, checksums, release notes, and operator
documentation.

Exit gate:

- all public commands and global-option combinations have golden text,
  JSON, and exit-status tests;
- `go test ./...`, `go test -race ./...`, `go vet ./...`, parser fuzz
  smoke tests, shell syntax checks, and repository mechanical checks pass;
- CI covers macOS, Linux, and Windows with supported Go/Git combinations;
- dependency licenses and generated-data provenance are present;
- the CLI validates this repository's canonical examples and schemas;
- built-in local repository operations are verified network-silent, and
  hook tests deny network where the host can enforce it; and
- the implementation plan, CLI contract, changelog, and user README
  describe the same shipped surface.

## 4. Test strategy

| Test layer | Required evidence |
|---|---|
| Pure unit | Exact scalar values, path bytes, Unicode keys, regex automata, dates, link resolution, catalogs, changeset ordering |
| Normative fixtures | One non-triggering and one triggering case per emit-able E/W identity; explicit no-emission regressions for retired codes; every documented suppression and cross-path attribution |
| Golden protocol | Complete JSON envelope/result shapes, deterministic arrays, status 0/1/2/3, stable error kinds |
| Git black box | Real SHA-1/SHA-256 repositories, linked worktrees, raw objects, filters, sparse state, index stages, replace refs, grafts, shallow history |
| Transaction fault injection | Process failure before/after every lock, hook, validation, object, ref, index, and worktree boundary |
| Procedural conformance | Positive and negative integration cases for every applicable normative executor, writer, attachment, and synchronizer obligation, asserted as outcomes/error kinds rather than invented check findings |
| Concurrency | Same ref/different worktree, different refs/same worktree, stale locks, CAS races, parallel clones |
| Hook security boundary | Exact selected bytes, trust invalidation, cleared `ENGRAM_*`/`GIT_*`, no Git discovery, base immutability, reserved output rejection, time/resource limits |
| Cross-platform | Filesystem case/Unicode behavior, LF preservation, executable lookup, POSIX guard under Git for Windows, path quoting with spaces and quotes |
| Fuzzing | YAML/frontmatter, raw path bytes, JSON Pointer, regex parser, CommonMark/wikilink boundaries, catalog escaping, Git `-z` record parsers |

Tests will compare structured finding identities before human details.
No fixture will depend on directory enumeration order, locale, timezone,
filesystem normalization, default Git configuration, or network access.

## 5. Main risks and controls

| Risk | Control |
|---|---|
| Libraries accept broader/different standards behavior | Prevalidate the engram subset, inject custom semantics, and gate M0 on pinned official fixtures |
| Go Unicode data drifts from Unicode 17 | Check in source data, hashes, generator, and generated tables; assert table version at build/test time |
| CommonMark AST lacks rewrite source ranges | Maintain a small source-range adapter validated against the same parser; block M1/M3 if lossless rewrite fixtures fail |
| JSON Schema engine rounds or resolves externally | Transfer canonical exact numbers, use `big.Rat` paths, deny external loaders, pre-resolve admitted local refs, and inject regex/formats |
| Git config or attributes transform bytes or race acceptance | Capability-check the effective presentation, fingerprint every consulted source, read accepted state from raw blobs, and reconcile through byte-raw mechanisms that consult no uncaptured mutable input |
| Crash after ref update leaves checkout inconsistent | Treat the commit as accepted, retain recovery metadata/lock, and require safe explicit reconciliation before another write |
| Arbitrary trusted hooks have external effects | Keep trust set-wide and byte-exact, use disposable trees and finite resources, deny network where the host can, and never rely on outside effects |
| Pull conflict cleanup destroys user work | Require a clean start and reserved checkout, use a private replay branch, make abort an explicit discard of one stably captured logical resolution draft, and preserve pruned/non-logical state |
| CLI contract drifts while commands are added | Generate help/schema tests from one command model and require JSON/text golden updates in the same change |

## 6. Work sequencing rule

M0 → M1 → M2 is the critical path. M3 can begin after the M1 parsers
and rewrite primitives are stable. M4 depends on both M2's Git model and
M3's safe file-update primitives. M5 starts only after transaction fault
injection is green. M6 is a release gate, not a place to defer
conformance defects.

Implementation changes will preserve the current specification-first
boundary: any newly discovered normative ambiguity is resolved in the
specification and changelog before code chooses a behavior.
