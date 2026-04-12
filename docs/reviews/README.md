# Overnight review history

The `per-labeler-definitions` branch went through **23 rounds of
review** before the first production-ish deploy. Each round
dispatched 20 reviewers with different perspectives, aggregated
their findings, applied fixes, and verified `go build / vet /
test / lint` stayed green.

This directory preserves that history so a future contributor
(or AI session) can see what's already been audited, which
findings were false positives on verification, and which items
were deferred with explicit reasoning.

## Files

| File                                            | Contents                                                                          |
|-------------------------------------------------|-----------------------------------------------------------------------------------|
| [`log_rounds_1_10.md`](log_rounds_1_10.md)     | Per-round summary table + highlights for rounds 1–10 (broad sweep, then deep-dives, adversarial, prod readiness, end-to-end, code quality, tests, security, performance, sanity) |
| [`log_rounds_11_18.md`](log_rounds_11_18.md)   | Per-round summary for the 8 fixed-roster rounds (11–18). Round 14 caught the only real defect of that cycle (Jetstream cursorDone state races). |
| [`report_rounds_1_10.md`](report_rounds_1_10.md) | First overnight report — totals, fix categories, deferred items, ship-readiness assessment for rounds 1–10. |
| [`report_rounds_11_18.md`](report_rounds_11_18.md) | Second report covering the additional 8 fixed-roster rounds. |
| [`roster_rounds_11_18.md`](roster_rounds_11_18.md) | The 20-perspective roster reused across rounds 11–18. Use this if you want to reproduce a fixed-roster review pass. |

## Combined totals

| Rounds | Reviewers | Critical | Major | Minor | Nice | Fixed |
|--------|-----------|----------|-------|-------|------|-------|
| 1–10   | 200       | 35       | 100   | 95    | 19   | 55 fixes + 3 regression tests |
| 11–18  | 160       | 2        | 1     | 0     | 0    | 3 fixes |
| 19–23  | 100       | 0        | 0     | 0     | 0    | 1 mid-session fix discovered during live deploy (`OptionalAuth` pass-through) |
| **total** | **460** | **37** | **101** | **95** | **19** | **59 fixes + 3 regression tests** |

## Rounds 19–23 (post-deploy)

These were not part of the original 18-round plan; they ran
*after* the user said "ship it" and during the live Railway
deploy. They are documented in the chat history for the deploy
session rather than in standalone log files. The one finding
they produced was the `OptionalAuth` middleware bug that broke
admin API-key auth — caught immediately when the first live
admin request returned `invalid_token`. Fix landed in commit
`f4e4169` ("OptionalAuth passes through on invalid OAuth token").

## How to reproduce a review pass

If you want to run a fresh round (or build round 24, 25, ...):

1. Pick a perspective set. Either reuse `roster_rounds_11_18.md`
   for breadth, or define a fresh 20-lens roster targeting whatever
   recent change you're worried about.
2. Dispatch the reviewers in parallel (this is much cheaper than
   serial — each reviewer gets a fresh agent context).
3. **Verify every flagged finding by reading the actual code.**
   Roughly half of all "CRITICAL" findings turned out to be false
   positives on verification. The signal-to-noise improves
   markedly if you make the agent show file:line evidence and
   you read the cited lines yourself.
4. Apply only the legitimate fixes. Commit + push.
5. Run `go build ./... && go test -race ./... && golangci-lint run ./...`
   and confirm green before declaring the round done.
6. Update the round table in this file.

## What's still deferred

- [#10 — Labeler signature verification](https://github.com/hb-agent/magic-indexer/issues/10) — needs upstream spec stability.
- [#13 — GDPR hard-delete endpoint](https://github.com/hb-agent/magic-indexer/issues/13) — needs a product/legal trigger.

Both have full deferral reasoning attached as issue comments.
