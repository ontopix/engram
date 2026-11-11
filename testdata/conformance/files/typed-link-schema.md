---
type: typed-link
version: 1
description: "Fixture type whose target must also be a typed-link record."
schema:
  type: object
  required: [type, description, target]
  additionalProperties: false
  properties:
    type: {const: typed-link@v1}
    description: {type: string, minLength: 1, maxLength: 200}
    target:
      type: string
      x-engram-link:
        types: [typed-link]
---
# typed-link
