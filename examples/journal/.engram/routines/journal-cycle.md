---
engram: routine/v1
cron: "0 3 * * *"
---
# Daily journal cycle

This declaration describes work for an independently authorized host. Use only
sources, tools, and store operations the host has granted. Its presence does
not grant authority or activate a scheduler. The bundled Atlas source file is
a fictional fixture, not a live ingestion source.

## Establish the run

Use the host-supplied execution time and the root map's `Europe/Madrid`
calendar. Work sequentially as the checkout's sole automated editor. Read the
applicable maps and schemas, and capture one accepted base for retrieval.
Distinguish that accepted evidence from the working draft built during this
operation. Preserve unrelated edits; stop if they cannot be separated safely.

## Capture daily evidence

1. Inspect newly available material from authorized sources, including backlog
   after a missed run and late evidence about older dates. Execution receipts
   and ingestion cursors, if used, remain host-owned outside the store.
2. Search existing daily records for each observed source identifier and event
   before adding it. An identifier already captured does not justify a second
   copy; an actual source correction warrants a clearly labeled new block.
3. File useful observations by the event's local date at
   `daily/<year>/<date>.md`. Create a directory map with a new year. Preserve
   observed sources, distinguish FACT, DECISION, HYPOTHESIS, and CORRECTION, and
   include the reason for a decision when the source provides it.
4. Append without changing earlier bytes. For late evidence, include the event
   date and the actual recording date. Append an explicit correction to the
   affected day's record; decisions made in response belong to their own day.
5. Capture only material useful to future work. A fully checked interval with
   nothing useful can be a host-recorded no-op. A missing source is a coverage
   gap, never evidence that nothing happened. Surface source failures to the
   host and make known material gaps visible in affected reviews.

## Review closed periods

6. Consider closed weeks and months that are due, were missed, or are affected
   by newly captured evidence. Do not rely only on the current weekday or day
   of the month: a delayed run can still complete an earlier period. Host
   receipts distinguish completed no-ops from missed runs without empty files.
7. For each affected closed ISO week, read its available daily evidence and
   author or revise `weekly/<ISO-week-year>/<ISO-week-year>-w<week>.md` using
   `period-review@v1`. Explain the editorial selection and its rationale. Keep
   decisions and open questions anchored to the period in which they occurred.
8. Then consider affected closed calendar months. Use weekly assessments for
   orientation and inspect all available daily evidence inside the month.
   Recheck consequential claims against those daily records, including items
   omitted from weekly highlights. Exclude out-of-month events even when they
   appear in a crossing week's review. Author or revise
   `monthly/<year>/<year>-<month>.md` with explicit coverage and late evidence.
9. Use the same path for each period. Preserve evidence links and make
   corrections visible; skip immaterial rewrites. Do not create a review that
   implies evidence where none exists. Recomputable extracts used to help the
   assessment stay outside accepted content, in host state or the root cache.

## Accept and report

Regenerate affected catalogs, inspect the complete authorized change set, and
stage only that operation. Delegate preparation, complete final-candidate
validation, and acceptance to the managed writer. Report accepted changes and
coverage gaps to the host. If nothing material changed, leave the store alone.

If acceptance or checkout completion fails, inspect the writer's evidence. A
rejected operation remains a draft; an accepted commit with recovery pending
is not permission to repeat the write. Surface an unknown outcome without
assuming safe retry. Local acceptance does not authorize network publication.
