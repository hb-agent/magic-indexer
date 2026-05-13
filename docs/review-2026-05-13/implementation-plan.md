# Implementation plan — review follow-ups

**Status**: PLAN — pending review.
**Drives from**: [`docs/review-2026-05-13/report.md`](./report.md) (six-reviewer audit of today's changes).
**Target branch**: `feat/review-2026-05-13-fixes`, off `origin/main` (PR target to be decided at draft time — `staging` per project convention, or `main` matching the recent direct-fix pattern in #78).

**Scope frame**: the user asked for "all changes via deep flow." Read strictly that's P0 + P1 from the report (12 items). P2 / skipped items remain backlog. This plan covers P0 + P1 + the minor Q/P items that fold naturally into the same commits.

---

## 1. Larger goal this serves

The report surfaced 89 findings across six reviewer passes. After round-2 verification three were downgraded. The action plan converged on five P0 items (closing the destructive-op posture and the OAuth/migration gaps) and seven P1 items (indexing, pgx cancel semantics, refactors, observability, hardening). This plan ships those as a single bundled PR with atomic commits per logical track so each is independently revertable.

Two outcomes:

1. **Close the patterns today's PRs left half-finished.** The biggest risk in the codebase right now is *inconsistency*: three destructive-op patterns, three DID-validation styles, two filter-builder copies. Today's review made this visible; landing the unifying refactors is the cheapest moment to do it (two consumers each, not five).
2. **Close real operational gaps before they bite production.** Migration 013 is a silent index regression on rollback. The new filter shapes are unindexed and will trip `QUERY_TIMEOUT` against busy collections. The pgx cancel-on-deadline churn amplifies the latency hit it's protecting against. None of these is theoretical.

---

## 2. Scope

**One PR.** Atomic commits per track so each can be reverted independently. Order matters where files overlap.

### Tracks — P0

| # | Track | Refs | Files |
|---|---|---|---|
| **1** | Migration 013 collision fix | P-1, A-5, V-004 | `internal/database/migrations/postgres/0NN_drop_legacy_gin_recreate_path_ops.up.sql` (new), corresponding `.down.sql`, `internal/database/migrations/migrations_test.go` (new `TestMigrations_UniqueIndexNames`) |
| **2** | `did.IsValid` rollout closure + lint | SEC-3, A-3, V-002, V-006 | `internal/graphql/admin/purge.go` (validate `did` in both resolvers), `internal/graphql/admin/resolvers.go` (`AddAdmin`, `RemoveAdmin`, `BackfillActor`), new `internal/lint/checks/no_did_prefix.go` (or doc note + tests if a custom analyzer is too heavy), `.golangci.yml` |
| **3** | `ResetAll` hardening | SEC-2, A-1, V-007, T-OBS-2 | `internal/graphql/admin/purge.go` (extend `PurgeTokenSigner` to support a "scope" claim), `internal/graphql/admin/resolvers.go` (`ResetAll` rewrite + complete table list), `internal/graphql/admin/schema.go` (mutation signature change: `previewResetAll` + `resetAll(confirmToken)`), `SECURITY.md` |
| **4** | OAuth `returnTo` allow-list | SEC-1, V-001 | `client/src/app/api/login/route.ts` (validate at write site), `client/src/app/api/oauth/callback/route.ts` (defense in depth: same check on the read side) |
| **5** | Settings form hydration | Q-1, V-003 | `client/src/app/settings/page.tsx` (`useState(()=>…)` → `useEffect`) |

### Tracks — P1

| # | Track | Refs | Files |
|---|---|---|---|
| **6** | pgx `CancelRequestContextWatcherHandler` | P-5 | `internal/database/postgres/executor.go` (pool config), `go.sum` if pgx version bump needed |
| **7** | Admin GraphQL `RequireAuth` + auth-before-body-decode | SEC-4 | `cmd/hypergoat/main.go` (router wiring), `internal/graphql/admin/handler.go` (order of operations) |
| **8** | `FilterKind` enum + per-lexicon descriptor registry | Q-2, A-2, Q-12 | `internal/database/repositories/filter.go` (introduce `FilterKind`, retire `IsArrayContributor` / `IsBadgeAwardSubject` booleans), `internal/graphql/schema/where.go` (collapse `buildContributorFieldFilter` + `buildBadgeAwardSubjectFieldFilter`; replace `wantsContributorFilter` / `wantsBadgeAwardSubjectFilter` with a per-lexicon descriptor lookup), tests updated |
| **9** | Index the new filter shapes | P-2, P-3 | New migration: partial-GIN expression index on extracted contributor identities for `org.hypercerts.claim.activity`; subject DID materialization (generated column on `record` or new `record_subject_did` sidecar — see § 3.6 alternatives); `internal/database/repositories/filter.go` (filter SQL rewritten to use the index) |
| **10** | Purge metrics + admin settings audit logs | T-OBS-1, T-OBS-2 | `internal/metrics/metrics.go` (counters + histogram), `internal/graphql/admin/purge.go` (wire metric calls), `internal/graphql/admin/resolvers.go` (audit logs for `UpdateSettings`, `ResetAll`, `AddAdmin`, `RemoveAdmin`) |
| **11** | Purge resolver hardening (error masking + token version + count-drift sentinel + redundant audit field) | Q-3, Q-4, Q-5, Q-8, Q-9 | `internal/graphql/admin/purge.go` |
| **12** | Resolver-level tests + Postgres-backed shape tests | T-COV-1, T-COV-3 | `internal/graphql/admin/purge_resolver_test.go` (new), `internal/database/repositories/records_filter_test.go` (extend with contributor + subject shape coverage), `internal/testutil` helpers if needed |

### Minor items folded into the above

- **Q-6** slog scrubbing helper → ships with track 10 (`internal/logsafe`).
- **Q-7** stale endorsement_test comment → trivial, ships with track 2.
- **Q-10** CHANGELOG entries → ships with the PR description / final commit.
- **Q-11** hoist `BatchItem` to module scope → trivial, ships with track 5 (client polish).
- **P-7** used-sig periodic sweeper → ships with track 11.
- **P-8** Tap call as fire-and-forget goroutine → ships with track 11.

### Commit order

The order matters where files overlap. Proposed:

```
1.  fix(db): migration to drop legacy GIN + recreate with jsonb_path_ops (P-1)
2.  fix(admin): close did.IsValid rollout gap + ban strings.HasPrefix(did,…) (SEC-3)
3.  feat(admin): preview-confirm + audit log for resetAll, addAdmin, removeAdmin (SEC-2 + A-1 + T-OBS-2)
4.  fix(client): strict returnTo allow-list at login + callback (SEC-1)
5.  fix(client): useEffect for settings form hydration + hoist BatchItem (Q-1 + Q-11)
6.  fix(db): pgx CancelRequestContextWatcherHandler (P-5)
7.  fix(server): RequireAuth + auth-before-body-decode on /admin/graphql (SEC-4)
8.  refactor(graphql): FilterKind enum + per-lexicon descriptor registry (Q-2 + A-2)
9.  feat(db): partial-GIN expression index for contributors + subject DID materialization (P-2 + P-3)
10. feat(metrics): purge metrics + admin audit log helpers (T-OBS-1 + Q-6)
11. fix(admin): purge resolver hardening — error masking, token v1, count-drift sentinel, audit field, periodic prune, fire-and-forget Tap (Q-3..Q-9 + P-7 + P-8)
12. test(admin): resolver tests for purge + Postgres-backed shape tests for filters (T-COV-1 + T-COV-3)
13. docs(changelog): unreleased section bundling all twelve tracks (Q-10)
```

Tracks 8 + 9 touch `filter.go` and must land in that order — the new index assumes the new SQL shape. Track 11 touches `purge.go` last because track 3 already extended that file; the `FilterKind` work in track 8 doesn't touch admin code. Track 12 lands last because it tests behaviour from tracks 3, 8, 9, 11.

---

## 3. Alternatives considered

### 3.1 One PR vs. one PR per track

**Chosen**: one PR with atomic commits.

**Rationale**: each track is small (most under ~150 LOC). The cross-cutting unification (FilterKind, did.IsValid, destructive-op posture) is *the* point — fragmenting it across multiple PRs would make the unification harder to read. The atomic-commits-per-track shape lets reviewers walk the change linearly and lets a bad commit get reverted in isolation.

### 3.2 PR target: `staging` (project convention) vs. `main` (recent #78 pattern)

**Chosen**: defer to PR-draft time. The recent #78 went directly to main; the standard convention per `AGENTS.md` is staging → main via a separate promotion PR. Both are valid; the user picks at draft.

### 3.3 Track 1 — drop + recreate vs. add a new differently-named index

**Chosen**: drop the legacy `idx_record_json_gin` and create `idx_record_json_gin_path_ops`. Migration is small, the down restores the legacy name.

**Considered**: leave both. **Rejected**: the legacy index pays an insert cost every write for zero query benefit after this PR (track 9 adds the partial-GIN that actually serves the new filters; the JSONB containment GIN is unused).

### 3.4 Track 2 — custom golangci-lint analyzer vs. doc + test + grep guard

**Chosen**: doc note in `AGENTS.md` + a `grep`-based guard wired into `make lint`. A custom analyzer is the right shape long-term but a 100-line Go analyzer for one pattern is over-engineering; a `! grep -r 'strings.HasPrefix(.*\"did:\"' internal/ --exclude-dir=did` line in the Makefile or a tiny `scripts/lint-no-did-prefix.sh` is enforceable in CI today.

**Considered**: a real `go/analysis` analyzer published in `internal/lint/checks/`. Defer until the ban applies to a second pattern.

### 3.5 Track 3 — preview-confirm HMAC for `resetAll` vs. a simpler typed-string-plus-audit-log

**Chosen**: full preview-confirm HMAC matching `purgeActor`'s contract. The whole point of the review's destructive-op critique is that `resetAll` should be **at least** as hardened as `purgeActor` because it's strictly more destructive. Half-measures defeat the unification.

**Considered**: emit only the audit log + admin DID binding, keep the `confirm: "RESET"` shape. **Rejected**: this would leave `resetAll` still less hardened than `purgeActor` — the unification doesn't land.

**Considered**: extract a `destructiveOp` helper now and migrate both `purgeActor` and `resetAll` to it. **Rejected for this PR**: the helper requires careful API design and a separate review round; not the cheapest path to closing the gap. Track 3 will instead duplicate the HMAC pattern from `purgeActor` (clearly a sign the helper is ready for extraction in a follow-up).

### 3.6 Track 9 — subject DID materialization: generated column vs. sidecar table vs. index-only

**Chosen**: **generated column on `record`** (`subject_did TEXT GENERATED ALWAYS AS (...) STORED`) + a partial btree index on `(subject_did)` where `collection = 'app.certified.badge.award'`. Reasons: zero ingestion-code change (the column is computed by Postgres), the existing record row is the canonical home, indexable directly.

**Considered**: a sidecar `record_subject_did(record_uri, did)` table maintained by an ingestion hook. More flexible but adds a write path that can drift. Defer.

**Considered**: index-only (an expression index on the JSONB extraction). Postgres allows this but the planner sometimes won't use it for `LIKE 'prefix%'` against the expression. Generated column is simpler and the planner uses the index reliably.

### 3.7 Implement P0 only in this PR, defer P1 to a follow-up

**Considered** but **rejected**: the user explicitly said "all changes." Bundling P0 + P1 keeps the unifying refactors (FilterKind, destructive-op posture) coherent. P2 stays backlog.

---

## 4. Acceptance criteria

### Per-track

Each track has its own acceptance criteria spelled out in the commit message. Global gates apply to every commit.

#### Track 1 — migration collision
- New migration up creates `idx_record_json_gin_path_ops` and drops the legacy.
- Down restores the legacy name (best-effort rollback).
- `TestMigrations_UniqueIndexNames` parses every `.up.sql` and asserts no two `CREATE INDEX … <name>` share a name. Fails on the current 001/013 collision; passes after the rename.
- Migration round-trip test extended to include `pg_indexes` snapshot.

#### Track 2 — did.IsValid rollout
- `previewPurgeActor` and `purgeActor` reject `did` that doesn't pass `did.IsValid` with a stable error message.
- `AddAdmin`, `RemoveAdmin`, `BackfillActor` use `did.IsValid` (not `strings.HasPrefix`).
- New `scripts/lint-no-did-prefix.sh` checked into `make lint`; documented in `AGENTS.md`.
- All resolver-level error paths surface a typed sentinel mapped to a stable GraphQL error code.

#### Track 3 — `ResetAll` hardening
- New `previewResetAll()` returns counts (total rows across all tables in the deletion list) + an HMAC `confirmToken` bound to `(admin_did, row_count, exp)`.
- `resetAll(confirmToken)` verifies the token, then iterates the FULL deletion list (record, actor, activity, reports, labels, notifications, notification_preferences, oauth grants if applicable, labeler config rows). Deletion order respects FKs.
- Structured audit log: `event=reset_all requested_by_did=… rows_deleted=… tables=N ts=…`.
- The legacy `resetAll(confirm: "RESET")` signature is removed; client follows.
- SECURITY.md updated with the operator-contract section mirroring actor_purge.

#### Track 4 — OAuth `returnTo`
- `/api/login` validates `returnTo` via `new URL(returnTo, env.PUBLIC_URL).origin === new URL(env.PUBLIC_URL).origin`. Rejects with 400 on mismatch.
- `/api/oauth/callback` does the same parse on the way out (defense in depth).
- Test cases: `/`, `/admin`, `//evil.com`, `/\evil.com`, `https://evil.com/`, `/%2f%2fevil.com`, `/%5cevil.com`, `   /admin` (leading whitespace), `/admin#evil` (fragment) — all expected results enumerated.

#### Track 5 — Settings form hydration
- `useEffect(() => { if (settings) { setDomainAuthority(settings.domainAuthority); … } }, [settings])` replaces the misused lazy init.
- Manual smoke test: admin loads page, fields populate from server, save round-trips correctly.

#### Track 6 — pgx cancel handler
- Pool configured with `CancelRequestContextWatcherHandler`.
- Test: a request that times out at Layer 2 (5s) causes Postgres `pg_cancel_backend` on a sideband connection; the original connection returns to the pool; a follow-up request succeeds within 1s.

#### Track 7 — admin auth ordering
- `/admin/graphql` now uses `RequireAuth` (or auth-before-decode).
- Unauthenticated probe returns 401 before any body decode or depth check.
- Test: assert ordering by sending a deliberately-malformed body with no auth header → 401, not 400.

#### Track 8 — FilterKind refactor
- `FieldFilter.Kind FilterKind` enum (`KindScalar`, `KindArrayContributor`, `KindUnionSubject`); the two booleans deleted.
- Per-lexicon descriptor registry: `map[lexID]map[fieldName]filterDescriptor`. `where.go` dispatcher becomes a table lookup.
- Existing tests still pass; no SQL behaviour change.

#### Track 9 — Index the new filter shapes
- Migration adds:
  - `subject_did` generated column on `record` (`COALESCE` over the three subject shapes).
  - Partial btree on `(subject_did)` where `collection = 'app.certified.badge.award'`.
  - Partial GIN expression index for contributor identities on `org.hypercerts.claim.activity` (see § 3.6).
- `filter.go` rewrites the SQL to use the new column + index.
- Tests: `EXPLAIN` snapshot includes `Index Scan using idx_…` for both filter shapes.

#### Track 10 — Metrics + audit logs
- New `hypergoat_purge_token_rejected_total{reason}`, `hypergoat_purge_actor_total{tap_status}`, `hypergoat_purge_records_deleted` histogram, `hypergoat_admin_settings_changed_total{field}`.
- `UpdateSettings`, `ResetAll`, `AddAdmin`, `RemoveAdmin` emit `event=admin_settings_changed actor_did=…` structured log lines.
- New `internal/logsafe` helper with `DID(s)`, `String(s)` scrubbers; applied at every user-controllable slog attribute site.

#### Track 11 — purge resolver hardening
- `PreviewPurgeActor` distinguishes `sql.ErrNoRows` (actor absent → `actorExists=false`) from other errors (returns wrapped error).
- `extractFieldFiltersRecursive` returns errors on non-map field bodies instead of silent `continue`.
- Audit log no longer redundantly emits `actor_did = target_did`.
- `purgeTokenClaims` gets `V int \`json:"v"\`` = 1; `Verify` rejects `v != 1`.
- New `ErrPurgeTokenCountDrift` sentinel; verify returns it specifically on count mismatch.
- Used-sig periodic sweeper goroutine started in `NewPurgeTokenSigner`.
- Tap removal moved to a fire-and-forget goroutine with bounded outer shutdown signal.

#### Track 12 — tests
- New resolver tests for `previewPurgeActor` / `purgeActor` cover happy path, Tap-failure-but-SQL-success, SQL-failure rolls back both, missing-admin-DID-in-context error, requires-admin gate.
- Postgres-backed table-driven tests for contributor + subject filters with all three subject shapes and the two contributor shapes.

### Global gates

Run after each commit. Acceptance: same packages green as the pre-implementation baseline (`docs/upstream-adoption/baseline.md`).

```bash
GOARCH=arm64 CGO_ENABLED=1 go build ./...
GOARCH=arm64 CGO_ENABLED=1 go vet ./...
GOARCH=arm64 CGO_ENABLED=1 golangci-lint run ./...
GOARCH=arm64 CGO_ENABLED=1 go test -race -count=1 ./...   # DB-dependent tests fail locally; CI runs them
cd client && npx tsc --noEmit
```

---

## 5. Out of scope

- **P2 items from the report** (sections 9 P2 entries 13–20). Backlog; documented in the report itself.
- **Skipped/Accepted items** (report § 9). Specifically: SEC-5 (`validateOperatorURL` internal IPs), SEC-11 (used-token unbounded), P-7 (lazy prune O(N)).
- **Production data migration / backfill** for the new `subject_did` generated column. Postgres computes it on read for existing rows; explicit backfill is a follow-up if performance demands it.
- **Custom `go/analysis` lint analyzer** (track 2 alternative). Doc + shell-script grep is the chosen shape; defer.
- **Extract `destructiveOp` helper** and migrate both `purgeActor` and `resetAll` to it. Track 3 duplicates the HMAC pattern, surfacing the helper for a follow-up.
- **Database migrations to backfill `subject_did` for existing rows**: Postgres generated columns are computed on read for STORED variants only on insert. For STORED to be populated for existing rows, the column must be added with `ALTER TABLE … ADD COLUMN … GENERATED …` which Postgres rewrites the table (lock!) — or use `VIRTUAL` (Postgres 18+). This plan adds `STORED` with the understanding that existing rows are backfilled by the migration's `UPDATE` (run as a separate concurrent migration). If the rewrite cost is unacceptable, fall back to an expression index on the JSONB extraction (less reliable for `LIKE prefix%` planner choice).
- **Subscription / WebSocket query budgets** (A-4). Documented as P2.
- **CHANGELOG style overhaul**. Track 13 adds the missing entries; format follows existing precedent.

---

## 6. Rollback plan

Each track is one atomic commit; revert individually. No track depends on a previous track's *runtime state*; all dependencies are *commit-chain* dependencies that revert cleanly in reverse order.

| Track | Worst-case symptom on revert | Notes |
|---|---|---|
| 1 | Revert restores both indexes (legacy + path_ops). Slightly more write-cost than current state. | Schema-safe. |
| 2 | Revert reopens prefix-only DID checks. Admin-gate still protects. | Audit-trail integrity regresses but doesn't break. |
| 3 | Revert restores legacy `resetAll(confirm:"RESET")`. Client still has the new flow; will return error until reverted in lockstep. | Roll back server + client together. |
| 4 | Revert restores the single `//`-prefix guard. Browser-mitigated; not catastrophic. | |
| 5 | Revert restores broken hydration. Cosmetic. | |
| 6 | Revert restores pgx default cancel watcher. Connection churn returns; latency stable in steady state. | |
| 7 | Revert restores `OptionalAuth` on `/admin/graphql`. Auth still enforced inside handler. | |
| 8 | Revert restores the two boolean markers + two builders. No behaviour change. | Schema-safe. |
| 9 | Revert drops the new indexes + column. Filter SQL must revert in lockstep (track 8 ordering). | Generated column adds storage; revert removes it. |
| 10 | Revert removes metrics + audit logs. Observability regresses but nothing breaks. | |
| 11 | Revert reopens the small hardening issues. None is exploitable post-track 2. | |
| 12 | Revert removes new tests. Coverage regresses; code unchanged. | |
| 13 | Revert removes CHANGELOG entries. | |

No database migrations beyond tracks 1 + 9. Track 9's column addition is the only operation requiring a Postgres table rewrite (for STORED). Operator runbook: schedule track 9 deploy during a maintenance window or use `VIRTUAL` columns if on Postgres 18+.

---

## 7. Open questions for the operator

1. **PR target.** `staging` (per `AGENTS.md` convention) or `main` (per recent #78 pattern)?
2. **Track 3 destructive helper extraction.** Defer to a follow-up PR (current plan), or include the `destructiveOp` extraction in this PR? Adds ~half day.
3. **Track 9 generated-column variant.** STORED (Postgres rewrites the table on `ALTER`, operator schedules maintenance) or VIRTUAL (Postgres 18+ only — confirm the production Postgres version)?
4. **Track 12 testutil.** Project doesn't have a strong Postgres-backed-test pattern outside the existing `*_test.go` style. Bring up a `internal/testutil/postgres.go` helper, or keep new tests inline?
5. **OAuth `returnTo` validation place.** Strict path regex (e.g. `^\/[A-Za-z0-9._~!$&'()*+,;=:@%/?#-]*$`), or `new URL(returnTo, env.PUBLIC_URL).origin === own_origin`? The latter is more robust against URL-encoded tricks; the former is shorter. Pick one.

---

## 8. Estimated effort

| Track | Impl | Tests | Review |
|---|---|---|---|
| 1 | 1.5 h | 1 h | 0.5 h |
| 2 | 2 h | 1 h | 0.5 h |
| 3 | 4 h | 1 h | 1 h |
| 4 | 1 h | 1 h | 0.5 h |
| 5 | 0.25 h | smoke | 0.25 h |
| 6 | 1.5 h | 2 h | 0.5 h |
| 7 | 1.5 h | 1 h | 0.5 h |
| 8 | 6 h | 2 h | 1 h |
| 9 | 6 h | 3 h | 1 h |
| 10 | 3 h | 1 h | 0.5 h |
| 11 | 4 h | 2 h | 1 h |
| 12 | 4 h | — | 1 h |
| 13 | 0.5 h | — | — |
| **Subtotal** | **~35 h** | **~15 h** | **~8 h** |
| **Total** | | | **~58 h, ≈ 7 working days** |

This is a substantial PR. Recommend: ship tracks 1–7 first (the P0 set + the two cheap P1 items) as the "high-impact subset," then 8–13 as a continuation in the same branch if time permits or in a follow-up PR if not.

---

## 9. Next steps (per `AGENTS.md` order of operations)

1. **Operator reviews this plan.** Resolve § 7 open questions.
2. **Plan review.** Spawn 3–4 reviewer agents in parallel: GraphQL schema correctness (tracks 8, 9), security & destructive-op safety (tracks 2, 3, 4, 7), DB / migration discipline (tracks 1, 6, 9), client UX (tracks 4, 5). Decisions in `docs/review-2026-05-13/implementation-review-round-1.md`.
3. **Implement on the branch** in the commit order from § 2. Quality gates after each commit.
4. **Implementation review.** Same shape as plan review.
5. **Draft PR.** Body links this plan + decision docs + the parent `report.md`.
6. **CI green.** Fix root causes; never `--no-verify`.
7. **Stop.** Operator merges.
