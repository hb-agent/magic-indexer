# Plan-review round 1 — review-2026-05-17

Reviewers (parallel, single round):

- **R1 — Concurrency** (Track 4: Tap ack, ingestion observability)
- **R2 — Admin surface** (Track 5: resolver split)
- **R3 — Ops** (Tracks 2, 4, 6, sequencing)
- **R4 — Security** (Track 3: OAuth defense-in-depth)

Each was given the plan, the audit, and a focused brief. All four
returned concrete dissents — round 2 not warranted; remaining
items would be nit-picking. Plan updated in place to reflect the
ACCEPTED items. REJECTED items below carry one-line rationale.

---

## Decisions

### R1 — Concurrency review of Track 4

| # | Finding | Decision | Rationale |
|---|---------|----------|-----------|
| R1.1 | Option 4-A's unique key `(operation, did, rkey, cid)` is broken: delete events have empty CID (`jetstream/event.go:43`, `event_test.go:184-185`); identity events have neither; a `create → delete → re-create` cycle on the same rkey silently drops the second create's audit row | **ACCEPT** | Reviewer cited specific files + test asserts. The dedup key as proposed defeats the audit log's purpose. |
| R1.2 | Use Tap `event.ID` (`tap/event.go:18`) / Jetstream `TimeUS` instead: add `source_event_id BIGINT` column with partial unique index `WHERE source_event_id IS NOT NULL`, `ON CONFLICT (source_event_id) WHERE ...` | **ACCEPT** | Matches the actual at-most-once semantics of the upstream protocols. Migrates cleanly. |
| R1.3 | Option 4-B (ack in transaction) is theatre — websocket reconnect semantics make at-least-once unavoidable; only idempotency closes the gap | **ACCEPT** | Drop 4-B from the open questions. The choice collapses to "do dedup right". |
| R1.4 | Jetstream path also has the issue (crash between `LogActivity` and record insert, `jetstream/consumer.go:244-264`), so the fix is cross-consumer not Tap-only | **ACCEPT** | Plan now scopes the dedup to both consumers. |
| R1.5 | `ON CONFLICT DO NOTHING ... RETURNING id` returns no row on conflict → `activityID == 0` → `updateStatus` becomes a no-op → orphan janitor (`jetstream_activity.go:168-183`) eventually marks legitimate successful redeliveries as orphaned | **ACCEPT** | Fix: `RETURNING id` UNION with a SELECT on the unique key to fetch the existing row's id. Add to Track 4 acceptance criteria. |
| R1.6 | Rate-limit counter goroutine safety: single-goroutine today but `/metrics` scraping introduces a reader → race | **ACCEPT** | Use `atomic.Int64` for counter; either `atomic.Pointer[time.Time]` or hold under `statsMu`. |

### R2 — Admin surface review of Track 5

| # | Finding | Decision | Rationale |
|---|---------|----------|-----------|
| R2.1 | `PopulateActivity` (resolvers.go:1494) is misfiled in `resolvers_backfill.go` — only touches `r.repos.Activity`, belongs with `ActivityBuckets`/`RecentActivity`/`ValidationStats` in `resolvers_activity.go` | **ACCEPT** | Code dependencies > usage convention. |
| R2.2 | `PreviewResetAll`/`ResetAll`/`resetAllCounts`/`resetAllTables`/`quoteIdent` belong in `purge.go` (or `purge_reset.go`) alongside `PreviewPurgeActor`/`PurgeActor` — they share `PurgeTokenSigner`, `ScopeResetAll` is already in `purge.go:47`, and the code comment at resolvers.go:1338 literally says "Mirrors the PreviewPurgeActor contract" | **ACCEPT** | The reviewer made the strong version of the argument: the existing comment already points the right way. Co-locate destructive ops. |
| R2.3 | `AddAdmin`/`RemoveAdmin` (L602-719) belong in core with `Settings`/`UpdateSettings`, not with OAuthClients CRUD — they mutate `admin_dids` config and share the `metrics.AdminSettingsFieldAdminDIDs` audit convention with `UpdateSettings` | **ACCEPT** | OAuth-client file is for OAuth client CRUD only. |
| R2.4 | Plan didn't enumerate file-scope helpers that must move with their callers: `maxLexicon*` constants → lexicons file, `clampAdminPageSize`/`maxAdminPageSize` → labels file, `auditSettingsChanged` → core, `validateOperatorURL`/`validateJetstreamURL` → core | **ACCEPT** | Plan-as-written would have compiled but with helpers in the wrong file. Explicit list added to Track 5. |
| R2.5 | Add a "where does this method live?" comment at the top of the kept `resolvers.go` to prevent partition drift | **ACCEPT** | Cheap insurance; the reviewer's "real risk is partition drift" framing is right. |
| R2.6 | Audit's "single highest-leverage change" framing is overstated — the split's leverage is modest | **NOTED** | Plan keeps the split but stops claiming "highest leverage". Track is still worth doing for file-size reasons alone. |

### R3 — Ops review

| # | Finding | Decision | Rationale |
|---|---------|----------|-----------|
| R3.1 | Track 4's queue-depth metric targets the wrong file — the buffered channel is in `jetstream/client.go:51,66` (size 1000 at `:18-21`), not `jetstream/consumer.go`; Tap has no channel at all | **ACCEPT** | Plan corrected: gauge in `jetstream/client.go`; for Tap, emit `hypergoat_tap_event_dispatch_seconds` histogram instead so stalls are visible. |
| R3.2 | Metric naming must follow existing convention: `hypergoat_<subsystem>_<thing>_<unit>` snake_case (see `metrics.go:36-67`). Plan's `ConsumerQueueDepth_*` violates both prefix and case | **ACCEPT** | Names corrected: `hypergoat_jetstream_event_buffer_depth` (Gauge), `hypergoat_jetstream_event_buffer_capacity` (Gauge constant), `hypergoat_tap_event_dispatch_seconds` (Histogram). |
| R3.3 | Rate-limit logging design is backwards for paging — operator wants first errors loudly, then thinning; plan's "1/min from the start" buries the first incident. Pattern exists at `labeler/consumer.go:399, 427-432`. Plus the verdict-row and open-question-5 wording disagree with each other | **ACCEPT** | Plan replaced: log first 5 errors at error severity, then 1/min with `occurrences_since_last_log` in the line, plus the `IngestionErrorRate` counter. Verdict row and open question now agree. |
| R3.4 | Restart-on-upload doc belongs in `docs/RUNBOOK.md` (already 733 lines, README.md:47-49 points operators there), not AGENTS.md (already 1125 lines and is for agent context). AGENTS.md gets a one-line pointer | **ACCEPT** | I didn't know RUNBOOK.md existed. That's the right home. |
| R3.5 | Track 6 line ranges are off — actual endpoints are at 430-468, 477-516, 523-543. Hidden extraction hazards: `checkAdminBearer` (L412-428) is a closure over `cfg.AdminAPIKey`, and `/admin/labeler/pause` closes over `bg.labelerMu`/`bg.labelerConsumers` | **ACCEPT** | Plan now names these dependencies explicitly. Extraction strategy: either pass `cfg` and `bg` to the new file's constructor, or convert `checkAdminBearer` to a package-level helper. Decided: package-level helper accepting `cfg.AdminAPIKey` (simpler, no breaking signature). |
| R3.6 | Sequencing — Track 7 LAST is the riskiest for rollback. Either move it FIRST (longest bake on staging) or ship it as a separate part-2 PR matching #79/#80 precedent | **ACCEPT** | Plan changed to: ship Track 7 as a separate part-2 PR. Reasons: (a) SQL-B is mechanical-but-wide; isolating it in its own PR makes review easier and bisect cleaner; (b) matches the precedent the project already uses; (c) removes the "what if something else breaks while SQL-B is in flight" ambiguity. Open question 3 is now resolved by this choice. |

### R4 — Security review of Track 3

| # | Finding | Decision | Rationale |
|---|---------|----------|-----------|
| R4.1 | Audit was WRONG that no JTI cleanup is wired — `oauth_handlers.go:1162-1190` has an hourly `StartCleanupWorker` already, wired from `cmd/hypergoat/main.go:665-667` | **ACCEPT** | Drop the "add a new ticker" item from Track 3. Replace with: tighten existing floor from `now-3600` → `now-DefaultMaxDPoPAge` (300s + small skew) at `oauth_handlers.go:1181`. One-line change. |
| R4.2 | The current cleanup goroutine isn't drained on shutdown — `bg.Stop()` only fires the cancel, doesn't `wg.Wait()`. With current hourly interval the race is rare; tighter intervals make it worse | **ACCEPT** | Add `sync.WaitGroup` to `backgroundServices`; `StartCleanupWorker` does `wg.Add(1)`/`defer wg.Done()`; `bg.Stop()` calls `Wait()` after cancel. |
| R4.3 | Other non-constant-time comparison sites the audit missed: `oauth_handlers.go:635, 639, 810` (ClientID/RedirectURI checks against auth codes and refresh tokens); `serviceauth.go:152, 177` (JWT aud/lxm claims) | **ACCEPT (partial)** | All 5 added to Track 3. The two `serviceauth.go` sites have the strongest defense-in-depth argument (attacker-controlled JWT claims). The three `oauth_handlers.go` sites are bound to one-time codes, but consistency with the existing PKCE pattern (`pkce.go:39,41`) is the right default. |
| R4.4 | Nil-check ordering at `middleware.go:157→160` must be preserved when switching to `subtle.ConstantTimeCompare([]byte(*ptr), …)` | **ACCEPT** | Added to Track 3 acceptance criteria + a test that passes nil `DPoPJKT` and asserts `ErrTokenNotDPoPBound` not panic. |
| R4.5 | Side channel: JKT-mismatch returns `ErrDPoPKeyMismatch` distinct from `ErrTokenNoUser` (post-JKT path), giving an oracle even after constant-time compare lands | **ACCEPT** | Add to Track 3: collapse the two error responses to the same `ErrInvalidToken` response shape on the public HTTP surface (internal error variables can stay distinct for logging). |
| R4.6 | JTI insert at `middleware.go:131` happens BEFORE JKT check — enables JTI-burning DoS on legitimate users | **DEFER** | Real, but a separate fix (reorder JKT check before JTI insert). Filed as follow-up; out of this batch's scope per "Larger goal". Added to "open findings filed for later" below. |
| R4.7 | **CRITICAL ADJACENT FINDING**: `client_secret` is generated (`oauth_register.go:127, 243`) and stored (`types.go:52`) but never *verified* on `/oauth/token`. Confidential clients are not actually authenticated | **DEFER, FILE PROMINENTLY** | This is an auth-bypass, not a Track 3 item. Reported to operator at the top of this plan-review pass; needs its own deep-flow track separate from review-2026-05-17. **The operator should see this finding before deciding whether to proceed with the rest of the plan.** |

---

## Items deferred / not adopted (with rationale)

- **R2.6 (split leverage is overstated)** — noted; plan keeps the split because the file-size threshold argument stands independent of the audit's "highest-leverage" claim.
- **R4.6 (JTI burning DoS)** — real issue, but the fix (move JTI insert after JKT check) reshapes the DPoP validation order in a way that needs its own plan and reviewers. Filed as a follow-up.
- **R4.7 (client_secret unverified)** — *critical adjacent finding*. Out of scope for this plan but flagged to operator immediately. Should not be folded into review-2026-05-17 — it deserves its own targeted fix with its own reviewers.

## Open findings filed for later (not in this plan)

1. **`client_secret` is never verified on `/oauth/token`** (R4.7). Confidential OAuth clients are effectively public. Severity: critical. Owner: operator decision.
2. **JTI-burning DoS via insert-before-JKT-check** at `internal/oauth/middleware.go:131` (R4.6). Severity: medium (DoS, not auth bypass). Owner: follow-up plan.
3. **DB-backed JTI store TTL is 12× longer than necessary** — partially addressed in this batch by tightening the floor, but if R4.7 is fixed by adopting a different token-binding approach, this whole loop may go away. Tracked here for completeness.

---

## Net effect on the plan

- **Track 3 (OAuth defense-in-depth)** grew: 7 comparison sites now, not 2; existing cleanup is tightened rather than duplicated; new shutdown WaitGroup; error-response collapse to remove the oracle.
- **Track 4 (Tap ack + observability)** reshaped: new column `source_event_id` with partial unique index; cross-consumer fix; `RETURNING id` UNION SELECT pattern; correct metric file and naming; correct rate-limit shape; atomic counter.
- **Track 5 (admin resolver split)** repartitioned: `PopulateActivity` → activity file; reset-all family → `purge.go`; admin DID mutations → core; explicit helper enumeration; TOC comment at top of kept file.
- **Track 6 (extract admin HTTP)** dependencies named explicitly (`checkAdminBearer`, `bg.labelerMu`/`labelerConsumers`); strategy decided (package-level helper).
- **Track 2 (doc)** moved from AGENTS.md to `docs/RUNBOOK.md` with a one-line AGENTS.md pointer.
- **Sequencing** changed: Track 7 (SQLite) split into its own part-2 PR, not the last commit of this batch.
- **Tracks 1, 7** unchanged in shape.

## Recommendation

Operator sign-off needed on:

1. The **R4.7 client_secret finding** — file as a separate critical issue *before* proceeding with the rest of this plan, or proceed in parallel?
2. Whether the part-2 PR for SQLite (Track 7) gets approved here in principle or held for a separate decision.
3. The reshaped plan as a whole.

Round 2 of plan review not recommended — round 1 surfaced enough that further dissents are likely to be marginal.
