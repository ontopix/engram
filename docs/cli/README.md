# engram reference CLI — Contract v1 (draft)

**Version:** v1
**Status:** Draft
**Revision:** 2026-08-09
**Normative status:** Non-normative with respect to the engram standard

This document defines the observable contract of the reference `engram`
command. Store semantics come from the
[core specification](../spec/README.md) and its normative annexes, especially
the [Git-managed stores annex](../spec/annex-git.md). This contract defines how
the reference CLI exposes those semantics; it does not redefine them.

The CLI handles operations that need whole-store validation, managed history,
or runtime integration. Ordinary reading and content search remain filesystem
operations.

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

Authority is intentionally centralized:

| Commands | Semantic authority |
|---|---|
| `init`, `clone` | Core snapshot/transition rules; Git annex §§2–4, §7, Appendix B |
| `attach`, `detach` | Core §12; Git annex §6 |
| `status`, `diff`, `log` | Git annex §§3 and 5 |
| `check` | Core §9; Git annex §§3 and 8 for managed forms |
| `add`, `fmt`, `new`, `mv`, `schema` | Core §§5–8; Git annex §5 for working drafts and staging |
| `commit`, `revert` | Core §8; Git annex §4 and Appendix B |
| `hooks` | Core §8 and Appendix C |
| local Git guard | Git annex §5 |
| `pull`, `push` | Git annex §7 and Appendix B |
| `doctor` | CLI integration checks; Git annex Appendix B for managed recovery |
| `version` | This CLI contract |

The normal managed-write flow is:

1. edit a working draft with file tools or draft helpers;
2. declare the initial candidate with `add` or ordinary compatible Git
   staging;
3. inspect it with `status`, `diff --staged`, and `check --staged`; and
4. prepare, validate, and accept it with `commit`.

The exact transition vocabulary is defined once in
[core §8.1](../spec/README.md#81-changesets). Managed acceptance, staging,
concurrency, and recovery follow the
[Git annex](../spec/annex-git.md). A working draft and initial candidate are not
accepted state.

## 2. Global options and discovery

```text
engram [GLOBAL-OPTIONS] COMMAND [ARGS]

GLOBAL-OPTIONS
  -s, --store PATH       Select a store root
      --format FORMAT    text or json; default text
      --no-color         Disable ANSI styling
  -q, --quiet            Suppress ordinary successful human output
  -h, --help             Show help
  -V, --version          Show CLI version
```

Global options may appear before or after the command, before an explicit
`--`. Command options appear after the command. Unknown options, duplicate
non-repeatable options, missing values, and forbidden combinations are usage
errors. `-V`/`--version` is an alias for `version` and cannot be combined with
another command. Human help and version output ignore `--quiet`. JSON help is
not part of protocol v1 and is a usage error.

Without `--store`, a store command walks from the current directory toward the
filesystem root and selects the first directory containing
`.engram/root.yaml`. A command that needs a managed store additionally verifies
the Git annex. Discovery never treats an enclosing project repository as the
owner of an independent store.

`--store PATH` selects that exact existing snapshot root. It is accepted by
`status`, `diff`, `log`, `add`, discovery-based `check`, `fmt`, `new`, `mv`,
`schema list/show/copy`, `commit`, `revert`, `hooks`, `pull`, `push`, and
`doctor` without a positional path. It is not accepted by `init`, `clone`,
`attach`, `detach`, `schema inventory`, `version`, explicit-path or
snapshot-pair `check`, or positional-path `doctor`.

Unless a command says otherwise, command paths are relative to the selected
store. Logical paths in output are store-root-relative and use `/`. Host paths
in JSON are absolute and use native platform spelling. The CLI defines no
`ENGRAM_STORE` environment variable; `ENGRAM_*` names are reserved for the
preparation-hook protocol.

Text results go to standard output. Common text errors go to standard error
and emit no ordinary standard-output result. `--quiet` suppresses ordinary
successful text, not errors or JSON.

### 2.1 Network and authority boundary

`clone`, `pull`, and `push` are the only built-in commands that initiate
repository network access. Local inspection, editing, staging, attachment,
acceptance, and recovery do not fetch or publish implicitly.

Read, write, hook-execution, credential, fetch, and publication authority are
separate. Selecting or attaching a store grants none of them. A trusted store
hook is an external program with its own host authority; its authorization
does not authorize synchronization. These boundaries follow
[core §8.5](../spec/README.md#85-trust-and-executor-conformance) and
[Git annex §7](../spec/annex-git.md#7-synchronizer-profile).

## 3. JSON protocol and exit status

With `--format json`, every invocation that reaches protocol output emits one
UTF-8 RFC 8259 object followed by LF on standard output, with no ANSI bytes.
`--quiet` does not suppress it. External-process diagnostics may still appear
on standard error; clients do not parse standard error.

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

`command` is the canonical dot-separated command name, such as `schema.list`
or `hooks.trust`. It is `null` only when parsing fails before a command is
identified. The outcome and process status mapping is:

| `outcome` | Status | Meaning |
|---|---:|---|
| `ok` | `0` | Operation completed successfully; warnings may be present |
| `issues` | `1` | Operation completed and found validation errors, drift, required diagnostic problems, or a resolvable synchronization conflict/rejection |
| `error` | `2` | Usage or operational failure |
| `indeterminate` | `3` | A requested transition could not be completely evaluated, or a remote publication outcome cannot be established safely |

Warnings alone retain status `0`. For a transition, `indeterminate` takes
precedence over individual E5xx identities because the CLI cannot claim a
complete result. An ambiguous local mutation is an operational `error` with
recovery information, not a complete `indeterminate` result.

For outcomes other than `error`, `error` is `null` and `result` has the
command shape in §3.3. A common error normally uses result `{}` and exactly:

```json
{"kind":"repository","message":"..."}
```

`message` is unstable human text. `kind` is stable:

| `kind` | Class |
|---|---|
| `usage` | Invalid command grammar, option, or argument value |
| `cancelled` | Caller interruption observed before another causal failure |
| `internal` | Violated CLI invariant or recognized impossible state |
| `capability` | Unsupported platform, Git, specification feature, required object/history availability, or protocol representation |
| `trust` | Missing authorization for the selected preparation-hook set |
| `hook` | Hook launch or execution failure, or rejection of hook-produced state under core §8 |
| `network` | Transport, credential, fetch, or push failure |
| `conflict` | Content, preservation, or history conflict requiring caller action |
| `concurrency` | Busy/stale lock, changed captured input, or compare-and-swap race |
| `integration` | Conflict in a CLI-owned launcher, attachment block, binding, or other host integration |
| `repository` | Ineligible local store, repository, ref, index, object, or presentation state |
| `io` | Host filesystem or process I/O failure not classified above |
| `operational` | Operational failure outside the preceding closed classes |

Classification follows the first causal class above; wrapping a failure in a
larger operation does not reclassify it. A completed check with an `E` finding
uses `issues`, not `error`. Warnings remain `ok`.

Protocol paths and refs must have reversible UTF-8 representations. The CLI
never substitutes U+FFFD for their identity. Object IDs are complete lowercase
hexadecimal strings at the repository's declared SHA-1 or SHA-256 width.
Arrays of logical paths and changes use UTF-8 byte order unless stated
otherwise. JSON member order has no meaning.

### 3.1 Shared result types

A logical change is:

```json
{"operation":"modified","path":"people/ada.md"}
```

`operation` is `added`, `modified`, or `deleted`.

A finding is:

```json
{"code":"E401","path":"topics/old.md","detail":"..."}
```

`detail` is optional and non-normative. Finding identity is `(code, path)`.

A validation result is:

```json
{
  "target": "changeset",
  "status": "complete",
  "findings": []
}
```

`target` is `snapshot`, `changeset`, or `managed-store`. `status` is
`complete` or `indeterminate`; snapshots are always `complete`. Findings use
the normative aggregation and ordering from
[core §9](../spec/README.md#9-validation).

A Git state is:

```json
{"ref":"refs/heads/main","commit":"0123456789abcdef0123456789abcdef01234567"}
```

`ref` or `commit` may be `null` when that state does not exist.

A commit is:

```json
{
  "id": "0123456789abcdef0123456789abcdef01234567",
  "parents": [],
  "author": {"name":"Ada","email":"ada@example.test"},
  "committer": {"name":"Ada","email":"ada@example.test"},
  "authored_at": "2026-08-07T10:00:00+02:00",
  "committed_at": "2026-08-07T10:00:00+02:00",
  "message": "Update memory\n"
}
```

Identity and timestamp members may be `null` when historical raw metadata
cannot be represented in that field. `parents` preserves raw parent order and
may expose a merge boundary. `message` preserves the complete decoded commit
message. These are Git bookkeeping fields, not record provenance.

A history audit is:

```json
{
  "base": "0123456789abcdef0123456789abcdef01234567",
  "candidate": "fedcba9876543210fedcba9876543210fedcba98",
  "validation": {"target":"changeset","status":"complete","findings":[]}
}
```

`base` is `null` for initialization. Audits are reported root to tip and only
for transitions actually evaluated; absence never implies validity.

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

`source` is `local` or `inventory`; inventory entries have `path: null`.

A supported-specification descriptor is:

```json
{
  "id": "core",
  "version": "v1",
  "revision": "2026-08-07",
  "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
}
```

The digest identifies the exact normative source bytes implemented by the
binary. Annex descriptors use their stable annex name as `id`.

### 3.2 Recovery-bearing errors

If an operation may have published local workflow state, changed a remote, or
left recognized recovery-required state, an `error` response replaces `{}`
with this result shape:

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
  "head": null,
  "checkout_changed": false,
  "remote": null,
  "recovery_required": true
}
```

`durable` is true only for a mutation known to have succeeded. `local_refs`
lists known accepted or private workflow-ref updates in chronological order;
`before` or `after` may be `null`. `head`, when non-null, contains `before`
and `after` Git states. `checkout_changed` reports a known index/worktree
change. `remote`, when non-null, contains string `name`, full `ref`, nullable
`before`, and non-null `after`. `recovery_required` says that another managed
write must wait for explicit recovery.

This shape prevents clients from treating an uncertain or partly published
operation as safe to retry. Recovery semantics follow
[Git annex Appendix B](../spec/annex-git.md#appendix-b--atomicity-and-recovery-profile-normative).

### 3.3 Complete command results

The following table defines every complete non-error `result`. Presentation
flags do not change JSON shape.

| Command | `result` members |
|---|---|
| `init` | `dry_run` boolean, `root` host path, `accepted` Git state, nullable `files` logical changes, `launcher` (`planned`, `installed`, `unchanged`), `validation` changeset result |
| `clone` | `root` host path, `remote` string, `accepted` Git state, `published` and `reused` booleans, `verified_commits` integer, `launcher`, managed-store `validation`, root-to-tip `audits` |
| `attach` | `project`, `store`, `entrypoint` host paths; `changed`; managed-store `validation`; root-to-tip `audits` |
| `detach` | `project`, `store`, `entrypoint` host paths; `changed` |
| `status` | `mode` (`normal`, `pull-replay`), `accepted`, `candidate_base`, `staged`, `unstaged`, nullable `replay` |
| `diff` | `from`, `to` state selectors, logical `changes`, and `stat` with `added`, `modified`, `deleted` counts |
| `log` | newest-first `commits` |
| `add` | `changed` and the complete resulting `staged` changes |
| `check` | one validation result |
| `fmt` | `dry_run`, `check`, `changed`, and affected logical `paths` |
| `new` | `dry_run`, `changed`, logical `record`, affected `catalogs` |
| `mv` | `dry_run`, `changed`, logical `from` and `to`, rewritten document `paths`, affected `catalogs` |
| `schema.inventory`, `schema.list` | `schemas` descriptions |
| `schema.show` | one `schema` description and exact UTF-8 `content` |
| `schema.copy` | `dry_run`, `changed`, inventory `schema`, destination logical `path` |
| `commit` | `dry_run`, `created`, nullable `commit`, nullable definitive `changes`, nullable `validation` |
| `revert` | commit-result members plus source ID `reverted` and ordered logical `conflicts` |
| `hooks.list`, `hooks.trust` | selected `state`, `changed`, set `sha256`, set-level `trusted`, ordered `hooks` |
| `hooks.revoke` | `changed` and ordered digest list `revoked_sets` |
| `doctor` | ordered `checks` plus `recovery` with `requested`, `needed`, `performed`, nullable `accepted` |
| `pull` | `state`, `remote`, full `remote_ref`, `before`, `after`, `fetched`, `replayed`, `conflicts`, nullable `changes`, managed-store `validation`, nullable `candidate_validation`, root-to-tip `audits` |
| `push` | `state`, `remote`, full `remote_ref`, `remote_observed`, nullable remote `before`, local `after`, `commits`, nullable `changed`, managed-store `validation`, root-to-tip `audits` |
| `version` | `cli_version`, ordered `core_versions`, `annex_versions`, `git` capability object, `build` object |

Nested objects are closed in protocol v1:

- a `diff` selector has `kind` (`accepted`, `index`, `working`, or
  `revision`) and nullable `value`; commit-backed selectors use the resolved
  full object ID;
- `status.replay` has Git states `original`, `private`, and `base` (`base.ref`
  is null), string `reason` (`conflict` or `rejected`), and ordered
  `conflicts` paths;
- each `doctor` check has `name`, `class`, `status`, nullable `path`, and
  nullable `detail`; its `recovery` shape is defined in §11.1;
- `version.git` has nullable string `version` and boolean `supported`;
  `version.build` has string `go`, `os`, and `arch` plus nullable string
  `revision`; and
- remote publication members always use a configured remote name and a full
  `refs/heads/<branch>` ref, never an abbreviated argument.

For read-only modes, `changed` means “would change”; otherwise it means the
effect was published. Path arrays contain byte-changing outputs, are
deduplicated, and use UTF-8 byte order. Schema arrays order by `type` then
nullable `path`; specification descriptors order by `id` then `version`.

`commit` and `revert` use `created: false` and `commit: null` for dry-run,
no-op, or validation rejection. A no-op has `changes: []` and
`validation: null`. Before a definitive changeset exists, `changes` is null.
Once a final candidate exists, `changes` and its validation describe that
candidate even when acceptance is rejected.

## 4. Creating and obtaining managed stores

### 4.1 `init`

```text
engram init [PATH] [--schema TYPE]... [--dry-run]
```

Creates a managed store and its initial accepted commit. Omitted `PATH` means
the current directory; a relative path is resolved from it. The target may be
absent or an existing real directory, but must not already own unrelated Git
administration. An absent target's parent must exist.

The initial candidate contains all existing logical files plus missing
canonical bootstrap files:

```text
README.md
.engram/root.yaml
.engram/schemas/note.md
```

Requested `--schema` values copy additional bundled inventory schemas.
Duplicates collapse. Existing files are never rewritten: a requested
destination must be absent or already byte-identical. Existing pruned or
non-logical bytes remain outside the initial commit.

Before publication, `init` evaluates the complete snapshot and explicit
empty-base transition. Rejection leaves the target unchanged. A successful
real invocation creates a non-bare worktree rooted exactly at the store,
directs `HEAD` to `refs/heads/main`, creates one parentless accepted commit,
installs the owned raw-Git guard, and configures byte-transparent presentation.
Missing Git author identity is an operational error.

`--dry-run` reports the prospective files, validation, accepted ref, and
launcher without writing. Initialization follows
[Git annex §§2–4](../spec/annex-git.md#2-repository-identity-and-presentation)
and Appendix B. It performs no network operation and grants no hook or
synchronization authority.

### 4.2 `clone`

```text
engram clone URL [PATH]
```

Obtains and verifies a managed store before publishing its checkout. Protocol
v1 accepts lowercase `https://`, `ssh://`, and `file://` URLs plus Git's
`[USER@]HOST:PATH` form. It rejects plaintext `http://`/`git://`, `ext::`,
unknown helpers, control characters, and option-like locations before Git
starts. Local paths use `file://`.

An explicit relative `PATH` is resolved from the current directory and its
parent must exist. With no `PATH`, the destination is
`<data-root>/engram/stores/<digest>`, where `digest` is SHA-256 of the exact URL
argument. The data root is `$HOME/Library/Application Support` on macOS,
`%LOCALAPPDATA%` on Windows, and `$XDG_DATA_HOME` or `$HOME/.local/share` on
other systems. A matching
previous default-destination clone may be reused without fetching; any URL,
identity, upstream, guard, or presentation drift is a conflict. Explicit paths
are never reused implicitly.

Clone selects the remote branch named by its symbolic default `HEAD`, audits
the complete accepted lineage, and publishes only a verified byte-transparent
worktree. It records that URL as `origin`, configures the selected branch's
upstream, installs the owned local guard, and does not copy ambient Git author
identity, trust store hooks, attach a project, or authorize push. Author name
and email must be configured in the cloned repository before a managed
commit. An invalid or indeterminate lineage is reported without publishing a
fresh destination.

The command is the only acquisition operation and therefore requires explicit
network and credential authority. It follows
[Git annex §§3 and 7](../spec/annex-git.md#3-accepted-history) and the same
atomic publication/recovery guarantees as Appendix B.

### 4.3 `attach`

```text
engram attach STORE [--project PATH] [--entrypoint FILE]
```

Records that a project uses an independent local managed store. `STORE` is a
path, not a URL; use `clone` separately for acquisition. The project defaults
to the current Git root, or the current directory outside Git.
`--entrypoint` defaults to the project's `AGENTS.md` and must stay below the
project root.

Attach completely audits the store before writing. It owns one versioned,
delimited block in the entrypoint containing canonical absolute store paths.
It creates or replaces only that block, preserves all surrounding bytes, and
deduplicates physical store identities. A malformed or duplicate owned block
is an integration error. Concurrent cooperating attach/detach operations do
not lose one another; successful publication is one atomic file replacement.

The owned block has these markers and semantic content:

~~~markdown
<!-- engram:adoption:v1 -->
Engram stores (spec v1; canonical absolute paths):
```json
{"stores":["/Users/ada/memories/shared"]}
```
Before touching a store, read its root README and follow the engram Agent Protocol.
<!-- /engram:adoption:v1 -->
~~~

The guidance in the block tells an agent to read the store's root README and
follow the engram Agent Protocol. It does not assume that every conforming root
README reproduces that protocol verbatim.

Attachment grants no authority and does not copy, move, commit, trust,
synchronize, or delete memory. Repository ownership follows
[Git annex §6](../spec/annex-git.md#6-attachments-and-repository-topology).

### 4.4 `detach`

```text
engram detach STORE [--project PATH] [--entrypoint FILE]
```

Removes the matching path or physical identity from the CLI-owned attachment
block. If other stores remain it rewrites the block; if the last is removed it
removes the complete block and no surrounding bytes. A missing entry is an
unchanged success. Conflicting duplicate physical identities are an
integration error.

Detach uses the same project, entrypoint, locking, preservation, and atomic
replacement rules as `attach`. It never deletes the store, changes history,
revokes other permissions, or edits another project's attachment.

## 5. Inspecting state

### 5.1 `status`

```text
engram status
```

Reports the accepted Git state and two unaccepted layers:

- `staged`: the index-declared initial candidate relative to accepted `HEAD`;
- `unstaged`: logical working-draft changes outside that candidate.

Entries identify added, modified, and deleted logical paths. Pruned tool state
is omitted; an ineligible raw index fails rather than being hidden. During an
active pull replay, `mode` is `pull-replay` and `replay` identifies the
original state, current private state, source base, immutable reason
(`conflict` or `rejected`), and current ordered conflict paths. Otherwise
`replay` is null.

`status` is local and read-only. Its accepted/draft distinction follows
[Git annex §5](../spec/annex-git.md#5-working-drafts-staging-and-git-integration).

### 5.2 `diff`

```text
engram diff [REV-A [REV-B]] [--staged|--cached] [--stat|--name-only]
```

With no revisions, compares the working draft with the index. `--staged`
(`--cached`) compares accepted `HEAD` with the index and exposes the candidate
that `commit` will prepare. One revision compares that accepted snapshot with
the working draft; two compare accepted snapshots. Staged selection and
revisions are mutually exclusive; `--stat` and `--name-only` are mutually
exclusive.

A revision is exactly `HEAD` or a full lowercase object ID at the repository's
declared width. It must belong to the audited current accepted lineage;
abbreviations, other refs, ranges, and revision expressions are not accepted.

Text output renders content, counts, or names. JSON always returns selectors,
the complete logical change array, and counts. It never emits a partial or
guessed changeset when boundary or raw-index eligibility fails.

### 5.3 `log`

```text
engram log [-n COUNT] [--oneline]
```

Shows the local accepted lineage newest first without fetching. A root ends
normally; a merge commit is emitted as the diagnostic boundary and traversal
stops without choosing a parent. `COUNT` is an integer from 1 through
2147483647. `--oneline` changes only text presentation.

A merge boundary returns `issues`; missing required objects are capability
errors, and malformed local objects are repository errors. Historical author,
committer, timestamps, and message are display bookkeeping, not record
semantics.

### 5.4 `check`

```text
engram check [PATH]
engram check --accepted
engram check --staged
engram check --base BASE --candidate CANDIDATE
```

The forms are mutually exclusive:

- `check [PATH]` validates one portable snapshot. An explicit path is resolved
  from the current directory and requires no Git. Without it, ordinary
  discovery or `--store` applies.
- `--accepted` checks managed repository conformance and the complete accepted
  lineage under the Git annex.
- `--staged` evaluates accepted `HEAD` against the index-declared initial
  candidate without running preparation hooks. Unstaged bytes do not affect it.
- `--base` and `--candidate` together evaluate two explicit snapshot
  directories without acceptance or managed-store discovery.

`check` is read-only and has no `--fix`. It returns the shared validation
result; clients use `status`, `(code, path)`, and ordering rather than optional
detail. Authority is [core §9](../spec/README.md#9-validation) and
[Git annex §8](../spec/annex-git.md#8-managed-check-attribution).

## 6. Working-draft and staging helpers

`fmt`, `new`, `mv`, and `schema copy` edit only the managed working draft and
never stage, run hooks, or create commits. `add` changes only the index. A
successful helper preserves unrelated working-draft and index bytes; no helper moves
an accepted ref. `--dry-run` computes the same result without mutation.

Mutating helpers coordinate with managed writers through the worktree
rendezvous. On interruption they never claim a complete result; recognized
recovery state is reported through §3.2. These are CLI guarantees over an
unaccepted working draft, not additional store semantics.

### 6.1 `add`

```text
engram add PATH...
engram add --all
```

Stages selected logical working-tree changes. `--all` selects every logical
addition, modification, and deletion and cannot be combined with paths.

Each path is a literal store-relative file or directory, never a Git pathspec,
glob, or revision. A file selects that exact change; a directory or changed
path prefix selects changed files recursively. Duplicate and overlapping
arguments collapse. Paths outside the logical store, reserved/pruned state,
ineligible files, and nonexistent selections are rejected.

Before replacing the index, `add` validates the complete prospective index's
boundary and raw eligibility. It uses the managed deterministic regular-file
modes from Git annex Appendix A. Staging may still produce a snapshot- or
transition-invalid initial candidate; `commit` owns whole-state and transition
acceptance.

### 6.2 `fmt`

```text
engram fmt [PATH...] [--check] [--dry-run]
```

Regenerates only catalog marker regions. With no paths it selects every
logical content-directory README; a directory path selects only its own
README, and a README path selects itself. Paths are literal, not globs.

`--check` is read-only and returns `issues` when bytes would change.
`--dry-run` reports edits without applying them; when combined, `--check`
controls the exit status. The command does not reformat YAML, rewrite prose,
create missing maps, repair marker grammar, stage, or commit. A valid
`catalog: none` map is an unchanged selection.

### 6.3 `new`

```text
engram new TYPE PATH
  --description TEXT
  [--fields FILE]
  [--body FILE|-]
  [--title TEXT]
  [--dry-run]
```

Creates one record and updates its containing catalog as one helper operation.
`PATH` is a new store-relative `.md` record below an existing real content
directory; it cannot be `README.md`, reserved state, or a schema/config file.
`TYPE` is resolved lexically at that destination.

`--fields` supplies a YAML mapping of additional frontmatter fields and cannot
override `type`, `description`, or use reserved `engram-` keys. `--body FILE`
copies an existing UTF-8 normed-text file; `--body -` reads standard input.
`--title` cannot accompany `--body`. Without a body, the CLI creates an H1
from the title or filename and emits schema-required section headings.

The generated record uses deterministic normed UTF-8 Markdown and validates
all supplied values against the resolved schema. The command never invents
required domain values; missing values reject before publication. It creates
no ancestor content directory, does not stage, and supports `--dry-run`.

### 6.4 `mv`

```text
engram mv FROM TO [--dry-run]
```

Moves one record between distinct literal store-relative record paths. The
source must exist, the destination must be absent, and its content-directory
parent must already exist. Version v1 does not move directories.

As one helper operation, `mv` moves the file, regenerates affected catalogs,
and rewrites inbound wikilinks plus local Markdown link/image destinations
whose meaning would otherwise change. Rewrites preserve labels, titles,
query/fragment suffixes, and every unrelated source byte. Unsupported or
ambiguous source presentations cause a conflict; the command never guesses or
partially rewrites. There is no option to suppress required rewrites.

`paths` reports documents whose non-catalog link bytes change, using final
paths; `catalogs` reports changed README catalogs. `--dry-run` computes the
same arrays without writing. Rename integrity follows
[core §7.4](../spec/README.md#74-identity-and-renames).

### 6.5 `schema`

```text
engram schema inventory
engram schema list [--at PATH]
engram schema show TYPE [--at PATH]
engram schema copy TYPE [--to SCOPE] [--dry-run]
```

- `inventory` lists the non-normative schemas bundled with the CLI.
- `list` reports schemas lexically visible at `--at` and their winning paths.
- `show` returns the resolved conforming local schema and exact content.
- `copy` copies one bundled schema into `SCOPE/.engram/schemas/`.

`PATH` and `SCOPE` are logical content directories and default to the root.
Discovery is all-or-nothing: invalid selected schema state is a repository
error, not a partial list or ancestor fallback. `copy` may create only the
final `.engram/schemas/` directory, rejects an existing destination or
shadowing, and never creates an update relationship. The result is an ordinary
local schema governed by [core §6](../spec/README.md#6-types-and-schemas).

## 7. Acceptance

### 7.1 `commit`

```text
engram commit -m MESSAGE [--dry-run]
engram commit --dry-run
```

Accepts the entire staged initial candidate through one managed transaction.
It has no path arguments; selection belongs to `add`. Unstaged and untracked
bytes remain outside the candidate and must be preserved.

A real invocation requires a non-empty UTF-8 `MESSAGE` with no NUL or CR
and no final LF; internal LF is allowed. The stored message adds one final LF.
Git author name and email must be configured and representable. `--dry-run`
needs no message and executes candidate materialization, preparation,
validation, and preservation checks without installing integration, creating a
commit, moving a ref, or changing the index/worktree.

The command audits accepted history, prepares the candidate once with the
trusted base hook set, validates the sealed final candidate and transition,
and accepts only a complete result with no `E` finding. A successful real
invocation creates one single-parent commit (or the initialization root via
`init`), compare-and-swap updates the accepted ref, and safely reconciles hook
output while preserving unrelated draft bytes.

An eligible index equal to accepted `HEAD` is a successful no-op even when the
worktree has unstaged changes. An ineligible index is not a no-op. Validation
rejection imports no hook output. Commit never fetches or pushes.

The transaction and recovery authority is
[Git annex §4](../spec/annex-git.md#4-managed-transactions) and
[Appendix B](../spec/annex-git.md#appendix-b--atomicity-and-recovery-profile-normative);
the hook authority is [core §8](../spec/README.md#8-changesets-and-preparation-hooks).

### 7.2 `revert`

```text
engram revert COMMIT [-m MESSAGE] [--dry-run]
```

Creates a new managed transaction that inverses one non-root, single-parent
commit in the current accepted lineage. It never resets a branch or rewrites
history. `COMMIT` uses the same full-ID/`HEAD` grammar as `diff` revisions.

Revert requires a clean logical index and worktree. It applies the inverse only
where current bytes match the source commit's expected postimage or are
already at the intended result. Every path is evaluated together; any content
or file/directory collision reports all conflicting paths and changes nothing.

Without `-m`, the message is `Revert <full-object-id>`. A conflict returns
`issues`; otherwise hooks, validation, dry-run, acceptance, and recovery follow
`commit`. Policy, link, or snapshot rejection leaves the index and worktree
unchanged.

## 8. Hooks and trust

```text
engram hooks list [--state accepted|working]
engram hooks trust [--state accepted|working]
engram hooks revoke [HOOK...]
```

`accepted` is the default state for `list` and `trust`. `list` reports the
complete selected set in execution order, including path, interpreter, exact
file digest, set digest, and local trust state. It does not execute hooks.

`trust` explicitly authorizes the complete non-empty selected set for this
physical managed-store binding. A copied, moved, added, removed, renamed, or
byte-changed set needs a new grant. The empty set needs no code-execution grant
but still needs valid local integration identity. `revoke` removes every
historical grant containing a named hook, or all grants for the store when no
name is given; duplicate names collapse. Each `HOOK` is one admitted direct
program filename with no `/`.

Trust lives in controller-owned user configuration outside the store and its
history. Repository-controlled marker bytes alone confer no trust. Moving or
copying a repository therefore requires an explicit new local binding and
authorization. An invalid selected hook tree is a repository error and never
produces a partial result or grant. Interpreter availability is checked only
when acceptance launches the hook.

There is no individual-hook execution command. One executor runs the complete
applicable base set or rejects. Exact selection, ordering, invocation, and
trust duties follow [core §8](../spec/README.md#8-changesets-and-preparation-hooks)
and [Appendix C](../spec/README.md#appendix-c--preparation-hook-protocol-normative).

## 9. Automatic local Git integration

`init` and `clone` install a CLI-owned `pre-commit` guard, and `commit` verifies
it before acceptance. The guard rejects unmanaged raw `git commit` and directs
the caller to `engram commit`; it does not parse stores, prepare candidates,
validate, or create commits. An unowned effective hook conflict is never
overwritten or chained.

The guard is interoperability guidance, not a security boundary. Removing or
bypassing it does not make a raw Git commit conforming. CLI-managed Git
operations suppress Git-native hooks so preparation runs exactly once through
the core hook protocol. Raw local reads never lazy-fetch missing objects.

Linked worktrees have separate working drafts, indexes, and worktree rendezvous while
sharing accepted refs as Git defines. Managed coordination follows
[Git annex §5](../spec/annex-git.md#5-working-drafts-staging-and-git-integration)
and [Appendix B](../spec/annex-git.md#appendix-b--atomicity-and-recovery-profile-normative).

## 10. Repository synchronization

### 10.1 `pull`

```text
engram pull [REMOTE [BRANCH]]
engram pull --continue
engram pull --abort
```

`REMOTE` is a configured remote name, never an inline URL. With no arguments,
the accepted branch's configured upstream is selected. With only `REMOTE`, the
same short branch name is selected there. `BRANCH` is relative to
`refs/heads/`. The selected URL must satisfy `clone`'s transport grammar.
Pull requires exactly one local URL for the remote. Push uses exactly one local
`pushurl`, or one local URL when no `pushurl` exists; multiple values are a
repository error rather than multiple network effects.

A new pull requires no active replay and a clean logical index/worktree. It
fetches only the selected branch into private workflow state, audits incoming
history, then either reports up to date, fast-forwards, or replays divergent
local commits linearly through managed transactions. It never creates a merge,
runs hooks from already accepted remote commits, or performs an automatic text
merge.

On a replay conflict, no path from that source changeset is partly applied.
The command leaves an explicit staged resolution draft against a private
accepted base and returns `state: "conflict"`. If mechanical application
succeeds but preparation or candidate validation rejects it, the unprepared
initial candidate is left as `state: "rejected"`; rejected hook output is not
exposed. `status`, `diff --staged`, and `check --staged` inspect that draft.

`pull --continue` accepts one complete staged resolution and resumes remaining
source commits; an eligible clean resolution is an explicit no-op resolution.
`pull --abort` performs no network or hook execution and discards only the
resolution draft owned by the active pull, restoring the captured original
branch and clean checkout. Both reject concurrent or unrelated changes rather
than overwriting them. Plain `pull` and `engram commit` are rejected while a
replay is active.

Complete states are `up-to-date`, `fast-forwarded`, `replayed`, `conflict`,
`rejected`, and `aborted`. `before` is the original accepted state; `after` is
the state currently named by `HEAD`, including a private replay branch.
`fetched` counts distinct incoming lineage commits examined beyond the local
lineage; `replayed` counts source changesets successfully processed. Candidate
fields are populated only for a currently evaluated rejected candidate.

Pull requires explicit network/credential authority. Incoming validation,
exact replay, compare-and-swap, active replay, atomicity, and recovery follow
[Git annex §7](../spec/annex-git.md#7-synchronizer-profile) and Appendix B.

### 10.2 `push`

```text
engram push [REMOTE [BRANCH]]
```

Remote and branch selection matches `pull`. Missing configuration is a
repository error; the CLI does not guess `origin` or a remote default.

Push completely audits the local managed lineage before network access, then
publishes only accepted commits through a conditional fast-forward update.
Version v1 has no force, deletion, merge, or remote-provisioning option.
Creation is allowed only when the selected branch was observed absent.

States are `up-to-date`, `pushed`, `rejected`, and `indeterminate`. A remote tip
outside the audited local lineage is `rejected` with no update. A
compare-and-swap race is a concurrency error and is not retried. If transport
cannot establish whether the remote update succeeded, the complete result is
`indeterminate`, `changed: null`; a later explicit invocation observes the
remote afresh.

Push requires separate credential and publication authority. Local write or
pull authority does not imply it. Remote updates follow
[Git annex §7.3](../spec/annex-git.md#73-local-and-remote-updates).

## 11. Advisory and runtime commands

### 11.1 `doctor`

```text
engram doctor [PATH] [--recover] [--format text|json]
```

Without a positional path, ordinary discovery or global `--store` applies.
With `PATH`, doctor may inspect an existing target or a known interrupted
`init`/`clone` target before a managed root exists. A missing target with no
recognized controller state is a repository error. Doctor never scans
unrelated controller state.

Doctor reports required integration health and explicitly labeled heuristics.
Protocol v1 emits each required name exactly once; heuristic rows occur only
for suspected logical paths:

| Stable `name` | `class` | Condition |
|---|---|---|
| `repository.shape` | `required` | Managed root, accepted history, required raw objects |
| `identity.binding` | `required` | Controller/store physical binding |
| `guard.ownership` | `required` | Owned hook path, guard bytes, executable/version |
| `initialization.state` | `required` | Init state absent, live, recoverable, or inconsistent |
| `acquisition.state` | `required` | Clone state absent, live, recoverable, or inconsistent |
| `recovery.state` | `required` | Managed locks and recovery records |
| `replay.state` | `required` | Pull replay absent, active, or inconsistent |
| `presentation.sparse` | `required` | Sparse presentation disabled |
| `presentation.transforms` | `required` | Git config and attributes are byte-transparent |
| `presentation.roundtrip` | `required` | Host path and file-byte round trip |
| `cache.exclusion` | `required` | Repository-local exclusion of `.engram/cache/` |
| `heuristic.duplicate` | `heuristic` | One possible duplicate record |
| `heuristic.orphan` | `heuristic` | One possible orphan record |

Each check has `name`, `class`, `status` (`ok`, `warning`, `error`), nullable
`path`, and nullable unstable `detail`. Required failures return `issues`;
heuristic warnings and a coherent live operation remain `ok`.

`--recover` is an explicit, bounded local recovery request. It acts only on
recognized CLI-owned state after proving that no live owner controls it. It
never guesses through unknown, malformed, foreign, concurrently changed, or
ambiguous state; those remain reported and block writers. Safe recovery is
idempotent, preserves accepted commits and unrelated draft bytes, runs no
store hook, moves no accepted ref, and performs no network access.

`recovery.requested` mirrors the flag; `needed` reflects the initial blocking
state; `performed` is true only when every recognized required action
completed and post-recovery state no longer needs recovery. `accepted` is the
resulting managed Git state, or null when successful cleanup correctly leaves
no store. Transaction and synchronization recovery are governed by
[Git annex Appendix B](../spec/annex-git.md#appendix-b--atomicity-and-recovery-profile-normative).

### 11.2 `version`

```text
engram version [--format text|json]
```

Reports the CLI version, exact supported core and annex descriptors, Git
capability, and build information without discovering or modifying a store.

## 12. Deliberate omissions

Version v1 has no schema profiles, public transaction handle, top-level changeset
family, `engram git` family, individual-hook runner, `check --fix`, implicit
synchronization during local commit, general destructive reset/checkout,
memory-serving API, required daemon/database/index, or wrappers for ordinary
filesystem read/write/search. `status`, `diff --staged`, `check --staged`, and
`commit` expose the candidate-and-acceptance flow without turning it into a
long-lived session.
