# Examples

Everything here is illustrative and non-normative. The snapshots conform to the
specification at HEAD and are validated in CI. Any people, projects and sources
that appear in them are fictional.

| Example | Kind | Use it to |
|---|---|---|
| [`minimal/`](minimal/README.md) | Snapshot | See the smallest conforming store: a baseline type, maps and linked records |
| [`schema-versions/`](schema-versions/README.md) | Snapshot | See three revisions of one type coexist without rewriting old records |
| [`journal/`](journal/README.md) | Snapshot with records | Read a fictional month of daily evidence with weekly and monthly reviews |
| [`packages/`](packages/README.md) | Snapshot without records | Copy reusable journal, project and people packages into your own store |
| [`integrations/`](integrations/README.md) | Runtime guides | Attach a store to Codex, Claude Code or a generic filesystem agent |

## Where to start

- To learn the format, read `minimal/` and then `schema-versions/`.
- To see what a populated store looks like, read `journal/`.
- To start your own memory, copy a package from `packages/`. Each one brings its
  conventions, record types and routines, and takes its settings from the host.
- To connect a store to an agent runtime, follow `integrations/`.

## Two journals

`journal/` and `packages/journal/` are different designs, each with a local
definition of the same type name. The first is a populated, append-only journal
for one fictional project, with one routine. The second is an empty, reusable
package: an editable daily log with entry ids and source coverage, three
routines and host-supplied settings. Neither replaces the other.

## Check the snapshots

From a trusted checkout of the Engram source repository:

```sh
go build -o ./engram ./cmd/engram
for example in minimal schema-versions journal packages; do
  ./engram check "examples/$example"
done
```
