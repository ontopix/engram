# Annex — Git-managed stores, v1 (draft)

**Version:** v1
**Status:** Draft
**Revision:** 2026-08-06
**Normative status:** Normative

This annex binds the core standard's state and changeset model to Git.
The core defines portable **snapshots**: logical store trees that can be
exported, read, and checked without repository metadata. This annex
defines a **managed store**: the writable form of an engram store.

A conforming snapshot does not require Git metadata. A consumer MAY
read or statically validate such a snapshot. An agent or tool that
accepts a persistent store mutation as an engram write MUST operate on
a managed store under this annex. An exported snapshot is therefore
portable but read-only until it is initialized or checked out as a
managed store.

The requirements language and BCP 14 interpretation of core §1.2 apply
to this annex.

---

## 1. Terms and conformance

- **repository worktree** — a non-bare Git working tree together with
  the repository metadata that owns it;
- **accepted ref** — the non-symbolic local branch named directly by
  symbolic `HEAD`;
- **accepted commit** — the commit named by the accepted ref;
- **accepted state** — the logical engram snapshot represented by the
  accepted commit's tree;
- **working draft** — the core working draft as represented in the
  repository worktree;
- **initial candidate** — the logical tree selected for a proposed
  commit, represented by the Git index in the standard worktree flow;
- **transaction** — one bounded attempt that takes an initial candidate
  and either rejects it or accepts its prepared final candidate in place
  of the accepted state;
- **commit** — the Git object that records an accepted final candidate;
- **attachment** — a project-to-store discovery relationship; it does
  not change repository ownership or establish authority.

The core definition remains exact: a **changeset** is the deterministic
net difference between base and candidate snapshots. A working draft
may exist before the transaction; selecting its logical tree declares
the initial candidate. The transaction materializes and prepares that
state into the final candidate, evaluates it, and either rejects it or
records it as a commit. These terms MUST NOT be treated as synonyms.

Managed-store conformance is independent from core snapshot
conformance. A managed-store claim covers the repository shape and
accepted-history rules in this annex; every accepted snapshot and
transition covered by that history MUST also conform under §3. A tool or
agent claim additionally covers the procedural obligations assigned
below.

---

## 2. Repository and store roots

The root of a managed store MUST be exactly the root of its Git
repository worktree. A managed store MUST NOT use a bare repository as
its writable checkout. The direct symbolic target of `HEAD` MUST be a
local `refs/heads/*` branch. That target ref MUST NOT itself be symbolic;
its exact full-refname bytes MUST decode as valid UTF-8 to Unicode scalar
values without replacement;
after initialization it MUST directly contain the object ID of an
accepted commit. This direct target is the unique accepted ref used for
locking and compare-and-swap. A violation produces E601.

Before its initial commit, a newly initialized repository MAY have an
unborn symbolic `HEAD`. It is an initialization candidate, not yet an
accepted managed state. Its first accepted transaction MUST create a
root commit whose tree is a conforming snapshot.

Git administration is outside the logical store. The worktree-root
`.git` entry — a directory in an ordinary checkout or a regular file in
a linked worktree — is reserved and pruned under core §2.4. A `.git`
entry encountered at a traversed boundary below the store root is
invalid structure (core E110). Bare repository storage, object
databases, locks, refs, and other Git metadata are never records, assets,
hook input, catalog input, or check paths.

The recursive raw-tree projection of every accepted commit MUST contain
exactly the explicit logical directories and the regular logical-file
paths and bytes of its core snapshot.
It MUST NOT contain an entry that the core boundary would prune,
including cache, unknown tool state, or dot-prefixed content (E603). Git
administration and other local state therefore remain untracked or in
the repository's administrative directories; they do not ride in
accepted history.
Untracked, ignored, index-only, and unstaged worktree content is not
accepted state.

A repository worktree used for managed writing MUST be a complete,
byte-transparent presentation of those paths. Sparse checkout and a
sparse index MUST be disabled. The effective `core.autocrlf` value MUST
resolve to `false`. For every
logical path, the effective `text` attribute MUST be unspecified or
unset, and `eol`, `filter`, `ident`, and `working-tree-encoding` MUST be
unspecified. The host filesystem, index, and checkout machinery MUST be
able to round-trip every logical pathname and file byte without case,
Unicode, line-ending, encoding, clean/smudge, or other transformation.
A managed check reports a violation as E601; a writer MUST stop before
using a presentation it cannot prove byte-transparent.

Here, **complete presentation** describes checkout topology,
configuration, and round-trip capability; it does not require the
current index or worktree bytes to equal the accepted commit. Ordinary
staged, unstaged, untracked, and deleted working-draft differences are
permitted by §5 and do not produce E601.

---

## 3. Accepted history

The initial accepted commit MUST have no parents. Every later accepted
commit MUST have exactly one parent. Merge commits are forbidden in the
accepted lineage (E602), so every accepted transition has one
unambiguous base.

For a non-initial accepted commit:

- the base state is its sole parent's logical store snapshot;
- the final candidate state is the commit's logical store snapshot; and
- the changeset is the core §8.1 net difference between those states.

Every commit in the accepted lineage, from the root through the current
tip, MUST contain a conforming core snapshot. The explicit empty base and
initial snapshot, and every later parent/candidate pair, MUST produce a
`complete` core transition result with no `E` finding. Historical hooks
are not rerun: conformance audits their observable candidate trees and
the transitions those trees record. When the history is structurally
linear, a managed check MUST inspect the complete available accepted
lineage in root-to-tip order and aggregate snapshot and E5xx findings
under core §9.1. If a required object or E5xx input is unavailable, the
result is a capability failure or `indeterminate`, as applicable, never
a successful managed-conformance result.

History discovery starts at the accepted tip and follows the sole parent
of each commit. The raw snapshot of every visited zero- or one-parent
commit is checked. On the first commit whose well-formed raw header has
more than one parent, check emits E602 and stops at that boundary before
resolving its tree or any parent target: target absence, type, or
malformation is causally suppressed. It does not choose a first parent,
traverse either parent DAG, validate the merge snapshot, or evaluate an
E5xx transition for the merge itself. It still
evaluates each unambiguous adjacent pair wholly within the visited
tip-to-boundary segment. This causal truncation produces a complete
managed result containing E602 rather than an indeterminate E5xx result;
the forbidden merge already decides managed nonconformance, and no
finding is inferred from the unvisited parent histories.

Commit author, committer, message, timestamp, signature, object format,
and remote topology are Git bookkeeping rather than store content. This
annex imposes no semantic meaning on them. Domain dates and provenance
remain explicit schema fields where a type needs them.

Accepted refs, commit parents, and trees MUST be interpreted from their
raw Git ref and object data. Replacement refs, graft mechanisms, or
environment overlays MUST NOT alter the lineage or snapshot seen by a
managed check or transaction.

Every required object that is present MUST be structurally well-formed
under the repository's declared Git object format. A visited commit MUST
use this closed raw grammar. Its content is a non-empty header block, one
empty LF-terminated line, then zero or more opaque message bytes. Every
initial header line is `<name><SP><value><LF>`; `name` is one or more
visible ASCII bytes from 0x21 through 0x7e, and `value` contains no NUL,
CR, or LF. A continuation line is `<SP><value><LF>` and belongs to the
immediately preceding header; an orphan continuation is malformed.
Unknown header names, repeated unknown headers, and continuations on
unknown headers are admitted and opaque.

The first header MUST be exactly one simple `tree` header with no
continuation. Zero or more simple `parent` headers MUST immediately
follow it; `tree` or `parent` anywhere else, or a continuation on either,
is malformed. Each such value is exactly the lowercase hexadecimal full
object ID at the width declared by the repository object format, with no
other byte. `author`, `committer`, `encoding`, signature, and every other
header are optional bookkeeping for this annex and do not affect
lineage or snapshot semantics. The message is every byte after the
separator and may be empty. A missing separator, malformed physical
header line, or byte after an object ID on a semantic header produces
E601.

This complete framing makes the parent sequence trustworthy exactly
when all physical header lines parse, the first/tree and contiguous-
parent placement rules hold, and every parent value has canonical form.
A canonical tree value on a zero- or one-parent commit that names a
missing, malformed, or wrong-type tree suppresses that snapshot but does
not invalidate the already trustworthy parent sequence. Except at the
E602 boundary above, every present `tree` or traversed `parent` target
MUST have its referenced object type; an absent required target remains
the capability case defined below.

For this annex, a raw tree object is zero or more concatenated entries
of `<mode><SP><name><NUL><object-id>`. The object ID has exactly the raw
byte length declared by the repository object format. `name` is a
non-empty single-component byte string: it contains neither `/` nor
NUL. `mode` is exactly one of these ASCII byte strings, with these
projections and required target types:

- `40000` projects one explicit real directory and, when core boundary
  precedence still requires traversal, names a tree object. The
  directory remains in the logical projection even when that tree is
  empty, so normal core checking produces E101 when it lacks
  `README.md`.
- `100644` and `100755` project one regular file whose exact bytes come
  from a blob object when core boundary precedence still requires its
  content.
- `120000` projects one symbolic link, producing core E103. Its
  correctly sized object ID is opaque to this annex: the target is not
  resolved and its availability or type is not inspected.
- `160000` projects one gitlink, producing core E104. Its object ID is
  opaque to this annex: the target commit need not exist in the
  superproject object database and, if it does, its type is not
  inspected.

The leading-zero `040000` spelling used by pretty-printers is therefore
not the raw tree mode spelling. Any other mode byte string, missing
delimiter, invalid object-ID length, or duplicate name makes that tree
malformed. Entries MUST also be in strict canonical tree order. Compare
their names bytewise to the first difference; when one name ends at the
shared prefix, its virtual next byte is `/` for mode `40000` and NUL for
every other admitted mode. The entry with the lesser byte sorts first.
An order violation makes the tree malformed; a checker MUST NOT silently
sort it before projection.

Raw entry grammar is evaluated before core boundary precedence. Once an
entry is grammatically valid, the checker applies every core boundary
rule decidable from the accumulated path, name, and projected mode
before resolving its target object. If that rule prunes the entry or
decides the finding without content or descendant inspection, the
target is neither required nor resolved. This causal exclusion includes
E103, E104, E106, E107, E109, E110, the closed schema/hook boundary cases
of E303 and E308, and every E603-pruned `.git`, cache, dot-prefixed, or
unknown tool-state entry.

Only a `40000` entry that remains traversable requires a tree, and only
a `100644` or `100755` entry whose content remains logical input requires
a blob. A zero- or one-parent commit also requires each parent commit it
must traverse. An absent such required object is a capability failure. A
present target of the wrong required type for one of those tree modes or
parent traversal, a present malformed object, or a malformed raw tree as defined above
produces E601 at the store root. A check uses every independently
reliable component: a malformed commit whose parent sequence cannot be
trusted stops ancestry traversal at that boundary, while a well-formed
commit header with a malformed tree still permits inspection of its
unambiguous parent lineage but suppresses that snapshot and each
transition that requires it. It MUST NOT guess, repair, choose a parent,
or derive findings from an unavailable projection. An absent required
regular-file or directory object is the capability case above and does
not become E601.
Like the E602 boundary, causal truncation caused by a present malformed
object returns a complete managed result containing E601 rather than an
indeterminate transition; the malformed raw state already decides
managed nonconformance, and no dependent finding is inferred.

A shallow or otherwise incomplete repository may expose its current
snapshot, but a consumer that cannot inspect the ancestry
needed for a managed-history claim MUST surface a capability failure and
MUST NOT claim complete managed-store conformance. It MUST NOT fetch
missing history without separate network authorization. Local object
inspection MUST also suppress promisor or other lazy-fetch behavior: a
missing required object remains a capability failure rather than an
implicit connection.

---

## 4. Managed transactions

A managed writer MUST implement each accepted write as one transaction:

1. resolve the exact symbolic accepted-ref name without yet treating its
   current object name as the transaction base;
2. obtain the exclusive locks for that accepted ref and repository
   worktree, then capture a stable observation of symbolic `HEAD`, the
   accepted ref, and the exact index: read all three, read them again,
   and continue only if both complete observations are byte-identical and
   `HEAD` still directly names that accepted ref;
3. use the accepted object name (or its absence for initialization) and
   exact index from that stable observation as the captured base/index;
4. outside initialization, require a complete managed audit with no `E`
   finding for the captured accepted lineage; a content-addressed prior
   audit MAY be reused only for the exact same tip and exact normative
   rule-set identity, including the versions, revisions, and byte
   digests of the core and every applicable normative annex, after
   re-proving that the complete raw ancestry and every object required
   by the causal projection rules of §3 are locally available; a target
   excluded by any boundary rule decidable without it is not required.
   Current E601 presentation inputs are always rechecked. Such a result
   may substitute for recomputation only when
   it was retained by the current controller process or loaded from
   controller-owned external storage with authenticated provenance and
   integrity. A cache file inside the store, repository, worktree, or
   other store-writable derived state is only a hint and MUST NOT confer
   audit authority, even when its lookup key matches;
5. materialize the initial candidate from the captured base
   plus only the paths and bytes declared for this transaction, normally
   by the Git index;
6. run the core §8.1 boundary preflight on base and candidate and reject
   any index entry that would be pruned from an accepted raw tree;
7. compute the initial core changeset;
8. prepare the initial candidate into the final candidate under core §8,
   using the applicable exact hook bytes from the captured base;
9. run complete core state and transition validation against the final
   candidate and captured base, and continue only for a `complete` result
   with no `E` finding;
10. verify that final candidate paths and bytes can be reflected exactly
   in the index and worktree without overwriting out-of-candidate draft
   bytes, and capture the live path, index, and presentation-input
   fingerprints on which that safety decision depends;
11. create a commit whose raw tree contains exactly the final candidate
   logical-file paths and bytes and whose sole
   parent is the captured base, or a parentless root commit for
   initialization, then durably write a recovery journal in the worktree
   Git administration area containing state `pending`, the accepted ref,
   expected old and new object IDs, and an exact reconciliation plan.
   That plan records the complete raw index preimage and final image and,
   for every affected worktree path and ancestor boundary, exact absent
   or present kind/byte preimages and final images plus the step-10
   fingerprints;
   inability to prove that this record survives a process or host crash
   stops the attempt before CAS; and
12. verify again that `HEAD` still symbolically names the captured ref
   and that the safety fingerprints from step 10 are unchanged, update
   that ref only if it still names the captured base, then reconcile the
   index and repository worktree while preserving out-of-candidate draft
   bytes. After successful reconciliation it MUST durably mark the
   journal `complete`, release the locks, and only then remove the
   completed journal.

If the step-12 pre-CAS recheck fails, or CAS definitively reports that no
update occurred, the writer first durably marks the journal `cancelled`,
then removes transaction-owned temporary state and releases the locks,
and finally removes the journal. A cleanup failure leaves the cancelled
journal and locks for recovery. If the CAS outcome is unknown because of
a crash or ambiguous native failure, the journal remains `pending`; it
MUST NOT be relabeled based on an assumption about the ref.

The captured index MUST describe one resolved tree before step 5: each
present path has exactly one stage-zero entry, with no unmerged higher-
stage entries and no intent-to-add placeholder. Otherwise the attempt
stops as an operational conflict without constructing a changeset or
invoking hooks. A resolved stage-zero entry whose Git mode represents a
symlink, gitlink, or other forbidden logical kind remains structurally
representable for the step-6 E103/E104 preflight; it is not followed or
silently reinterpreted as a regular file.

Regular-file modes in a newly created accepted tree are deterministic
from the captured base, not from worktree permissions or disposable
materialization. A final-candidate file at a path that was a regular file
in the captured base MUST retain that base entry's exact `100644` or
`100755` mode. A final-candidate file at every other path MUST use
`100644`; every required tree entry uses `40000`. Accordingly, an
initial-candidate stage-zero regular entry MUST already use the base mode
when that path was a base regular file and MUST use `100644` otherwise.
Any other admitted regular mode at such an index path is an operational
index conflict before changeset construction. A writer MUST ignore
permission bits assigned by the host to hook materializations or newly
created worktree files. These requirements constrain newly authored
managed commits; checking or fast-forwarding an existing accepted raw
tree continues to admit both regular modes under §3.

The complete raw-index final image recorded in step 11 and installed in
step 12 MUST be tree-equivalent to the newly accepted raw tree. It has
exactly one stage-zero entry for every regular-file path in that tree,
with the identical blob object ID and `100644` or `100755` mode, and no
entry for a directory or any other path. It has no higher-stage or
intent-to-add entry. Host-specific stat-cache fields and other
non-semantic index representation bytes MAY vary, but the journal records
the exact final raw bytes chosen for this operation so recovery compares
one concrete image.

The step-10 safety fingerprints MUST cover the existence, kind, and
exact bytes of every live worktree path and the exact value of every
index entry that reconciliation could create, replace, or delete,
including ancestor boundaries needed to reach those paths. They also
MUST cover every effective configuration, attribute, environment, and
presentation input consulted by the byte-transparency proof, including
the identity and exact bytes or exact absence of each source. Their
comparison is equal if and only if all of those observations are
unchanged; a timestamp, resolved value detached from its mutable source,
or other lossy metadata check is insufficient. Reconciliation MUST use
only captured and still-equal inputs, or a raw byte/index mechanism that
does not consult mutable presentation inputs; otherwise the writer MUST
stop before updating the accepted ref.

Reconciliation, including recovery, is idempotent over that journal.
From one stable observation, every planned index or path item MUST equal
either its exact preimage or its exact final image. A final image is
already satisfied; a preimage may be changed to its final image only
after its dependencies are rechecked. Any other value blocks all
further reconciliation and is never overwritten. A crash may therefore
leave a mix of preimages and final images, but a retry can apply only the
remaining preimages without reversing completed work.

These preservation guarantees cover conforming writers that honor the
rendezvous locks and every non-cooperating change observable before the
last exact recheck for its mutation. Portable filesystems do not provide
one common content-CAS primitive for arbitrary paths: a non-cooperating
write in the final recheck/atomic-replacement window is outside this
guarantee. A tool MUST NOT claim to exclude that residual race unless
the host supplies a primitive that atomically conditions the mutation on
the captured identity and bytes.

Conforming writers rendezvous through two local lock files. For an
accepted ref, the ref lock is
`<common-git-dir>/engram/locks/refs/<digest>.lock`, where `<digest>` is
the lowercase hexadecimal SHA-256 digest of the exact full refname
bytes. The worktree lock is
`<worktree-git-dir>/engram/locks/worktree.lock`; linked worktrees have
distinct worktree Git directories but share the common Git directory.
A writer MUST atomically create the ref lock and then the worktree lock,
in that order, and MUST stop if either already exists. It holds both from
their acquisition in step 2 through rejection cleanup, or through
successful ref update and reconciliation. It releases them in reverse
order. A process failure leaves a conservative stale lock: removing one is an
explicit recovery operation and MUST occur only after establishing that
no writer still owns the attempt. Controller-private owner metadata MUST
durably distinguish a pre-journal phase from a phase in which a journal
is required; CAS MUST NOT occur before both the `pending` journal and the
journal-required phase are durable. A stale pre-journal lock with no
journal may be cleaned only when its recognized owner metadata proves
that phase. A missing or malformed journal in any phase that requires one
is an unresolvable recovery conflict, never evidence that CAS did not
occur. Git's own finer-grained object, ref,
`HEAD`, and index lock protocols remain additional mechanisms, not
substitutes for these shared rendezvous locks. A writer MUST use Git's
native atomic lock/update protocol for each actual ref, `HEAD`, or index
mutation and MUST stop when that short-lived native operation reports a
conflict. It need not, and portable implementations cannot, hold a
Git-native ref/`HEAD`/index lock throughout hook execution. Candidate
construction therefore uses a disposable index. Implementations MUST NOT
assume ordinary Git clients honor the engram rendezvous files: `HEAD`,
the target ref, the live index, and every affected worktree byte remain
fingerprinted concurrent inputs that are rechecked in step 12. A change
observed before the accepted-ref CAS rejects the attempt; a race observed
only after a successful CAS is the explicit recovery state below, never
permission to overwrite the racing bytes.

Every Git-native operation initiated by a managed writer or
synchronizer MUST suppress Git's own hook dispatch, including
reference-transaction and checkout hooks. Those programs are not the core
`prepare-changeset` phase and cannot participate in managed preparation,
acceptance, or reconciliation. A local pre-commit guard MAY intercept an
ordinary non-managed Git client, but the managed writer itself MUST NOT
invoke that guard while creating the accepted commit.

The journal's path and serialization are implementation-private; its
common interoperability signal is the annex-defined ref/worktree lock
that remains present while a `pending` journal needs recovery. Only the
controller that recognizes its own journal format may recover or remove
it. An unknown, malformed, or foreign journal or lock is never guessed
through or deleted automatically; cross-implementation recovery is not
claimed by this annex.

The accepted-ref update MUST use compare-and-swap semantics and can
succeed only if, at the atomic update instant, the ref names exactly the
captured base object ID (or is absent for initialization). Any different
observed value or native conflict rejects and discards the attempt. A
retry is a new transaction from the then-current accepted state. A
writer MUST NOT silently merge, overwrite, or reuse the rejected
candidate. Transient changes by non-cooperating Git clients that return
the ref to the exact captured object before CAS are not observable
history and are outside this condition; conforming writers serialize
through the rendezvous lock.

Discarding an attempt means discarding its disposable prepared
materialization and never moving the accepted ref to its unaccepted Git
objects; ordinary Git garbage collection MAY remove unreachable objects
later. It MUST NOT discard or reset the pre-existing working draft or
its staged declaration. A retry MAY use those declared edits only after
reviewing them against the new accepted base and constructing a fresh
disposable candidate.

The candidate MUST NOT be prepared in the live repository worktree.
Failure before the accepted-ref update MUST leave the accepted state
unchanged and MUST NOT import partial hook output. Failure while
reconciling the index or worktree after a successful ref update MUST be
reported as an operational checkout failure; it does not roll back or
obscure the already accepted commit. The step-11 journal already records
the remaining reconciliation obligation before the CAS, and the writer
MUST leave that journal plus its ref and worktree rendezvous files
present in a recovery-required state. Every managed writer MUST stop on
that state.

An explicit recovery operation MUST take ownership of those locks and
inspect the journal state. A `cancelled` journal proves that this attempt
did not update the ref, so recovery removes only transaction-owned
temporary state regardless of the ref's current value, releases the
locks, and removes the journal. For `pending`, a ref that names the new
object ID permits the idempotent reconciliation above. A ref that names
the old object ID does **not** prove that CAS never occurred: an external
client could have moved old→new→old. That state is ambiguous and remains
blocked for explicit resolution; recovery MUST NOT relabel it, clean the
attempt, or change live bytes. Any other ref value is likewise a
concurrent-history conflict that cannot be guessed through. After safe
new-state reconciliation, recovery durably marks the journal `complete`,
releases the locks, and then removes it. A recognized journal already
marked `complete` requires no reconciliation: recovery releases any
proven-stale remaining locks and removes the journal. Every other case
leaves the store blocked for explicit resolution.

Warnings do not reject a transaction. Hook rejection, unavailable or
untrusted hooks, tool capability failure, `indeterminate` transition
status, any `E` finding, or an accepted-ref race rejects it. These
attempt failures are operational results, not additional check
findings.

---

## 5. Working drafts, staging, and ordinary Git use

A dirty worktree is a working draft, and the Git index may represent an
initial candidate; neither is accepted memory. Consumers MAY inspect
either when explicitly asked, but MUST NOT present its contents as the
accepted state without identifying it as uncommitted.

An automated writer MAY edit the managed worktree before an acceptance
transaction begins. It MUST coordinate so that it is the sole automated
editor of that repository worktree for the bounded operation, and
MUST stop if it cannot distinguish its authorized changes from existing
logical edits. It MUST NOT silently discard, overwrite, stage, or commit
unrelated user changes. Distinct writers that need concurrent drafts
MUST use distinct clones or worktrees rather than one shared repository
worktree.

For a staged acceptance attempt, the current accepted commit is the
base and the Git index is the initial candidate. Unstaged and
untracked worktree bytes are outside that candidate and MUST NOT
influence preparation, validation, or the resulting commit. Before
acceptance, a managed writer MUST materialize that candidate away from
the live worktree and follow §4. Successful hook-generated bytes MUST be
imported into the index and reflected to the worktree as part of safe
reconciliation; if that would overwrite unstaged content, the attempt
MUST be rejected before the accepted ref moves.

An environment MAY expose a Git-shaped commit interface only when its
adapter retains control through every step of §4, including commit
creation, the compare-and-swap ref update, and post-update
reconciliation. A callback that returns control to Git before commit
creation — such as a `pre-commit` hook — cannot by itself be a managed
writer or the designated executor. It MUST NOT prepare a candidate and
then allow Git to accept that candidate outside the transaction; it MAY
instead reject the unmanaged commit and direct the caller to a managed
writer. Such local integration is not logical store content and does
not travel in repository history.

Exactly one executor owns an attempt. A managed commit engine that has
already prepared a candidate MUST create its commit without causing a
Git hook to prepare that same candidate again. A second integration
layer MAY validate the prepared result but MUST NOT rerun preparation.

---

## 6. Attachments and repository topology

A project MAY attach any number of managed stores, and one managed store
MAY be attached to any number of projects. The attachment names the
store root and root README as described by core §12. It neither copies
the store into the project nor changes which Git repository owns it.

Keeping a reusable store outside consumer repositories is the
RECOMMENDED topology. If a managed store is physically nested below a
different repository worktree, the outer repository SHOULD exclude the
entire nested worktree. A deliberate Git submodule MAY instead record a
pinned store commit, but the inner managed-store transaction and the
outer gitlink update remain separate commits. An unmanaged embedded
repository that is accidentally staged as a gitlink is not an
attachment mechanism.

Attachment is discovery only. It MUST NOT grant read, write, commit,
hook-execution, network, or synchronization authority. Removing an
attachment MUST NOT delete the independent store or rewrite its
history.

---

## 7. Concurrency and synchronization

Acceptance transactions against one accepted ref, index, and repository
worktree execute serially under the writer lock. The editing discipline
of §5 also permits only one automated draft writer in that repository
worktree at a time. Distinct managed stores, clones, or
worktrees MAY draft and prepare independently. Sharing one physical
checkout among projects does not create multiple repository worktrees:
the projects coordinate one editor and use the same lock and accepted
ref.

Fetch, push, remote creation, credential use, and publication are not
implied by local write authority. They require separate external
authorization. The built-in steps of a local transaction MUST NOT
initiate or rely on repository network access. A separately trusted
external hook may attempt effects outside its disposable trees; those
effects remain under the core §8.5 host boundary, are not synchronization
authority, and SHOULD have network access denied unless separately
authorized.

Fetching objects alone does not change accepted memory. Before a
fast-forward update accepts an incoming segment, a synchronizer MUST
require a complete managed audit with no E finding for the entire
would-be accepted lineage. That audit includes exact raw-tree projection
and E603 evaluation for every commit, complete core validation of every
snapshot, the explicit empty-base-to-root transition when the lineage
starts at a root commit, and every later parent/candidate transition in
root-to-tip order. A verified local prefix MAY be reused only under the
cache rule of §4; every incoming object and pair is still covered. The
synchronizer rejects the update on any E finding, indeterminate result,
missing ancestry, or capability failure. It does not rerun the hooks
that produced an already accepted remote commit; their observable result
is the candidate tree being validated.

When independent clones diverge, a synchronizer MUST start from a
verified remote tip. It MUST find the nearest exact commit-object-ID
ancestor common to the completely audited linear local and remote
lineages. No common ancestor is an unrelated-history conflict and MUST
NOT be joined. The replay source segment is exactly the local commits
strictly after that ancestor through the captured local tip, processed
oldest to newest; each parent/candidate net difference is the local
logical changeset replayed as a new managed transaction, including local preparation and validation. It
MUST NOT introduce a merge commit into the accepted lineage or choose a
semantic conflict resolution silently.

Mechanical replay is exact and path-based. For each original net change,
the synchronizer compares absence or complete regular-file bytes in the
original base (`O`), original candidate (`N`), and current verified
replay base (`C`):

- an addition applies `N` when `C` is absent, is already satisfied when
  `C` equals `N`, and otherwise conflicts;
- a modification applies `N` when `C` equals `O`, is already satisfied
  when `C` equals `N`, and otherwise conflicts; and
- a deletion removes the path when `C` equals `O`, is already satisfied
  when `C` is absent, and otherwise conflicts.

The synchronizer MUST evaluate every changed path against one unchanged
`C` before applying any of them. It then derives one tentative complete
regular-file map by applying every non-conflicting result simultaneously
to `C`. That map is structurally representable only when no regular-file
path is a strict path-component prefix of another regular-file path; all
intermediate ancestors are then directories. Every violating prefix
pair is a conflict, including a pair between a changed path and an
otherwise unchanged path from `C`.

If any exact-preimage or structural conflict exists, the synchronizer
MUST apply none and MUST report the deduplicated union of both paths from
every conflicting pair and every other conflicting path in UTF-8 byte
order. It MUST NOT perform an automatic text merge, normalization,
marker insertion, or file-to-directory conversion. Otherwise the
simultaneous results form the initial candidate for the new managed
transaction. A completely already-satisfied source changeset advances
replay bookkeeping without running hooks or creating an empty commit.
Conflict resolution requires a separately authorized complete candidate
from the controlling environment. The final local or remote ref update
retains the compare-and-swap rule of §4.

A synchronizer that changes a local accepted ref, `HEAD`, index, or
worktree MUST use the same target-ref and worktree rendezvous locks,
stable captures, safety fingerprints, native atomic mutation protocols,
and compare-and-swap discipline as §4. An update to the ref currently
named by `HEAD` also performs §4's final symbolic-`HEAD` check. When one
local operation needs several ref locks, it acquires all of them by the
exact full refname bytes in ascending order, then acquires the worktree
lock; it verifies every expected object ID and releases the locks in
reverse order.

After each synchronizer update to the local ref currently named by
`HEAD`, or each switch of symbolic `HEAD`, that leaves no intentional
conflict-resolution candidate, the raw index MUST become tree-equivalent
to the accepted raw tree then named by `HEAD`, using the exact stage-zero
path/blob/mode rule of §4. That exact index final image and every
worktree final image MUST be journaled before the first related mutation.
The synchronizer MUST finish their §4 exact preimage/final-image
reconciliation before beginning a replay transaction from that checkout
or releasing the locks, while preserving any separately authorized out-
of-candidate draft bytes. It MUST NOT report completed local
synchronization while such reconciliation is incomplete: failure after
an accepted update remains recovery-required. A separately authorized,
explicitly identified resolution candidate MAY instead be left in the
reserved index/worktree while its private accepted base remains at
`HEAD`; it is an active replay state, not a completed update.

Changing symbolic `HEAD` from one local branch to another is allowed only
under locks for both refs and the worktree, after rechecking the current
symbolic name, completely auditing the target lineage, and verifying the
same checkout-safety fingerprints. The target ref MUST still name its
audited object ID when the Git-compatible `HEAD` update occurs. Remote
updates use the remote service's compare-and-swap or equivalent
conditional update and never assume the local lock controls another
clone.

---

## 8. Managed check findings

Managed checking adds these identities to the core Appendix B catalog;
all use the store-root path `.`:

- **E601** — the target is claimed as managed but is not the exact root
  of a non-bare Git worktree, its `HEAD` does not directly name the
  required valid-UTF-8 non-symbolic local branch (or that branch does not
  directly contain a commit after initialization), a present required
  raw Git object/reference is structurally malformed or has the wrong
  object type, or its managed worktree is not the complete
  byte-transparent presentation required by §2;
- **E602** — the accepted lineage contains a commit with more than one
  parent;
- **E603** — an accepted commit's raw tree contains a path that is
  pruned rather than part of the exact core logical snapshot.

Every audited accepted snapshot emits ordinary core findings at its
normal logical paths. Historical transition auditing emits the
applicable E5xx identities against each parent/candidate pair. The same
identity arising in several commits is aggregated; optional detail MAY
name the affected object IDs.

Repository incompleteness, unsupported Git capabilities, trust denial,
ref races, hook failure, stale writer locks, and network denial are
capability or operational results, not E601–E603.
