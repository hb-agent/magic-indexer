# Additional review rounds 11–18

Fixed 20-perspective roster (see `/tmp/fixed_roster.md`). Each round
runs the same 20 lenses on a fresh agent context. Stopping criterion:
user-specified — run all 8 rounds regardless.

Baseline: commit `6d5e149` (post-Round 10, all prior fixes landed).

## Round summary table

| Round | Critical | Major | Minor | Nice | Fixed | Commit |
|-------|---------|-------|-------|------|-------|--------|
| 11 | 0 | 0 | 0 | 0 | 0 | clean — all flagged items were false positives on verification |
| 12 | 0 | 0 | 0 | 0 | 0 | clean — no findings across all 20 perspectives |
| 13 | 0 | 0 | 0 | 0 | 0 | clean — reviewer ran "harder" pass, still found nothing |
| 14 | 2 | 1 | 0 | 0 | 3 | d8edda8 — jetstream cursorDone / config state races fixed |
| 15 | 0 | 0 | 0 | 0 | 0 | clean — one stylistic fragility noted, not actionable |
| 16 | 0 | 0 | 0 | 0 | 0 | clean — public pagination is already clamped at MaxPageSize=100 via query/connection.go:ClampPageSize |
| 17 | 0 | 0 | 0 | 0 | 0 | clean — regression sweep across all recent commits, no broken invariants |
| 18 | 0 | 0 | 0 | 0 | 0 | clean — final pass, no concrete defects |

### Round 14 highlights
- Real finding (first in this fixed-roster cycle): Jetstream consumer reset `c.cursorDone` and mutated `c.config.Collections` without holding `clientMu` in two places (Start reconnect loop + UpdateCollections). Concurrent Stop() races the pointer swap. Fixed by taking `clientMu` around every write. `go test -race` green.

### Round 11 notes
- Jetstream cursor reinit: FP — cursor is loaded fresh via `loadCursor` at consumer.go:158.
- backgroundServices.Stop double-stop: FP — slice is snapshotted + nil'd under lock; second caller gets empty snapshot.
- WebSocket GET-only enforcement: FP — gorilla/websocket.Upgrader already rejects non-GET per spec.
- ADMIN_DIDS log leak: FP — LogConfig logs `admin_dids_set` bool, not value.
