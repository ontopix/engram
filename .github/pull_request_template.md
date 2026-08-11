## Summary

<!-- What changes, and what user-visible problem does it solve? -->

## Authority and scope

- [ ] Normative core specification
- [ ] Normative managed Git annex
- [ ] Reference CLI contract or implementation
- [ ] Non-normative documentation, schema, skill, adapter, or example
- [ ] Build, test, packaging, or repository maintenance

<!-- Name the authoritative document for the behavior being changed. -->

## Compatibility and security

<!-- Describe compatibility, migration, finding-code, trust, authority, filesystem, Git, credential, and network effects. Write "None" where appropriate. -->

## Verification

<!-- List exact commands and focused cases exercised. -->

- [ ] `go test ./...`
- [ ] `go vet ./...`
- [ ] `go build ./cmd/engram`
- [ ] `go mod tidy -diff`
- [ ] `git diff --check`

## Repository invariants

- [ ] Tests cover new behavior or the PR explains why no test is needed.
- [ ] Observable CLI docs, output, goldens, and implementation agree.
- [ ] Normative revisions and `CHANGELOG.md` are updated when required.
- [ ] Curated schemas and examples remain synchronized with the core format.
- [ ] `schemas/note.md` remains byte-identical to core Appendix A.3.
- [ ] The patch contains no credentials, private store content, or accidental absolute paths.
