# engram reference CLI — Contract v1 (draft)

**Version:** v1
**Status:** Draft
**Revision:** 2026-08-06
**Normative status:** Non-normative with respect to the engram standard

This document specifies the intended user-facing contract of the
reference `engram` command. The normative authority remains the
[core specification](../spec/README.md), the
[Git-managed stores annex](../spec/annex-git.md), and normative annexes.
The CLI implements those rules; it does not define additional store
semantics.

The command surface is deliberately files-first. It provides operations
whose correctness depends on whole-store structure, changesets, or
runtime integration. Ordinary reading and content search remain normal
filesystem operations.

---

## 1. Command model

```text
engram
├── init
├── clone
├── attach
├── detach
├── status
├── diff
├── log
├── add
├── check
├── fmt
├── new
├── mv
├── schema
│   ├── inventory
│   ├── list
│   ├── show
│   └── copy
├── commit
├── revert
├── hooks
│   ├── list
│   ├── trust
│   └── revoke
├── doctor
├── pull
├── push
└── version
```

The public surface deliberately resembles Git where the underlying
operation is familiar, while adding engram's store semantics and
validation. Git supplies storage and local history; this CLI is the
domain interface that preserves managed-store invariants.

The terminology is intentional:

- a **working draft** is the unaccepted content visible in the worktree;
- the **initial candidate** is the logical tree declared by the Git
  index when acceptance begins;
- the **final candidate** is the logical result after preparation hooks
  and before acceptance or rejection;
- a **changeset** is the computed net difference between base and
  candidate; it is a domain object, not a top-level command;
- a **transaction** is the internal, one-shot preparation and acceptance
  attempt performed at commit time, not a public editing session;
- a **commit** is the accepted durable result visible to users.

`status` distinguishes staged and unstaged state, `diff --staged`
renders the current changeset, `check --staged` validates it without
hooks, and `commit` prepares and accepts it. Humans, skills, and
adapters use the same Git-shaped working-tree and staging workflow; no
public transaction handle is required across tool calls.

---

## 2. Global options and discovery

```text
engram [GLOBAL-OPTIONS] COMMAND [ARGS]

GLOBAL-OPTIONS
  -s, --store PATH        Select a store root
      --format FORMAT    text or json; default text
      --no-color         Disable ANSI styling
  -q, --quiet            Suppress ordinary successful human output
  -h, --help             Show help
  -V, --version          Show CLI version
```

Global options may appear in any option position before an explicit
`--`, either before or after the command; after `--` every token is a
command argument. Each global option may appear at most once, including
across its short and long spellings. `-h`/`--help` may therefore follow
a known command, while `-V`/`--version` cannot be combined with any
command or command argument. Help and version actions are mutually
exclusive. A requested human help or version action ignores `--quiet`
and always prints its requested text; `--quiet` suppresses only ordinary
successful command output. Any forbidden combination is a usage error.

Command-specific options occur only after the command and, like global
options, may be interleaved with command arguments until `--`. A long
option that takes a value accepts exactly `--name VALUE` or
`--name=VALUE`; a short value option accepts exactly two tokens such as
`-n COUNT`. Short options are not clustered or value-concatenated. Once
an option token has selected a required value, the next token is that
value even when it begins with `-`. Only the particular operand or
option value marked with `...` in a synopsis may repeat; that marker does
not make any other option repeatable. Unless its own synopsis position
contains `...` or the text explicitly permits its repetition, each
command option appears at most once; two aliases of one option count as
duplicates. Empty required values and every unrecognized option are
usage errors.

Global `-V`/`--version` is an exact alias for the `version` command. It
uses canonical envelope command `version`, honors `--format`, and cannot
be combined with a command or command arguments. Global `-h`/`--help`
and `COMMAND --help` are human-presentation actions: without
`--format json` they print help and return 0 without store discovery.
Help combined with `--format json` is deliberately a usage error rather
than a successful result: it emits the common JSON error envelope with
`kind: "usage"`, `{}` result, and canonical command name when one was
already parsed, otherwise `command: null`. Thus the exhaustive non-error
result table needs no unstable help-text object.

Without `--store`, store commands walk from the current directory toward
the filesystem root and select the first `.engram/root.yaml`. A command
that requires a managed store additionally verifies the Git annex. It
does not reinterpret an enclosing project repository as ownership of an
independent store.

Global `--store PATH` resolves a relative host path from the current
directory and selects that exact existing real snapshot root instead of
walking upward. It is admitted only by commands that operate on a
selected store: `status`, `diff`, `log`, `add`, the discovery-based
`check` forms, `fmt`, `new`, `mv`, `schema list/show/copy`, `commit`,
`revert`, `hooks`, `pull`, `push`, and `doctor` without positional
`PATH`. It is a usage error with `init`, `clone`, `attach`, `detach`,
`schema inventory`, `version`, explicit-path/snapshot-pair `check`, or
positional-path `doctor`. A human help action may still display any
command's help without resolving or applying `--store`; global version
is subject to the `version` rejection. This allowlist is exhaustive.

Paths accepted by store commands are resolved relative to the selected
store unless documented otherwise. Displayed logical paths are
store-root-relative and use `/`. The CLI does not define an
`ENGRAM_STORE` environment variable; `ENGRAM_` names belong to the hook
protocol.

`text` output is for humans. Every complete non-error outcome goes to
standard output. Every common-error diagnostic, for every stable error
kind, goes to standard error and emits no ordinary result on standard
output. JSON output follows the versioned protocol below.

`clone`, `pull`, and `push` are the only built-in operations that
initiate repository network access.
Read-only, editing, staging, local-path attachment, and local-commit
operations never fetch or publish implicitly; separately trusted store
hooks remain external programs under their own authority boundary.

### 2.1 JSON protocol

When `--format json` is selected successfully, every command emits
exactly one UTF-8 RFC 8259 object followed by LF on standard output,
including outcomes that return status `1`, `2`, or `3`. It emits no
other standard-output bytes and contains no ANSI escapes. Opaque child-
process diagnostics may still be forwarded to standard error; machine
consumers do not parse standard error. `--quiet` does not suppress JSON.

Every response has exactly this envelope:

```json
{
  "version": 1,
  "command": "check",
  "outcome": "issues",
  "exit_status": 1,
  "result": {},
  "error": null
}
```

`command` is the canonical dot-separated name, such as `schema.list` or
`hooks.trust`. It is null only when parsing fails or a global help error
occurs before any canonical command is identified; after command
identification, success and error envelopes retain that canonical name.
The mapping is fixed:

| `outcome` | `exit_status` | Meaning |
|---|---:|---|
| `ok` | `0` | Operation completed successfully |
| `issues` | `1` | Operation completed and reported validation errors, comparison drift, required diagnostic problems, or a resolvable synchronization conflict/rejection |
| `error` | `2` | Operational failure |
| `indeterminate` | `3` | Transition evaluation was indeterminate, or a remote publication outcome cannot be established without retained local recovery state |

For an outcome other than `error`, `result` has the complete command
shape below and `error` is `null`. For `error`, `result` is normally
`{}` and `error` has exactly this shape:

```json
{"kind": "repository", "message": "..."}
```

`kind` is one of `usage`, `io`, `capability`, `trust`, `hook`,
`repository`, `network`, `conflict`, `concurrency`, `integration`,
`cancelled`, `internal`, or `operational`. `message` is unstable human
text; consumers use `kind` and `exit_status`. Object-member order has no
semantic meaning. Logical paths use `/` and are store-root-relative;
host paths are absolute in native platform spelling. Git object names
are complete lowercase hexadecimal strings of the repository's object
format width; only human `log --oneline` output may abbreviate them.
Arrays of logical paths or changes use UTF-8 byte order unless another
order is stated.

The stable error classification is causal and uses the first applicable
row below; wrappers do not reclassify an underlying failure merely
because it occurred during another command:

| `kind` | Exact class |
|---|---|
| `usage` | Command grammar, option, or argument-value rejection before target discovery |
| `cancelled` | Explicit caller interruption observed before another causal failure |
| `internal` | Violated CLI invariant or recognized impossible internal state |
| `capability` | Unsupported platform/Git/spec feature, missing required object/history, or an unrepresentable protocol value |
| `trust` | Required external authorization for the selected engram hook set is absent |
| `hook` | A selected hook cannot be launched, or its core §8.3 attempt is rejected after launch: resource/timeout failure, abnormal or non-zero termination, changed exposed base, forbidden candidate boundary, or unstable private capture |
| `network` | Transport, credential, remote-protocol, fetch, or push failure |
| `conflict` | A semantic patch, preservation, or unrelated-history topology conflict that requires the caller to change content or history |
| `concurrency` | Encountered shared lock, stale lock requiring recovery, or compare-and-swap race |
| `integration` | Owned launcher, attachment block, skill target, registry binding, or other host-integration conflict |
| `repository` | Local Git/store shape, ref, index, object, presentation, or raw-state ineligibility outside a completed validation result and not classified by an earlier row |
| `io` | Host filesystem or process I/O failure not classified by an earlier row |
| `operational` | Operational failure that fits none of the preceding closed classes |

An engram hook's unavailable interpreter and every post-invocation
§8.3 rejection above are `hook`, not `capability`, `repository`, or
`io`; a causal failure before the first invocation retains its earlier
table class. A missing Git object needed for a complete local claim is `capability`,
not `repository`; a complete validation result with an `E` finding has
outcome `issues`; and an indeterminate transition has outcome
`indeterminate`, not an error. Ambiguity about a local ref, symbolic
`HEAD`, index, or worktree mutation is instead an operational
`concurrency` error: the controlling journal remains pending and the
typed recovery result is returned. Warnings alone retain outcome `ok`.
If one operation observes several independent failures before returning,
it selects the earliest failure in its specified processing order.

Every host path selected or emitted by protocol v1 must have a
reversible UTF-8 native representation. A command whose project, store,
entrypoint, destination, or controller path cannot be represented that
way fails with `capability` before mutation. Every Git ref emitted as a
JSON string must likewise decode from its exact raw refname bytes as
valid UTF-8; the accepted ref is additionally a managed-store rule, and
an unrepresentable ref required by a command's stable result causes a
pre-mutation `capability` error. The CLI never substitutes U+FFFD for a
path or ref identity. This protocol representation preflight does not
suppress a conformance identity when the result need not serialize the
offending value: `check --accepted` and `check --staged` report E601 at
`.` for a non-UTF-8 accepted ref. Commands whose stable non-error result
requires that ref string cannot construct that result and use the common
`capability` error instead.

Where this contract says **canonical physical host path**, the CLI opens
the existing target, follows symlinks in its resolved parent chain, and
asks the host for that opened object's absolute final path. It retains
the host-returned case and Unicode spelling and performs no additional
case folding or normalization. A **physical directory object identity**
is the host's stable filesystem object ID (device/inode on POSIX;
volume/file ID on Windows). It is used for physical deduplication even
when two opened aliases report different final paths. A **physical
directory binding identity** is the tuple of the canonical physical host
path and that object identity; trust uses this stricter identity.
Commands that need either relation fail with `capability` when its
components cannot be obtained reliably. Moving a directory changes its
binding identity even when the host retains its object ID, and a copy
has a new object identity; a same-path replacement is rejected whenever
the host reports a different object ID. Host reuse of an ID after
deletion is an explicit platform trust assumption, not a non-reuse
guarantee supplied by this CLI, so hostile same-path replacement
requires external revocation. For a missing entrypoint only its opened
parent has such an identity and the final filename remains a validated
UTF-8 path component.

There is one typed exception to the empty error result. If an operation
has already published workflow state — an accepted or private workflow
ref, symbolic `HEAD`, the managed index/worktree, or a remote CAS — or
leaves a recovery-required journal/lock even before CAS, `result` has
exactly this recovery shape so a client cannot mistake the operation for
safe retry:

```json
{
  "durable": true,
  "local_refs": [
    {
      "ref": "refs/heads/main",
      "before": "0123456789abcdef0123456789abcdef01234567",
      "after": "fedcba9876543210fedcba9876543210fedcba98"
    }
  ],
  "head": {
    "before": {
      "ref": "refs/heads/main",
      "commit": "0123456789abcdef0123456789abcdef01234567"
    },
    "after": {
      "ref": "refs/heads/main",
      "commit": "fedcba9876543210fedcba9876543210fedcba98"
    }
  },
  "checkout_changed": false,
  "remote": null,
  "recovery_required": true
}
```

`durable` is true iff at least one of those published mutations is known
to have succeeded. Fetched objects, `FETCH_HEAD`, transport bookkeeping,
remote-tracking refs isolated from the managed workflow, unreachable
commit objects, and removable temporary state do not set it. `local_refs`
contains one entry for every
successful accepted/private workflow-ref CAS in chronological order;
updates performed in
one atomic multi-ref transaction use exact full-refname byte order within
that transaction. The same ref may therefore occur several times, and
`before` or `after` may be `null` for creation or deletion. `head` is
`null` when symbolic `HEAD` is not known to have changed; otherwise it
contains exactly its `before` and `after` Git states. `checkout_changed`
is true when the real index or worktree is known to have changed, even
if no ref or `HEAD` mutation occurred. `remote` is `null`
unless a remote CAS is known to have succeeded, in which case it contains
exactly string `name`, full `ref`, and nullable complete-object-ID
`before` plus non-null `after`. `recovery_required` is true exactly when
a local journal, repository or controller target lock, external init or
acquisition intent, `HEAD`, index, or worktree recovery remains required;
it may be true with `durable: false` after a pre-CAS cleanup failure.

This result is used by draft/staging helpers, `init`, `clone`, `commit`,
`revert`, or `pull` after any durable local mutation or retained recovery state,
including a resumable private replay (`recovery_required: false` when
internally consistent), and by `push` after a durable remote change. The
accompanying `error` remains present. Object creation or a temporary
journal that is fully cleaned up before returning, with no successful
CAS, uses `{}`. A local native mutation whose success cannot be
established never uses a complete command result or outcome
`indeterminate`; it returns outcome `error`, status 2, kind
`concurrency`, and this recovery shape with `recovery_required: true`.
`durable` and the mutation arrays describe only effects known to have
succeeded, so they remain false or empty for an ambiguous effect until
recovery proves its state.

Before a state-inspection or staging command mutates the index or
serializes a logical change array, it completes the core §8.1 boundary
preflight for the exact prospective states being compared. For a
Git-backed state it also inspects the raw tree/index and rejects any raw
path inside reserved state that the logical snapshot would omit; such an
entry is not silently hidden behind a clean-looking result. A forbidden
stage-zero mode at an otherwise logical path remains visible to the core
E103/E104 preflight. Thus `add` preflights its
complete prospective index before replacing the live index. If either
eligibility check fails, `status`, `diff`, or `add` makes no change,
emits no partial result, and returns the common `error` outcome with kind
`repository`; the matching `check` form (`check`, `check --accepted`,
`check --staged`, or explicit `--base`/`--candidate`) obtains normative
findings where defined. A pruned raw index entry is an operational index
conflict rather than a new finding and must be unstaged with ordinary Git
index tooling before retrying. `commit` or `revert` instead returns its
validation result with nullable `changes` when the core preflight is
indeterminate, but still returns an operational error for an ineligible
raw index. No command serializes a partial or guessed changeset.

The Git annex's regular-mode declaration is part of raw-index
eligibility: an index regular file uses its accepted-base `100644` or
`100755` mode when that path exists as a base regular file, and `100644`
when it does not. `status`, `diff`, `add`, `commit`, and `revert` return
the common `repository` error for a different admitted regular mode
rather than hiding a mode-only staged difference. The matching
`check --staged` may still project either admitted mode to the same
logical regular file and report the normative candidate result; this
operational writer restriction has no invented finding code.

#### Shared JSON types

A logical change is:

```json
{"operation": "modified", "path": "people/ada.md"}
```

`operation` is `added`, `modified`, or `deleted`.

A finding is:

```json
{"code": "E401", "path": "topics/old.md", "detail": "..."}
```

`detail` may be omitted and is non-normative. Finding identity remains
`(code, path)`.

A validation result is:

```json
{
  "target": "changeset",
  "status": "complete",
  "findings": []
}
```

`target` is `snapshot`, `changeset`, or `managed-store`. `status` is
`complete` or `indeterminate`; it is always `complete` for a snapshot.
Findings retain their normative aggregation and ordering.

A Git state is:

```json
{"ref": "refs/heads/main", "commit": "0123456789abcdef0123456789abcdef01234567"}
```

`ref` or `commit` may be `null` when that state does not yet exist.

A commit is:

```json
{
  "id": "0123456789abcdef0123456789abcdef01234567",
  "parents": [],
  "author": {"name": "Ada", "email": "ada@example.test"},
  "committer": {"name": "Ada", "email": "ada@example.test"},
  "authored_at": "2026-08-06T10:00:00+02:00",
  "committed_at": "2026-08-06T10:00:00+02:00",
  "message": "Update memory\n"
}
```

`parents` contains every complete parent object ID in raw commit-header
order; it is empty for a root commit, has one entry in a conforming later
commit, and preserves multiple entries so `log` can diagnose a merge
boundary without hiding it.

For each identity kind, extraction is attempted only when the raw commit
has exactly one simple `author` or `committer` header with no
continuation. Its value is split at its last two ASCII spaces into
`<identity> <seconds> <zone>`. A missing, duplicate, or continued header,
or a value that cannot be split into those three parts, makes both the
identity and timestamp for that kind null. After a successful split,
the identity and timestamp parts are parsed independently. `identity`
is usable when it is exactly `<name> <LT><email><GT>`, with one ASCII
space before `<`, and neither name nor email contains `<` or `>`; either
may otherwise be empty. A usable identity populates `author` or
`committer` even when its timestamp is unusable, and a usable timestamp
populates its timestamp field even when the identity is unusable.

`seconds` is `0` or an optional `-` followed by a non-zero ASCII decimal
digit and then zero or more digits, with no leading zero. `zone` is
`+HHMM` or `-HHMM`, where `HH` is 00–23 and `MM` is 00–59. A timestamp is
non-null only when those tokens are canonical and the resulting instant
can be represented with a four-digit RFC 3339 year; it is rendered with
seconds and the original numeric offset. Timestamp failure does not
erase an independently usable identity. Commits created by this CLI
populate both identities and timestamps in this canonical form.

Git bookkeeping strings are display data, not record provenance or
conformance input. The raw byte slices for author/committer `name` and
`email` and for the complete raw `message`, including any final LF, are
decoded as UTF-8 independently of any Git `encoding` header or host
locale; no transcoding is attempted. Each maximal subpart of an ill-
formed UTF-8 sequence under the Unicode 17.0 definition becomes exactly
one U+FFFD. Paths and refs retain the stricter no-replacement rule of
§2.1.

A history audit is:

```json
{
  "base": "0123456789abcdef0123456789abcdef01234567",
  "candidate": "fedcba9876543210fedcba9876543210fedcba98",
  "validation": {
    "target": "changeset",
    "status": "complete",
    "findings": []
  }
}
```

`base` is `null` for initialization.

A hook description is:

```json
{
  "path": ".engram/hooks/prepare-changeset/20-catalog.py",
  "interpreter": "python3",
  "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}
```

A schema description is:

```json
{
  "type": "note",
  "source": "local",
  "path": ".engram/schemas/note.md",
  "version": 1,
  "description": "..."
}
```

`source` is `local` or `inventory`; `path` is a logical path for a local
schema and `null` for an inventory entry.

A supported-specification descriptor is:

```json
{
  "id": "core",
  "version": "v1",
  "revision": "2026-08-06",
  "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}
```

It has exactly those string members. `sha256` identifies the exact
canonical LF-terminated normative source bytes implemented by the
binary, so draft rule changes remain distinguishable even on the same
revision date. Annex descriptors use their stable annex name, such as
`git` or `skills`, as `id`.

#### Command results

The following table defines every complete non-error `result` object.
No other members occur in protocol version 1.

| Command | `result` members |
|---|---|
| `init` | `dry_run` boolean, `root` host path, `accepted` Git state, nullable `files` logical changes, `launcher` (`planned`, `installed`, or `unchanged`), `validation` changeset result |
| `clone` | `root` host path, `remote` string, `accepted` Git state, `published` and `reused` booleans, `verified_commits` non-negative integer, `launcher` (`planned`, `installed`, or `unchanged`), top-level `validation` managed-store result, and `audits` history audits in root-to-tip order |
| `attach` | `project`, `store`, and `entrypoint` host paths; `changed` boolean; top-level `validation` managed-store result; and root-to-tip `audits` |
| `detach` | `project`, `store`, and `entrypoint` host paths; `changed` boolean |
| `status` | `mode` (`normal` or `pull-replay`), `accepted` Git state, `candidate_base` Git state, `staged` logical changes, `unstaged` logical changes, and nullable `replay` object |
| `diff` | `from` and `to` state selectors, `changes` logical changes, and `stat` containing non-negative `added`, `modified`, and `deleted` counts |
| `log` | `commits` commit objects, newest first |
| `add` | `changed` boolean and the complete resulting `staged` logical changes |
| `check` | one validation result |
| `fmt` | `dry_run`, `check`, and `changed` booleans plus affected logical `paths` |
| `new` | `dry_run` and `changed` booleans, `record` logical path, and affected `catalogs` |
| `mv` | `dry_run` and `changed` booleans, logical `from` and `to` paths, rewritten source-document `paths`, and affected `catalogs` |
| `schema.inventory`, `schema.list` | `schemas` schema descriptions |
| `schema.show` | `schema` schema description and exact UTF-8 `content` string |
| `schema.copy` | `dry_run` and `changed` booleans, source inventory `schema` description, and destination logical `path` |
| `commit` | `dry_run` and `created` booleans, `commit` object or `null`, definitive logical `changes` or `null` before changeset construction (including accepted-audit or boundary failure), and `validation` result or `null` for a no-op |
| `revert` | the `commit` result members plus source commit ID `reverted` and ordered logical `conflicts` |
| `hooks.list`, `hooks.trust` | selected `state` (`accepted` or `working`), `changed` boolean, set `sha256`, set-level `trusted` boolean, and complete ordered `hooks` |
| `hooks.revoke` | `changed` boolean and `revoked_sets`, an ordered list of set SHA-256 strings |
| `doctor` | ordered `checks`; each contains stable `name`, `class` (`required` or `heuristic`), `status` (`ok`, `warning`, or `error`), nullable `path`, and nullable non-normative `detail`; plus `recovery` containing booleans `requested`, `needed`, and `performed` and nullable `accepted` Git state |
| `pull` | `state` (`up-to-date`, `fast-forwarded`, `replayed`, `conflict`, `rejected`, or `aborted`), `remote`, `remote_ref`, `before` and `after` Git states, non-negative `fetched` and `replayed` counts, logical `conflicts`, nullable definitive logical `changes`, top-level `validation` managed-store result, nullable `candidate_validation` changeset result, and root-to-tip `audits` |
| `push` | `state` (`up-to-date`, `pushed`, `rejected`, or `indeterminate`), `remote`, `remote_ref`, `remote_observed` boolean, nullable remote `before` object ID, local `after` object ID, non-negative `commits` count, nullable `changed` boolean, top-level `validation` managed-store result, and root-to-tip `audits` |
| `version` | `cli_version`, ordered `core_versions`, `annex_versions`, `git` object containing `version` and `supported`, and `build` containing `go`, `os`, `arch`, and nullable source `revision` |

For `fmt`, `new`, `mv`, or `schema.copy`, `changed` means that the
computed final bytes differ from the captured draft. It therefore
reports the would-change result when `dry_run: true`, and for `fmt` also
when `check: true`; only a real non-check invocation publishes those
bytes. In result shapes without a read-only flag, `changed` means the
command actually published its stated effect.

In `status`, `accepted` is the exact state currently named by symbolic
`HEAD`, including the private branch during a pull replay, and
`candidate_base` is the same state because it is the base against which
the live index declares its initial candidate. Outside replay, `replay`
is `null`. During replay it contains exactly: immutable `original`, the
original accepted branch/tip captured when pull began; current `private`,
the pull-owned branch/tip, equal to `accepted`; and current `base`, the
parent of the original local logical changeset presently being applied
or resolved, represented with `ref: null` and its complete historical
commit ID; `reason`, the immutable origin of this active source draft,
exactly `conflict` when exact application first conflicted or `rejected`
when exact application succeeded but selected-hook trust/preparation or
candidate evaluation first rejected it; plus
`conflicts`, the current complete logical conflict-path array in UTF-8
byte order. It is non-empty for `reason: "conflict"` and empty for
`reason: "rejected"`; in both cases an explicit resolution is pending.
A rejected `pull --continue` does not change that origin or erase a
persisted conflict list; the command result reports the new rejection.
Between
replayed changesets, `base` advances to the source parent of the next one;
after the last succeeds replay metadata is removed rather than reporting
a completed replay object. A state selector in `diff` has `kind` equal
to `accepted`, `index`, `working`, or `revision`. Its `value` is the
resolved complete commit ID for `accepted` and `revision`, and null for
the live `index` and `working` layers. Presentation-only flags such as
`--stat`, `--name-only`, `--oneline`, and color options do not change
the JSON shape. In `clone`, `attach`, `pull`, and `push`, the top-level
`validation` is authoritative for the complete managed claim.
`clone.remote` is the exact URL argument string; `pull.remote` and
`push.remote` are the selected configured remote name. `remote_ref` in
both commands is always the selected full `refs/heads/<branch>` name,
never the relative argument. `pull --continue` and `--abort` retain the
exact remote name and full ref captured in their active replay metadata;
they do not re-resolve current upstream configuration. `audits`
contains only the unambiguous parent/candidate transition audits that
were actually evaluated; it contains no invented pair for a merge
boundary, and its absence or truncation never implies that the lineage
was valid. `clone.verified_commits` counts commits for which the raw
commit and required tree projection, the core snapshot, and the
applicable empty-base or single-parent transition were all evaluated to
a complete result. A complete result containing `E` findings counts; a
merge or malformed boundary, missing input, and a merely discovered but
causally truncated commit do not. Schema arrays are ordered by UTF-8 `type`, then nullable
`path` with `null` before strings. `revoked_sets` uses ascending ASCII
digest order. `doctor.checks` is ordered by UTF-8 `name`, then nullable
logical `path` with `null` before strings. `core_versions` and
`annex_versions` contain supported-specification descriptors ordered by
UTF-8 `id`, then `version`. The `git` result object contains exactly a
nullable human version string and a `supported` boolean; `build`
contains exactly string `go`, `os`, and `arch` plus nullable string
`revision`. `cli_version` is a non-empty string containing the CLI's
build-supplied version identifier.

For `init --dry-run`, `accepted.ref` is `refs/heads/main`,
`accepted.commit` is `null`, and `launcher` is `planned`. A real
initialization fills `accepted.commit` only after successful acceptance;
a validation rejection leaves it `null` and also reports the launcher as
`planned`. For `commit` and `revert`, `dry_run: true`, a no-op, or a
complete validation rejection always has `created: false` and `commit:
null`; only a real successful acceptance has `created: true` and a
non-null commit object.
A no-op has `changes: []` and `validation: null`. Failure of the captured
accepted-lineage audit has `changes: null` and a `managed-store`
validation result. A core boundary-preflight failure has `changes: null`
and an `indeterminate` `changeset` validation. Once a definitive final
candidate exists, a complete candidate-validation rejection or dry-run
uses its complete `changeset` validation and definitive `changes` array.
An operational failure — including trust, hook, preservation,
concurrency, or I/O failure — uses the common error envelope instead of
this command shape, with the typed recovery result when applicable.
For `revert`, an exact-preimage conflict has outcome `issues`,
`created: false`, `commit: null`, `changes: null`, `validation: null`,
and the complete conflicting paths; every other result has
`conflicts: []`. An already-satisfied inverse is the ordinary no-op.

---

## 3. Exit status

The reference CLI reserves:

| Status | Meaning |
|---:|---|
| `0` | Operation completed; validation has no `E` finding |
| `1` | A completed operation reports an `E`, drift, a required diagnostic problem, or a resolvable synchronization conflict/rejection |
| `2` | Usage, I/O, capability, trust, hook, repository, or other operational failure |
| `3` | Transition evaluation is `indeterminate`, or a remote publication outcome cannot be established without retained local recovery state |

Warnings never change status `0`. For a transition, `indeterminate`
takes precedence over the presence or absence of E5xx identities because
the CLI cannot claim complete transition validation. For a remote
publication without retained local recovery state, it takes precedence
whenever the external effect cannot be established, regardless of an
otherwise complete local validation. Ambiguous local publication follows
the recovery-bearing status-2 rule of §2.1.

---

## 4. Creating and obtaining managed stores

### 4.1 `init`

```text
engram init [PATH] [--schema TYPE]... [--dry-run]
```

Creates a managed store and its initial accepted commit. Omitted `PATH`
means the current directory; a relative value is resolved from the
current directory. The target may be absent or an existing real
directory. It must not already own Git administration unless the exact
recognized init intent below makes this invocation its idempotent retry.
For an absent target, its immediate parent must already be an existing
real directory; init creates no arbitrary parent hierarchy.

Before mutating anything, init captures the target and constructs the
prospective candidate from every existing unpruned logical path and byte
plus only missing bootstrap files. Existing reserved, pruned, ignored,
or otherwise non-logical state is preserved physically outside the
candidate and initial commit. Missing `README.md`, `.engram/root.yaml`, and
`.engram/schemas/note.md` use core Appendices A.1–A.3 exactly. A requested
inventory schema is added only when absent or already byte-identical;
any different existing destination is `conflict`. No existing file is
rewritten. The complete prospective snapshot and empty-base transition
are aggregated in one `validation` with `target: "changeset"`. They must
pass before target creation, file writes, or Git setup. If boundary
preflight cannot construct the changeset, an `indeterminate` result has
`files: null`; once construction succeeds, any `issues` or later
`indeterminate` result reports the complete empty-base `files` changeset.
A validation rejection leaves the target byte-identical.

A real initialization then acquires a controller-owned lock keyed by the
prospective canonical target and durably writes an external init intent
before its first live mutation. The intent records the exact invocation,
target absent/present identity and complete pre-publication fingerprints,
prospective snapshot digest and accepted object ID, every missing
bootstrap file with absent preimage and exact final bytes, every missing
ancestor with absent/real-directory images and its planned descendants,
private staging identity, marker/binding generation, and phase. A retry with the
same normalized invocation resumes that recognized intent; a different
invocation or unrecognized pre-existing Git administration is rejected.

For an absent target, init constructs the complete worktree and Git
administration in a private sibling on the target filesystem, including
the accepted root commit, guard, configuration, exclusion, marker, and
ungranted external binding. After rechecking the parent and absent target
it publishes the whole directory with one atomic non-overwriting rename.
For an existing target, it first constructs the complete Git
administration in a private same-filesystem directory. It then creates
recorded missing ancestors shallowest first and publishes only missing
bootstrap files in UTF-8 path order through sibling
temporary files and atomic non-overwriting creation, rechecking every
captured input and ancestor before each change. After all target bytes
equal the prospective checkout and all non-logical bytes remain
untouched, one atomic non-overwriting rename publishes that administration
as root `.git`. The initial ref and commit already exist inside that
administration; there is no interval with a published `.git` that lacks
its accepted root commit.

After publication, init verifies the final marker/binding and accepted
state, durably marks the intent complete, releases the lock, and removes
the intent. A normal failure before target publication rolls back only
exact bootstrap finals and then empty recorded ancestor directories to
their absent preimages, and removes only the recorded private
administration and ungranted binding. A crash or failed
rollback retains the intent and lock and uses the durable error result.
Before `.git` or the absent target is published, `doctor --recover`
performs that same exact rollback; any third path value or changed
captured input blocks without overwrite. After publication it never
removes the accepted store: it verifies the recorded accepted object,
repairs only the recorded marker/binding completion, clears the intent,
and preserves later working-draft edits. All phases are idempotent. The
portable final-recheck window for a non-cooperating writer is the same
explicit limit as §6. `--dry-run` creates no lock, intent, binding, or
private target.

The initial
candidate contains at least:

```text
README.md
.engram/root.yaml
.engram/schemas/note.md
```

When missing, `note` is copied byte-identically from the core canonical
definition; a present file is preserved and must pass the same normative
normal-form check. Repeated `--schema` values name optional inventory types;
duplicates collapse and destinations are considered in UTF-8 type order
before the initial validation and commit.

The command initializes a non-bare Git worktree whose root is exactly
the store root and owns the annex-defined initialization transaction. It
sets the direct unborn `HEAD` target to `refs/heads/main`, independently
of host `init.defaultBranch` or template configuration. It
uses the explicit empty base, performs boundary preflight plus complete
snapshot and empty-base transition validation, and creates a parentless
commit with exact raw message `Initialize engram store\n` only after a complete
result with no `E` finding. No prior-lineage
audit or hook execution applies because initialization has no accepted
base. It also installs the CLI-owned local `pre-commit` guard, which
rejects later commits initiated through ordinary Git clients and directs
the caller to `engram commit`; these two commands are the managed writers
for initialization and later acceptance, respectively. Local setup
disables sparse presentation and Git content transformations, verifies
pathname/byte round-tripping, and adds an owned `.engram/cache/` rule to
the repository-local exclude file without tracking it. Missing Git
author configuration is an operational error rather than a reason to
invent identity.

An incompatible target, pre-existing Git administration without the
exact resumable intent, or unowned local Git hook causes the whole
operation to stop before logical files or an initial commit are
published. There is no destructive force option. When the target
is nested under a different worktree, the command proceeds only if the
outer repository already excludes it or represents it deliberately as
a submodule. `--dry-run` reports the proposed files, repository setup,
local launcher, and initial commit without creating any of them.

### 4.2 `clone`

```text
engram clone URL [PATH]
```

Clones a managed store into `PATH`. Protocol v1 accepts only an exact
lowercase `https://`, `ssh://`, or `file://` URI, or Git's scp-like
`[USER@]HOST:PATH` form with a non-empty host and path and no `/` before
the separating colon. An IPv6 host uses `ssh://`; local path arguments
use `file://`. Control characters, a leading `-`, `ext::`, plaintext
`http://`/`git://`, and every other scheme/helper spelling are rejected
as `usage` before Git starts. The remainder is retained as exact UTF-8
argument bytes rather than URL-normalized.

Every acquisition child starts with inherited `GIT_*` variables removed,
passes the location after an option terminator, sets the default Git
protocol policy to deny, and enables only the transport selected above.
It disables Git-native hook dispatch and never permits an unknown remote
helper. These controls apply verbatim to remote locations selected by
`pull` and `push`; a stored remote outside this grammar is a `capability`
error until the user changes it explicitly.

Every network Git child runs against a controller-created private
repository with system/global configuration and configuration includes
disabled. Its synthesized configuration contains only the exact selected
URL/ref operation, the admitted protocol, and controller-owned object
access; it never imports `url.*.insteadOf`/`pushInsteadOf`, aliases,
`core.sshCommand`, hooks, filters, or executable commands from store,
repository, user, or system Git configuration. Pull/push discovery reads
only the non-included local values of `remote.<name>.url`,
`remote.<name>.pushurl`, `branch.<branch>.remote`, and
`branch.<branch>.merge`, then transfers the selected exact values into
that private configuration. Transport-provided terminal, SSH-agent, and
OS credential facilities remain part of the explicitly authorized
network environment, but they do not rewrite the URL identity used by
the registry or result.

A relative explicit path is resolved against the current directory; its
parent must already be an existing real directory, and clone creates no
arbitrary explicit-path ancestors. When `PATH` is omitted, the final root is
`<data-root>/engram/stores/<digest>`, where `<digest>` is lowercase
hexadecimal SHA-256 of the exact UTF-8 bytes of the `URL` argument; URL
case, escaping, trailing separators, and equivalent spellings are not
normalized. The platform `<data-root>` is:

- macOS: `$HOME/Library/Application Support`;
- Windows: `%LOCALAPPDATA%`; and
- other systems: an absolute non-empty `$XDG_DATA_HOME`, otherwise
  `$HOME/.local/share`.

A missing required home/data variable is a capability error. The
controller creates each absent component of
`<data-root>/engram/stores/` shallowest first under a controller-wide
registry lock, without following links. It reuses an existing component
only when it is a real directory; a symlink, special, uninspectable, or
otherwise conflicting component is an integration or capability error
under the common classification rules. These
durable controller directories are shared infrastructure and are not
rolled back merely because one clone fails. Before inspecting the
destination or registry, every clone acquires the same controller-owned
target lock as init and holds it through reuse, publication, or cleanup.

The controller-owned external registry records the exact URL bytes for each
default destination. If that destination already exists, reuse is
considered without fetching only when the registry binding matches those
bytes and the directory has the exact recorded managed-clone identity;
both binding and accepted ref are re-read under the target lock
immediately before returning. Reuse also requires the exact `origin` URL, fetch mapping, and
accepted-branch upstream values specified below, plus the owned guard,
cache exclusion, and byte-transparent presentation; any drift is a
conflict and is not repaired or fetched by the reuse path. Otherwise
the command reports a conflict. Explicit `PATH` never
implicitly reuses an existing directory. JSON `reused` reports this distinction.
A fresh successful publication has `published: true` and `reused:
false`. Once the exact reuse prerequisites pass, clone completely audits
the stably captured accepted lineage under the current supported rule
set. A complete audit without `E` is a successful default-destination
reuse with both booleans true. A complete audit with `E`, or an
indeterminate audit, also uses the clone command shape with `published:
true`, `reused: true`, the existing root and accepted state, `launcher:
"unchanged"`, and the evaluated validation/audits; its outcome is
respectively `issues`/1 or `indeterminate`/3. It does not become a common
conflict error merely because validation failed. Every reuse outcome
performs no network or target-filesystem mutation; target-lock
acquisition/release and registry reads are coordination, not store
mutation.

After validation and complete private materialization but before any
external binding or final rename, clone durably writes an acquisition
intent keyed by the prospective canonical target under that already-held
target lock. The intent records
the exact URL bytes, explicit/default destination mode, absent target and
parent fingerprints, private directory identity, expected final physical
identities, selected accepted ref/object, marker generation, and any
default URL-registry record. It creates the marker binding as ungranted
and the default URL record as `pending`, then rechecks the target and
publishes the complete directory with one atomic non-overwriting rename.
Only after auditing that exact published identity does it activate the
URL record, verify the marker binding, durably mark the intent complete,
release the target lock, and only then remove the intent.

An exact retry may resume its recognized intent even though the target
now exists. With the target still absent it may resume publication or
remove only its private directory and pending records; with the target
equal to the recorded physical identity and accepted object it completes
only the bindings and intent cleanup. Any other target, object, URL, or
binding value is a conflict and is not overwritten. `doctor --recover`
removes recorded private/pending state before publication or completes
recorded bindings after publication, using the same identity checks.
Both paths are idempotent. A failure after intent creation uses the
durable error shape; an existing destination without the exact active
binding or recognized intent remains an ordinary conflict.

Clone selects exactly the `refs/heads/*` target advertised by the
remote's symbolic default `HEAD` and creates the same full local branch
name; it does not guess from branch enumeration or host defaults. A
missing, detached, non-head, non-UTF-8, or non-commit target fails before
publication under the global representation/error rules. It verifies
the repository shape, complete accepted lineage of that branch, and each
snapshot/transition pair in that lineage before publishing the checkout
at its final path. Acquisition occurs in a private temporary repository
without checkout, with repository hooks, templates, attributes, and
content filters prevented from executing or transforming data. The CLI
audits the selected ref and raw objects first; only after success does it
install its owned guard, establish and verify byte-transparent
presentation, materialize the checkout, and publish the final path.
The published repository has exactly one local URL value
`remote.origin.url` equal to the original URL argument, no `pushurl`, and
one upstream binding for the accepted branch:
`branch.<short>.remote=origin` and
`branch.<short>.merge=<selected-full-ref>`. Its sole fetch mapping is the
exact value
`+<selected-full-ref>:refs/remotes/origin/<short>`. These exact local,
non-included values make argument-free pull/push deterministic; `origin`
is assigned by clone, never guessed by later commands.
Other fetched refs are not part of the managed claim. It adds the same
repository-local cache exclusion as `init` before publication and does
not rerun already committed remote hooks. Clone does not trust store
hooks, attach a project, or authorize later pushes. An unowned
conflicting Git hook rejects final publication rather than being
overwritten. If complete validation reports an `E`, JSON outcome
`issues` returns `published: false`, `reused: false`, the prospective
final `root`, the selected prospective `accepted` state, `launcher:
"planned"`, the exact `verified_commits`, and the validation/audits
without publishing the temporary checkout. An indeterminate managed
audit returns the same fields with outcome `indeterminate` and status 3.
An operational clone failure uses the common error shape instead.

### 4.3 `attach`

```text
engram attach STORE [--project PATH] [--entrypoint FILE]
```

Records that a project uses an independent store. `STORE` is an existing
local managed-store root; a relative value is resolved from the current
directory. Network acquisition is deliberately separate: use `engram
clone URL`, then attach the concrete path it reports. This separation
keeps each command's published effect independent and observable.

The project defaults to the current project's Git root, or the current
directory when no project repository exists. `--project` is only an
explicit override; a relative override is resolved from the current
directory. It is not normally needed:

```bash
cd project-a
engram attach ~/memories/shared-memory
```

The default entrypoint is the project's `AGENTS.md`; a relative
`--entrypoint` is resolved below the project root, while an absolute one
must also resolve below that root. The project and store roots are
canonical physical absolute paths with symlinks resolved. An entrypoint
must be missing or a regular non-symlink UTF-8 file; its parent must
already exist. Attach completely audits the local managed store before
writing. Its top-level `validation` and `audits` have the same meanings
as `clone`; any `E` or indeterminate result leaves `changed: false` and
returns the corresponding non-error outcome. If the store is physically
nested below the project, it also verifies that the outer repository
excludes it or owns it explicitly as a submodule.

The CLI owns only this exact versioned block, encoded as UTF-8 and with
the displayed LF line endings:

~~~markdown
<!-- engram:adoption:v1 -->
Engram stores (spec v1; canonical absolute paths):
```json
{"stores":["/Users/ada/memories/shared"]}
```
Before touching a store, read its root `README.md` and follow the Agent Protocol it carries.
<!-- /engram:adoption:v1 -->
~~~

The JSON line has exactly the member `stores`; it has no insignificant
whitespace and ends with LF. Its non-empty array contains one canonical
physical store-root path per physical directory object identity, sorted
by the path's UTF-8 bytes. Existing serialized paths are re-resolved before
deduplication; an unavailable former store remains distinct by its exact
stored path. When the requested store has the same physical directory
object identity as an existing entry with another final path, the
existing serialized path wins, `changed` is false for that identity,
and JSON `store` reports that winning path. Strings
use `\"` and `\\` for quotation mark and reverse solidus, `\u00XX` with
uppercase hexadecimal digits for U+0000–U+001F, and literal UTF-8 for
every other scalar; `/` and non-ASCII characters are not escaped. Every
other byte of the block, including prose and fence lines, is exactly as
shown. Platform-native path spelling is retained, so a Windows path uses
escaped reverse solidus inside JSON.

With no owned block, attach appends one, first adding one LF only when a
non-empty existing file lacks its final LF. With exactly one valid owned
block, it replaces only that block to insert the path; attaching an
already listed canonical store is an unchanged success. More than one
owned opening or closing marker, wrong marker pairing, or any malformed
owned block is an `integration` error and no byte changes. Unversioned
or differently versioned adoption conventions belong to the project,
not this CLI, and are preserved rather than interpreted.

Before its first read, the command acquires an exclusive advisory lock in
controller-owned external state keyed by SHA-256 of the canonical
entrypoint path's exact UTF-8 bytes. Every `attach`/`detach` invocation
uses that same rendezvous and holds it through unchanged return or
publication, so two CLI updates cannot lose one another; a busy or
unreliable lock is `concurrency` or `capability`, respectively. With the
lock held, the command fingerprints the source entrypoint and its
containing directory, writes the complete prospective bytes to a sibling
temporary regular file, rechecks those fingerprints, and atomically
publishes the replacement without following links. A mismatch is
`concurrency`; an unsupported atomic replacement is `capability`. It
removes its temporary file on every definitive pre-publication failure.
This lost-update guarantee covers cooperating `attach`/`detach`
processes. Portable host filesystems do not offer a common content-CAS
for an arbitrary project file, so callers must separately serialize
non-CLI editors; a non-cooperating write in the final recheck/rename
window is outside the guarantee. Atomic replacement prevents a partial
file but does not claim power-loss durability beyond the host filesystem.
The adoption update itself does not copy, move, delete, commit, trust,
or synchronize memory.

### 4.4 `detach`

```text
engram detach STORE [--project PATH] [--entrypoint FILE]
```

`STORE` is a local path, not a URL. If it exists, detach resolves its
canonical physical directory object identity exactly as attach does and removes the entry
whose re-resolved identity matches, regardless of path alias; if it no
longer exists,
it resolves a relative spelling from the current directory, makes it
absolute and lexically clean without resolving missing components, and
matches that exact UTF-8 string. It removes only that equal entry from
the valid owned block using the same guarded atomic update. If other
stores remain, it rewrites the canonical block; if the last store is
removed, it removes the complete block and no surrounding bytes. A
missing entry is an unchanged success. A block whose currently available
entries resolve to a duplicate physical identity is an `integration`
error rather than an arbitrary choice. On removal, JSON `store` is the
exact serialized path removed from the block. On an unchanged missing
match, it is the canonical physical path derived from an existing
argument or the absolute lexically clean spelling derived from a missing
argument. It never deletes the store,
changes its Git history, revokes separate permissions, or removes
another project's attachment.

---

## 5. Inspecting state

### 5.1 `status`

```text
engram status
```

Shows the accepted commit and branch plus a summary of the repository's
two unaccepted layers:

- **staged** paths form the initial candidate relative to accepted
  `HEAD`;
- **unstaged** and untracked paths are working-draft changes outside
  that candidate.

The summary identifies added, modified, and deleted logical paths in
each layer but does not replace the exact changeset representation from
`diff --staged`. Unstaged pruned repository/tool state is not listed; a
pruned raw index entry instead makes the index operationally ineligible
and causes `status` to fail rather than conceal a staged entry. Dirty
working content is always labeled unaccepted, never memory at `HEAD`.
During an interrupted pull replay it additionally identifies the
original branch/tip, private replay branch, and current replay base as
specified in §10.1.

### 5.2 `diff`

```text
engram diff [REV-A [REV-B]] [--staged|--cached] [--stat|--name-only]
```

With no revisions, the default shows unstaged working-tree differences
relative to the index. `--staged` (with Git-compatible alias `--cached`)
shows the initial candidate in the index relative to accepted `HEAD`;
this is the changeset that `commit` will prepare. One revision compares
that accepted snapshot with the working tree; two compare their accepted
snapshots. Staged selection cannot be combined with revisions.
`--staged` and `--cached` are two spellings of one option and cannot
both be supplied; `--stat` and `--name-only` are mutually exclusive.
Each forbidden combination is a usage error.

A `REV` is exactly the literal `HEAD` or a lowercase full object ID at
the repository's declared SHA-1 or SHA-256 width. Abbreviations, other
refs, ranges, `~`/`^` expressions, and general revision-parser syntax are
not accepted. Resolution reads raw objects without replacement or graft
overlays, and the commit must be an exact member of the completely
audited current accepted lineage. `HEAD` resolves to that lineage's tip.
In JSON, a revision selector's `value` is always the resolved full object
ID, never the argument spelling.

Human output renders content differences. `--stat` reduces that output
to counts and `--name-only` to ordered logical paths. JSON output is the
exact machine-readable difference for the selected pair: its entries
contain only `operation` and normalized `path`, in normative order.
Thus `diff --staged` is both the familiar inspection command and the
public representation of the commit changeset; no separate `changeset
show` spelling exists. If its boundary or raw-index eligibility checks
fail, it returns the common repository error and serializes no partial
`changes`; `check --staged` reports the applicable causal findings and
transition status.

### 5.3 `log`

```text
engram log [-n COUNT] [--oneline]
```

Shows the local accepted lineage without fetching. It displays commit
identity, all raw parents, author/committer bookkeeping, timestamp, and
message, but does not present those fields as record semantics or
provenance. Traversal starts at the exact accepted tip and emits newest
first. A one-parent commit continues to that parent; a zero-parent commit
ends normally; a commit with any other parent count is emitted as the
diagnostic boundary and traversal stops without choosing or visiting a
parent. `-n` counts emitted commits, including that boundary. `COUNT` is
a canonical ASCII decimal integer from 1 through 2147483647 with no
leading zero; when omitted, traversal continues to a root or diagnostic
boundary. `--oneline` changes only human presentation.

A zero- or one-parent traversal ending normally has outcome `ok` and
status `0`. Emitting a well-formed commit with any other parent count
returns the same complete `log` result with outcome `issues` and status
`1`; the commit's `parents` array identifies the boundary. A missing
required object is the common `capability` error, while a present
wrong-type or malformed required raw object is the common `repository`
error. Neither condition silently shortens a successful result or
fetches an object.

### 5.4 `check`

```text
engram check [PATH]
engram check --accepted
engram check --staged
engram check --base BASE --candidate CANDIDATE
```

- With an explicit `PATH`, statically checks that portable logical
  snapshot without managed-store discovery. It is a host filesystem
  path resolved from the current directory, must select an existing
  real directory, and cannot be combined with global `--store` or any
  other selector. With no path or selector, global `--store` selects the
  snapshot root when present; otherwise ordinary upward snapshot
  discovery applies. Neither static form requires Git.
- `--accepted` checks managed repository conformance and, for a linear
  history, every accepted snapshot and transition from its root through
  `HEAD`. At a merge it follows the Git annex's E602 causal truncation
  rule and never chooses a parent merely to claim a complete lineage.
- `--staged` evaluates accepted `HEAD` against the initial candidate in
  the index without running hooks. Unstaged and untracked bytes cannot
  affect it. An unmerged, intent-to-add, or raw index entry that would be
  pruned is an operational index conflict, so no changeset result is
  claimed until the index is resolved.
- Explicit `--base` and `--candidate` must occur together, accept no
  positional path or global `--store`, and are host filesystem paths
  resolved from the current directory to existing real snapshot roots.
  They are not Git revision expressions; they evaluate that transition
  without accepting it or discovering a managed store.

`--accepted` and `--staged` are mutually exclusive, accept no positional
path or explicit snapshot pair, and require ordinary managed-store
discovery. Every forbidden selector combination is a usage error.

`check` is read-only, performs no preparation, and has no `--fix` mode.
Findings retain normative `(code, path)` identity and ordering. In the
JSON protocol, `result` is exactly the shared validation result of §2.1;
machine consumers use `code`, `path`, ordering, and transition
`status`, never optional `detail`.

---

## 6. Working-draft and staging helpers

The draft helpers `fmt`, `new`, `mv`, and `schema copy` edit the managed
working draft but do not stage their output, execute preparation hooks,
or create commits. `add` is the distinct staging helper and changes only
the index. This keeps file editing separate from declaring the
candidate. These helpers do not target an arbitrary exported snapshot
or a disposable hook materialization; hook programs mutate their
supplied candidate through ordinary filesystem operations.

Every mutating helper acquires the annex worktree rendezvous lock before
reading and holds it through publication or retained recovery state. It
therefore serializes with other helpers, `commit`, `revert`, pull replay,
and every conforming automated editor. It computes all output first and
captures exact preimages and fingerprints for every input, output,
ancestor boundary, and index value it may affect. Immediately before
each mutation it rechecks the applicable capture; a mismatch stops as
`concurrency`. The annex's final-window limit still applies to a non-
cooperating filesystem writer.

Before the first worktree or index mutation, the helper durably writes a
controller-owned draft-operation journal containing each exact absent or
present preimage and final image. An item is the complete raw index, a
regular file with exact bytes, or a real directory with its exact set of
operation-planned descendant names/kinds; every missing ancestor
directory is explicit with an absent preimage. Publication creates
directories shallowest first without following links, then files in
UTF-8 path order. Files use sibling temporary regular files plus atomic
replacement; the index uses its native lock/update protocol. On success every item is final, the journal is durably marked
`complete`, the lock is released, and the journal is removed. A detected
failure before any mutation cleans up and changes nothing. A failure or
crash after one mutation retains the journal and lock, returns the
durable error shape when it can return, and blocks later writers pending
`doctor --recover`; it never moves an accepted ref.

Recovery for a non-complete draft-operation journal rolls the operation
back: from one stable observation, each item must equal its recorded
preimage or final image, final items are restored to their preimages, and
preimage items are already satisfied. A created directory containing
only recorded descendants in their allowed preimage/final states is an
operation-owned intermediate, not a third value; recovery restores files
deepest first and removes recorded absent-preimage directories deepest
first only when empty. Any unrecorded entry, wrong kind, non-empty final
removal, or other third value blocks recovery without further mutation.
Thus a crash may expose a visibly partial but
always unaccepted working draft until recovery, while successful return
is all-or-nothing and unrelated bytes are never recovery targets. The
four draft helpers support `--dry-run`; `add` does not mutate until its
complete prospective index passes the checks below. A helper failure
does not authorize cleanup of edits it did not create.

### 6.1 `add`

```text
engram add PATH...
engram add --all
```

Stages the selected logical working-tree changes into the Git index.
`--all` stages every logical addition, modification, and deletion in
the store; it cannot be combined with paths. The command rejects paths
outside the store, pruned or reserved state, and files that already
violate path or normed-text eligibility. It never stages unrelated
paths, runs hooks, or claims that a partially assembled candidate is
conforming.

Each `PATH` is one literal store-relative logical path with `/`, never a
Git pathspec, glob, or revision. If it names a regular file in the
working tree, index, or accepted snapshot, it selects that exact file,
including a deletion when the working file is absent. Otherwise, if it
names a real directory in any of those states or is a strict component
prefix of a changed logical file, it selects every changed logical file
recursively below that prefix, including deletion of an absent former
directory. A path with no exact-file or subtree match is a repository
error discovered after store and index inspection, not a syntax error.
Duplicate and overlapping arguments collapse before the selected paths
are processed in UTF-8 byte order.

`engram add` writes the Git annex's deterministic regular mode for every
selected present file, irrespective of its host executable bit. Ordinary
`git add` remains interoperable when its resulting stage-zero modes match
that rule. The domain wrapper is safer because it applies the logical-
tree, path, and mode checks before updating the index.

After staging, `status`, `diff --staged`, and `check --staged` describe
the candidate that `commit` would attempt to accept. Complete state and
transition validation remains the responsibility of `commit`; staging
one path at a time may temporarily produce an invalid candidate.

### 6.2 `fmt`

```text
engram fmt [PATH...] [--check] [--dry-run]
```

Regenerates only catalog marker regions in requested READMEs, or all
store catalogs when no path is supplied. It does not reformat YAML,
rewrite prose, run hooks, or commit. `--check` is read-only and returns
status `1` when regeneration would change bytes. `--dry-run` reports
the proposed edits without applying them. They may be combined; when
both are present the invocation remains read-only and `--check` controls
the would-change exit status.

Each `PATH` is one literal store-relative logical path, not a glob. It
must name either an existing real content directory, which selects that
directory's `README.md`, or that exact `README.md` file. No descendant
catalog is selected implicitly. Duplicate selections collapse and the
READMEs are processed in UTF-8 path order. With no paths, every logical
content-directory README is selected recursively. A selected valid
`catalog: none` README has no region, is accepted as an unchanged
selection, and contributes no result path. For every other catalog
policy, exactly one valid owned region is required. A selected directory
without a regular README, a `catalog: none` README with any catalog
marker, or another README with missing/invalid marker grammar is a
repository error; `fmt` does not create maps or repair marker grammar.

### 6.3 `new`

```text
engram new TYPE PATH
  --description TEXT
  [--fields FILE]
  [--body FILE|-]
  [--title TEXT]
  [--dry-run]
```

Resolves `TYPE` lexically at the destination, builds record frontmatter,
and updates the containing catalog in the same helper operation.
`PATH` is one literal store-relative logical record path ending in
`.md`; it cannot be `README.md`, enter reserved/tool state, or name a
schema/configuration file. Its real content-directory parent must exist,
and the destination must be absent. `new` creates no ancestor directory. `--fields` is one YAML
mapping containing additional frontmatter fields; it
cannot override the separately supplied `type` or `description`.
Without `--body`, the CLI creates an H1 from `--title` or the filename
and emits headings required mechanically by the schema body rules.

`--fields FILE` and a non-`-` `--body FILE` are host input paths,
resolved from the current directory rather than the store. Each must be
an existing regular file, not a symlink or special entry. A fields file
is UTF-8 without BOM or CR, ends in LF, and contains exactly one
non-empty YAML block mapping under the core YAML restrictions, with its
top-level entries beginning at column zero and no document marker or
directive. It must not contain `type`, `description`, or a reserved
`engram-` key. Its exact mapping-entry bytes, in source order, follow
the generated universal fields; omission contributes no entries.

The generated frontmatter starts with exact `---` plus LF, then
`type: "`, the ASCII type slug, `"`, and LF; quoting prevents a type
such as `true` from acquiring YAML Core scalar semantics. The
description line is `description: ` followed
by a double-quoted scalar and LF: quotation mark and reverse solidus in
`TEXT` are escaped with one reverse solidus, while every other admitted
Unicode scalar is emitted as literal UTF-8. The fields bytes follow,
then exact `---` plus LF. `TEXT` must itself satisfy the core record-
description grammar.

`--body -` reads stdin to EOF; no other input does. A supplied body is
copied byte-for-byte after the closing frontmatter delimiter and must be
empty or be normed UTF-8 text ending in LF. `--title` cannot be combined
with `--body`. Without a body, the title must be a non-empty single-line
normed string without forbidden controls; when omitted it is the
destination filename without `.md`, with each ASCII hyphen replaced by
one space and no case conversion. The generated body is exactly `# `,
the title, and LF, followed for each resolved `body.required-sections`
entry in declared order by LF, `## `, the entry, and LF.

The command never invents domain values. Missing required fields or an
unresolvable type reject the helper operation before files change.

### 6.4 `mv`

```text
engram mv FROM TO [--dry-run]
```

`FROM` and `TO` are distinct literal store-relative logical record paths
under the same content-record path grammar as `new PATH`. `FROM` must be an
existing regular record, `TO` must be absent, and `TO`'s real content-
directory parent must already exist; no ancestor is created. Equal
arguments are a usage error, a missing/ineligible source is a repository
error, and an existing destination is a conflict.

The command moves that one record and regenerates affected catalogs as
one helper operation. It rewrites every inbound wikilink whose resolved
target is `FROM` to the store-root-relative `TO` without `.md`. In a
Markdown body it replaces only the target bytes and preserves every
other wikilink byte, including an optional label.

An affected record-frontmatter value is rewritable only when its complete
YAML scalar presentation is an unambiguously ranged, single-physical-line
single-quoted or double-quoted scalar. If any affected value uses another
presentation, `mv` returns the common `conflict` error before any helper
change; it never reformats a mapping or guesses a block-scalar extent.
For a rewritable value, the decoded leading/trailing ASCII whitespace and
optional label are retained and the changed complete decoded string is
serialized as one double-quoted YAML scalar. Inside it, U+0022 QUOTATION
MARK is emitted as `\"`, U+005C REVERSE SOLIDUS as `\\`, and each C0 or
C1 control code point as `\xHH` with two uppercase hexadecimal digits;
every other code point is emitted unchanged in UTF-8. Replacements are
not rescanned. Every source byte outside that scalar token, including
separation whitespace and a following comment, remains unchanged.

The command also rewrites every local
CommonMark link or image destination whose resolution would otherwise
change because either its target is the moved record or its containing
document is the moved record. A destination supplied by a reference
definition is one destination source range and is rewritten at most
once. Labels, titles, and the decoded suffix beginning at the first `?`
or `#` are preserved.

For each CommonMark destination, first resolve its original decoded path
against the original containing document. If that target is `FROM`, its
desired final target is `TO`; otherwise the desired target is unchanged.
The final containing document is `TO` when that document is `FROM` and
is otherwise unchanged. If the original destination spelling already
resolves from the final containing document to the desired final target,
its bytes remain untouched. Otherwise the replacement path is computed
from the final containing directory to the desired target: remove the
longest common prefix of logical path segments, emit one `..` for each
remaining containing-directory segment, append the remaining target
segments, and join them with `/`. A directory target retains one final
`/`; the current containing directory is spelled exactly `./`. A file
target has no final `/`. The result has no `.` segment and no redundant
`..`, and never has a leading `./` except for that exact current-
directory spelling.

A replaced destination token is serialized exactly as `<`, then the
replacement path followed by the unchanged decoded query/fragment
suffix, then `>`. Within that value U+0026 AMPERSAND is emitted as
`&amp;`, U+005C REVERSE SOLIDUS as `&#92;`, U+003C LESS-THAN SIGN as
`&lt;`, and U+003E GREATER-THAN SIGN as `&gt;`; every other code point is
emitted unchanged in UTF-8. This preserves the resolved suffix while
making every rewritten destination byte-deterministic. All source bytes
outside the destination token remain unchanged. The same CommonMark
parser and code-span/fence exclusions as `check` apply. Version v1 does
not move whole directories. There is no option to suppress these
rewrites.

Helper result path arrays contain byte-changing outputs, not merely
inputs inspected. `fmt.paths` is the ordered logical `README.md` paths
whose catalog bytes would change. `new.catalogs` and `mv.catalogs` are
the ordered logical `README.md` paths whose catalog regions would
change; a valid `catalog: none` map never appears. `mv.paths` contains
each Markdown document whose non-catalog link bytes are rewritten, using
its final logical path (therefore `TO` for the moved record), and excludes
the move itself when its content bytes need no link rewrite. A README
whose prose links and catalog both change appears in both corresponding
arrays. Dry-run/check modes report the same would-change arrays; a real
success reports the paths actually changed. Empty arrays are `[]`, and
all four arrays use UTF-8 path order.

### 6.5 `schema`

```text
engram schema inventory
engram schema list [--at PATH]
engram schema show TYPE [--at PATH]
engram schema copy TYPE [--to SCOPE] [--dry-run]
```

- `inventory` lists the non-normative schemas bundled with this CLI.
- `list` reports schemas lexically visible at `--at` and their winning
  source paths.
- `show` prints the resolved local schema file.
- `copy` copies a curated file into `SCOPE/.engram/schemas/`.

`inventory` is a binary invariant: failure to parse one of its bundled
schema descriptors is an `internal` error and no partial inventory is
returned. For `list`, each lexically selected local candidate must be a
regular, readable, conforming schema file from which every schema-
description member can be constructed. An invalid or unavailable schema
directory boundary or selected candidate makes the whole command a
common `repository` error; no candidate is omitted and there is no
fallback to an ancestor. `show` likewise succeeds only for an existing,
fully conforming selected local candidate, and otherwise returns the
common `repository` error without partial descriptor or content. These
discovery commands do not turn schema defects into a partial protocol;
the corresponding `check` command reports their normative findings.

`PATH` and `SCOPE` are logical content-directory paths relative to the
selected store root; both default to the store root when omitted. They
must name an existing traversable logical directory (`SCOPE` itself may
not be reserved), while `copy` may create only the final
`.engram/schemas/` directory needed at that scope.

`copy` is intentionally not called install or profile add. The copied
file becomes an ordinary local schema with no upstream ownership or
automatic update relationship. Existing files and shadowing are
rejected.

---

## 7. Acceptance

### 7.1 `commit`

```text
engram commit -m MESSAGE [--dry-run]
engram commit --dry-run
```

Accepts the staged initial candidate through one managed
transaction. The command has no pathspec arguments: selection belongs
to `add`, and the resulting whole snapshot and transition must conform
even when other working-tree changes remain unstaged. A real commit
requires a non-empty message; `--dry-run` needs none because it creates
no commit. Every invocation performs steps 1–5 below. A real invocation
with a non-empty initial changeset continues through steps 6–10; a
non-empty `--dry-run` executes preparation, validation, and safety proof
through step 8 but does not create a commit object or journal:

`MESSAGE` is a non-empty valid UTF-8 argument containing neither NUL nor
CR and not ending in LF; internal LF is admitted. The raw commit message
is its exact bytes followed by exactly one LF. The fixed init and default
revert messages use the same serialization, yielding respectively
`Initialize engram store\n` and
`Revert <full-commit-object-id>\n`. Replay alone preserves the source
commit's complete raw message instead of applying this rule again.

For a newly authored commit, the effective Git identity must provide a
non-empty valid-UTF-8 name and email. Name rejects `<`, `>`, NUL, CR, and
LF; email additionally rejects every ASCII whitespace byte. The CLI
captures one clock instant and numeric UTC offset. Each simple raw
identity header consists, in order, of the literal kind, one ASCII
space, the name, one ASCII space, literal `<`, the email, literal `>`,
one ASCII space, seconds, one ASCII space, zone, and LF. Seconds and zone
use the canonical tokens from §2.1, and no `encoding` header is written. A
normal or initial commit uses that same captured identity/instant for
author and committer. A replay preserves the original source author
header bytes exactly and writes only a new canonical committer header;
if the source has no single simple author header, replay is a repository
error rather than inventing one.

Every commit object authored by this CLI has exactly this header order:
one `tree <full-object-id>` line; one `parent <full-object-id>` line for a
non-root commit and no parent line for initialization; one `author ...`
line; and one `committer ...` line. The header block then ends with one
empty LF-terminated line followed by the exact message bytes above. A
normal, init, or revert author line uses the canonical identity form; a
replay places its preserved simple source-author line in that same
position. The CLI writes no `encoding`, signature, mergetag, or other
header. Tree and parent IDs are complete lowercase object IDs. The raw
tree uses `40000` for directories and the Git annex's base-preserving/
new-`100644` rule for regular files; index, worktree, temporary-file, or
hook-output permission bits cannot alter it.

1. verifies ownership of the local Git guard and the byte-transparent
   managed presentation; a real commit installs or refreshes a missing
   owned guard, while `--dry-run` only proves that the planned install is
   conflict-free and performs no installation;
2. resolves the direct non-symbolic accepted branch named by symbolic
   `HEAD`, acquires its shared ref/worktree locks, and captures a stable
   double observation of `HEAD`, the accepted ref, and exact raw index as
   the base/index;
3. verifies the complete captured accepted lineage, reusing only a
   content-addressed audit for that exact unchanged tip and identical
   digested normative rule set after rechecking local ancestry/object
   completeness, and only when the result remains in the current process
   or has controller-authenticated external provenance and integrity;
   store/repository cache files are hints, never audit authority;
4. first requires the captured raw index to have exactly one stage-zero
   entry per present path, no intent-to-add entry, and no reserved raw
   path that an accepted tree would prune; only then materializes the
   initial candidate from those entries, excluding every other worktree
   byte whether unstaged, untracked, or ignored. A stage-zero symlink,
   gitlink, or special logical mode remains materialized as a structural
   boundary for the next step's E103/E104 evaluation rather than being
   followed or coerced;
5. runs the complete path, kind, schema-tree, and hook-tree boundary
   preflight before constructing the initial changeset;
6. prepares hooks once against that disposable materialization,
   producing the final candidate;
7. requires complete state and transition validation with no `E`
   findings and a `complete` evaluation;
8. rejects before acceptance with the common `conflict` error if hook-
   generated bytes cannot be reflected without overwriting unstaged
   content, and fingerprints every live path, index, configuration,
   attribute, environment, and presentation input on which that decision
   depends;
9. creates one single-parent commit whose raw tree is exactly the final
   logical snapshot without moving a ref, durably writes the annex
   recovery journal with old/new IDs, reconciliation plan, and
   fingerprints, rechecks the symbolic `HEAD`, accepted branch, and all
   safety fingerprints, and on any pre-CAS rejection durably marks the
   journal `cancelled` before cleanup; a cleanup failure retains that
   recovery state and uses the typed error result, while an ambiguous CAS
   outcome leaves it `pending` and returns the common `concurrency` error
   with `recovery_required: true`; and
10. compare-and-swap updates the accepted branch, then updates the index
   to the Git annex's exact stage-zero path/blob/mode equivalence with the
   accepted tree and reconciles generated bytes back to the worktree
   through byte-raw mechanisms that consult no uncaptured mutable presentation input;
   full reconciliation durably marks the journal `complete`, releases
   the locks, and only then removes the completed journal.

Only a resolved, eligible index whose logical candidate is proven
byte-identical to accepted `HEAD` after boundary preflight is a
successful no-op with no commit, even when the working tree is dirty. An
unmerged, intent-to-add, pruned, wrong-kind, or otherwise ineligible
index never becomes a no-op. `--dry-run` verifies local-launcher ownership and
installability and, for a non-empty changeset, performs initial-candidate materialization,
preparation, final-candidate safety checks, and validation, but it does
not install the launcher, create a commit, or update the index or
worktree. It is the public way to exercise the complete preparation path
without accepting its result; there is no separate
changeset-preparation command. Commit never fetches or pushes.

### 7.2 `revert`

```text
engram revert COMMIT [-m MESSAGE] [--dry-run]
```

Requires a clean logical index and worktree and rejects a root commit or
a commit outside the current accepted lineage. It constructs the inverse
of `COMMIT` relative to its sole parent and declares that inverse against
current accepted `HEAD` as the initial candidate of a new internal
transaction. Application evaluates every path simultaneously against
the unchanged current snapshot:

`COMMIT` uses exactly the `REV` grammar and raw accepted-lineage
resolution of §5.2; no other revision spelling is accepted.

- for an original addition, current bytes equal to its postimage are
  removed, absence is already satisfied, and any other value conflicts;
- for an original deletion, absence receives the original preimage,
  current bytes equal to that preimage are already satisfied, and any
  other value conflicts; and
- for an original modification, current bytes equal to its postimage are
  replaced by the original preimage, current bytes equal to that
  preimage are already satisfied, and any other value conflicts.

The command applies all non-conflicting path outcomes tentatively to one
complete current regular-file map. If any resulting regular-file path is
a strict path-component prefix of another, that structural collision is
also a conflict; its path set includes both members of every colliding
pair, including unchanged survivors. The reported conflict list is the
deduplicated union of those paths and all exact-preimage conflict paths,
in UTF-8 byte order. On any conflict no path, index byte, or worktree byte
changes, and no file-to-directory conversion is attempted.

Otherwise it then runs ordinary hooks and complete validation and, if
accepted, creates a new commit. Without `-m`, the deterministic message
is `Revert <full-commit-object-id>` before §7.1's final-LF
serialization. It never resets a branch or rewrites history. A patch
conflict, policy violation, stale link, or invalid resulting snapshot
rejects without changing the index or worktree.

---

## 8. Hooks and trust

```text
engram hooks list [--state accepted|working]
engram hooks trust [--state accepted|working]
engram hooks revoke [HOOK...]
```

The default `--state` for `list` and `trust` is `accepted`.
`list` shows normative execution order, relative path, selected
interpreter, SHA-256 digest of exact bytes, and local trust state.
`trust` displays the complete selected set and records an explicit
external authorization for that complete ordered set plus every path and
byte digest. `revoke` removes trust for any set containing a named hook,
or for all of the store's sets when no name is supplied.

`list` and `trust` must first select a complete structurally valid hook
set in the requested state. An invalid or unavailable hook-tree boundary,
program name, program kind, first line, or selected-interpreter token
returns the common `repository` error with no partial hook array; `trust`
performs no registry mutation. Interpreter installation or launchability
is not tested by these two commands and does not change the description;
an acceptance attempt classifies a later launch failure as `hook`.
`revoke` operates only on external historical grant records and therefore
does not require the current store hook tree to be valid.

Each `HOOK` argument is one complete ASCII program filename admitted by
core §8.2, with no `/`; it denotes exactly
`.engram/hooks/prepare-changeset/<HOOK>`. Duplicate arguments collapse.
Under the current external binding, revoke removes every historical set
grant whose stored canonical record contains at least one denoted path,
even when that program no longer exists in the store. With no arguments
it removes every set grant under that binding. `revoked_sets` contains
the removed set digests in ascending ASCII order, and `changed` is true
exactly when that array is non-empty.

The set SHA-256 preimage is this canonical UTF-8 JSON record plus LF,
with no insignificant whitespace:

```json
{"version":1,"hooks":[{"path":".engram/hooks/prepare-changeset/20-catalog.py","sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}]}
```

Top-level members are ordered `version`, `hooks`; hook members are
ordered `path`, `sha256`; hook objects use normative execution order;
paths use their exact logical ASCII bytes with `/`; and each lowercase
digest is SHA-256 of the hook's exact file bytes. The displayed set
`sha256` is SHA-256 of that complete record including its LF. This
framing also defines the empty-set digest preimage as
`{"version":1,"hooks":[]}\n`.

Trust is kept in the platform user configuration area, outside the
store and outside its commits. A random marker in the Git common
administration directory is only one half of local store identity. The
external configuration registry binds that marker to one physical
common-Git-directory binding identity as defined in §2.1, including both
final path and stable filesystem object ID. Trust lookup succeeds only when
both values match that controller-owned binding and the marker is not
bound to another common directory. `init` and `clone` create that marker
and binding; linked worktrees resolve to and share the same common
directory and identity.

`hooks trust` is the sole public repair path for an otherwise valid raw
clone, moved repository, or copied repository. It first displays the
selected complete set. Trust and revoke serialize on a controller-owned
registry lock for the physical common-directory binding identity. The operation
is phased and idempotent, not a claimed cross-filesystem transaction:

1. if no marker/binding exists, write a fresh random marker and then a
   matching external binding in state `ungranted`;
2. if a copied marker is bound to another common directory, replace only
   this repository's marker with a fresh one and create a new `ungranted`
   binding, never moving the old binding or any grant;
3. re-read the marker, physical binding identity, selected hook paths/bytes, and
   set digest; only if all still match, write the external grant for a
   non-empty set as the final authority-conferring step. An empty set
   completes with the valid binding and no grant record.

A crash or failure before step 3 may leave a marker or `ungranted`
binding, but it leaves no code-execution authority; the next `hooks
trust` repairs or rotates it using the same phases. An operation that
will execute a non-empty selected set requires one matching marker,
binding generation, physical binding identity, set digest, and final
grant. An acceptance operation with the empty set still requires its
matching marker and binding as owned integration identity, but no grant;
`trusted` is then true. `hooks revoke` removes matching grants before
optional orphan cleanup, so interruption cannot restore authority.

Read-only commands do not fail merely because integration identity or a
grant is absent: `hooks list` reports `trusted: false`, `doctor`
diagnoses the condition, and raw managed checking remains available.
`hooks trust` is the repair operation. A command that requires owned
integration rejects an unbound, copied, or duplicated marker as
`integration`; execution of a non-empty ungranted set is instead
`trust`. An ungranted empty set with a valid binding is accepted and
confers no code-execution authority.
Moving or copying a repository therefore requires this explicit new
local binding and authorization rather than inheriting trust from
repository-controlled bytes.

Trust keys contain that externally bound identity and the canonical set
digest; they are not assembled from independent per-hook grants.
Addition, deletion, rename, or byte change therefore requires
authorization for the new non-empty set. An empty set needs no grant.
These commands do not execute hooks and do not grant write or network
authority.

There is no command for running one selected hook. Preparation executes
the complete applicable base-state set or rejects the attempt.

---

## 9. Automatic local Git integration

`init` and `clone` install a CLI-owned `pre-commit` guard under an owned
hooks directory in the Git common administration area and set the
repository-local `core.hooksPath` to that exact absolute directory.
`commit` ensures that both remain present before accepting a staged
candidate. Existing effective hook-path configuration or an active
unowned hook that this binding would displace is an `integration`
conflict; the CLI never chains, overwrites, or silently disables it.
There is no public `engram git` command family. The guard and its private
CLI entrypoint are implementation plumbing, not logical store content or
store-hook trust.

The installed file is a minimal POSIX `sh` launcher with a CLI ownership
marker, launcher version, and an exactly quoted absolute path to the
`engram` executable that installed it. It contains no validation or
preparation logic: it `exec`s the private guard entrypoint. That
entrypoint verifies the managed-store context, makes no logical change,
runs no store hook, and rejects every raw `git commit` with a diagnostic
directing the caller to `engram commit`.

A `pre-commit` process returns before Git creates the commit, so it
cannot retain the writer lock through commit creation and compare-and-
swap or reconcile the checkout afterwards. Only `engram commit` owns
that full lifecycle. It creates the commit through Git plumbing that
does not invoke the guard.

Every Git child initiated by the CLI starts with all inherited `GIT_*`
variables removed. The controller supplies the intended repository,
worktree, disposable index/object locations, and configuration through
explicit arguments and restores only its own operation-specific
variables with fixed meanings. Every Git operation inside the managed engine overrides
`core.hooksPath` with a controller-owned empty directory and suppresses
all Git-native hook dispatch, including reference-transaction and
checkout hooks. The private guard is inspected and installed by direct
filesystem path, never by asking Git to dispatch it. Local raw-object
reads also disable promisor/lazy fetching; a missing object needed for a
complete claim is `capability`, and a local command makes no connection
even when repository configuration advertises a promisor remote.

`doctor` reports a missing, stale, or conflicting guard, an invalid local
store identity, or an executable path/version mismatch. A checkout
obtained with raw `git clone` is readable, but raw `git commit` remains
unsupported; the first `engram commit` may install a missing guard before
it begins the transaction.

The guard is an interoperability aid, not a security boundary. Removing
or bypassing it, rewriting refs, amending history, merging, or creating
commits through lower-level Git plumbing is outside the supported write
path and cannot support a managed-conformance claim merely because Git
accepted it.

Each linked Git worktree has its own working draft, index, and
worktree lock. The engine uses the exact common-ref and per-worktree lock
rendezvous paths from the Git annex, acquiring them in normative order
and leaving a conservative stale lock after a crash for explicit
recovery. Distinct worktrees may prepare independently when they do not
contend for the same accepted ref; attempts against one ref or worktree
serialize.

---

## 10. Repository synchronization

### 10.1 `pull`

```text
engram pull [REMOTE [BRANCH]]
engram pull --continue
engram pull --abort
```

`REMOTE` is a configured remote name, never an inline URL. With no
arguments, the accepted branch must have one non-included local configured upstream and
that exact remote/full branch is selected. With `REMOTE` only, the
selected branch is the accepted branch's short name on that remote. An
explicit `BRANCH` is a valid UTF-8 branch name relative to
`refs/heads/`; a full `refs/heads/` spelling is not accepted as the
argument. The resolved remote URL must pass §4.2's transport grammar.
Pull requires exactly one non-included local `remote.<name>.url` and
fetches only the selected full ref into private workflow state, ignoring
configured fetch refspec destinations. Push requires exactly one
non-included local `pushurl`, or exactly one local `url` when no
`pushurl` exists; multiple values are a `repository` error rather than
multiple network effects.

A new pull (the form without `--continue` or `--abort`) starts only with
no active replay, a raw index tree-equivalent to accepted `HEAD`, and a
logical worktree byte-identical to that index. It fetches the selected
upstream, validates the required ancestry and every incoming
snapshot/transition, and then either advances the accepted branch by
fast-forward or replays divergent local commits linearly through the
managed preparation and validation engine. It never creates a merge
commit and never reruns hooks from already accepted remote commits.

`--continue` and `--abort` accept no remote or branch arguments and
require exactly one complete intentional active replay with no separate
recovery-required journal. Continue requires an eligible index and a
logical worktree byte-identical to that index; the staged tree is the
complete resolution candidate, including the explicitly allowed clean
no-op case below. Abort instead stably captures the current active index
and worktree, performs no hook or network operation, and explicitly
discards that resolution draft while restoring only the original state
recorded by this pull; any concurrent post-capture change blocks. Either
form without valid active metadata is a repository error.

After fetching, every local mutation follows the Git annex's exact
synchronizer protocol. The command captures and audits exact full object
IDs, acquires every affected full-ref lock in raw-refname byte order and
then the worktree lock, obtains stable captures, and records the required
checkout-safety fingerprints. Before each branch
creation or update, symbolic-`HEAD` switch, index change, or checkout
reconciliation, it rechecks the applicable object IDs, `HEAD`, and
fingerprints; any mismatch stops without assuming that a prior check or
network observation is still current. Each actual ref, `HEAD`, or index
mutation uses Git's short-lived native atomic lock/update protocol;
preparation never pretends to hold such a Git lock across hooks. The
`--continue` and `--abort` forms use the same protocol for their
remaining mutations.

Every clean symbolic-`HEAD` switch, including the switch to the verified
private/remote tip before replay begins, first journals and then installs
the exact target-tree-equivalent raw index plus its byte-identical logical
worktree image. Replay does not begin from a stale index or a checkout
whose incoming `100755` modes were normalized. The same exact images are
used for the final switch and abort restoration.

Every non-error pull result that leaves no active replay returns with the
raw index exactly tree-equivalent to the accepted tree named by `HEAD` —
one matching stage-zero path, blob ID, and mode per regular file — and a
logically clean worktree byte-identical to that index. This includes
up-to-date, fast-forwarded, fully replayed, incoming-lineage rejected, and
aborted results; pruned non-logical state remains untouched. A conflict,
candidate rejection, or typed operational error after the coherent
handoff may instead leave only the explicitly identified staged replay
draft described below. Any accepted update whose final index/worktree
images are not yet installed returns the typed recovery error rather than
a completed pull result.

Before the first local ref, `HEAD`, index, worktree, or replay-metadata
mutation, each pull form durably writes one pull-operation journal under
the held locks. It records the original branch/tip and clean
index/worktree preimages, remote/ref/tip, common ancestor and ordered
source commits when applicable, every private/original ref expected
old/final value, symbolic-`HEAD` and checkout preimages/finals, pull-owned
metadata, and the exact per-item reconciliation plan. Its durable phases
distinguish prepared, private-ref creation, checkout switch, replaying,
original-ref publication, final checkout, and cleanup. The next phase is
durable before its mutation and completion is durable afterwards; each
item uses the annex's preimage/final/third-value and pending-ref ABA
rules. Managed transactions during replay are subrecords of this same
owner/journal rather than independently recoverable competing journals.
Every coherent terminal result or intentional active-replay handoff
durably marks the journal `complete`, releases all owned locks, and only
then removes the journal. A definitive failure that proves no recorded
effect was or can be published instead marks it `cancelled`, releases
the locks, and then removes it. A crash before removal therefore always
leaves enough state for idempotent cleanup.

A semantic conflict becomes an intentional coherent active replay only
after its private branch, clean checkout, ordered conflict list, and
metadata are all durable; the ordered completion rule above is followed
before returning. That active state is not recovery-needed:
`pull --continue` or `--abort` is the next explicit operation, and each
writes a fresh journal before mutation. Plain `pull` while active is a
repository error.

After a crash, `doctor --recover` first proves the journal owner dead and
takes over the same locks. Before original-branch publication it restores
each journaled item to its exact captured preimage: an item with a
present preimage is restored to those raw bytes, while an item with an
absent preimage is removed only when it still equals its recorded final
image. This includes `HEAD`, index, worktree, private refs, and metadata,
so recovery of an interrupted `--continue` or `--abort` restores rather
than deletes pre-existing active-replay state. Any third value blocks. A
pending original ref still equal to its old value is ABA-ambiguous and
blocks instead of being rolled back. If that ref equals the recorded new
value, or publication was durably completed, recovery never moves it
back: it finishes each recorded final checkout/index/ref/metadata image
from its captured preimage, removes only absent-preimage items, and
cleans the journal. Any third ref/checkout value blocks without
overwrite. Every recovery step is idempotent and runs no hooks or
network operation.

For divergent history, the command records the original accepted branch
and tip, creates a private pull-owned branch at the verified remote tip,
and switches the clean checkout to that branch. It requires the Git
annex's nearest exact common ancestor and processes exactly the captured
local commits after it from oldest to newest; unrelated histories return
the common error with `kind: "conflict"` without creating replay state.
Each replayed logical changeset
is then a managed transaction whose base is the private
branch's current `HEAD`. It uses the Git annex's simultaneous exact-
preimage algorithm: absent/equal complete bytes can be applied or
recognized as already satisfied, but any other value conflicts; it never
invokes Git's textual merge machinery. A recreated commit preserves the original local
commit's exact raw message, author identity, and author timestamp; it
uses the current configured committer identity and current timestamp,
and its tree/parent come from the new prepared candidate/base. The
original branch remains unchanged until all replays succeed; final
publication compare-and-swap updates it only if it still names the
recorded tip, switches the checkout back, and removes the private branch
and replay metadata. If the local original-ref CAS result cannot be
established, the pull journal remains pending and the command returns the
common `concurrency` error with the typed recovery result and
`recovery_required: true`; it does not return a structured
`indeterminate` pull result.

If replay needs semantic conflict resolution, the complete ordered
conflict-path list is reported and no path from that source changeset is
applied: the private branch checkout and index remain clean at its
`HEAD`. If exact application succeeds but preparation or complete
candidate validation rejects the result, the command instead durably
publishes the exact unprepared initial candidate to the private
worktree and index, records an intentional active replay with an empty
conflict list, and discards all rejected hook output before returning.
This gives the user or agent one concrete staged draft to correct; it
does not expose a partly prepared candidate. Both active-state handoffs
complete under the pull journal before it is removed. `status`
identifies the active replay, original branch/tip, source parent,
conflict paths, and staged/unstaged resolution layers.
`diff
--staged` and `check --staged` keep their ordinary `HEAD`/index meaning.
`engram commit` is rejected while replay metadata exists; after the user
or agent resolves and stages the complete draft, `pull --continue`
accepts it on the private branch and proceeds with later commits. An
explicit `--continue` with a clean eligible index chooses the current
private state as the complete resolution, marks the source changeset
processed without an empty commit, and proceeds.
Entering that resolvable conflict state, or rejecting an incoming
lineage after complete validation, returns the structured `pull` result
with outcome `issues` and status `1`. A network, capability, trust, I/O,
or other operational failure instead uses outcome `error`, status `2`,
and the common error shape.

A selected-hook trust failure or core §8.3 preparation rejection uses
the common `trust` or `hook` operational error and status `2`, after the
same coherent active-replay handoff; its typed durable result has
`recovery_required: false`. A complete candidate validation rejection
uses the structured `pull` result and status `1` or `3` below. If the
handoff itself cannot complete coherently, the operational error instead
retains the pull journal/locks with `recovery_required: true`.

For `up-to-date`, `fast-forwarded`, or incoming-lineage `rejected`, the
top-level `validation` covers the complete would-be local accepted
lineage; `candidate_validation` and `changes` are `null`. During an
active replay, the top-level result covers the currently accepted
private lineage. In every structured pull result, `audits` contains
exactly the root-to-tip history audits belonging to the same lineage as
that top-level validation, with its causal truncation; audits of another
original, incoming, or private lineage inspected during the operation are
not mixed in. For terminal `replayed` and `aborted` results, both the top-
level validation and `audits` cover the complete accepted lineage named
by `after`; for abort that is the unchanged original lineage restored by
the operation. `candidate_validation` is non-null only when the current
attempt is rejected after evaluating a candidate: it covers the prepared
final candidate and transition, not merely the staged initial candidate;
`changes` is that final candidate's definitive changeset. A boundary
failure before a changeset can be constructed instead returns the
indeterminate candidate validation with `changes: null`.

Complete final-candidate validation with an `E` returns state `rejected`,
outcome `issues`, and status `1`; an indeterminate candidate returns
state `rejected`, outcome `indeterminate`, and status `3`. Rejected hook
output is discarded while the exact staged initial candidate—either the
mechanically applied source changeset or the user's staged resolution—
and replay metadata remain for correction. A patch conflict has state
`conflict`, `changes: null`, and `candidate_validation: null`. Completed
or aborted pulls also set both candidate fields to `null`, including
after several successful replay transactions.

`conflicts` is `[]` for `up-to-date`, `fast-forwarded`, `replayed`,
incoming-lineage `rejected`, and `aborted` results. While an active replay
has an unresolved source changeset, it is the exact persisted ordered
conflict list for both `conflict` and any candidate-`rejected` result,
including `--continue`; successful continuation either replaces it with
the next source conflict list or returns `[]` when no conflict remains.

In every result, `before` is the original accepted branch/tip captured
when this pull began; `after` is the exact Git state currently named by
`HEAD`, including the private replay branch while replay remains active.
`fetched` is the number of distinct commit object IDs on the selected
remote tip-to-causal-boundary walk that are absent from the captured
original local accepted lineage. It includes the first present E601 or
E602 boundary commit and excludes ancestry not visited beyond that
boundary; a missing required commit is instead a capability error with
no pull result. For a completely valid lineage this is exactly the
incoming segment after the common ancestor. It never counts pack objects,
bytes, or Git's transport report. `replayed` is the cumulative number of local
source changesets processed successfully since that pull began,
including already-satisfied or explicitly resolved no-ops that create no
commit. Both counts retain those meanings for `--continue` and
`--abort`.

The checkout is reserved for that replay until `--continue` or `--abort`.
`--abort` discards the pull-owned conflict draft, switches back to the
unchanged original branch, and removes the private branch and replay
metadata. Because a new pull required a clean start and reserves the
checkout, every stably captured logical index/worktree difference while
the active replay is coherent is the resolution draft that explicit
`--abort` authorizes discarding. Pruned/non-logical bytes remain
untouched, and a change after the abort capture still blocks rather than
being overwritten.

### 10.2 `push`

```text
engram push [REMOTE [BRANCH]]
```

Remote and branch selection is identical to `pull`; an omitted
configuration is an operational repository error rather than a guessed
`origin` or remote default branch.

Publishes only accepted commits. It verifies that the destination can
advance from its observed tip to the completely validated local linear
lineage and updates it with compare-and-swap semantics. Version v1 has
no force, deletion, or remote-repository provisioning option. Creation
of the selected branch is only the explicit observed-absent case defined
below. Authentication, credential use, and publication authority belong
to the environment that explicitly invokes the command.

Push resolves its configured destination, captures local `after`, and
completely audits the local managed lineage before observing the remote
or initiating network access. A complete local validation with an `E`
returns outcome `issues`, status 1, state `rejected`,
`remote_observed: false`, `before: null`, `commits: 0`, and `changed:
false`, with that validation and its evaluated audits. An indeterminate
local validation returns the same fields with outcome `indeterminate`
and status 3. A capability failure that prevents a managed result uses
the common error shape. Every later complete push result has
`remote_observed: true`; `before: null` then means the remote branch was
observed absent rather than unobserved.

When remote `before` is null or an exact commit in the local accepted
lineage, `commits` is respectively the complete local lineage length or
the number of commits strictly after `before` through local `after`; it
never counts Git objects or pack-transfer output. Equal tips return
outcome `ok`, state `up-to-date`, `commits: 0`, and `changed: false`. A
successful creation or fast-forward returns outcome `ok`, state
`pushed`, and `changed: true`.

If a non-null remote `before` is not in the audited local accepted
lineage, push performs no update and returns the complete command result
with outcome `issues`, status 1, state `rejected`, `commits: 0`,
`changed: false`, that
observed `before`, local `after`, and the local managed `validation` and
`audits`. A remote compare-and-swap race after this observation is
instead the common `concurrency` error; it is never retried as force.

If the transport cannot establish whether the remote conditional update
succeeded, push performs no automatic retry and returns the complete
command result with outcome `indeterminate`, status 3, state
`indeterminate`, `changed: null`, the last observed `before`, intended
local `after`, and the attempted fast-forward `commits`, validation, and
audits. The common durable error shape is not used because no remote CAS
is known to have succeeded. A later explicit push invocation must
observe the remote afresh and treats equality with local `after` as the
ordinary `up-to-date` result; it never infers who caused that state.

---

## 11. Advisory and runtime commands

### 11.1 `doctor`

```text
engram doctor [PATH] [--recover] [--format text|json]
```

`doctor` is the discovery exception needed to inspect interrupted
creation before a managed root exists. A positional `PATH` cannot be
combined with global `--store`; it is resolved from the current
directory. For an existing target, the CLI resolves its physical root.
For an absent target, its immediate parent must be an existing real
directory, and the prospective canonical target is that resolved parent
plus the final component without following it. `doctor` first obtains a
stable double observation of the corresponding external target lock and
init or clone intent without acquiring or taking ownership of that lock;
a change between observations is a concurrency error. It may therefore
diagnose a live owner, or diagnose or recover an interrupted creation,
without requiring `.engram/root.yaml`. `--recover` acquires a free lock
or takes over the exact stale lock only under the proven-dead-owner rule
below; it never waits for or steals a live or owner-unprovable lock. An
absent target with no target-keyed controller lock or intent bytes at
all is a repository error. Observed malformed, foreign, or unrecognized
state still produces the structured error checks below and remains
untouched; it does not authorize takeover. A recognized target lock is
sufficient even before init or clone has created its intent. With no positional `PATH`,
global `--store` selects the target when present;
otherwise ordinary upward discovery applies. This form never scans
unrelated controller intents or guesses a target.

Reports local integration health, including the owned Git launcher and
any interrupted pull replay, stale shared lock, sparse presentation,
path/content-transforming Git setting or attribute, pathname/byte
round-trip failure, and repository-local cache exclusion. It also
reports clearly labeled heuristic diagnostics such as duplicate or
orphan suspicion. Heuristics are not check findings and do not change
exit status `0`; detected breakage in required local integration returns
the structured `doctor` result with outcome `issues` and status `1`. A
failure that prevents the diagnostic operation itself returns outcome
`error` and status `2`. History-derived analysis reads raw local history
without replacement or graft overlays and never fetches automatically.

Protocol v1 emits each required check name below exactly once. Required
checks use `status: "error"` for the stated breakage and `ok` otherwise,
except that a valid active replay or a recognized lock with a provably
live owner uses `warning`. Heuristic rows emit zero or more entries, one
per suspected logical path; their status is always `warning` and absence
is represented by no row rather than a synthetic `ok` entry.

| Stable `name` | `class` | Condition represented |
|---|---|---|
| `repository.shape` | `required` | Managed root, accepted ref/history shape, and raw object availability |
| `identity.binding` | `required` | Controller marker/common-directory binding |
| `guard.ownership` | `required` | Owned hooks path, launcher bytes, executable path, and launcher version |
| `initialization.state` | `required` | Absent, live, recoverable, or inconsistent external init intent |
| `acquisition.state` | `required` | Absent, live, recoverable, or inconsistent clone acquisition intent |
| `recovery.state` | `required` | Live/stale locks and recognized/foreign recovery records |
| `replay.state` | `required` | Absent, valid active, or inconsistent pull replay metadata |
| `presentation.sparse` | `required` | Sparse checkout/index disabled |
| `presentation.transforms` | `required` | Effective Git config/attributes are byte-transparent |
| `presentation.roundtrip` | `required` | Host pathname and file-byte round trip |
| `cache.exclusion` | `required` | Owned repository-local exclusion of `.engram/cache/` |
| `heuristic.duplicate` | `heuristic` | One non-normative duplicate suspicion |
| `heuristic.orphan` | `heuristic` | One non-normative orphan suspicion |

Required rows have `path: null`; heuristic rows use the suspected logical
path. `detail` explains the observed condition but remains unstable.
Names, classes, cardinality, and path rules are protocol-stable.

While an exact recognized live init/clone target lock owns a target that
is not yet a managed store, `recovery.state` is `warning`; the
store-dependent repository, binding, guard, presentation, and cache rows
are also `warning` because creation is coherently in progress, not
reported as broken. Intent rows follow the rules below and
`replay.state` is `ok`. Warnings alone retain outcome `ok` and status
`0`. An owner-unprovable or proven-stale target lock instead makes the
applicable rows `error` and recovery `needed`; only the latter is
eligible for takeover.

For `initialization.state` and `acquisition.state`, absence is `ok`; a
recognized intent whose owner is provably live is `warning`; and a
recognized stale, incomplete, recoverable, or lingering `complete`
intent is `error` until cleanup. A malformed, foreign, identity-
mismatched, or owner-unprovable intent is also `error` and remains
untouched. These mappings do not depend on whether a separate lock file
still exists.

Without `--recover`, `checks` describe the initial observation. With
`--recover`, they are recomputed after the bounded recovery attempt and
describe the returned state. In the `recovery` object, `requested`
equals whether the flag was present; `needed` is true iff the initial
observation contained writer-blocking stale, incomplete, malformed,
foreign, or recoverable lock, journal, init-intent, or acquisition-
intent state; and `performed` is true iff recovery was
requested, at least one recognized action was needed, every such action
completed, and the post-recovery observation no longer needs recovery.
It is false for a no-op request or any partial/unresolved attempt.
`accepted` is non-null exactly when `performed` is true and recovery ends
at a managed accepted state; it contains that exact post-recovery Git
state. A successful pre-intent or pre-publication init/clone rollback leaves no
managed store—an originally absent target is absent, while an existing
init target is restored without `.git`—and therefore has `accepted:
null`. Recovery of a draft
helper, acceptance journal, or pull operation that ends at an existing
managed state reports that exact accepted Git state even when unrelated
working-draft bytes remain. A live
owner is a warning and does not set `needed`; a proven-stale recognized
state, a recognized writer-blocking lock whose owner liveness cannot be
proved either way, a missing required journal, or a foreign/malformed
recovery format sets `needed` and makes `recovery.state` an error until
completely resolved. The unprovable-owner case remains untouched and
cannot make `performed` true. Outcome and exit status are computed from
the post-recovery required checks when the flag is present.

When successful pre-intent or pre-publication init/clone recovery clears
the recognized operation state and leaves no managed store,
recomputation still emits the complete structured
`doctor` result. `initialization.state`, `acquisition.state`,
`recovery.state`, and `replay.state` are `ok`; `repository.shape` and
the binding, guard, presentation, and cache checks are `error` because
there is no managed store to inspect. The result has `performed: true`,
`accepted: null`, outcome `issues`, and status `1`. Thus successful
cleanup is distinguishable from both a managed-store repair and a
failed recovery without inventing a phantom store.

`--recover` is the explicit bounded recovery path for CLI-owned stale
writer locks and post-CAS reconciliation records. Each owned lock carries
a host-instance identifier, PID, process-start identity, random attempt
nonce, and durable `pre-journal` or `journal-required` phase. Recovery
proceeds only when the CLI can prove that exact
owner process no longer exists; an unknown host, reused PID without a
matching start identity, malformed/unowned lock, or platform without a
reliable process-identity proof is left untouched. Taking over a stale
lock uses atomic compare-and-replace/create operations and stops on any
race.

After ownership is established, a recognized `pre-journal` lock with no
journal proves that CAS was impossible and permits cleanup of only
CLI-owned temporary state. If a journal exists, the command follows it
even when the lock still says `pre-journal`; a `journal-required` lock
with a missing or malformed journal remains blocked.

For a `cancelled` journal, recovery removes only CLI-owned temporary
state regardless of the ref's current value, then releases the locks and
removes the journal. For a `pending` journal, recovery compares the
recorded ref with its complete old/new object IDs and obtains one stable
observation of every journaled index/path preimage and final image. If
the ref names the new ID, recovery audits that accepted commit; each
item already equal to its final image is satisfied, each item still
equal to its preimage is eligible for the recorded final image, and any
other value blocks all further mutation. Remaining eligible items are
rechecked immediately before byte-raw replacement, so a crash can be
retried without reversing an already completed item.

If a pending ref names the old ID, recovery treats the result as
ambiguous rather than assuming CAS never occurred: an external client
could have produced old→new→old. That state, any third ref value, failed
audit, conflicting item, or unprovable preservation performs no
destructive guess, returns outcome `issues`, and leaves the recovery-
required state blocking writers. The annex's stated final-window limit
for non-cooperating filesystem writers applies. Success durably marks
the journal `complete`, releases the locks, and only then removes it. A
recognized `complete` journal needs only proven-stale lock release and
journal removal. Unknown or foreign lock/journal formats remain
untouched. Recovery never runs store hooks, moves an accepted ref,
creates a commit, or accesses the network.

For a recognized draft-operation journal, `doctor --recover` uses §6's
exact rollback rule instead of transaction-ref logic. It restores only
recorded final images to their recorded preimages, treats preimages as
already restored, and blocks on any third value. A journal durably
marked `complete` needs no rollback. After complete rollback or complete-
state cleanup it releases the proven-stale worktree lock and removes the
journal; interruption remains idempotently recoverable.

For a recognized external init intent, `doctor --recover` uses §4.1's
phase rule. Before target or `.git` publication it removes only the
recorded private staging/binding, exact bootstrap finals, and recorded
absent-preimage ancestor directories, restoring files first and then
empty directories deepest first; after publication it preserves the accepted ref
and finishes only the recorded marker/binding and intent cleanup. Any
identity mismatch, changed pre-publication input, or third bootstrap
value blocks without mutation. Recovery remains serialized by the init
lock and is idempotent across another interruption.

For a recognized clone acquisition intent, `doctor --recover` uses
§4.2's publication boundary. While the recorded target is absent it
removes only the private directory and pending external records. When
the exact recorded directory identity and accepted object are already
published, it preserves them and completes only the marker/URL bindings
and intent cleanup. Any third target, URL, object, or identity value
blocks without overwrite.
Recovery first takes over the same target lock under the proven-dead-
owner rule; an active or unprovable owner remains untouched.

For `replay.state`, absence is `ok`. One complete intentional active
replay, or one recognized in-progress pull journal whose exact owner is
provably live, is `warning` and does not set recovery `needed`. A stale
recognized journal, or malformed, partial, duplicated, foreign, or
journal-inconsistent replay state, is `error` and sets recovery
`needed`. A recognized stale pull-operation journal is recovered with
§10.1's pre-publication rollback or post-publication completion rule; a
valid active replay without such a journal is changed only by explicit
`pull --continue` or `--abort`.

### 11.2 `version`

```text
engram version [--format text|json]
```

Reports CLI version, supported core and annex versions, Git capability,
and build information without inspecting or modifying a store.

---

## 12. Deliberate omissions

Version v1 has no:

- `profile` commands;
- top-level `changeset` command family: `status`, `diff --staged`,
  `check --staged`, and `commit` expose its lifecycle;
- public `transaction` commands or persistent transaction handles:
  acceptance transactions are internal and one-shot;
- public `engram git` integration commands: `init`, `clone`, and
  `commit` manage the local launcher automatically;
- `derive` hooks or event/stage selector;
- individual hook execution;
- `check --fix`;
- automatic fetch, pull, push, or credential use during local commit;
- general destructive reset or checkout command; `pull --abort` restores
  only state created by the current pull replay;
- MCP or another memory-serving API: agents and adapters operate on the
  store through filesystem tools;
- mandatory daemon, database, embedding service, or index;
- top-level `read`, `write`, or `search` wrappers when ordinary
  filesystem tools already provide them.
