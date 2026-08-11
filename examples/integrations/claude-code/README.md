# Claude Code integration

This example exposes an independent managed store through a project's
`CLAUDE.md` and installs the canonical skills in Claude Code's project-scoped
skill directory. The attachment is discovery metadata only.

## Install the trusted skills and attach the store

Run this from a trusted checkout of the engram repository. Set all three paths
to existing absolute paths:

```sh
source_root="/absolute/path/to/engram"
store_root="/absolute/path/to/integration-memory"
project_root="/absolute/path/to/your-project"

mkdir -p "$project_root/.claude/skills"
cp -R "$source_root/skills/." "$project_root/.claude/skills/"

engram attach "$store_root" \
  --project "$project_root" \
  --entrypoint CLAUDE.md
```

Install the skills only from a source or release trusted independently of the
store. `engram attach` audits the managed store, preserves existing
`CLAUDE.md` content, and creates or replaces one delimited adoption block with
the store's canonical local path.

## Try a session

Open Claude Code in `project_root` and ask it to retrieve memory without loading
the whole store:

> Use the attached engram store. Start at its root map and find the records
> relevant to derived state. Combine catalog descent with two content-search
> phrasings, then summarize the answer with the record paths you inspected.

An explicitly authorized write can be requested like this:

> Update the attached store with a durable note about this project's indexing
> policy. Read the note schema and relevant maps first. Preserve unrelated
> drafts, stage only the intended record and catalog changes, validate the
> candidate, and ask before the managed commit. Do not trust hooks implicitly.

## Verify or remove the integration

```sh
engram --store "$store_root" check --accepted
engram --store "$store_root" status
engram detach "$store_root" \
  --project "$project_root" \
  --entrypoint CLAUDE.md
```

Detaching leaves both the store and the project-scoped skill installation in
place.
