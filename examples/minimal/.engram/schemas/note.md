---
type: note
version: 1
description: "Free-form note; the universal low-ceremony baseline type."
schema:
  type: object
  properties:
    type: {const: note}
    description: {type: string, minLength: 1, maxLength: 200}
    pinned: {type: boolean}
    tags:
      type: array
      items: {type: string}
  additionalProperties: true
---
# note

The floor of the store: a typed record with nothing required beyond the
universal labels. Deliberately cheap — the value of a markdown store is
low write friction, so remembering something must never wait for
ceremony.

## When to use it — and when not

Use `note` when no schema-bearing type fits. If you find yourself
writing the third `note` about the same kind of thing, that is a
missing type, not a reason to force structure into notes.

## Fields

`description` is the record's catalog line: write it for someone who
does not know the file exists. `tags` are optional free labels; prefer
links to records over tags when the target deserves existence.

## Template

```markdown
---
type: note
description: "<one line for the catalog>"
---
# <Title>

<Self-contained content; first sentence stands alone.>
```
