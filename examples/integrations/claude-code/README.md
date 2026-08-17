# Claude Code integration

This example exposes an independent managed store through project `MEMORY.md`
and installs a project-scoped Claude Code integration.

For one-command onboarding, commit this project-root manifest with the actual
store URL:

```yaml
version: 1
harness: claude-code
attachments:
  - name: project
    url: git@github.com:example/project-memory.git
```

Run `engram setup` from that project. It acquires a missing
current-state-validated clone below `.memory/project`, reconciles `MEMORY.md`,
and installs the integration. Use `engram setup --check-history` to require a
complete accepted-lineage audit.

## Imperative local attachment

Set both paths to existing absolute paths:

```sh
store_root="/absolute/path/to/integration-memory"
project_root="/absolute/path/to/your-project"

engram attach "$store_root" --project "$project_root"
engram setup --harness claude-code --project "$project_root"
```

Setup verifies the canonical bundle embedded in the trusted CLI before writing
`.claude/skills/`, then preserves existing `CLAUDE.md` bytes around its owned
pointer to `MEMORY.md`.

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
engram detach "$store_root" --project "$project_root"
```

Detaching leaves an empty registry and the project-scoped skill installation
in place.
