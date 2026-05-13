# Issue #64 — Implementation review, Round 2

**Date**: 2026-05-13
**Implementation reviewed**: `staging` at `d7e1d46` (filter), `3e130db`
(notifications fix), `a2a0ac6` (shared predicate + rename) on top of
`3b00958` (plan + round-1).
**Reviewers**: three parallel agents, three lenses (plan-fidelity,
security+SQL, code quality + tests + docs).

---

## Verdicts

| Lens | Verdict | Critical | Major | Minor | Nice-to-have |
|------|---------|----------|-------|-------|--------------|
| Plan-fidelity | approve with conditions | 0 | 2 | 3 | 2 |
| Security + SQL | **rework needed** | 0 | 3 | 5 | 3 |
| Code quality / tests / docs | approve with conditions | 0 | 1 | 7 | 5 |

The lone "rework needed" verdict surfaced two material problems the
round-1 plan-review did not catch:

1. **M-A** (Security+SQL): Postgres does not guarantee left-to-right
   AND evaluation in WHERE. My guards-first text is a planner hint,
   not a contract. A future planner choice could invoke
   `jsonb_array_elements` on a non-array record and brick every
   query touching the contributor filter. The documented fix is a
   `CASE WHEN` wrapper.
2. **M-B** (Security+SQL): The round-1 rename
   (`oauth.IsValidDID` → `oauth.HasDIDMethodPrefix`) was supposed to
   remove a foot-gun. It didn't — all 5 call sites still use the
   weak prefix-only check, and the reviewer found concrete attack
   paths at each (config-key namespace escape via `/../`, log
   injection via newlines in admin DIDs, URL injection via `?` in a
   JWT `iss` claim that flows into `ResolveDID`).

Both M-A and M-B are local, mechanical fixes. M-B also subsumes the
code-quality reviewer's M1 (SECURITY.md/code mismatch on ADMIN_DIDS).

---

## Decisions per finding

Legend: **A** = accepted (applied in round-2 commits). **D** =
deferred (recorded, not applied). **R** = rejected (rationale below).

### Plan-fidelity lens

| ID | Source | Decision | Rationale |
|----|--------|----------|-----------|
| PF-Major-1 | Pagination-under-filter test (acceptance E) missing | **A** | Add `TestGetByCollectionFiltered_Contributor_PaginationKeyset` exercising cursor advance under the filter. |
| PF-Major-2 | M-5 EXPLAIN-plan integration test not landed | **A** (waiver documented) | EXPLAIN-plan assertions are brittle across Postgres versions and require parsing the explain tree. The acceptance criterion is informational ("planner uses idx_record_collection_keyset") and is observable via staging `EXPLAIN ANALYZE` rather than via a test that could regress on a planner change. Documenting the waiver here and in the PR body. |
| PF-Minor-1 | Compose-with-`excludePds` test missing | **A** | Add a small integration test. |
| PF-Minor-2 | No `metrics_test.go` file | **R** | Coverage exists transitively via `shared_test.go` using `counterDelta` against the global registry. The accessor functions are one-liners; a parallel `metrics_test.go` would duplicate the assertion without adding signal. |
| PF-Minor-3 | Round-1 doc/plan minor wording drift | **D** | Cosmetic; round-1 decisions and the plan agree on substance. |
| PF-NTH-1 | `buildSingleFilter` doesn't call `f.Validate()` on the contributor path | **D** | Defense-in-depth; the resolver already validates. Programmatic internal callers can choose to call `Validate` themselves. Not load-bearing. |
| PF-NTH-2 | Whitespace-trim asymmetry between extractor and SQL | **A** (subsumed by SS-Minor-3) | Resolved via SS-Minor-3 (drop trim in extractor). |

### Security + SQL lens

| ID | Source | Decision | Rationale |
|----|--------|----------|-----------|
| **SS-Major-A** | Postgres WHERE-clause AND ordering is not guaranteed; guards could be bypassed | **A** | Rewrite the contributor SQL with a `CASE WHEN <guards> THEN EXISTS(...) ELSE FALSE END` wrapper. CASE is the documented way to force evaluation order in Postgres. |
| **SS-Major-B** | 5 `oauth.HasDIDMethodPrefix` callers still vulnerable to config-key escape / log injection / URL injection | **A** | Migrate **all 5** call sites to `did.IsValid`. Once all callers are off `HasDIDMethodPrefix`, **delete the function entirely** (Security n-3). The strict charset rejects `/`, `\n`, `?`, and other injection chars, closing each concrete attack the reviewer surfaced. |
| **SS-Major-C** | SECURITY.md / config.go comments out of sync with actual code | **A** (subsumed by SS-Major-B) | Once the migration lands, the SECURITY.md text becomes accurate. `config.go` comment updated to point at `did.IsValid` and drop the stale cycle claim. |
| SS-Minor-1 | Empty `in: []` not rejected with a clear error | **A** | Reject in `buildContributorFieldFilter` before SQL is built. One line. |
| SS-Minor-2 | Empty bare-string classified as `unrecognized_shape` rather than `non_did` | **A** | Reclassify per the plan definitions: a string that fails `did.IsValid` (including empty) is `non_did`. `unrecognized_shape` is reserved for non-string-shaped values (the strong-ref signal). |
| SS-Minor-3 | Trim asymmetry: extractor trims, SQL does not | **A** | Drop `TrimSpace` in `extractContributorDID`. Stored DIDs with stray whitespace become `non_did` outcomes (the metric is the signal); the symmetric policy keeps the extractor and filter aligned. |
| SS-Minor-4 | Round-1 m-5 test case (whitespace-bearing stored DID) accepted but not implemented | **A** | Add the integration test. With the SS-Minor-3 decision the test verifies the symmetric behaviour. |
| SS-Minor-5 | `config.go:227` comment misleading | **A** (subsumed by SS-Major-C) | Fixed. |
| SS-NTH-1 | `MaxFilterConditions=20` vs `MaxInListSize=50` clarification | n/a | No action — separate caps for separate purposes. |
| SS-NTH-2 | Multi-colon identifiers (`did:plc:x:y`) — not a problem | n/a | Confirmed safe; documented for posterity. |
| SS-NTH-3 | Delete `HasDIDMethodPrefix` outright | **A** (subsumed by SS-Major-B) | After the 5-caller migration there are no callers; deleting the function eliminates the foot-gun permanently. |

### Code quality / tests / docs lens

| ID | Source | Decision | Rationale |
|----|--------|----------|-----------|
| CQ-Major-1 | SECURITY.md says ADMIN_DIDS uses strict; code uses prefix-only | **A** (subsumed by SS-Major-B) | Resolved by the migration. |
| CQ-Minor-1 | `config.go:226-229` doc comment stale | **A** (subsumed by SS-Major-C) | Resolved. |
| CQ-Minor-2 | `where.go:29-30` comment references `docs/issue-64/plan.md` | **D** | Low-priority; the explanatory body of the comment already carries the WHY. The doc-pointer is fine. |
| CQ-Minor-3 | `isValidDID` shim in extractors retired | **A** | Two call sites only; replace with direct `did.IsValid` and delete the shim. |
| CQ-Minor-4 | `unrecognized_shape` conflates null+malformed | **A** (subsumed by SS-Minor-2) | Reclassification fixes this — empty bare strings move to `non_did`; the remaining `unrecognized_shape` cases are non-string shapes (objects without `.identity`, arrays, numbers, malformed JSON). |
| CQ-Minor-5 | Const formatting (long single-line string) | **D** | Cosmetic. |
| CQ-Minor-6 | Hardcoded NSID in `wantsContributorFilter` | **D** | Per plan §"Alternative C" deferral and CQ-NTH-1; revisit when a second collection adopts the shape. |
| CQ-Minor-7 | `IsArrayContributor` naming | **D** | Per plan m-3; rename when a second collection adopts the shape. |
| CQ-NTH-1..5 | Various observations, retired-test note, CHANGELOG voice | n/a | All confirmation-only or already in place. |

---

## Apply order (round-2 commits on `staging`)

Two new commits on top of the implementation, plus a CHANGELOG/doc
amendment:

1. **`refactor(security): replace oauth.HasDIDMethodPrefix with strict
   did.IsValid at all call sites; delete the prefix-only helper`**
   - Migrate `cmd/hypergoat/main.go:432, 475, 1209`,
     `internal/oauth/serviceauth.go:212`,
     `internal/graphql/admin/handler.go:147`,
     `internal/graphql/admin/resolvers.go:1118` to call
     `did.IsValid`.
   - Delete `oauth.HasDIDMethodPrefix` and its test
     (`internal/oauth/did_test.go:TestHasDIDMethodPrefix`).
   - Fix `internal/config/config.go:226-229` comment to point at
     `did.IsValid` and drop the stale cycle claim.
   - Confirm `SECURITY.md:19` claim about ADMIN_DIDS is now accurate.
   - Closes SS-Major-B, SS-Major-C, CQ-Major-1, CQ-Minor-1, SS-Minor-5.

2. **`fix(graphql): CASE-WHEN guard the contributor EXISTS subquery;
   tighten classification and validation`**
   - SQL: rewrite `buildContributorFilter` to wrap the EXISTS in
     `CASE WHEN <guards> THEN EXISTS(...) ELSE FALSE END`. The
     guards are evaluated only inside the THEN branch is not the
     point — CASE forces left-to-right per the Postgres docs.
   - Extractor: drop `strings.TrimSpace` in
     `extractContributorDID`. Reclassify empty bare-string and
     empty `.identity` as `non_did` (consistent with the plan's
     definition of `unrecognized_shape` as the strong-ref signal).
   - Resolver: reject `in: []` in `buildContributorFieldFilter`
     with a clear error message.
   - Retire `isValidDID` shim in extractors; call `did.IsValid`
     directly from `activity_contributor.go` and `endorsement.go`.
   - Tests:
     - `TestGetByCollectionFiltered_Contributor_PaginationKeyset` —
       acceptance E.
     - `TestGetByCollectionFiltered_Contributor_ComposeWithExcludePds`
       — PF-Minor-1.
     - `TestGetByCollectionFiltered_Contributor_WhitespaceBearingStoredDID`
       — SS-Minor-4. Asserts the symmetric policy (stored whitespace
       blocks both the filter and the notification).
     - `TestBuildContributorFieldFilter_RejectsEmptyInList` — SS-Minor-1.
     - Update `TestExtractContributorDID_EmptyBareString` /
       `_ObjectMissingIdentity` to expect `non_did` /
       `unrecognized_shape` per the new classification (empty
       bare → `non_did`, empty `.identity` → `non_did`, no `.identity`
       at all → `unrecognized_shape`).
   - Closes SS-Major-A, SS-Minor-1..4, CQ-Minor-3, CQ-Minor-4, PF-Major-1, PF-Minor-1.

3. **CHANGELOG amendment to the existing entry** — document the
   round-2 changes (CASE-WHEN guard, full migration of
   `HasDIDMethodPrefix` callers + deletion, classification tightening,
   pagination test) so the CHANGELOG entry is complete when merged.

---

## Round 3?

**Targeted, yes.** SS-Major-A and SS-Major-B touch security-sensitive
code (the SQL evaluation contract and the input-validation gate at 5
call sites). A focused security re-review on just the round-2 fixes
(not the whole feature) is justified — same lens, smaller scope,
quick.

The other reviewers' Minors are all small, well-bounded, and don't
warrant another round.

After round-3 sign-off the PR opens for the operator to merge.

---

## Follow-up issues (still to file before/with PR)

1. Ingest-time hard cap on `contributors` array length (round-1 C-1
   follow-up).
2. Per-query `statement_timeout` on the public GraphQL endpoint
   (round-1 Security n9 follow-up).
3. Strong-ref variant support for `contributorIdentity`, gated on
   the `contributor_identity_total{outcome="unrecognized_shape"}`
   metric trending up (round-1 follow-up).
