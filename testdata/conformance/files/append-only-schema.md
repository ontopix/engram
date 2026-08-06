---
type: append-log
version: 1
description: "Append-only record used by transition fixtures."
schema:
  type: object
  required: [type, description]
  additionalProperties: false
  properties:
    type: {const: append-log}
    description: {type: string, minLength: 1, maxLength: 200}
policy:
  append-only: true
---
# append-log

Modifications must retain the complete previous byte sequence as a prefix.
