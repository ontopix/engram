---
type: note
description: "Indexes and caches are rebuildable projections; the files stay the truth."
---
# Derived state

Any index built over this store — full-text, vectors, backlinks — must
be reconstructible from the files alone, and loses every disagreement
with them. That one rule keeps retrieval infrastructure optional,
replaceable, and incapable of holding the memory hostage.
