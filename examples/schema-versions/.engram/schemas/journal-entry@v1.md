---
type: journal-entry
version: 1
description: "Daily journal revision 1."
schema:
  type: object
  required: [type, description, date]
  additionalProperties: false
  properties:
    type: {const: journal-entry@v1}
    description: {type: string, minLength: 1, maxLength: 200}
    date: {type: string, format: date}
policy:
  append-only: true
---
# Journal revision 1

One dated entry. Append new evidence to the body without rewriting existing
bytes. This revision remains installed for the records that declare it.

## Template

```markdown
---
type: journal-entry@v1
description: Daily journal.
date: 2026-11-11
---
# Daily journal

- Captured evidence.
```
