---
type: dated-event
version: 1
description: "Dated event used to verify YAML scalar resolution."
schema:
  type: object
  required: [type, description, date]
  additionalProperties: false
  properties:
    type: {const: dated-event}
    description: {type: string, minLength: 1, maxLength: 200}
    date: {type: string, format: date}
---
# dated-event

Fixture type proving that a plain YAML date scalar is a string under the
YAML 1.2.2 Core Schema used by engram.
