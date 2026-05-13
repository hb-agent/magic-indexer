# Issue #64 — Plan review, Round 1

**Date**: 2026-05-13
**Plan reviewed**: `docs/issue-64/plan.md` at commit prior to this round's amendments
**Reviewers**: five parallel agents, one lens each (schema correctness, SQL correctness,
security, performance, API consumer ergonomics).

---

## Verdicts

| Lens | Verdict | Critical | Major | Minor | Nice-to-have |
|------|---------|----------|-------|-------|--------------|
| GraphQL schema correctness | approve with conditions | 0 | 3 | 3 | 2 |
| Postgres SQL correctness | approve with conditions | 0 | 4 | 5 | 3 |
| Security | **rework needed** | 1 | 3 | 4 | 2 |
| Performance | approve with conditions | 0 | 3 | 4 | 3 |
| API consumer ergonomics | approve with conditions | 0 | 5 | 3 | 3 |
| **Totals** | | **1** | **18** | **19** | **13** |

The lone Critical is real and identical across the security and performance reviewers
(query-time DoS via an unbounded `contributors` array). It has a clean local mitigation.

Three Majors recur across reviewers (the GIN-index misattribution, the
`jsonb_array_length`/`jsonb_typeof` guards, and the deferred-index proposal's
bare-string blind spot). All accepted.

---

## Decisions per finding

Legend: **A** = accepted (plan updated). **D** = deferred (filed as follow-up,
not in this PR). **R** = rejected (rationale recorded). Anchors point at the
relevant section of `plan.md` after the round-1 update.

### Critical

| ID | Source | Decision | Rationale |
|----|--------|----------|-----------|
| C-1 | Security #1 (corr. Perf #3) | **A** | Add `jsonb_typeof(r.json->'contributors') = 'array' AND jsonb_array_length(r.json->'contributors') <= 200` as guard clauses before the EXISTS subquery. 200 mirrors `MaxContributorsBeforeReject` (`internal/notifications/types.go:26`). Fail-safe: oversized arrays do not match this filter. Ingest-time hard cap is filed as a separate follow-up issue (not in this PR — would change record-acceptance policy). |

### Major

| ID | Source | Decision | Rationale |
|----|--------|----------|-----------|
| M-1 | Schema #1 | **A** | Test-target absence assertion will use `app.certified.badge.award` (loaded in `testdata/`) instead of the unloaded `app.certified.temp.graph.endorsement`. |
| M-2 | Schema #2 | **A** | Expand the `where.go` task description in §"File ownership" to spell out: (a) branch placement before the lexicon-driven loop, (b) per-value `isValidDID` validation order (before `FieldFilter` is constructed), (c) `lexiconType` derivation bypass, (d) the `IsArrayContributor` marker. |
| M-3 | Schema #3 | **A** | Extract the lexicon-ID test into a named predicate `wantsContributorFilter(lexID) bool` so a future "second collection" maintainer has a discoverable seam. |
| M-4 | SQL M1 | **A** (subsumed by C-1) | The `jsonb_typeof` guard from C-1 covers this finding. |
| M-5 | SQL M2 | **A** | Add an integration test asserting `EXPLAIN` plan still uses `idx_record_collection_keyset` for the default contributor-only query. |
| M-6 | SQL M3 | **A** | Resolver normalises `[]interface{}` → `[]string` (rejecting non-string elements) before running `isValidDID`. Same pattern as existing `authors` parsing. |
| M-7 | SQL M4 (corr. Perf #1) | **A** | Plan's §Performance edited to remove the false claim that `idx_record_json_gin` accelerates `eq`. Index opclass is `jsonb_path_ops`, only `@>` etc. are supported; the chosen EXISTS shape uses none of them. Only `idx_record_collection_keyset` does the work. |
| M-8 | Security M2 | **A** | Strengthen the shared `IsValid` predicate: reject leading/trailing whitespace, require lowercase method prefix (`did:[a-z]+:`). Existing `extractContributorDID` callsite is unaffected because real DIDs from PLC/Web are canonical-lowercase. |
| M-9 | Security M3 | **A** | The shared `did.IsValid` lives at `internal/atproto/did/` and is the canonical input-validation predicate. The pre-existing `oauth.IsValidDID` is structurally weaker (prefix-only); rename it to `oauth.HasDIDMethodPrefix` in this PR to remove the foot-gun. SECURITY.md gains a one-line entry naming the canonical predicate. |
| M-10 | Security M4 | **A** | Document COALESCE precedence explicitly in §"SQL shape": bare string wins when present; the object access is only consulted when the bare access is `NULL`. Add a fixture test exercising a record with one bare-string contributor and one object-shape contributor — both must be reachable. |
| M-11 | Perf #2 (corr. SQL m5) | **A** | Edit §"Performance considerations" deferred-index sub-paragraph to note that the proposed `jsonb_path_query_array(..., '$[*].contributorIdentity.identity')` indexes **only** the object variant; if the indexer ever ships it, the index must cover both shapes (or the bare-string variant must be deprecated by then). Also note `IMMUTABLE` wrapper requirement. |
| M-12 | Perf #3 | **A** (subsumed by C-1) | Covered by the `jsonb_array_length` bound. |
| M-13 | Ergonomics #1 | **A** | Pin the GraphQL field description string in the plan verbatim. Includes the DID-only policy, the silent-skip behaviour on handle records, the strong-ref deferral, and a concrete `_or` composition example. |
| M-14 | Ergonomics #2 | **A** | Error message becomes `"contributor filter values must be DIDs (did:...); resolve handles to DIDs in the session layer — handle values are not indexed as a queryable identity"`. Includes the offending value in the wrapped error. |
| M-15 | Ergonomics #3 | **A** | Plan §"Alternatives considered" gains a one-sentence note that the issue's top-level `contributorIdentityIn` shorthand was considered and rejected for composability. CHANGELOG entry will state the composition pattern. |
| M-16 | Ergonomics #4 | **A** | Concrete `_or` example baked into the field description so consumers do not write `where: { did, contributor }` (intersection) when they want `_or` (union). |
| M-17 | Ergonomics #5 | **A** | Implement the notifications fix and the filter as **two separate commits** on `staging`. A partial revert is then `git revert <fix-commit>` or `git revert <filter-commit>` independently. Rollback plan section updated. |

### Minor

| ID | Source | Decision | Rationale |
|----|--------|----------|-----------|
| m-1 | Schema #4 | n/a | Verification only — `DIDFilterInput` confirmed to expose `eq`/`in` only. Plan updated to cite the confirmation. |
| m-2 | Schema #5 | n/a | Verification only — `contributor` field name vs `contributors` output array: no GraphQL collision (input vs output namespaces). Plan updated to cite. |
| m-3 | Schema #6 | **D** | `FieldFilter.IsArrayContributor` naming kept as-is for v1. Plan acknowledges the name is intentionally narrow and will be renamed if a second collection adopts the shape. |
| m-4 | Schema #7 | n/a | Note added to plan: introspection path is `schema.QueryType().Fields()["orgHypercertsClaimActivity"].Args` → find `where` → cast → `.Fields()["contributor"]`. |
| m-5 | SQL m1 | **A** | Add test fixtures for JSON `null` literal and a whitespace-bearing-DID stored shape. |
| m-6 | SQL m2 | **A** | Author the `jsonb_typeof` guard **before** the EXISTS in the SQL text so the planner can short-circuit cheaply. Tiny ordering matter; cost-free. |
| m-7 | SQL m4 | n/a | Verification only — SQL injection surface unchanged from current builder. |
| m-8 | SQL m5 | **A** (folded into M-11) | Same edit. |
| m-9 | Security m5 | **A** | Add a §"Security" stub to the plan stating no new data disclosure (the contributor identities are already returned in record content). |
| m-10 | Security m6 | **A** | One sentence in §"Security": notification fan-out is bounded per-record by `MaxFanOutPerRecord = 100`; PR re-enables a previously-broken path. |
| m-11 | Security m7 | n/a | Verification only — metric label cardinality confirmed bounded (3 fixed outcomes). |
| m-12 | Security m8 | **A** | Plan explicitly states the `non_did` / `unrecognized_shape` outcomes are **DEBUG-level or no log** (metric is the signal). Mirrors `PDSResolveNoEndpoint` precedent. |
| m-13 | Perf #4 | **A** | Plan §"Performance considerations" gains one sentence: recent-active contributors are fast (cursor short-circuits early), ancient/inactive contributors are slow (planner walks newer rows to confirm no further matches). Documents the expected asymmetry. |
| m-14 | Perf #5 | n/a | Verification only — `excludePds`+EXISTS composition confirmed compatible. |
| m-15 | Perf #6 | n/a | Verification only — cursor predicate confirmed compatible with `idx_record_collection_keyset`. |
| m-16 | Perf #7 | n/a | Verification only — metric overhead at ingest negligible. |
| m-17 | Ergonomics #6 | **A** | Plan adds one-line code comment requirement near field declaration: "singular is deliberate; the predicate is per-record." |
| m-18 | Ergonomics #7 | **A** | Plan §"DID-only policy" gets one sentence: "Loud at the input boundary, silent at the data boundary — we own the contract, producers own the data." |
| m-19 | Ergonomics #8 | n/a | Verification only — `/metrics` exposure is operator-only by reverse-proxy convention. |

### Nice-to-have

| ID | Source | Decision | Rationale |
|----|--------|----------|-----------|
| n-1 | Schema n1 (test plumbing) | n/a | Confirmation; implementer follows the existing pattern. |
| n-2 | Schema n2 (unknown-op warn-log path unaffected) | n/a | Confirmation only. |
| n-3 | SQL n2/n3 | **D** | Belt-and-braces tests for JSON-array shape rejection — leave to implementer's judgement. |
| n-4 | Security n9 (statement_timeout) | **D** | Out of scope. File as a separate hardening issue across the GraphQL endpoint. |
| n-5 | Security n10 (oauth IsValidDID rename) | **A** (folded into M-9) | Done. |
| n-6 | Perf n8 (test data sizing) | **A** | Plan §"Performance" gains one sentence: perf measurement happens via staging `EXPLAIN ANALYZE`, not synthetic load tests. |
| n-7 | Perf n9 (subscriptions unaffected) | **A** | One sentence in plan: `contributor` filter applies to the connection query only; the existing Jetstream-driven subscription is collection-only and unaffected. |
| n-8 | Perf n10 (W3C DID charset divergence) | **A** (folded into M-8) | The stricter charset is intentional; documented in plan §"DID-only policy". |
| n-9 | Ergonomics n9 (future-regret check on field name) | n/a | Verified — `contributor` reads correctly even if strong-ref ships later. |
| n-10 | Ergonomics n10 (track `unrecognized_shape` as strong-ref signal) | **A** | Plan §"Acceptance criteria" gains a note: operators should watch this outcome; a rising trend indicates producers shipping strong-refs and is a follow-up trigger. |
| n-11 | Ergonomics n11 (endorse `did.IsValid` move) | **A** (already M-9) | Confirmed. |

---

## Round 2?

**No.** The 18 Majors plus the lone Critical are mechanical, well-bounded plan edits.
None of them changes the design — they sharpen the implementation contract. The
Critical's mitigation (`jsonb_array_length <= 200`) is a single SQL clause with
clear precedent.

The implementation-side review at the end of the work will catch any miss
introduced while applying these edits. Spawning a round 2 here would be
nit-picking and would consume reviewer budget that's better spent on the
implementation review.

---

## Follow-up issues to file before merge

1. **Ingest-time `contributors` array hard cap.** Reject (or truncate) records
   with > 200 contributors at ingestion. Belt-and-braces alongside the SQL
   bound from C-1. Severity: medium. Filer: this PR's author.
2. **Per-query `statement_timeout`** on the public GraphQL endpoint. Generic
   defence across all JSON-scanning filters, not specific to this filter.
   Severity: medium. Filer: this PR's author.
3. **Strong-ref variant support for `contributorIdentity`.** Track
   `contributor_identity_total{outcome="unrecognized_shape"}` after deploy; if
   trending upward, prioritise. Severity: low until the metric moves.

The three issues will be filed in a single GitHub session after the PR is open,
with cross-references in the PR body.
