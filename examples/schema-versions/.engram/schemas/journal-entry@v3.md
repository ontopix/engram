---
type: journal-entry
version: 3
description: "Daily journal revision 3."
schema:
  type: object
  required: [type, description, date, source, category]
  additionalProperties: false
  properties:
    type: {const: journal-entry@v3}
    description: {type: string, minLength: 1, maxLength: 200}
    date: {type: string, format: date}
    source: {type: string, minLength: 1}
    category: {enum: [work, personal]}
policy:
  append-only: true
---
# Journal revision 3

One dated entry. Append new evidence to the body without rewriting existing
bytes. This revision remains installed for the records that declare it.

## Template

```markdown
---
type: journal-entry@v3
description: Daily journal.
date: 2026-11-11
source: user-provided
category: work
---
# Daily journal

- Captured evidence.
```
