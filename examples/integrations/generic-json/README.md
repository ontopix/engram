# Generic filesystem agent with JSON feedback

This example needs no runtime-specific plugin. A host gives an agent an
authorized managed-store path, uses ordinary filesystem tools for navigation,
and consumes deterministic CLI JSON for validation and state feedback.

## Read-only session bootstrap

[`session-context.sh`](session-context.sh) performs four bounded operations:

1. verifies the accepted store without mutation;
2. emits machine-readable status;
3. prints the root map; and
4. searches maps and Markdown content with both an original and a reformulated
   query, limiting the displayed matches.

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
result after every step. Hook execution, credentials, pull, and push each need
separate authority; a successful local commit implies none of them.
