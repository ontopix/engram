---
type: journal-entry
version: 2
description: "Daily journal revision 2."
schema:
  type: object
  required: [type, description, date, source]
  additionalProperties: false
  properties:
    type: {const: journal-entry@v2}
    description: {type: string, minLength: 1, maxLength: 200}
    date: {type: string, format: date}
    source: {type: string, minLength: 1}
policy:
  append-only: true
---
# Journal revision 2

One dated entry. Append new evidence to the body without rewriting existing
bytes. This revision remains installed for the records that declare it.

## Template

```markdown
---
type: journal-entry@v2
description: Daily journal.
date: 2026-11-11
source: user-provided
---
# Daily journal

- Captured evidence.
```
