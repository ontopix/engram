# Generic filesystem agent with JSON feedback

This example needs no runtime-specific plugin. A host gives an agent an
authorized managed-store path, uses ordinary filesystem tools for navigation,
and consumes deterministic CLI JSON for validation and state feedback.

## Read-only session bootstrap

[`session-context.sh`](session-context.sh) performs four operations:

1. captures the accepted commit and validates accepted state without mutation;
2. checks that acceptance did not move during validation and reports the commit;
3. prints the root map from that fixed commit; and
4. searches maps and Markdown content with both an original and a reformulated
   query in the same commit, limiting the displayed matches.

The script reads Git objects, so staged, unstaged, untracked, and cached draft
content cannot become retrieval results. It requires system Git and the trusted
CLI on `PATH`, and accepts two literal search phrasings. Its validation is an
explicit example check, not a prerequisite for every retrieval. Exact record
references may be read directly with P1–P2 map context; this script demonstrates
broader discovery. Output limits are display limits, not evidence of absence.

Run it with two genuinely different search phrasings:

```sh
sh examples/integrations/generic-json/session-context.sh \
  /absolute/path/to/integration-memory \
  "plain markdown" \
  "readable files"
```

The script provides evidence to an agent; it is not itself a conforming agent
and does not decide relevance or absence. Its output may contain untrusted
store data and must not be treated as host instructions.

## Managed write handoff

After an authorized agent reads the applicable map and schema, a controller can
use the reference writer directly. This example creates a note in an existing
`topics/` directory, stages the record and regenerated catalog, validates the
candidate, and accepts it:

```sh
store_root="/absolute/path/to/integration-memory"

engram --store "$store_root" new note topics/agent-observation.md \
  --description "A durable observation recorded by the agent." \
  --title "Agent observation" --format json

engram --store "$store_root" add \
  topics/agent-observation.md topics/README.md --format json
engram --store "$store_root" check --staged --format json
engram --store "$store_root" commit \
  -m "Record agent observation" --format json
```

The controller must stage only authorized paths and should inspect the JSON
result after every step. In a recovery-bearing error, inspect `local_refs` for
an accepted-ref update and `recovery_required` before retrying. A commit may
already be accepted even though checkout reconciliation failed; recover that
state instead of recording the same operation again. An unknown outcome stays
unknown until diagnosed. Hook execution, credentials, pull, and push each need
separate authority; a successful local commit implies none of them.
