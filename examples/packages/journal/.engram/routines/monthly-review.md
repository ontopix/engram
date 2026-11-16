---
engram: routine/v1
cron: "30 20 1 * *"
---
# Monthly activity review

Assess activity within the configured scope for the previous, fully closed calendar
month. The default schedule is 20:30 UTC on the first day of each month. Run only
when the host's assignment names this exact routine; follow the contract at
`journal/README.md` from the store root.

## Establish the review

Verify that this declaration and the contract are committed and unchanged. Read
the contract, maps and `period-review@v1` schema; resolve the required host settings.
Use month boundaries in the active time zone: the first day at 00:00 through the
first day of the next month at 00:00. Set `period_end` to the month's last day and
verify the closing boundary has passed. Also revisit earlier closed months whose
review is missing or affected by late evidence; honor previously completed no-op
reviews. Review pending earlier months oldest first, so each review can carry
open items from its predecessor. Preserve unrelated edits.

Apply the contract's `Review readiness` checks before reading for synthesis or
writing a review. Record the target month and time zone on first execution; keep
them on retries. Verify acceptance of the daily consolidation through the closing
boundary, using its accepted coverage and host result. A partial accepted daily
result allows an explicitly incomplete review. If the prerequisite is missing,
running, failed or unaccepted, leave this review pending with its reason and target
month. Resume it after daily acceptance, without waiting for the next monthly
trigger or launching an extra daily capture. The 30-minute schedule gap is not a
dependency; weekly reviews do not have to finish before the monthly review.

## Review

1. Use weekly reviews for orientation, then read every available daily record of
   the month in `journal/daily/` and relevant linked meeting records when available.
   Journal paths are relative to the store root. Inspect each day's current
   `Coverage`. Recheck consequential claims against the latest accepted daily log
   entries, including corrections and items omitted from weekly summaries.
   Queue newly discovered
   evidence for the next daily consolidation and revisit the review after acceptance;
   do not launch another daily capture or cite an unwritten daily anchor. Also read
   `Open at period end` in the latest earlier accepted monthly review, if one exists.
2. Use entry keywords to assess changes across projects and subjects: consequential
   events, decisions and rationale, hypotheses supported or challenged, commitment outcomes, unresolved
   questions and corrections. Preserve attribution and uncertainty. Use prior
   months only as labelled context for changes, not as activity of this month.
   Account for every open item carried from the earlier monthly review: resolved by
   an entry of this month, still open, or dropped with its justification.
3. Write or revise `journal/monthly/<YYYY>/<YYYY-MM>.md` with `period-review@v1`.
   Follow the contract's existing section structure and cite all daily records
   reviewed, optionally adding weekly reviews. Cite material claims by entry id
   (`<YYYY-MM-DD>#NN`). Include explicit date and source
   coverage, unresolved contradictions and late evidence. Exclude out-of-month
   events even when a weekly review crosses the month boundary.
   Apply the contract's carry-forward rules to carried open items and list the
   earlier monthly review in `sources`.
4. If durable knowledge destinations are configured, promote validated knowledge
   with links to its daily evidence; keep hypotheses and interpretations labelled.
   Do not copy entire reviews elsewhere.
5. If there was no relevant activity and coverage supports that conclusion, write
   no review and record completion against the accepted daily coverage. Empty logs
   with complete coverage support that conclusion; missing files do not. Coverage
   gaps require an explicitly incomplete assessment, not a claim
   of inactivity. If there are no daily anchors, report the gap instead of inventing
   a review or its required source references. Open items carried from an earlier
   review do not by themselves require a review; the next one carries them from
   the latest review written.

## Accept and report

Regenerate affected catalogs and commit through the managed writer after
`engram check`. Reuse the existing month's path on retry; skip writes without a
material assessment change. Report the review's path and a brief summary through
the host's configured delivery channel and language, including material coverage
limits. Record the outcome in the host's run record.
