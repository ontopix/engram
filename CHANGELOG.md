# Changelog

## Unreleased

- Writable engram memory is now Git-managed while snapshots remain
  portable. Core distinguishes snapshots, managed stores, working
  drafts, staged candidates, internal acceptance transactions,
  changesets, and commits; a persistent agent write ends as one
  validated local commit. A working draft becomes the initial candidate
  only when its logical tree is selected for evaluation; hook
  output produces the final candidate. The new normative Git annex fixes
  store/worktree root ownership, accepted state at symbolic `HEAD`,
  linear single-parent history, index-backed initial candidates,
  disposable preparation, one automated editor per checkout, exclusive
  acceptance locking with shared ref/worktree rendezvous paths,
  stable double capture, checkout-safety fingerprints, observable
  compare-and-swap ref updates, a write-ahead recovery journal with
  `pending`/`cancelled`/`complete` states, explicit stale-owner recovery,
  conservative pending-old ABA handling, idempotent per-item
  preimage/final-image reconciliation, and the portable final-window
  limit for non-cooperating filesystem writers,
  dirty-state handling, attachment semantics, concurrency, and separate
  network authority. Managed
  worktrees must be complete byte-transparent presentations without
  sparse checkout or Git content transformations. Every accepted raw
  tree is exactly its logical snapshot, every snapshot and transition in
  the full accepted lineage is audited, raw replacement/graft overlays
  are ignored, and unresolved/intent-to-add indexes are rejected before
  a changeset. Reusable audit results require controller-authenticated
  provenance; store-writable cache files are never audit authority. Root
  Git metadata is pruned from the logical tree, nested
  `.git` entries produce E110, and managed repository violations use
  E601–E603. Accepted refnames must be reversible UTF-8; local object
  reads may not trigger promisor/lazy fetches, and managed Git operations
  suppress Git-native hook dispatch so `prepare-changeset` remains the
  only preparation phase. Present malformed raw commits/trees and
  wrong-type references now produce E601 with causal truncation. Raw
  commit header framing, opaque bookkeeping headers/message bytes, and
  exact tree/parent placement and object-ID grammar are closed. Raw
  tree mode spellings, mode/type projections, canonical entry order,
  explicit empty directories, and target-free core boundary precedence
  are closed; newly authored managed trees preserve each surviving base
  regular mode and use `100644` for every new file, independent of host
  or hook-materialization permissions, and the reconciled stage-zero
  index exactly matches the accepted tree's paths, blobs, and modes.
  Missing objects still needed after that causal boundary
  remain capability failures; E602 merge boundaries do not resolve tree
  or parent targets. Divergent replay and
  revert use simultaneous exact per-path preimages, reject both
  directions of file/directory-prefix collision, and never use a
  version-dependent text merge or partial application. Projects attach
  independent memories rather than transferring repository ownership;
  one memory may serve several projects.
- A draft non-normative reference CLI contract now defines discovery,
  output and exit statuses; managed `init`, `clone`, `attach`/`detach`,
  state and history inspection, non-rewriting `revert`, deterministic
  working-draft helpers, staged selection with `add`, schema inventory
  copy, staged changeset inspection through `status`/`diff`, validation
  through `check`, atomic preparation and acceptance through local
  `commit`, explicit repository `pull`/`push`, external hook trust,
  automatic local Git integration. Canonical skills remain ordinary
  filesystem artifacts distributed by adapters rather than a CLI sync
  surface. The CLI has
  no MCP or other memory-serving surface; agents interact with stores
  through the filesystem. Its JSON v1 protocol now closes the common
  envelope, error kinds, command result objects, ordering, and exit
  statuses. Draft helpers serialize on the managed worktree lock and use
  exact recoverable preimage/final-image journals; initialization uses a
  separate durable intent with atomic absent-target publication and
  idempotent existing-target recovery; clone has the same exact
  pending/published recovery around directory and URL/marker bindings.
  Mutating pull operations use one phased durable journal, preserve
  present replay-state preimages across interrupted continue/abort,
  block pending-old ABA, and hand a rejected automatic replay back as
  its exact unprepared staged candidate. Completed local synchronization
  reconciles the raw index and logical checkout exactly to the accepted
  tip; only an identified active replay draft may differ without recovery.
  `doctor PATH` can recover an
  interrupted init or clone even before the store root exists.
  Remaining command grammars now close catalog selection, record input
  and byte generation, diff flag combinations, log boundaries/counts,
  detach result paths, clone-reuse configuration drift, active-replay
  reason/base serialization, deterministic raw tree modes and commit-
  header order, and no-store/live-lock doctor results.
  Network Git runs from isolated synthesized configuration without URL
  rewrites or store/user command injection. `changeset` remains the net difference, the
  acceptance transaction is internal and one-shot, `commit` is the
  accepted result, and `--project` on attach is only an optional override
  because the current project is discovered by default. `attach` takes
  an already local store and owns one byte-exact, path-sorted adoption
  block; network acquisition remains the separate `clone` operation.
- The Go reference implementation now has a phased, gate-based plan:
  conformance corpus and dependency proofs first, then the portable
  checker and managed read paths, safe authoring and acceptance, linear
  synchronization, and cross-platform release hardening. Heavy work
  remains in Go; local Git installs only a minimal POSIX `sh` guard that
  rejects raw commits and delegates diagnostics to the CLI. The parser
  baseline uses the YAML organization's maintained
  `go.yaml.in/yaml/v3` module rather than the archived import path.
- `$id` and `$anchor` are forbidden in v1 schemas. Engram schemas are
  self-contained and use only structural local references, so URI base
  mutation and named-fragment resolution add ambiguity without serving
  a current use case.
- Asserted `date` and `date-time` formats now use exact portable forms:
  real Gregorian dates in years 0001–9999, uppercase and
  offset-qualified timestamps, and no leap seconds. Format validation
  remains representational and does not normalize or compare instants.
- YAML numbers and JSON Schema numeric validation now use exact
  mathematical values: implementations may not silently round or
  overflow them, numeric assertions use exact arithmetic, and
  `type: integer` is determined by value rather than scalar spelling.
- JSON Schema regular expressions are now portable by construction:
  `pattern` is restricted to a precisely defined ASCII-syntax subset,
  while the unused and less portable `patternProperties` keyword is
  forbidden in v1. The curated project-name pattern remains
  conforming.
- The JSON Schema profile no longer silently accepts arbitrary unknown
  keywords: unknown or malformed keywords produce E303, while explicit
  `x-<vendor>-*` annotations remain valid, have no validation effect,
  and produce one W904 per schema file. `x-engram-*` stays reserved,
  `$schema` is pinned to the draft 2020-12 URI, and an implementation
  missing an admitted keyword must report a tool capability failure
  rather than misclassify the store. Schema-file frontmatter,
  `policy`, and `x-engram-link` are closed grammars whose unknown keys
  also produce E303; `required-sections` values and heading matching
  now have an exact source-text grammar. Schema directories and expected
  kinds are closed, record type lookup accepts only an opaque type slug,
  local `$ref` pointers resolve only below `$defs` to schema-valued
  positions, and link declarations use a direct `properties`/`items`
  path with no applicator ambiguity. E305 and the reserved `engram-`
  property namespace now have finite syntactic checks rather than schema
  implication guesses.
- Schema lookup now distinguishes an absent local schema directory from
  an uninspectable `.engram` boundary, never falls through a broken
  nearer candidate, and retains component-level evaluation for checks
  whose required schema data remains available. E204 and E206 now state
  explicitly that non-string descriptions are invalid.
- The canonical write skill now uses a schema template when present and
  otherwise constructs from declared fields and prose, matching the
  core rule that `## Template` is recommended rather than required.
- README catalog regions are now the explicit materialized exception to
  the general derived-state location rule; their §5.2 source inputs
  remain authoritative and E405 identifies stale generated bytes.
- The normative skills annex now explicitly inherits the core BCP 14
  requirements interpretation and marks the independent trust of the
  loaded `using-engram` bootstrap as a MUST.
- Check findings now have the deterministic normative identity
  `(code, path)`, with at most one finding per pair and exact UTF-8
  path/ASCII code ordering. Cross-artifact path attribution is defined;
  multiple same-code violations at one path are aggregated. `detail`
  is explicitly optional and non-normative, including E301's
  recommended sorted JSON Pointer diagnostics.
- Finding suppression now follows evaluability: causal encoding,
  parsing, or component errors suppress only findings whose required
  inputs are unavailable, while independent checks continue. E5xx
  evaluation with unavailable base/candidate inputs is explicitly
  indeterminate and cannot be reported as a successful transition.
- Check codes remain mutable until the first release. From that release
  onward, published codes are append-only, keep their meanings, and are
  never reused.
- D4 now distinguishes semantic context loading from mechanical file
  processing. Agents must load store content lazily through maps, while
  validators, grep-class search, and index builders may scan the whole
  logical tree without injecting unrelated content into model context.
  P1, P3, the canonical skill, and the README skeleton now use that
  boundary explicitly.
- Agent trust now has an explicit boundary. Core Protocol P0 states
  that store content cannot expand authority and that records/assets
  are data, while the canonical `using-engram` skill owns the complete
  operating procedure: independent trust bootstrap, read-only default,
  control/data separation, resistance to instruction-like content, and
  the separate hook-execution boundary. Adoption locates stores but no
  longer implies trust; pinned records are explicitly contextual data.
- The hook model is reduced to one runtime-neutral
  `prepare-changeset` phase. Stores may carry ordered, shebang-selected
  scripts that receive base/candidate snapshots and the accumulated
  changeset over a versioned process protocol, may modify the candidate
  store, and may reject by exit status. Hooks are root-only, selection
  uses the base state, duplicate two-digit ordering bands are allowed,
  and complete filenames determine ASCII byte order. One executor owns
  each preparation attempt and candidate; isolated worktrees may prepare
  concurrently, but integration layers do not rerun the same attempt.
  Idempotence, containment, and avoidance of external effects are
  operational recommendations rather than statically provable script
  properties. Executors use disposable materializations, reject
  observable base or reserved-state mutations, and always run complete
  validation; after each hook they seal a stably rechecked private
  capture and give the next hook a fresh materialization, so retained
  background handles cannot alter later input or accepted bytes. Exact
  hook bytes require local trust.
  Changesets are normalized as net logical-file differences in UTF-8
  path order, giving every executor the same hook input.
  Executors clear inherited `ENGRAM_*` and `GIT_*` variables under
  ASCII case-insensitive name comparison and keep
  disposable trees outside Git worktrees. Only the managed engine owns
  preparation and acceptance; the local Git `pre-commit` integration is
  a minimal guard that rejects raw commits instead of returning control
  to Git after preparing a candidate.
- Reserved-tree traversal is now explicit: dot-prefixed entries are
  pruned, as are unknown direct `.engram/` children and cache descendants.
  Expected kinds, direct-name-before-kind ordering, E103 precedence, and
  E109/E110 pruning/suppression are exact. The changeset and hook
  preflights reject unrepresentable or pruned error boundaries before
  serializing hook input, while avoiding scans of repository/tool state.
- Deterministic format closure: normed YAML now uses the YAML 1.2 Core
  Schema from pinned YAML 1.2.2 over a JSON-compatible value model and
  rejects multiple documents, directives, explicit tags, merge keys,
  and non-finite numbers. Normed text is UTF-8 without BOM, LF-only, and
  LF-terminated. CommonMark is pinned to 0.31.2 and frontmatter
  delimiters have an exact byte grammar shared by records, maps, and
  schema files.
- Content-directory names now obey the same safe-path rules as record
  and asset names, with additional markdown/URI-breaking characters
  forbidden. Logical names are valid UTF-8 Unicode scalar strings in
  NFC; Unicode 17.0.0 full default case folding defines E106 without
  locale or host-filesystem dependence. Catalog regeneration now has an
  exact empty/non-empty byte layout, forbids markers under
  `catalog: none`, and applies a specified CommonMark-safe text escape
  to decoded names and descriptions. W901's ASCII slug/extension form is
  now exact and remains advisory rather than a new Unicode restriction.
- Conformance closure: E209 now covers missing or invalid README
  frontmatter; E105 explicitly covers a missing root manifest; the
  `note` baseline has a deterministic syntactic normal form for E307;
  schema versions cannot decrease and validation/policy changes require
  a strict increase under E504; and E109 covers root-only configuration
  directories. Parse failures now suppress dependent findings while
  leaving independent structural checks intact.
- Profiles, manifests, hashes, E309, and E310 have been removed from v1.
  Root schemas are visible store-wide, nested schemas are visible only
  in their lexical subtree, and shadowing remains forbidden. Only the
  root `note` baseline is mandatory. The former base-profile files now
  form a non-normative curated schema inventory under `schemas/`; copied
  files become ordinary local schemas.
- W902 is retired because portable check has no record-age input;
  age-based orphan detection is explicitly doctor-class and may use
  environment history.
- Changeset validation now explicitly receives base and candidate
  store states. Existing-record policies are resolved against the base
  state, making `immutable` (E501) and `append-only` (E502) enforceable
  even across deletion or type/schema changes. Managed Git maps them to
  the accepted parent commit and disposable final candidate; the
  standard staged binding uses `HEAD` (or no base at initialization)
  and the Git index.
- Determinism pass over core v1 after first review: catalog sort order
  fixed to normalized-name UTF-8 byte order; link extraction ignores
  fenced/inline code; wikilink and local markdown path resolution,
  URI classification, dot-segment handling, and E403/E401/E404/E402
  precedence are exact; a wikilink final segment may itself end in
  `.md`, with the mandatory suffix still appended; frontmatter
  wikilinks are exact-value only;
  CommonMark source text defines ATX matching for `required-sections`;
  character counts are Unicode code points; BOM is forbidden (E108
  widened); space, parentheses, and C0 controls are forbidden in
  filenames (§2.6).
- Portable regex syntax now enumerates every literal, escape, class,
  range, anchor, and quantifier rule and defines substring matching over
  Unicode scalar values, removing host-regex recovery differences.
- Wikilink extraction now scans eligible CommonMark source ranges
  left-to-right with a non-overlapping malformed-bracket recovery rule.
  E503 is a transition invariant over removed record paths and surviving
  required links, without inferring rename intent from the net diff.
- Check catalog: E208 applies only to top-level `pinned` in record
  frontmatter; E308 covers invalid hook layout, filename, or interpreter
  line while allowing repeated ordering prefixes; E206 mirrors E204.
- §5.1 README body obligations downgraded MUST → SHOULD (uncheckable
  MUSTs are vacuous under §1.4's conformance definition).
- `pinned`: generated catalogs mark pinned records (`(pinned)` between
  link and dash); the co-reading discipline (read pinned records of a
  directory and its ancestors alongside its maps) is normed in the
  skills annex (engram-find, engram-write).
- Skills annex: new `using-engram` orientation skill (panorama, store
  location, entry through the map, operation→skill routing, red
  flags); the "four skills, no more" cap dropped — the set is open,
  disciplines earn skills by recurrence (type-authoring noted as a
  future split of engram-evolve); adapters annex updated to match.
- Initial draft of the engram standard: core specification v1 (draft),
  skills annex (draft), adapters annex (non-normative, draft), rationale,
  curated schemas, and minimal example store.
