---
type: unknown-extension
version: 1
description: "Schema carrying an unassigned reserved engram keyword."
schema:
  type: object
  properties:
    type: {const: unknown-extension}
    description: {type: string, minLength: 1, maxLength: 200}
  x-engram-computed: true
---
# unknown-extension

`x-engram-computed` is intentionally invalid in v1.
