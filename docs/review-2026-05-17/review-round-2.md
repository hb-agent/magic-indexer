# Implementation-review round 2 — review-2026-05-17

Per AGENTS.md §"deep flow" step 6. Three parallel reviewers ran
after the 11-commit part-1 series landed on staging:

- **IR1 — Functional correctness** (Track 4b SQL, Track 3c
  WaitGroup, Track 3a compares, Track 5 split correctness,
  Track 6 closures, things prior reviewers missed)
- **IR2 — Smoke test** (quality gates, metric naming, migration
  conventions, TOC accuracy, Track 1 regression)
- **IR3 — Code quality** (consistency with existing codebase,
  comment density, commit-message conventions, magic numbers)

All three reported the part-1 PR is ship-able. No data corruption,
auth-bypass, or panics. Findings clustered around: one missing
test promised in the plan, one numeric promise miss, several
small comment / consistency nits.

Round 3 not warranted — the remaining items are nit-tier.

---

## Decisions

### IR1 — Functional correctness

| # | Finding | Decision | Rationale |
|---|---------|----------|-----------|
| IR1.A | **Track 4 acceptance criteria #1 + #2 unverified** — the dedup unit tests promised in plan.md were not written. The load-bearing UNION-SELECT fallback at `jetstream_activity.go` is verified only by manual walk-through. The repository test infra IS Postgres-backed, so the tests are buildable; they just weren't written. | **ACCEPT** | Real gap. Add two tests in `jetstream_activity_test.go`: (a) same `*int64` twice → `id1 == id2`, `GetCount() == 1`; (b) two distinct ids → two rows. Highest-priority follow-up. |
| IR1.B | `resolvers.go` is **520 lines**, plan acceptance criterion #3 said "under 500". | **ACCEPT (relax)** | The ~50-line TOC comment is load-bearing for preventing partition drift (the R2.5 obligation from round 1). Trimming the TOC undermines the round-1 fix. Update the acceptance criterion in plan.md to "under 550 lines" with a one-line note that the TOC counts. |
| IR1.C | rate-limited error logger comments justify atomic counters by referencing `/metrics` scraping, but those fields aren't exposed to metrics. Real reason for atomic is the CAS at the throttle-emit site. | **ACCEPT** | Tighten the comments in both `internal/jetstream/consumer.go` and `internal/tap/consumer.go`. |
| IR1.D | `clampAdminPageSize` / `maxAdminPageSize` live in `resolvers.go` but their only caller is `resolvers_labels.go`; plan R2.4 said helpers travel with callers. | **DEFER (accept drift)** | The TOC comment at `resolvers.go` already names them as living there; the contract is transparent. Moving them would re-bloat `resolvers_labels.go` by 12 lines and reduce-bloat `resolvers.go` by 12 lines — net zero. The R2.5 TOC obligation is met; calling this drift would be pedantic. |

### IR2 — Smoke test

All 7 checks pass. No follow-ups required from IR2.

The +1 bonus constant-time site IR2 found at `oauth_handlers.go:1067` is `checkRefreshTokenDPoPBinding` — already a constant-time compare from a prior pass, correctly counted in the surface but not added by this batch.

### IR3 — Code quality

| # | Finding | Decision | Rationale |
|---|---------|----------|-----------|
| IR3.1 | rateLimitedErrLogger duplication: `internal/jetstream/consumer.go` comments cite A12 explicitly, `internal/tap/consumer.go` does not. | **ACCEPT** | Add the A12 cross-reference to the Tap-side comment so a future contributor reading only one file finds the decision trail. |
| IR3.2 | `init2` docstring at `internal/metrics/metrics.go:638` calls the function `init2` but the actual function is `init`. Godoc convention says the comment should start with the function name. | **ACCEPT** | Rewrite the comment to start with `init`. |
| IR3.3 | Magic numbers `loudLimit: 5` and `throttleEvery: time.Minute` duplicated across both consumers. | **ACCEPT (constants)** | Extract package-level constants per consumer so the next change to the policy is a one-line edit, not two. Per-package keeps the A12 "don't share machinery" decision intact. |
| IR3.4 | Plan R3.3 cites `labeler/consumer.go:399, 427-432` as the rate-limit "existing pattern" being mirrored, but `labeler/consumer.go` has no such pattern. | **ACCEPT (doc fix)** | The reviewer agent that made the original R3.3 claim was wrong about the citation; the code shipped is correct, only the citation rots. Note in this doc; do not modify the plan files (they're historical record). |

---

## Net follow-up: single "review polish" commit on staging

Bundled into one commit because each individual change is tiny and they share scope:

1. Add `TestJetstreamActivity_LogActivity_DedupOnSourceEventID` and `TestJetstreamActivity_LogActivity_DistinctSourceEventIDs_NoDedup` to `jetstream_activity_test.go` — covers IR1.A.
2. Add `TestJetstreamActivity_LogActivity_NilSourceEventID_NoDedup` for completeness — verifies the historical-row path (no sourceEventID) doesn't accidentally dedup.
3. Tighten the rate-limited logger comments in both consumer files (IR1.C + IR3.1).
4. Extract `defaultErrLogLoudLimit` and `defaultErrLogThrottle` constants in each consumer package (IR3.3).
5. Rewrite the second `init()` docstring in `metrics.go` (IR3.2).
6. Update plan.md acceptance criterion #3 from "<500 lines" to "<550 lines (TOC counts)" with a one-line note (IR1.B).

That's it. No code structure changes, no behavioural changes.

## Items deliberately not adopted

- **IR1.D** — partition-drift on `clampAdminPageSize` / `maxAdminPageSize`. Decision: accept the drift because the TOC documents it.
- **IR3.4 plan rewrite** — don't rewrite historical plan.md / review-round-1.md to fix a stale citation; note in this doc instead.

## Round 3?

Not warranted. The follow-up commit closes the only real gap (IR1.A test coverage); everything else is polish. Round 3 would be marginal.
