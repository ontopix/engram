# Codex integration

This example gives a Codex project a discoverable, independently owned engram
store. `engram attach` writes only its versioned adoption block to the project's
`AGENTS.md`; it does not grant access, trust hooks, or synchronize the store.

## Install the trusted skills and attach the store

Run this from a trusted checkout of the engram repository. Set all three paths
to existing absolute paths:

```sh
source_root="/absolute/path/to/engram"
store_root="/absolute/path/to/integration-memory"
project_root="/absolute/path/to/your-project"

mkdir -p "$project_root/.agents/skills"
cp -R "$source_root/skills/." "$project_root/.agents/skills/"

engram attach "$store_root" \
  --project "$project_root" \
  --entrypoint AGENTS.md
```

The copied `manifest-v1.json` closes the expected skill set. The source
checkout or release that supplied it must already be trusted independently of
the store. The generated adoption block contains the store's canonical
absolute path, so it is intentionally generated locally rather than committed
as a reusable fixture.

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
engram detach "$store_root" \
  --project "$project_root" \
  --entrypoint AGENTS.md
```

Detaching removes only the CLI-owned adoption block. It does not remove the
skills, delete the store, or change accepted memory.
