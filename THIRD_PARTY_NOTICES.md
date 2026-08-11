# Third-party notices

The engram reference CLI is MIT licensed. Its compiled dependency closure
includes the following third-party software; release builds preserve the exact
versions in `go.mod` and `go.sum`.

| Module | Version | License |
|---|---:|---|
| `github.com/santhosh-tekuri/jsonschema/v6` | v6.0.2 | Apache-2.0 |
| `github.com/yuin/goldmark` | v1.8.5 | MIT |
| `go.yaml.in/yaml/v3` | v3.0.5 | Apache-2.0 and MIT notices |
| `golang.org/x/text` | v0.14.0 | BSD-3-Clause |

Each release archive includes the authoritative texts for this compiled
dependency closure under `licenses/third-party/`. This inventory is
informational; those texts control their respective software.

Release archives also include the Go toolchain's BSD-style license and patent
grant under `licenses/go/`, applicable module notices and patent grants under
`licenses/third-party/`, and the Unicode Character Database 17.0.0 source
notice and Unicode License v3 under `provenance/unicode17/`.
