# Annex — Git-managed stores, v1 (draft)

**Version:** v1
**Status:** Draft
**Revision:** 2026-08-13
**Normative status:** Normative

This annex binds the core standard's portable snapshots and changesets to Git-managed writable
stores. A snapshot remains readable and statically checkable without repository metadata.
Software that accepts a persistent engram write MUST use the managed binding defined here.

The requirements language and BCP 14 interpretation of core §1.2 apply.

## 1. Git terms and conformance

Core terminology applies unchanged. This annex adds only these Git terms:

- **repository worktree** — a non-bare Git working tree and its owning repository metadata;
- **accepted ref** — the non-symbolic local branch named directly by symbolic `HEAD`;
- **accepted commit** — the commit named by the accepted ref; and
- **accepted state** — the logical snapshot represented by the accepted commit's tree.

The core state model has this single Git representation:

| Core concept | Git representation |
|---|---|
| base state | The accepted commit's logical snapshot captured for the attempt; initialization uses an explicitly absent commit and the known empty base |
| working draft | Unaccepted edits in the repository worktree |
| initial candidate | The logical tree declared by the exact, resolved Git index captured for the attempt |
| final candidate | The prepared logical tree submitted to final validation, before acceptance or rejection |
| accepted state | The logical tree of the commit directly named by the accepted ref |
| commit | The Git object recording a final candidate accepted by compare-and-swap of the accepted ref |

A changeset remains the core §8.1 net difference between base and candidate. A transaction remains
the bounded acceptance attempt, not a changeset, editing session, or public handle.

Managed-store conformance covers repository shape, accepted history, and the applicable rules of
this annex. Every accepted snapshot and transition MUST also conform to the core. A tool claim
additionally covers each procedural role—managed writer or synchronizer—that it performs.

## 2. Repository identity and presentation

### 2.1 Root and accepted ref

The managed-store root MUST be exactly the root of its repository worktree; a bare repository is
not a writable checkout. `HEAD` MUST directly name a non-symbolic local `refs/heads/*` branch whose
full refname bytes are valid UTF-8; replacement decoding does not qualify. After initialization,
that branch MUST directly contain a commit object ID. It is the unique accepted ref used for
locking and compare-and-swap. A violation produces E601.

A newly initialized repository MAY have an unborn symbolic `HEAD`; it is not yet accepted state.
Its first accepted transaction MUST create a parentless commit with a conforming snapshot.

### 2.2 Logical projection

Git administration is outside the logical store. The root `.git` entry—a directory in an ordinary
checkout or a regular file in a linked worktree—is pruned under core §2.4. A lower `.git` boundary
produces core E110; Git objects, refs, locks, journals, and other metadata are never logical input.

Every accepted commit's recursive raw-tree projection MUST exactly equal its core snapshot's
explicit directories and regular-file paths and bytes. A raw-tree entry whose Appendix A grammar is
valid but which the core prunes without emitting its own `E` finding—including root `.git`, cache,
unknown tool state, or dot-prefixed content—produces E603. An entry pruned by E103, E104, E107,
E109, E110, E303, E308, or E309 produces that core finding and MUST NOT additionally produce E603.
Hook trees at every logical content directory remain in the projection and are
validated and diffed as ordinary normed configuration; they are not pruned
as unknown state. Routine-declaration trees at every logical content directory
likewise remain in the projection and are validated and diffed as ordinary
normed configuration. Untracked, ignored, index-only, and unstaged worktree
content is not accepted state. Appendix A defines projection.

### 2.3 Writable presentation

A repository worktree used for managed writing MUST present every logical path
completely and byte-transparently:

- sparse checkout and a sparse index are disabled;
- effective `core.autocrlf` is `false`;
- effective `text` is unspecified or unset for every logical path;
- `eol`, `filter`, `ident`, and `working-tree-encoding` are unspecified; and
- filesystem, index, and checkout machinery round-trip every logical pathname and byte without
  case, Unicode, encoding, line-ending, clean/smudge, or other transformation.

A managed check reports a violation as E601; a writer MUST stop before using a presentation it
cannot prove byte-transparent. Completeness concerns topology, configuration, and round-trip
capability, not equality of the live index or worktree with the accepted commit. §5 permits working drafts.

## 3. Accepted history

The initial accepted commit MUST have no parents and every later accepted commit exactly one; a
merge in the accepted lineage produces E602. For each later commit, the sole parent's snapshot is
the base, the commit's snapshot the final candidate, and their core §8.1 difference the changeset.

A complete managed audit MUST inspect the available accepted lineage root-to-tip. Every snapshot
MUST conform; the empty-base/root pair and every later parent/candidate pair MUST produce a
`complete` transition with no `E` finding. Historical hooks are not rerun: their recorded trees are
the evidence. Findings aggregate under core §9.1; Appendix A defines raw parsing and suppression.

Accepted refs, parents, and trees MUST come from raw Git ref and object data; replacement refs,
grafts, and environment overlays MUST NOT alter them. Author, committer, message, timestamp,
signature, and remote topology have no engram semantics; domain data remains in schema fields.

A prior audit MAY replace recomputation only when all of these remain true:

1. it covers the exact accepted tip and exact normative rule-set identity,
   including versions, revisions, and byte digests of the core and every
   applicable normative annex;
2. complete raw ancestry and every object required by Appendix A are proved
   locally available again;
3. current E601 presentation inputs are rechecked; and
4. the result was retained by the current controller process or loaded from
   controller-owned external storage with authenticated provenance and
   integrity.

Store-, repository-, or worktree-writable cache data is only a hint and MUST NOT confer audit
authority. Unavailable required history or objects produce capability failure or `indeterminate`,
never successful conformance. Inspection MUST suppress lazy fetching and MUST NOT fetch without
separate network authorization.

## 4. Managed transactions

A managed writer MUST accept each persistent write through these phases. Appendix B closes locking,
fingerprinting, journaling, compare-and-swap, reconciliation, and recovery.

### 4.1 Capture and audit

1. Resolve the exact symbolic accepted-ref name.
2. Acquire the accepted-ref and worktree rendezvous locks in Appendix B order.
3. Read symbolic `HEAD`, the accepted ref, and the exact index twice. Continue
   only if both complete observations are byte-identical and `HEAD` still
   directly names the resolved ref.
4. Capture that ref value—or its absence for initialization—as the base and the
   observed index as the candidate declaration.
5. Outside initialization, require a complete managed audit of the captured
   lineage with no `E` finding, subject only to §3's reuse rule.

The captured index MUST resolve to one tree: every present path has exactly one stage-zero entry,
with no higher-stage entry or intent-to-add placeholder. Otherwise the attempt stops before a
changeset or hook invocation. A stage-zero symlink, gitlink, or other forbidden kind remains
representable for boundary preflight; it is never followed or treated as a regular file.

### 4.2 Construct, prepare, and validate

1. Materialize the initial candidate away from the live worktree, using the
   captured base and only the paths and bytes declared by the captured index.
2. Run the core §8.1 boundary preflight on base and candidate. Reject any entry
   that would be pruned from an accepted raw tree, then compute the initial
   changeset and freeze its affected hook scopes.
3. Prepare under core §8 with the exact applicable hook set and bytes selected
   from the captured base for those scopes. One designated executor runs that
   set exactly once; no Git or integration hook may prepare the candidate again.
4. From the sealed final candidate, run complete core snapshot and transition
   validation against the captured base. Continue only on a `complete` result
   with no `E` finding.

Preparation MUST NOT run in the live worktree. Under Appendix A, a surviving base regular file
retains its mode, every other regular file uses `100644`, and host or hook-materialization permissions do not
determine accepted modes.

### 4.3 Prove, accept, and reconcile

1. Prove that the final candidate can be reflected exactly in the index and
   worktree without overwriting out-of-candidate draft bytes, and capture every
   exact safety input required by Appendix B.
2. Create a commit whose tree is exactly the final candidate and whose parent
   is the captured base, or no parent for initialization.
3. Durably record a `pending` recovery journal and advance the durable owner
   phase to `journal-required`, as Appendix B defines, before attempting to
   update the ref.
4. Recheck symbolic `HEAD` and every safety input. Compare-and-swap the accepted
   ref only if it still has the captured value, or is still absent for
   initialization.
5. After a successful ref update, reconcile the index and worktree
   idempotently without overwriting unrelated draft bytes. Mark the journal
   `complete` durably, release the locks, and only then remove the journal.

Failure before the ref update leaves accepted state unchanged and imports no
partial hook output. Failure after a successful update does not undo or hide
the accepted commit: it is an operational checkout failure and leaves the
journal and locks in a recovery-required state. Every managed writer MUST stop
on that state.

Warnings do not reject a transaction. Hook rejection, unavailable or untrusted
hooks, capability failure, an `indeterminate` transition, any `E` finding, or a
ref race does reject it. These are operational results, not new check
findings. A retry is a new transaction from the then-current accepted state.

## 5. Working drafts, staging, and Git integration

A dirty worktree is a working draft, and the captured resolved index declares
the initial candidate; neither is accepted state. A consumer MAY inspect
either when explicitly asked but MUST identify it as uncommitted rather than
presenting it as accepted state.

An automated writer MAY assemble a bounded operation in the managed worktree
before acceptance. It MUST be the sole automated editor of that repository
worktree for the operation and MUST stop if it cannot separate its authorized
changes from existing logical edits. It MUST NOT discard, overwrite, stage, or
commit unrelated user changes. Concurrent automated working drafts MUST use distinct
clones or worktrees.

Unstaged and untracked worktree bytes are outside the index-declared initial candidate
and MUST NOT influence preparation, validation, or the resulting commit.
Successful hook output is imported into the index and reflected in the
worktree only through Appendix B reconciliation; the attempt is rejected
before the ref moves if that would overwrite unstaged content.

A Git-shaped commit interface is conforming only when its adapter retains
control through all of §4 and Appendix B. A callback that returns before Git
creates the commit, such as `pre-commit`, cannot by itself be the managed writer
or executor. It MAY reject an unmanaged commit and direct the caller to a
managed writer, but MUST NOT prepare a candidate that Git later accepts outside
the transaction. Such integration is local metadata, not store content.

Every Git-native operation initiated by a managed writer or synchronizer MUST
suppress Git's hook dispatch, including reference-transaction and checkout
hooks. These are not core `prepare-changeset` programs. A writer that already
prepared a candidate MUST create its commit without invoking a second
preparation layer.

## 6. Attachments and repository topology

Core §12 `MEMORY.md` attachment registries discover independent stores; they do
not change which Git repository owns a managed store. One store MAY be attached
to many projects, and one project MAY attach many stores.

Keeping a reusable store outside consumer repositories is RECOMMENDED. If it
is nested below another repository worktree, the outer repository SHOULD
exclude the complete nested worktree. A deliberate submodule MAY pin an inner
store commit, but the inner transaction and outer gitlink update remain
separate commits. An accidentally staged embedded repository is not an
attachment mechanism.

Attachment grants no read, write, commit, hook, network, or synchronization
authority. Removing one MUST NOT delete the store or rewrite its history.

## 7. Synchronizer profile

This section applies only to software that changes accepted refs or integrates
accepted histories. Local write authority never implies fetch, push, remote
creation, credential use, or publication authority. The built-in steps of a
local transaction MUST NOT initiate or depend on network access. A separately
trusted hook remains subject to core §8.5 and does not acquire synchronization
authority.

### 7.1 Incoming history

Fetching objects alone does not change accepted state. Before a fast-forward
accepts an incoming segment, a synchronizer MUST completely audit the
would-be accepted lineage with no `E` finding. The audit covers exact raw-tree
projection and E603 for every commit, complete validation of every snapshot,
the empty-base-to-root transition, and every later parent/candidate transition
in root-to-tip order. A verified prefix MAY be reused only under §3. Any
finding, `indeterminate` result, missing ancestry, or capability failure rejects
the update. Hooks are not rerun; their committed trees are the evidence.

### 7.2 Divergent replay

To integrate divergent clones, the synchronizer MUST begin at a verified
remote tip and find the nearest exact commit-ID ancestor shared by the fully
audited linear local and remote histories. Histories with no common ancestor
conflict and MUST NOT be joined. Local commits after that ancestor
through the captured local tip are replayed oldest first, each as a new managed
transaction; replay MUST NOT create a merge or choose a resolution silently.

For one source changeset, let `O` be the original base path value, `N` the
original candidate value, and `C` the value in the unchanged current replay
base. A value is either absent or the complete regular-file bytes:

| Source operation | Apply source result | Already satisfied | Conflict |
|---|---|---|---|
| addition (`O` absent) | `C` absent → write `N` | `C = N` | Any other `C` |
| modification | `C = O` → write `N` | `C = N` | Any other `C` |
| deletion (`N` absent) | `C = O` → remove | `C` absent | Any other `C` |

Every changed path MUST be evaluated against the same unchanged `C`, then all
non-conflicting results applied simultaneously. The tentative regular-file map
is representable only when no file path is a strict path-component prefix of
another; a violating pair conflicts even when one path was otherwise unchanged
in `C`.

If any value or file/directory conflict exists, the synchronizer MUST apply
nothing and report the deduplicated union of all conflicting paths in UTF-8
byte order. It MUST NOT perform text merging, normalization, marker insertion,
partial application, or file/directory conversion. Otherwise the simultaneous
map is the next initial candidate. A wholly already-satisfied changeset advances
replay bookkeeping without hooks or an empty commit. Conflict resolution needs
a separately authorized complete initial candidate.

### 7.3 Local and remote updates

A synchronizer that changes a local accepted ref, symbolic `HEAD`, index, or
worktree MUST use §4 and Appendix B's locks, stable captures, fingerprints,
native atomic mutations, journals, compare-and-swap, and reconciliation. When
one operation needs several ref locks, it acquires them in ascending order of
the exact full refname bytes, then acquires the worktree lock, and releases in
reverse order after verifying every expected object ID.

After updating the local ref named by `HEAD`, or switching symbolic `HEAD`, the
index MUST become tree-equivalent to the newly accepted raw tree and the
worktree MUST be reconciled before another replay transaction begins or locks
are released. Exact index and path images are journaled before mutation;
out-of-candidate draft bytes are preserved. A synchronizer MUST NOT report
completion while reconciliation remains pending. The only exception is a
separately authorized and identified conflict-resolution candidate left in the
index/worktree against its private accepted base at `HEAD`; that is active
replay state, not completed synchronization.

Switching symbolic `HEAD` between local branches requires locks for both refs
and the worktree, a recheck of the current symbolic name, complete audit of the
target lineage, and unchanged checkout-safety inputs. The target ref MUST still
name its audited object when `HEAD` changes. Remote updates use the remote
service's compare-and-swap or equivalent conditional update; local locks never
control another clone.

## 8. Managed check attribution

Managed findings E601–E603 are defined once in the
[core Appendix B catalog](README.md#e6xx--managed-git-store). They use the
store-root path `.`.

Each audited accepted snapshot emits ordinary core findings at its logical
paths. Historical transition auditing emits applicable E5xx findings for each
parent/candidate pair. Repeated `(code, path)` identities are aggregated under
core §9.1; optional non-normative detail MAY name affected object IDs.

Repository incompleteness, unsupported Git capabilities, trust denial, ref
races, hook failure, stale locks, and network denial are capability or
operational results, not E601–E603.

## Appendix A — Raw Git profile (normative)

### A.1 Commit grammar

Every required object that is present MUST be structurally well-formed under
the repository's declared Git object format. A visited commit is parsed from
raw bytes using this closed grammar:

- content is a non-empty header block, one empty LF-terminated line, then zero
  or more opaque message bytes;
- an initial header line is `<name><SP><value><LF>`, where `name` is one or more
  bytes from 0x21 through 0x7e and `value` contains no NUL, CR, or LF;
- a continuation is `<SP><value><LF>` attached to the immediately preceding
  header; an orphan continuation is malformed; and
- unknown names, repeated unknown headers, and their continuations are opaque
  and admitted.

The first header MUST be one simple `tree` header with no continuation. Zero or
more simple `parent` headers MUST immediately follow it. `tree` or `parent`
elsewhere, a continuation on either, or malformed physical framing produces
E601. Each tree or parent value is exactly the lowercase hexadecimal full
object ID at the repository format's width. Author, committer, encoding,
signature, other headers, and message bytes are optional bookkeeping.

The parent sequence is trustworthy only when every physical line parses, the
tree/parent placement is valid, and every parent value is canonical. A
canonical tree value on a zero- or one-parent commit may fail independently
without invalidating that parent sequence. Except at the E602 boundary in
§A.3, each present tree or traversed parent target MUST have the required object
type; an absent required target follows §A.3's capability rule.

### A.2 Tree grammar and projection

A raw tree is zero or more concatenated
`<mode><SP><name><NUL><object-id>` entries. The object ID has the repository
format's exact raw width. `name` is a non-empty single-component byte string
containing neither `/` nor NUL. The admitted modes are:

| Raw mode | Projection and target |
|---|---|
| `40000` | One explicit directory; if traversal remains required, its target is a tree. An empty directory remains projected and normally produces core E101 because it lacks `README.md`. |
| `100644`, `100755` | One regular file; if content remains required, its bytes come from a blob. |
| `120000` | One symbolic link, producing core E103. Its correctly sized object ID is opaque and its target is not resolved. |
| `160000` | One gitlink, producing core E104. Its object ID is opaque; the target need not exist and is never inspected. |

`040000` is a pretty-printer spelling, not an admitted raw mode. Any other mode,
missing delimiter, invalid object-ID width, or duplicate name makes the tree
malformed.

Entries MUST be in canonical tree order. Compare names bytewise to the first
difference. If one ends at the shared prefix, its virtual next byte is `/` for
`40000` and NUL otherwise; the lesser byte sorts first. An order violation is
malformed and MUST NOT be silently repaired by sorting.

Raw entry grammar is evaluated before core boundary precedence. For a valid
entry, every core rule decidable from accumulated path, name, and projected
mode is applied before resolving its target. If the rule prunes the entry or
decides the finding without content or descendants, the target is not required
or resolved. This covers E103, E104, E106, E107, E109, E110, closed E303/E308/E309
boundaries, and E603 for every grammatically valid raw entry that the core
prunes without its own `E` finding, including root `.git`, cache,
dot-prefixed, or unknown tool-state entries.

Only a traversable `40000` entry requires a tree. Only a logical-input
`100644` or `100755` entry requires a blob. A zero- or one-parent commit
requires only the parents that ancestry traversal reaches.

### A.3 Causal outcomes and availability

Checks use every independently reliable component and never guess, repair,
choose a parent, or derive a finding from an unavailable projection:

| Condition | Required outcome |
|---|---|
| Well-formed commit has more than one parent | Emit E602 and stop at that boundary before resolving its tree or any parent target. Do not choose a first parent, inspect either parent DAG, validate the merge snapshot, or evaluate its E5xx transition. Continue evaluating unambiguous adjacent pairs already visited tip-to-boundary. |
| Present commit is malformed and its parent sequence is untrustworthy | Emit E601 and stop ancestry traversal at that boundary; suppress dependent snapshot and transition findings. |
| Parent sequence is trustworthy but a required present tree, blob, or parent has wrong type or is malformed | Emit E601. Continue any independently trustworthy parent traversal, but suppress the affected snapshot and transitions. |
| A grammatically valid boundary already decides/prunes an entry | Do not resolve its target; target absence or type cannot add a finding. |
| A still-required tree, blob, or parent is absent | Report capability failure, not E601; suppress every result requiring it. Do not fetch implicitly. |

E601 and E602 causal truncation return a complete managed result containing
that decisive finding rather than an `indeterminate` dependent transition. No
finding is inferred from unvisited or unavailable state. A shallow or otherwise
incomplete repository may expose its current snapshot but cannot support a
complete managed-history claim without all required ancestry.

### A.4 Candidate and accepted modes

For newly authored accepted trees, regular-file modes are determined from the
captured base:

| Final-candidate path | Required raw mode |
|---|---|
| Regular file that was a regular file in the base | Preserve the base entry's exact `100644` or `100755` |
| Every other regular file | `100644` |
| Directory | `40000` |

The initial-candidate stage-zero entry MUST already use the base mode for a
surviving base regular file and `100644` for every other regular file. Any
other admitted regular mode is an operational index conflict before changeset
construction. Host and disposable-tree permission bits are ignored. Existing
accepted raw trees and fast-forwarded history continue to admit both regular
modes under §A.2.

The reconciled index MUST be tree-equivalent to the accepted raw tree: exactly
one stage-zero entry for each regular-file path, with identical blob ID and
mode, and no directory, higher-stage, intent-to-add, or other entry. Host stat
cache fields MAY vary, but the journal records the exact raw index image chosen
for the operation.

## Appendix B — Atomicity and recovery profile (normative)

### B.1 Rendezvous and native locks

Conforming writers and synchronizers use these local rendezvous locks:

| Lock | Path | Scope |
|---|---|---|
| accepted ref | `<common-git-dir>/engram/locks/refs/<digest>.lock`, where `digest` is lowercase hexadecimal SHA-256 of the exact full refname bytes | All worktrees sharing that ref |
| worktree | `<worktree-git-dir>/engram/locks/worktree.lock` | One repository worktree |

A controller MUST atomically create the ref lock and then the worktree lock,
stopping if either exists. It MUST hold both through cleanup or successful
reconciliation and release them in reverse order. Linked worktrees share the
common Git directory but have distinct worktree Git directories. See §7.3 for multi-ref ordering.

Controller-private owner metadata MUST durably distinguish `pre-journal` from
`journal-required`. Compare-and-swap MUST NOT occur until both the `pending`
journal and `journal-required` phase are durable. A stale pre-journal lock with
no journal may be removed only after recognized owner metadata proves that no
writer remains and that phase. A missing or malformed journal once one is
required is an unresolved recovery conflict, never evidence that no update
occurred.

Git's native object, ref, `HEAD`, and index locks remain additional short-lived
mechanisms. Each actual ref, `HEAD`, or index mutation MUST use Git's native
atomic protocol and stop on conflict. Portable implementations cannot hold
those locks through preparation, so candidate construction uses a disposable
index. Ordinary Git clients need not honor engram locks; exact live inputs are
therefore rechecked immediately before the accepted-ref update.

### B.2 Safety capture and durable journal

Safety fingerprints MUST cover:

- existence, kind, and exact bytes of every worktree path reconciliation could
  create, replace, or delete, including required ancestor boundaries;
- the exact value of every affected index entry; and
- every configuration, attribute, environment, and presentation input used by
  the byte-transparency proof, including each source's identity and exact bytes
  or exact absence.

Fingerprints compare equal only when all observations are unchanged. A
timestamp, a resolved value detached from its mutable source, or other lossy
metadata is insufficient. Reconciliation MUST use captured, still-equal inputs
or a raw mechanism that consults no mutable presentation input.

Before compare-and-swap, the controller MUST durably write a `pending` journal
in the worktree Git administration area. It records the accepted ref, expected
old and new object IDs, safety fingerprints, the complete raw index preimage
and final image, and the exact absent-or-present kind/byte preimage and final
image of every affected worktree path and ancestor boundary. Inability to prove
that the journal survives process or host failure stops the attempt.

The journal path and serialization are implementation-private. The common
interoperability signal is the annex-defined lock retained while recovery is
needed. Only a controller that recognizes the journal format may recover or
remove it; foreign, unknown, or malformed state is never guessed through.

### B.3 Compare-and-swap and reconciliation

Immediately before acceptance, recheck that `HEAD` still directly names the
captured accepted ref and every safety fingerprint is unchanged. The accepted
ref update MUST atomically compare the current value with the captured base ID,
or with absence for initialization, and install only the new commit. Any other
value or native conflict rejects the attempt. A transient non-cooperating
old→other→old change before that instant is not observable history; conforming
writers serialize through the rendezvous lock.

Reconciliation, including recovery, is idempotent over one journal and stable
observation. Every planned index or path item MUST equal its exact preimage or
final image. A final image is already satisfied; a preimage may advance only
after its dependencies are rechecked. Any third value blocks all further
reconciliation and is never overwritten. A crash may therefore leave a mix of
preimages and final images, and retry applies only remaining preimages.

After reconciliation, the controller MUST durably mark the journal `complete`,
release the locks, and only then remove the journal. Unreachable objects from a
rejected attempt MAY later be garbage-collected, but the pre-existing working
draft and staged declaration MUST NOT be reset or discarded.

These guarantees cover conforming lock users and non-cooperating changes
observable before the last exact recheck. Portable filesystems have no common
content-CAS primitive for arbitrary paths; a write in the final
recheck/atomic-replacement window remains outside the guarantee. A tool MUST
NOT claim to exclude that race unless the host atomically conditions mutation
on the captured identity and bytes.

### B.4 Failure and explicit recovery

Before a journal exists, rejection discards only transaction-owned disposable
state and releases locks. Once `pending` is durable:

- if the final recheck fails or compare-and-swap definitively reports no
  update, durably mark the journal `cancelled` before cleanup; remove the
  journal only after transaction-owned state and locks are cleaned;
- if the update outcome is unknown, leave the journal `pending`; never relabel
  it from an assumption about the ref; and
- if the ref update succeeded but reconciliation failed, accepted state remains
  the new commit and the `pending` journal and locks remain recovery-required.

A cleanup failure leaves its journal and locks. Explicit recovery first proves
the owner no longer controls the attempt, takes ownership of the locks, and
then follows this table:

| Journal/ref observation | Recovery action |
|---|---|
| `cancelled` | It proves this attempt did not update the ref. Remove only transaction-owned temporary state, release locks, then remove the journal, regardless of the current ref. |
| `pending`, ref equals new ID | Apply §B.3 reconciliation; mark `complete`, release locks, then remove the journal. |
| `pending`, ref equals old ID or is absent as the old initialization value | Remain blocked: old→new→old is possible, so do not relabel, clean, or change live bytes. |
| `pending`, any other ref value | Remain blocked on concurrent-history conflict. |
| `complete` | Reconciliation is done; release any proven-stale locks and remove the journal. |
| Missing required, malformed, unknown, or foreign journal/lock | Leave it untouched and remain blocked for explicit resolution. |

Recovery never guesses whether compare-and-swap occurred and never claims
cross-implementation journal recovery. Every case not explicitly safe above
remains blocked.
