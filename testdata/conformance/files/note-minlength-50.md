---
type: note
version: 1
description: "Invalidly restrictive note baseline fixture."
schema:
  type: object
  properties:
    type: {const: note}
    description: {type: string, minLength: 50, maxLength: 200}
    pinned: {type: boolean}
  additionalProperties: true
---
# note

This fixture is valid JSON Schema but violates the syntactic note baseline.
