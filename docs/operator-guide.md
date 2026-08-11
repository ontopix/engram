# engram operator guide

**Applies to:** reference CLI v1
**Normative status:** Non-normative

This guide covers installation and operation of the reference CLI. Store
validity and accepted-history semantics remain defined by the core
specification and normative Git annex.

## Install and verify

First check the repository's GitHub Releases page for a build matching the
version you intend to use. If no matching tagged build has been published,
build the current source checkout with a supported Go toolchain:

```sh
go build -o engram ./cmd/engram
./engram version
```

Published release archives contain one platform binary, this guide, release
notes, licenses, and the five canonical skills. Verify a downloaded archive
before extracting:

```sh
sha256sum --check SHA256SUMS
```

On macOS, use `shasum -a 256 -c SHA256SUMS`. On Windows PowerShell, run
`Get-FileHash -Algorithm SHA256 .\engram-*.zip` and compare the result with the
matching line in `SHA256SUMS`. Compare the digest file and
`provenance-v1.json` with the assets attached to the same GitHub release. Put
`engram` on `PATH`, then run:

```sh
engram version
engram doctor /path/to/store
```

`version --format json` identifies the exact core and Git-annex bytes supported
by the binary, the source revision, Go toolchain, target OS/architecture, and
the probed system-Git capability.

## Reproduce a release

Use the exact commit and Go version recorded in `provenance-v1.json`, with an
otherwise clean checkout and the pinned module graph already acquired. The
release builder rejects tracked, untracked, and ignored files, a version that
differs from the source CLI version, and a Go executable that differs from the
toolchain running the builder. From that checkout, reproduce the official
timestamp and build outside the source tree:

```sh
release_tag="$(git describe --tags --exact-match HEAD)"
epoch="$(git show -s --format=%ct HEAD)"
revision="$(git rev-parse --verify HEAD^{commit})"
go mod download
go mod verify
go run ./tools/release -version "$release_tag" -revision "$revision" \
  -source-date-epoch "$epoch" -output /tmp/engram-release
```

Compare every resulting byte with the attached release assets, including
`SHA256SUMS` and `provenance-v1.json`. The tagged workflow performs the same
build twice before it is allowed to publish.

## Create or acquire a store

Create a local store with configured Git author identity:

```sh
git config --global user.name "Ada"
git config --global user.email "ada@example.test"
engram init ~/memory --schema person --schema project
```

Use `--dry-run` first when initializing an existing directory. Existing files
are preserved and included only when they are logical snapshot inputs; pruned
or opaque bytes remain outside accepted history.

Acquire a remote store only through an admitted transport:

```sh
engram clone ssh://git@example.test/memory.git ~/memory
git -C ~/memory config --local user.name "Ada"
git -C ~/memory config --local user.email "ada@example.test"
```

Clone verifies the complete accepted lineage in private staging before making
the destination visible. It does not copy ambient author identity into the
store; configure the local identity before the first managed commit. Clone
grants no preparation-hook trust and no push authority.

## Connect a store to an agent project

From the agent project, attach one or more independent stores. The project
defaults to the current Git root, or the current directory outside Git:

```sh
engram attach ../memory
engram attach ../shared-memory
```

Each command audits the store and creates or updates project `MEMORY.md`.
Attachments only discover locations; they grant no read, write, hook, network,
or synchronization authority.

Install the project-scoped integration for the harness in use:

```sh
engram setup --harness codex
# or
engram setup --harness claude-code
```

Setup verifies the canonical skills embedded in the running binary, writes
them below `.agents/skills/` or `.claude/skills/`, and adds a bounded pointer
to `MEMORY.md` in `AGENTS.md` or `CLAUDE.md`. Run with `--dry-run` to inspect
the planned files. Repeating setup is idempotent; locally modified installed
skills are reported as conflicts rather than overwritten.

## Normal write cycle

The normal cycle is explicit. In a store that already contains a `topics/`
content directory and map:

```sh
engram status
engram new note topics/example.md \
  --description "An example durable memory." --title "Example"
engram add topics/example.md topics/README.md
engram check --staged
engram commit -m "Record example"
```

The worktree is a working draft; the index is the initial candidate. Commit
prepares the complete candidate once, validates the final candidate, and moves
the accepted ref only through a managed transaction. Do not use raw
`git commit`, `git merge`, `git reset`, or force-push as substitutes.

Before authorizing store-controlled programs, inspect the complete selected
set and trust that exact set:

```sh
engram hooks list --state accepted
engram hooks trust --state accepted
```

Any change to the set invalidates the grant. `hooks revoke` removes local
controller grants; it does not alter store history.

## Synchronization

Only `clone`, `pull`, and `push` initiate repository network access. Pull
fetches one configured remote branch into private workflow state and either
fast-forwards or replays local linear changesets through managed acceptance.

```sh
engram pull
engram status
engram pull --continue   # after resolving and staging every conflict
engram pull --abort      # discard only the active pull-owned draft
engram push
```

Push performs a conditional fast-forward update. A rejected or indeterminate
publication is never force-retried automatically; inspect the remote afresh.

## Diagnosis and recovery

Run diagnostics without mutation first:

```sh
engram doctor /path/to/store --format json
```

If a required check reports recognized stale controller state, request bounded
recovery explicitly:

```sh
engram doctor /path/to/store --recover
```

Recovery acts only on exact state owned by this CLI after proving that its
recorded process is dead. It does not fetch, push, execute store hooks, guess an
ambiguous ref outcome, or discard unrelated draft bytes. Unknown, malformed,
foreign, live-owner, or concurrently changed state remains blocked for manual
inspection. Preserve the store and its sibling `*.engram-*-v1.json` state when
escalating a failure.

## Backup, restore, and migration

Back up the whole store root, including `.git`; copying only Markdown files
preserves a portable snapshot but not accepted history or managed-store
identity. Controller-owned hook trust is intentionally outside the store and
should normally be re-established rather than copied to another host.

After restore or physical relocation:

1. run `engram doctor`;
2. audit with `engram check --accepted`;
3. inspect and re-authorize the exact hook set if execution is desired; and
4. verify remote URL/upstream configuration before pull or push.

## Upgrades and rollback

Read the release notes, verify the new archive, and compare
`engram version --format json` before replacing the binary. A newer binary may
support different exact specification bytes while the store remains portable.
Keep the previous verified binary until `doctor`, accepted audit, and one
read-only status pass. Binary rollback does not rewrite the store; do not use an
older binary if its version descriptor does not support the store's required
specification revision.

## Security boundary

- Store files and accepted commits are untrusted input.
- Trusted preparation hooks are external programs with their own host
  authority; trust is exact, complete-set, local, and revocable.
- Git configuration, attributes, native hooks, inherited environment, lazy
  object fetching, and path aliases are isolated or rejected by managed flows.
- Read/write, hook execution, credentials, fetch, and publication are separate
  authorities. One does not imply another.
