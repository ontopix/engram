# Contributing

Until v1 is declared stable, the spec is a draft and may change without
migration guarantees. Focused bug fixes, portability improvements,
documentation corrections, conformance cases, and well-scoped design proposals
are welcome.

## Before starting

- Report vulnerabilities privately through [SECURITY.md](SECURITY.md), never in
  a public issue or pull request.
- Open an issue before investing in a large feature, new command surface,
  diagnostic-code change, or normative change. This lets maintainers confirm
  scope and the correct authority document first.
- Small fixes with an obvious expected result can go directly to a pull
  request.

## Development setup

Development requires a system Git executable and Go 1.25 or 1.26. Work on a
focused branch from current `main`, keep generated and vendored provenance
files intact, and run the standard local checks:

```sh
go test ./...
go vet ./...
go build ./cmd/engram
test -z "$(gofmt -l .)"
go mod tidy -diff
git diff --check
```

CI repeats these checks across macOS, Linux, and Windows and adds race, offline,
fuzz-smoke, module, generated-data, and repository-integrity gates. A change
that depends on platform-specific behavior should include corresponding test
coverage or explain the gap in its pull request.

## Normative changes

A normative change is any edit that alters what a conforming snapshot,
tool, or agent must do — in the core spec or in a normative annex.

Every normative change MUST:

1. update the `Revision` date of the affected document;
2. add a `CHANGELOG.md` entry describing the change and its impact;
3. keep `schemas/` and `examples/` conforming in the same commit, and
   keep `schemas/note.md` byte-identical to core Appendix A.3 — the
   repository never points at itself in a broken state.

## Non-normative changes

Rationale, skills and adapters guidance, curated schema documentation, README,
and examples' prose receive editorial review. Keep the boundary visible:
non-normative documents must not smuggle in RFC 2119 requirements that the
normative core and annexes do not state.

The CLI contract is non-normative with respect to the standard, but it is the
authority for the reference CLI's observable behavior. A command-surface
change must update the contract, implementation, applicable text and JSON
goldens, and `CHANGELOG.md` together.

## Pull requests

Keep each pull request centered on one coherent change. Its description should
explain the user-visible problem, the selected boundary or authority document,
compatibility and security effects, and the verification performed.

Before requesting review, confirm as applicable that:

- new behavior has positive and negative tests;
- observable CLI behavior, help, JSON, goldens, and the CLI contract agree;
- normative documents have a current `Revision` date and `CHANGELOG.md` entry;
- schema-format changes update curated schemas and examples in the same commit;
- `schemas/note.md` remains byte-identical to core Appendix A.3;
- `examples/minimal/` still passes `engram check`;
- documentation links and copyable command sequences work; and
- no credentials, private store content, or machine-specific absolute paths
  entered the patch.

Reviews may ask for a smaller change when a proposal crosses standard, CLI,
runtime-adapter, and deployment boundaries at once.

## Versioning discipline

- Core spec: major version bumps on breaking structural or semantic
  change after stabilization; minor for backward-compatible additions.
- Annexes version independently. An annex's version is never inferred
  from the core's.
- Before the first stable release (`v1.0.0`), check codes may change without
  compatibility guarantees. From `v1.0.0` onward, a published code never
  changes meaning and is never reused.

## License

By submitting a contribution, you agree that it may be distributed under the
repository's [MIT License](LICENSE), unless the maintainers explicitly agree to
different terms before submission.
