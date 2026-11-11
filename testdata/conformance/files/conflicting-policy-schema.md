---
type: conflicted
version: 1
description: "Fixture with mutually exclusive lifecycle policies enabled."
schema:
  type: object
  properties:
    type: {const: conflicted@v1}
  additionalProperties: true
policy:
  immutable: true
  append-only: true
---
# conflicted
