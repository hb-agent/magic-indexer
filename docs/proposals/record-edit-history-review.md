# Review — record edit history proposal

**Reviews two rounds**:
- Round 1: four parallel reviewers — DB, API, Ops, Adversarial.
- Round 2: two focused reviewers — Product/architecture, Ingest performance.

**My job**: synthesise, with judgment. I take what survives scrutiny and reject what doesn't. Each finding below is annotated **Accept / Modify / Reject** with one-line reasoning.

---

## 1. The single most important finding

**Product-R2 went looking for a consumer and found none.**

- Zero hits for `recordHistory` / `recordVersion` / `prevCid` / "revision" / "edited" across `/workspace/certs-social`, `/workspace/maearth-social`, `/workspace/certified-app`. The consumer apps query `Activities(...)` and `Endorsements(...)` — both current-state.
- Zero open issues on `hb-agent/magic-indexer` ask for record history. Issue #13 (GDPR erasure) actively *fights* a long-lived history table. Issue #4 (admin audit) was solved by the 13-May admin audit-log shipment.
- The indexed lexicons (`org.hypercerts.*`, `app.certified.badge.*`) are mostly claim-shaped and one-shot. `claim.activity` is the only plausibly mutable type, and even that mostly accrues new claims rather than mutating old ones.

**The proposal is solving a hypothetical.**

This is not a fatal objection — the operator may have a use case in mind that hasn't been written down yet — but it inverts the cost calculus on every other decision in the doc. If the use case is "operator wants a longer audit window," **Option A** (extend `jetstream_activity` retention + add URI index + admin resolver) is one day of work, zero hot-path risk, no new schema, no new GDPR-vs-audit collision. If the use case is "user-facing edit-history UI on `org.hypercerts.claim.activity`," Option C is right but the scope shrinks dramatically (one collection, narrow indexes, modest retention).

**My acceptance**: I take this finding fully. The proposal should not advance to a plan doc until the operator names the consumer in one paragraph. § 6 of this review surfaces it as the decision gate.

---

## 2. Accepted findings (with my reasoning)

### From DB-R1

| ID | Finding | My reasoning |
|---|---|---|
| F-DB-001 | Add `rkey` (generated) at create time, optionally `subject_did` | Free at create, expensive later. Same logic that drove migration 002's `rkey` column. **Accept** — defer the `subject_did` decision to the operator (open Q5). |
| F-DB-002 | `(uri, indexed_at DESC, id DESC)` URI index + separate `indexed_at` index for retention | Pagination correctness AND retention-sweep planner-friendliness. Two real bugs. **Accept** — both fixes. |
| F-DB-005 | TOAST / compression note | One-sentence doc fix. **Accept**. |
| F-DB-006 | Retention `DELETE` must be batched with `ORDER BY` + an `indexed_at` index | The proposal's citation of `jetstream_activity.CleanupOldActivity` was wrong — that code does an unbatched `DELETE`. For `record_history` at long retention with millions of rows, batched-with-CTE is the only safe shape. **Accept**. |
| F-DB-007 | `subject_did` at create-time is free; deferred is expensive | Reframes open Q5 — the cost is asymmetric. **Accept** the framing; the actual yes/no is still the operator's call. |
| F-DB-008 | Don't put bulk-`INSERT … SELECT` seed inside an up.sql | Real foot-gun. If seed ships, it's a CLI tool. **Accept**. |
| F-DB-009 | Partitioning trigger-point doc | One paragraph. **Accept**. |
| F-DB-010 | Wrap both writes in `BeginTx` — cost is sub-ms | Strong argument. **Accept** — combined with F-ADV-007 and Perf-R2's quantified verdict, this is the new default. |
| F-DB-011 | Operation CHECK conflicts with `'initial'` seed | Drop the seed (F-DB-008 already says so) or include `'initial'`. **Accept** — pick the no-seed path. |
| F-DB-012 | `pds` LEFT-JOIN cost note | One-line doc clarification. **Accept**. |

### From API-R1

| ID | Finding | My reasoning |
|---|---|---|
| F-API-001 | Cursor lacks stable tie-breaker; `encodeCursorV2` is wrong here | Real correctness bug — pagination drift on same-millisecond writes. The notifications repo already solved this exact shape (`(sort_at, id)` cursor); copy it. **Accept**. |
| F-API-002 | `uri` needs `aturi.IsValid` validation parity with `did.IsValid` | The day-13-May lint rule `lint-no-did-prefix.sh` already established the project's appetite for strict input validation; an `at://` URI accepting arbitrary strings re-opens the same class of bug. **Accept** — write `internal/atproto/aturi/aturi.go` mirroring the `did` package. |
| F-API-004 | `recordVersionAt` deletion semantics collapse three states into two | Real consumer ergonomics bug. **Accept** — return a `{status, entry, deletedAt}` wrapper, or pin the semantics in the field description. |
| F-API-005 | `DateTime!` scalar is a no-op shim | This is a cross-cutting bug worth fixing regardless of this feature. **Accept** — fix the scalar's `ParseValue` / `ParseLiteral` to actually validate RFC3339Nano. |
| F-API-007 | `purgeActor` clearing history with no sentinel — three states collapse to two | Same shape as F-API-004. **Accept** — pin in the field description rather than add a sentinel row (Ops-R1 agrees — DID in the audit log, not in a survivor row). |
| F-API-008 | Document subscription guarantee | One sentence. **Accept**. |
| F-API-009 | Add `RecordHistoryEdge` to the schema snippet | Missing type in the proposal. **Accept**. |
| F-API-010 | Q3 SQL can't compose with `until:` — copy `GetByCollectionWithKeysetCursor` shape | The existing keyset pattern handles this. **Accept** if F-API-003 lands. |
| F-API-011 | Pin policy text in field descriptions | Project norm (cf. `contributorFieldDescription`). **Accept** — draft the descriptions at proposal time. |
| F-API-012 | Filter expressivity: flat AND, not `_and`/`_or` | The full `FilterGroup` machinery is overkill here. **Accept** — flat input only. |

### From Ops-R1

| ID | Finding | My reasoning |
|---|---|---|
| F-OPS-001 | **Retention default = 90 days, not "forever".** Match SECURITY.md audit-log floor. Use `_DAYS` not `_HOURS`. | Strong precedent argument. The "forever" default buries a future ops crisis. **Accept** as the default; allow `0 = forever` only as an explicit opt-in with a startup warning. |
| F-OPS-002 | `purgeActor` deletes history rows; audit-log line stays with `history_count` | Correct GDPR-Art.17(3)(b) reading. **Accept** — this is the contract. |
| F-OPS-003 | `resetAll` MUST include `record_history` in its hard-listed deletion set | Obvious — `resetAll` is the nuclear option. **Accept**. |
| F-OPS-004 | Bounded-label Prometheus metrics | Matches Track 10 metrics discipline. **Accept** — five counters/histograms exactly as specified. |
| F-OPS-005 | Append-failure detection — every slog.Warn paired with a counter | Operator must be able to detect the gap. **Accept**. |
| F-OPS-006 | RUNBOOK.md needs a full section | Mirror Notifications precedent. **Accept**. |
| F-OPS-007 | Three env-var knobs: `_RETENTION_DAYS`, `_ENABLED`, `_EXCLUDED_COLLECTIONS` | One knob is too coarse. **Accept** — but `_ENABLED` is the kill-switch and matters most; the excluded-collections list is a v2 nicety. |
| F-OPS-008 | Phased rollout (write-only → admin GraphQL → public) | Notifications shipped this way; same shape applies. **Accept** — also subsumes F-API-006 (public-vs-admin), F-ADV-009 (seed). |
| F-OPS-009 | Backup/restore implications in RUNBOOK | One paragraph. **Accept**. |
| F-OPS-011 | SECURITY.md needs a new "Record history" subsection | Mirror actor-purge / reset-all sections. **Accept**. |
| F-OPS-012 | Retention is per-row by `indexed_at`; document the discontinuous-timeline edge case | Honest doc fix. **Accept**. |

### From Adv-R1

| ID | Finding | My reasoning |
|---|---|---|
| F-ADV-001 | "Show every version" is a feature in search of a consumer | **Accept**. Confirmed by Product-R2. This is THE finding. |
| F-ADV-002 | If audit goal, you don't need the JSON body — CID-only is 90% of the value at 10% of the cost | Genuinely good. **Accept as a sub-option**: if the consumer use case turns out to be "audit," offer a `record_history` variant that stores only `(uri, cid, operation, indexed_at)` and recovers JSON from `jetstream_activity` for the retention window. |
| F-ADV-003 | Option A dismissal was hand-waved | **Accept** — the dismissal reasons against A were soft. Option A is genuinely viable for an audit use case. Product-R2 independently recommends A. |
| F-ADV-005 | Option D dismissal symmetric to its own pro — reframe as migration cost | Fair. **Accept** the framing fix; the underlying dismissal still holds. |
| F-ADV-007 | "Best-effort audit trail" is an oxymoron — pick a contract | **Accept** — combined with F-DB-010 + Perf-R2, the answer is "wrap in tx." |
| F-ADV-008 | "Forever" retention is operator-hostile | **Accept** — same as F-OPS-001. 90 days is the right floor. |

### From Round 2

| ID | Finding | My reasoning |
|---|---|---|
| Product-R2 Claim A | TRUE-WITH-CAVEATS — no consumer found in workspace, no open issues asking | **Accept**. Surface to operator as the decision gate. |
| Product-R2 Claim B | TRUE-WITH-CAVEATS — Magic Indexer is the wrong layer for canonical history; PDS is. For audit, `jetstream_activity` is. | **Accept** — informs the recommendation. Magic Indexer should not present itself as a record-history source of truth. |
| Perf-R2 — wrap in tx, ≤1% pool capacity, ~0.4-0.8 ms | TX, with `Records.InsertWithParamsTx` + `Records.AppendHistoryTx` matching the existing `*Tx` convention. Inject `idle_in_transaction_session_timeout=5000`. | **Accept fully** — the math is sober and the implementation guidance matches the project's existing pattern. |

---

## 3. Rejected / partially-rejected findings (with my reasoning)

### F-DB-003 (cut indexes from three to one)
**Reject as written**, partially accept the principle. The reviewer is right that an index without a query is a liability — but the resolver surface is part of the same proposal, and dropping the `did` and `collection` indexes means we also have to drop the corresponding query shapes (F-API-003), which API-R1 was pushing back on. **My call**: defer the index decision to the operator's answer about the consumer use case. If the consumer is "per-record history viewer," ship only the `uri` index. If "per-actor / per-collection audit," ship all three.

### F-API-003 (add did-scoped query + operation/range filters)
**Partially reject** — most of the additions would be speculative API surface for an unproven use case. **My call**: add the resolvers ONLY if the operator's named use case demands them. Default scope is two resolvers — `recordHistory(uri)` and `recordVersionAt(uri, at)`. Anything more lives in v2.

### F-API-006 (admin-only + DID-bound `recordHistoryForOwner` for the author case)
**Modify**. Admin-only is right for v1 (subsumed by the phased rollout). The DID-bound owner-only field is a real feature design — but designing it before the use case is named is the wrong order. **My call**: ship admin-only in phase 2; defer the owner-only design until a phase 3 use case is named.

### F-ADV-004 (run the dedup measurement before dismissing Option E)
**Reject the action, accept the principle**. The measurement (`SELECT COUNT(DISTINCT json::text), COUNT(*) FROM record`) is cheap, but the bigger argument — that Option E's three-table join cost in the hot read path probably eats the storage savings — wasn't really challenged. **My call**: if the operator wants this measured, run the query once; otherwise the dismissal stands.

### F-ADV-006 (Inserted-vs-Skipped breaks under HA / backfill / manual edits)
**Modify**. Single-writer is a safe assumption today and the notifications subsystem makes the same one (RUNBOOK.md acknowledges). Manual SQL is out-of-band by definition. The `UNIQUE(uri, cid, operation, indexed_at)` suggestion is impractical (millisecond truncation defeats it). **My call**: add a one-paragraph "assumptions" subsection naming single-writer + no-OOB-SQL as preconditions. Don't enforce.

### F-ADV-009 (seed migration is mandatory UX)
**Reject the "mandatory" framing**. Phased rollout (F-OPS-008) gives the table time to accumulate naturally before public exposure. Mandatory seed re-opens the F-DB-008 foot-gun. **My call**: no seed in v1; if phase 3 (public exposure) needs a seed for UX reasons, ship it as a CLI tool then.

### F-ADV-010 (deep flow ceremony is overkill)
**Partially accept**. The reviewer's point that the policy questions are the real work is fair. **My call**: keep the plan doc but scope it tightly — the policy decisions in § 6 below ARE the plan; the implementation is the easy part. One review round, not multiple.

### F-ADV-011 (Magic Indexer is the wrong layer)
**Modify**. The argument is good if the use case is "render historical version of someone else's record" (PDS wins). It's wrong if the use case is "cross-actor query for edits matching pattern X" (Magic Indexer is the only layer that can answer that). **My call**: surface this directly in the operator decision in § 6 — the answer to "which layer" depends entirely on the answer to "which consumer."

### F-OPS-010 (defer seed migration; if shipped, CLI tool)
**Accept the recommendation, reject the "operator might still want it later" framing**. With phased rollout, the seed is unnecessary. **My call**: drop it from the plan entirely; revisit only if a phase-3 use case explicitly needs it.

### F-DB-004 (storage estimate undercounts)
**Accept the math correction**; I had a doubt about the "60 MB" number too. **My call**: restate as a range in the doc (60-300 MB depending on collection mix). Reviewer is correct that index overhead is roughly 1× body size for small records.

---

## 4. The set of changes that survives

If the operator confirms the feature should ship at all, the proposal should be revised to incorporate these decisions:

**Schema (DB layer)**
1. Add `rkey TEXT GENERATED ALWAYS AS (substring(uri from '[^/]+$')) STORED` at create time (F-DB-001).
2. URI index becomes `(uri, indexed_at DESC, id DESC)` — pagination correctness (F-DB-002, F-API-001).
3. Add a separate `idx_record_history_indexed_at` for retention sweeps (F-DB-002, F-DB-006).
4. `did` and `collection` indexes are deferred until v1 resolver surface is confirmed.
5. Retention `DELETE` uses CTE-batched pattern, not unbatched (F-DB-006).
6. Drop the `'initial'` seed; the CHECK stays at `('create', 'update', 'delete')` (F-DB-011, F-DB-008).
7. `subject_did` generated column → operator open question (F-DB-007).

**Write path**
8. **Both writes wrap in `BeginTx`.** Use `Records.InsertWithParamsTx` + `Records.AppendHistoryTx` matching the existing `*Tx` convention (`actors.go:195`, `records.go:781`). Skip the wrap on the `Skipped` (cid-unchanged) path (Perf-R2, F-DB-010, F-ADV-007).
9. Inject `idle_in_transaction_session_timeout=5000` alongside `statement_timeout` in `internal/database/postgres/executor.go:injectStatementTimeout` (Perf-R2).
10. Every append failure emits a paired counter increment alongside the `slog.Warn` (F-OPS-005).

**GraphQL surface**
11. New cursor format `["v1:rechist", indexed_at_rfc3339nano, id_decimal]` — not `encodeCursorV2` (F-API-001).
12. New `internal/atproto/aturi` package with `aturi.IsValid` mirroring `did.IsValid` (F-API-002).
13. Fix `DateTimeScalar` to validate RFC3339Nano in `ParseValue` / `ParseLiteral` (F-API-005).
14. Pin policy descriptions on every new field (F-API-011): `recordHistoryFieldDescription`, `recordVersionAtFieldDescription`, `recordHistoryEntryDescription`. Cover retention, purge interaction, deletion semantics.
15. `recordVersionAt` returns explicit absent-vs-deleted-vs-present semantics — wrapper type OR pinned description (F-API-004).
16. Add `RecordHistoryEdge` to the schema definition (F-API-009).
17. Filter input (if any) is flat AND only — no `_and`/`_or` (F-API-012).

**Ops surface**
18. Retention default = **90 days** with floor enforcement of `0 or >= 30` at startup (F-OPS-001).
19. Three env vars: `RECORD_HISTORY_RETENTION_DAYS`, `RECORD_HISTORY_ENABLED`, `RECORD_HISTORY_EXCLUDED_COLLECTIONS` (F-OPS-007).
20. Phased rollout: phase 1 (write-only + metrics) → phase 2 (admin GraphQL) → phase 3 (public, gated by `RECORD_HISTORY_PUBLIC=true`) (F-OPS-008).
21. `purgeActor` deletes from `record_history WHERE did=$1` in the same transaction; audit-log line gains `history_count` (F-OPS-002).
22. `resetAll` adds `record_history` to its hard-listed deletion set (F-OPS-003).
23. New SECURITY.md "Record history" section under "Admin surface" (F-OPS-011).
24. New RUNBOOK.md "Record history" section mirroring Notifications shape (F-OPS-006).
25. Five new Prometheus series with bounded labels (F-OPS-004).

**Documentation**
26. Add one-paragraph "Assumptions" note: single-writer indexer, no out-of-band SQL (F-ADV-006 modified).
27. Replace "best-effort" framing with "complete (under tx)" framing (F-ADV-007).
28. Storage estimate restated as a range (F-DB-004).
29. TOAST/compression note (F-DB-005).
30. Partitioning trigger-point note (F-DB-009).

---

## 5. Decisions deferred to the operator

Six. The proposal cannot advance to a plan without these.

1. **Consumer use case.** Name the consumer in one sentence. The answer determines almost every downstream choice. (F-ADV-001 / Product-R2)

2. **Layer.** If the consumer is "render historical version," is the right layer PDS, `jetstream_activity` extended, or `record_history` new? Don't build C until A is ruled out for the actual use case. (F-ADV-011 / Product-R2)

3. **JSON body or CID-only?** If the use case is audit, CID-only at 10% storage cost is the better default. (F-ADV-002)

4. **Subject DID column.** Add at create time (free) or never. Don't defer — adding later is expensive. (F-DB-007 / F-OPS-007)

5. **GDPR posture on `purgeActor`.** Confirmed: history rows deleted; audit-log line stays with the DID + `history_count`. Operator should sign off on this contract explicitly before SECURITY.md ships it. (F-OPS-002)

6. **Retention default value.** 90 days proposed. Operator may want longer for legal retention or shorter for storage budget. (F-OPS-001 / F-ADV-008)

---

## 6. Recommended next step

**Do NOT advance to `docs/proposals/record-edit-history/plan.md` yet.**

Instead:

1. **Answer § 5 question 1** in one paragraph at the top of the proposal: who is the consumer, what query do they issue, what is the operator's compliance contract?
2. If the answer is "audit / longer-than-7-days observation window for the indexer's own state," replace this entire proposal with a much smaller Option-A proposal: extend `jetstream_activity` retention, add a URI-shaped composite index, expose an admin `recordHistory(uri)` resolver that parses `event_json` server-side. ~1 day. Stop.
3. If the answer is "user-facing edit history for one or two specific lexicons," scope Option C tightly to those lexicons (per-collection partial indexes, narrower retention, public exposure on the named collections only). Then advance to a plan with the decisions in § 4 baked in.
4. If the answer is "we want everything — audit + user-facing — across all lexicons," advance the full Option C with the § 4 changes, but accept that the storage and complexity ceiling is high and revisit in 6 months once consumption is observed.

**Most likely answer (per Product-R2's evidence): § 6.2.** One day of work on `jetstream_activity` retention + index buys 90% of the value at <5% of the storage and complexity cost of Option C.

---

## 7. Reviewer attribution

| Pass | Role | Findings |
|---|---|---|
| DB-R1 | Schema + storage + index | F-DB-001 → F-DB-012 (12 findings) |
| API-R1 | GraphQL + cursor + consumer ergonomics | F-API-001 → F-API-012 (12 findings) |
| Ops-R1 | Retention + GDPR + monitoring + runbook | F-OPS-001 → F-OPS-012 (12 findings) |
| Adv-R1 | Adversarial / contrarian | F-ADV-001 → F-ADV-011 (11 findings) |
| Product-R2 | Stress-test "feature in search of a consumer" against the actual workspace + issue tracker | Claim A + Claim B |
| Perf-R2 | Stress-test the tx-vs-no-tx tradeoff with concrete numbers | Cost analysis + recommendation |

Round 2 reshaped the headline: Product-R2's evidence (no consumer found in the workspace, no issues asking, lexicon mutability profile doesn't support it) elevated F-ADV-001 from "good critique" to "blocking decision." Perf-R2 settled the tx question with sub-1% pool-load math and a clean implementation pattern (`*Tx`-suffixed methods, idle-in-transaction timeout injection).

The accepted-finding count is 30 (across 47 round-1 findings + 3 round-2 verdicts), of which 6 are decisions only the operator can make. The rejected/modified count is 9; each has reasoning in § 3.
