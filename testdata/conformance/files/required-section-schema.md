---
type: decision
version: 1
description: "Decision fixture requiring one body section."
schema:
  type: object
  required: [type, description]
  additionalProperties: false
  properties:
    type: {const: decision}
    description: {type: string, minLength: 1, maxLength: 200}
body:
  required-sections: [Outcome]
---
# decision
