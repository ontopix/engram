# Conformance fixture manifest

`cases.json` is the machine-readable seed corpus for the normative behaviors
that must not be delegated to host or library defaults. It is intentionally
small; the implementation plan requires expanding it to cover every emitted
catalog code before v1.

## Materialization contract

- `seed` and operation `source` values are repository-root-relative UTF-8
  paths. Operation `path` values are normalized store-root-relative paths.
- Each state starts as a fresh byte-for-byte copy of `seed`.
- `common` operations are applied first, followed by the state's own
  operations in array order.
- `write_text` copies the source file's bytes to `path`.
- `write_base64` decodes `content` with strict RFC 4648 Base64 and writes the
  resulting bytes to `path`.
- `remove` removes exactly one regular file. It is an error when that file is
  absent or is not regular.
- Materialization creates missing parent directories as real directories and
  never follows symbolic links.
- A snapshot case materializes `snapshot`. A changeset case materializes
  `base` and `candidate` independently. `base: "unavailable"` denotes the
  non-initial missing-base condition of core §8.1; it does not denote the
  known empty initialization base.
- `expected.findings` is the complete normative ordered sequence of
  `(code, path)` identities. A changeset case additionally declares the
  expected `complete` or `indeterminate` status.

The harness must reject an unknown manifest version, operation, or field. It
must not infer fixture behavior from directory enumeration order, locale,
timezone, filesystem normalization, or network access.
