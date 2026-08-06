---
type: fixed
version: 1
description: "Immutable record used by transition fixtures."
schema:
  type: object
  required: [type, description]
  additionalProperties: false
  properties:
    type: {const: fixed}
    description: {type: string, minLength: 1, maxLength: 200}
policy:
  immutable: true
---
# fixed

Records of this fixture type cannot change after creation.
