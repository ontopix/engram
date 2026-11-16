---
description: "Structured daily activity log, consolidated once a day from multiple sources."
catalog: dirs
---
# daily

One file per local day, consolidated once a day by the designated writer as a flat structured log.
Follow the sources, entry format and coverage rules in the parent journaling
contract. Weekly and monthly reviews provide the thematic summaries.

Keep a record for every reviewed date, including an empty log with a brief outcome
and source coverage. A missing record means unknown coverage. Correct entries and
the existing Coverage section in place when new evidence becomes available. Keep
stable entry ids; managed Git history retains earlier accepted versions.

## Map

<!-- engram:catalog -->
<!-- /engram:catalog -->

## Placement

Create the year directory and its README in the same operation as its first record.
Each log item includes a stable id, event time, kind, keywords, a description and source references.
