# Codex integration

This example gives a Codex project a discoverable, independently owned engram
store. A tracked `engram.yaml` can acquire and attach it in one setup; the
imperative `engram attach` form remains available for an existing local store.
Setup installs verified project skills and points `AGENTS.md` at `MEMORY.md`.

## Declarative setup

Commit a project-root manifest using the store's actual repository URL:

```yaml
version: 1
harness: codex
attachments:
  - name: project
    url: git@github.com:example/project-memory.git
```

Then run `engram setup` from that project. The verified checkout is materialized
below ignored `.memory/project` and registered in `MEMORY.md`.

## Imperative local attachment

Set both paths to existing absolute paths:

```sh
store_root="/absolute/path/to/integration-memory"
project_root="/absolute/path/to/your-project"

engram attach "$store_root" --project "$project_root"
engram setup --harness codex --project "$project_root"
```

Setup verifies the canonical bundle embedded in the trusted CLI before writing
`.agents/skills/`. The generated `MEMORY.md` registry uses a project-relative
store path when possible. Attachment grants no authority; declarative setup
uses network credentials only to acquire a missing configured store.

## Try a session

Open Codex in `project_root` and use a bounded retrieval request such as:

> Read the attached engram store's root map, then find any durable context
> about why files are the source of truth. Search both the catalogs and record
> content, reformulating the query once. Report the paths you used.

For a write, make the authorization explicit:

> In the attached project-memory store, record the decision we just made as a
> note under `topics/`. Read the applicable schema first, update the catalog,
> stage only the files you changed, validate the staged candidate, and ask me
> before accepting it. Do not run hooks or use the network without separate
> authorization.

## Verify or remove the integration

```sh
engram --store "$store_root" check --accepted
engram --store "$store_root" status
engram detach "$store_root" --project "$project_root"
```

Detaching leaves an empty registry and the project-scoped integration in place.
It does not remove skills, delete the store, or change accepted memory.
