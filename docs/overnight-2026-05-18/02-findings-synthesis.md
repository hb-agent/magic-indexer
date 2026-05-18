# 02 — Synthesis findings (architecture / API-contract / operational)

Date: 2026-05-18 (overnight pass).
Scope: the three remaining lenses from the operator brief, scoped to the
last-week deltas (#87 joinedWhereRegistry, #88 arrayWhereRegistry,
#89 derivedFieldRegistry) plus the supporting plumbing they touch.
Calibration: high = real coupling break / production error; medium = real
gap, fix soon; low = cosmetic. Hard ceiling: 10. Honest empties valued.

Three findings. Most-important first.

---

### O-1: Operator note in migration 030 references a RUNBOOK section that doesn't exist
**Severity:** medium
**Location:** `internal/database/migrations/postgres/030_add_award_badge_uri_index.up.sql:41-47` (the operator note) vs. `docs/RUNBOOK.md` (no INVALID-CONCURRENTLY recovery section anywhere)
**Problem:** Migration 030 contains:
```
-- Operator note: if this migration runs but the index ends up
-- INVALID (a known foot-gun of CONCURRENTLY + IF NOT EXISTS),
-- recover by running `DROP INDEX CONCURRENTLY
-- idx_record_award_badge_uri` then re-running this migration.
-- A RUNBOOK section documenting this once for all CONCURRENTLY
-- migrations is tracked as follow-up §9.5 in
-- docs/issue-89/plan.md.
```
The operator note effectively contains the recovery procedure inline
(four lines), so it's not a missing-runbook for *this* migration. But
the note explicitly promises a RUNBOOK section that documents the
generic recovery for ALL `CONCURRENTLY` migrations once for all —
and `docs/RUNBOOK.md` has no such section. `grep -i "INVALID\|CONCURRENTLY.*recover\|reindex\|25001"` against the RUNBOOK returns
zero matches. Migrations 008, 010, 012, 025, 026, 029, 030 are all
CONCURRENTLY-creating partial expression indexes and inherit the same
recovery procedure; none reference a RUNBOOK section either.
**Why it matters:** at 3am during a deploy that left a partial index
in INVALID state, the operator is grep-ing the RUNBOOK for "INVALID"
and finding nothing. They'd then have to read migration 030's source
comment in full to learn the recovery shape, then generalise to the
migration that actually failed. The deferred follow-up is the right
place for the fix; the issue is that the deferral is *invisible* to
the operator (it lives in `docs/issue-89/plan.md`, an archive doc,
not the RUNBOOK or AGENTS.md). Low blast radius (one short SRE
session to figure it out), but the kind of thing that compounds
during a real incident.
**Proposed fix:** add a `## Recovering an INVALID partial index after
CONCURRENTLY` subsection to `docs/RUNBOOK.md` under the existing
GraphQL/migrations area. Three short paragraphs: (1) how to detect
(`SELECT indexrelid::regclass, indisvalid FROM pg_index WHERE NOT
indisvalid;`); (2) the generic recovery (`DROP INDEX CONCURRENTLY
<name>` then re-run the migration — IF NOT EXISTS makes the rerun
safe); (3) note that the inline migration comments stay because the
RUNBOOK might be out of date for a brand-new migration. Then update
migration 030's operator note from "tracked as follow-up §9.5" to
"see RUNBOOK § Recovering an INVALID partial index after
CONCURRENTLY." Same single-line tweak in any future
CONCURRENTLY migration template.
**Effort:** S
**Risk of fix:** low (documentation only)
**Reversibility:** easy

---

### O-2: No metric for nested-where usage — operator cannot see adoption of #87 vs. #88 vs. legacy filters
**Severity:** low
**Location:** `internal/graphql/schema/where.go:449-475` (the joined-where dispatch in `extractFieldFiltersRecursive`) and `internal/graphql/schema/where.go:478-490` (the array-where dispatch); compare with the existing `metrics.RecordAuthorsFilterApplied(collection, len(*authorsFilter))` instrumentation at `internal/graphql/schema/builder.go:659`.
**Problem:** The authors filter (older feature, in production) emits three Prometheus counters: `RecordAuthorsFilterApplied`, `RecordAuthorsFilterEmptyBlocked`, `RecordAuthorsFilterTooLarge`. The three new nested-where features emit zero metrics. There is no operator signal for:
(a) "Is anyone using the joinedWhere `badge` filter on `app.certified.badge.award`?" — relevant because the EXISTS subquery is the most expensive code path in the read layer and #87's plan noted this would be load-bearing once the certified-app's Endorsements tab shipped.
(b) "Is the awardCount field being selected on large pages?" — relevant because P-5 in the performance findings catalogues the N+1 cost; without a counter the operator can't correlate latency spikes to awardCount adoption.
(c) "Is the array-where `items` filter ever returning empty results because of the EXISTS-against-missing-array semantics?" — relevant because #88's pinned description acknowledges that `{items: {}}` semantics are non-obvious to consumers.
The brief asks: is the metrics-omission deliberate? Re-reading the per-issue plans, the §9.x follow-ups in issues 87/88/89 explicitly defer metrics work, but the deferral rationale is "wait until we see read-path latency anomalies." That's reasonable for the per-call latency angle, but it omits the "how often is this code path even hit" angle — which is the question an operator asks BEFORE the latency anomaly, not after.
**Why it matters:** without these counters, when the certified-app team adopts (or fails to adopt) the new nested-where surface, the operator has no in-process signal. They'd have to scrape access logs or query the Postgres `pg_stat_statements` view — both of which they CAN do, both of which they shouldn't have to. Low severity because (a) the per-call latency is already covered by the global request-duration histogram, and (b) the brief explicitly notes the no-metrics decision is plausibly the right call for read-path features.
**Proposed fix:** add three counters in `internal/metrics/metrics.go` and call them from the corresponding extractor/resolver sites:
- `hypergoat_graphql_joined_where_applied_total{lexicon,field}` — incremented at `where.go` joined-where dispatch.
- `hypergoat_graphql_array_where_applied_total{lexicon,field}` — incremented at `where.go` array-where dispatch.
- `hypergoat_graphql_derived_field_resolved_total{lexicon,field}` — incremented at `resolveAwardCount` (and any future derived resolver). Counts per-row resolution, so the operator sees "awardCount fired 1247 times in the last 15 minutes."
The label-cardinality is bounded: each registry has a tiny fixed set of (lexicon, field) pairs. Three-counter addition, ~30 LOC including the metric declarations.
Alternative: just one counter `hypergoat_graphql_registry_feature_used_total{kind, lexicon, field}` covering all three patterns. Single counter, three label dimensions. Cleaner but slightly heavier on Prometheus storage.
**Effort:** S
**Risk of fix:** low (additive, no behaviour change)
**Reversibility:** easy

---

### X-1: GraphQL error shapes are inconsistent across the three rejection paths on the public surface
**Severity:** low
**Location:** `internal/graphql/handler.go:145-152` (depth.Check → HTTP 400 plain text), `internal/graphql/handler.go:99-116` (timeoutResponse → HTTP 200 GraphQL body with `extensions.code = "QUERY_TIMEOUT"`), `internal/graphql/schema/builder.go:667-670` (extractFieldFilters error → returned to graphql-go which produces HTTP 200 GraphQL body with bare `errors[].message`, no `extensions.code`)
**Problem:** Three rejection paths, three shapes:

1. **Depth cap exceeded** (`maxGraphQLQueryDepth = 15`): HTTP 400 plain-text body `"query rejected: nested too deeply"`. No JSON, no `errors` array.

2. **Query timeout exceeded** (`GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS`): HTTP 200 GraphQL body, single error entry with `extensions: {code: "QUERY_TIMEOUT", budgetMs: N, retryable: false}`. Pinned shape per `docs/issue-71/plan.md`.

3. **Schema-level error** (filter-depth cap exceeded inside extractor; unknown sort field; invalid where shape; nested-where target not registered): HTTP 200 GraphQL body, single error entry with `message: "invalid where filter: filter nesting exceeds maximum depth of 3"`. No `extensions.code`. The bare-string contract means consumers have to substring-match the message to do retry-vs-no-retry decisions.

A consumer building a retry-handler against the public endpoint can't write a unified "is this a permanent client error?" check. They'd need three branches: status-400-text-prefix-match, status-200-extensions-code-match, status-200-message-substring-match. The first is brittle to copy edits; the third is brittle to error-message reformatting.

**Why it matters:** the certified-app team is the load-bearing consumer here. They've already filed friction issues against past API decisions; another "we can't tell which errors are retryable" issue is the predictable next one. Low severity because (a) the GraphQL-over-HTTP spec doesn't actually mandate consistent shapes for these three error categories, and (b) the timeout shape is the only one with a documented contract — the other two were never promised to be machine-parseable. Worth flagging because the three paths grew organically and the inconsistency wasn't a deliberate choice.

**Proposed fix:** unify all three on the timeout shape's pattern — return HTTP 200, GraphQL-shaped body, `errors[].extensions.code` set to a SCREAMING_SNAKE_CASE sentinel. Specifically:
- `depth.Check` failure: emit `{code: "QUERY_TOO_DEEP", maxDepth: 15, retryable: false}` in extensions, 200 status, GraphQL body. Drop the `http.Error` plain-text response.
- Extractor failures: wrap the returned error into a `gqlerrors.FormattedError` with `code: "INVALID_WHERE_FILTER"` (or a more specific code derived from the underlying error type — `FILTER_DEPTH_EXCEEDED`, `FILTER_CONDITIONS_EXCEEDED`, `IN_LIST_TOO_LARGE`). Requires a tiny tagging shim in the extractor since it currently returns bare `fmt.Errorf`.

Both changes are additive at the wire level (consumers reading `errors[].message` still see something readable) but give consumers a stable machine-parseable signal. ~40 LOC including the new sentinel constants.

Alternative: leave depth.Check as 400 (the only path that's a pre-execution rejection, not a query result) but unify the other two. Smaller fix, preserves the existing semantic that pre-parse rejections are HTTP errors. My recommendation, actually — depth.Check is happening before graphql-go ever sees the request, so it's structurally different from the other two.

**Effort:** S (alternative) / M (full unification)
**Risk of fix:** low — additive, no removal of existing fields
**Reversibility:** easy

---

## Honest empties — lenses scanned, no finding

### A-N/A: Architecture — registry pattern is contained; dependency direction is healthy
**Result:** Scanned for cross-package leakage that snuck in over the
last week. None found. Specifically:

- The three registries (`joinedWhereRegistry`, `arrayWhereRegistry`, `derivedFieldRegistry`, plus the older `filterRegistry`) all live in `internal/graphql/schema/` package-private and are NOT referenced from anywhere else. Grep across `internal/database/repositories/` returns only two doc-comment mentions in `filter.go` (lines 224, 265) pointing readers from the SQL layer back to the GraphQL-layer source — exactly the documentation cross-reference the security-comment convention (R-20) calls out as load-bearing. No CODE in `repositories/`, `server/`, or `graphql/admin/` reads any registry. Clean.

- `internal/graphql/schema/derived_fields.go` imports `internal/graphql/resolver` and (transitively, via the resolver Repositories struct) `internal/database/repositories`. Same import shape as `internal/graphql/schema/builder.go` (which has every connection resolver and every JSON-field resolver) and the same shape as `internal/graphql/schema/where.go` (which builds `repositories.FilterGroup` from extracted GraphQL input). Not a new pattern; it's the established direction (schema → resolver → repositories). The "registry contains code" pattern was deliberate per issue #89's plan §9.4 — derived-field Resolve funcs need to call repositories the same way connection resolvers do.

- `internal/graphql/types/object.go` (the `ObjectBuilder` that #89 modified to accept `derivedFieldsByLexicon`) does NOT import `internal/graphql/schema/` — the derived-fields map is passed in as a `map[string]map[string]*graphql.Field` parameter at construction time. The doc comment on the field (line 56-58: "Owned by the schema package's derivedFieldRegistry; passed in at construction so this package stays free of schema-layer imports") explicitly identifies the dependency-direction concern and confirms the chosen design avoids it. The `types` package depends only on `lexicon` and `graphql-go`. The schema package owns the registry and converts it to the parameter shape via `derivedFieldsForObjectBuilder()`. Direction: `schema` depends on `types`, NOT the reverse. Healthy.

### X-N/A: API/contract quality — pinned descriptions are consumer-grade
**Result:** read all three pinned descriptions
(`badgeAwardBadgeDescription`, `collectionItemsArrayDescription`,
`awardCountDescription`) end-to-end against the brief's checklist:
explains-what-isn't-possible, shows-realistic-examples,
not-just-technical-documentation.

All three pass on all three counts:

- **badgeAwardBadgeDescription** (#87) — explains the EXISTS-against-missing-ref semantics ("An award pointing at a missing or deleted definition fails the existence check"); shows a worked example with the actual use case ("endorsements OR verifications received by me"); names the consumer-side read pattern ("Endorsements-tab read pattern"). The "what's not possible" is implicit (the inner where is full-power, so there's nothing to *exclude*) but the example shape demonstrates the limit. Good.

- **collectionItemsArrayDescription** (#88) — explains the any-element semantics + the empty-array exclusion + the strongRef shape inside the element; shows a concrete worked example; explicitly calls out that `{items: {}}` is a meaningful empty-predicate ("filters to collections that have at least one item"). The non-obvious "EXISTS-against-empty-array drops the row" is called out explicitly, which is the brief's "what's NOT possible" test. Good.

- **awardCountDescription** (#89) — names the consumer-facing UI surface ("certified-app's Lists section"); explains the count is "independent of the award subject" (i.e., crosses all subjects/DIDs — the consumer might assume otherwise); explicitly calls out the limit ("Filtered counts (e.g. by issuer or by award properties) are not yet exposed"). The performance claim ("per-row cost is sub-millisecond") is the only weakness — P-5 in the performance findings caught it as misleading for high-count definitions and proposed a doc fix. Already covered by P-5; not duplicating.

None of the three reads as "technical reference for someone who already knows the internals." All three read as "consumer-facing documentation that anticipates the predictable misuses." The pinning-via-test discipline (`Test*Description_*`) keeps them honest.

### X-N/A: Filter operator availability per kind — discoverable from introspection
**Result:** scanned the GraphQL surface for whether a consumer can
figure out, from introspection alone, that `KindScalar` supports many
operators while `KindStringSubject` supports only `eq`/`in`.

The answer is YES, cleanly: the input-type system enforces it
structurally. `KindStringSubject` fields are typed as
`DIDFilterInput` (`internal/graphql/types/filters.go:94-101`) which
declares only `eq` and `in` as input fields. A consumer running
`__type(name: "DIDFilterInput")` sees the exact two-operator
surface; sending any other operator fails at GraphQL parse-time
with a clear "field not defined on input type" error. Same for
`StringFilterInput` (8 operators) vs `IntFilterInput` (8
operators, different set) vs `BooleanFilterInput` (2 operators).

The bottom-of-stack defence — SQL emitters returning "operator X
not supported on Y filter" — is unreachable from a valid query
because the schema rejects the invalid input before it reaches
the extractor. That's the right shape: the input-type system is
the consumer-facing contract; the SQL-emitter defence is the
internal-API safety net.

No finding. The discoverability is the input-type definition,
which IS the schema introspection result.

### O-N/A: Log levels on new code paths are correctly chosen
**Result:** the only `slog` calls in #89's added code are:

- `slog.WarnContext(p.Context, "awardCount: source is not a map", ...)` (`derived_fields.go:71`) — only fires when a future refactor breaks the connection-resolver contract that always sets a map source. Warn is correct (it's a "shouldn't happen" with observability value if it does).
- `slog.WarnContext(p.Context, "awardCount: repositories unavailable in context")` (`derived_fields.go:80`) — only fires if a future caller mounts the schema without the WithRepositories middleware. Warn is correct.
- `CountAwardsByBadgeURI` (`records.go:792-809`) — zero log lines. Per-row resolver hot path; no log spam. Correct.

The brief's specific concern — log spam from awardCount firing once per row in a large page — does NOT materialise. There's nothing to log per-row because the happy path has no log line, and the warn-cases only fire on contract violations.

#87 and #88 added one log line each in `builder.go` ("joinedWhereRegistry entry references unregistered lexicon" at line 135; "arrayWhereRegistry entry references missing element def" at line 156) — both at Warn level, both fire at schema-build time only (once per process per startup), so log volume is bounded. No per-request emission.

---

## Counts

| Severity | Count |
|----------|------:|
| High     | 0 |
| Medium   | 1 (O-1) |
| Low      | 2 (O-2, X-1) |
| Honest empties (lenses scanned, no finding) | 4 |
| **Total findings** | **3** |

Three findings, four honest-empty lens reports. Well under the 10 ceiling, as the brief predicted ("most of the high-leverage stuff is already in the prior docs").

## Highest-leverage fix for the morning

**O-1** — pure docs change, 15-minute task. The deferred follow-up
landed in an archive doc instead of the RUNBOOK; bringing it to
the RUNBOOK is the move. The other two are low-severity polish.

## Cross-pass cross-references

- **O-2** complements P-5 (awardCount N+1 doc fix): with a per-call counter, the operator can correlate the latency claim against actual adoption. Land them together.
- **X-1** complements the GraphQL surface hygiene already addressed in the earlier `internal/graphql/handler.go` work (the timeout shape is the gold standard; the other two paths haven't been brought up to it).
- **O-1** is purely additive to RUNBOOK; no other pass touches the docs.

No critical/high findings. The brief was right: most of the night's
leverage was upstream of this pass.
