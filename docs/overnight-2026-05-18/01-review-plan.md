# 01 — Review plan

## Lenses chosen, with rationale

### Definitely in scope

**Security.** Highest-stakes lens for this codebase. There are two
HTTP surfaces (public `/graphql` + admin `/admin/graphql`), an
OAuth implementation with DPoP + JWT + service-auth, a notifications
bridge that mints service-auth JWTs, and SQL goes out over a raw pgx
pool. The codebase relies on parameterised queries — verify that
holds *everywhere*, including in the three nested-where features
shipped today. AGENTS.md lists ~half the historical reviews as
having had at least one security-adjacent finding. Worth a careful
pass on: SQL string interpolation in repository helpers (parameter
binding vs. concat), admin auth bypass paths, input validation gaps
on user-supplied identifiers (DIDs, URIs, lexicon IDs), log
injection on request-derived strings, secret-handling at startup +
during DPoP replay rotation.

**Correctness / robustness.** Multiple concurrent subsystems with
shutdown ordering that matters: Jetstream consumer, Tap consumer
(optional), N labeler consumers, OAuth cleanup ticker, backfill
worker. `backgroundServices.Stop()` has explicit ordering and
locking — verify the locking is sound. Per-PR `go test -race` is
the safety net; this pass looks for hazards `-race` cannot see
(time-of-check/time-of-use, partial-failure handling, panic in a
goroutine that should propagate).

**Reuse and consistency.** Three nested-where registries shipped
today (`joinedWhereRegistry` #87, `arrayWhereRegistry` #88,
`derivedFieldRegistry` #89). Plan §9.4 in each defers the "extract a
generic abstraction" decision until a 2nd or 3rd of the SAME kind
lands. Right now the three are different KINDS — but adjacent code
in `where.go` and `builder.go` may have crystallised genuine
duplication. This lens looks at *concrete* duplication (same dozen
lines twice) not principle violations.

**Performance.** Hot paths to verify:
- `RecordsRepository.Insert` (per-record ingestion path).
- `buildFilterGroupRecursive` (per-query SQL emission).
- Connection-resolver `totalCount` paths.
- The new `awardCount` N+1 (documented but verify the cost claim).
- Filter SQL → index alignment (drift tests cover the byte-equality,
  but verify the planner actually picks the partial indexes in
  realistic shapes).

**Code quality (selective, not nits).** Three large files
(builder.go 1166L, records.go 1177L, filter.go 1084L, main.go
1372L) — worth asking "is there a natural seam being missed" but
NOT "rename things" or "extract methods for style." Look for
dead code, defensive checks for impossible states (the directive
calls this out as the prime deletion target), and duplicated
patterns within a single file.

**Testing.** Verify the load-bearing paths have failure-mode
tests, not just happy-path. Specifically: what happens when a
Jetstream reconnect interleaves with a labeler restart? What
happens when a DB query times out mid-transaction? Are there tests
for the partial-failure cases, or only the success cases?

### Selectively in scope (depends on what waves 1-2 surface)

**Architecture.** The package layout already has clear boundaries.
Looking for cross-package leakage that snuck in over the last week
(new files in `internal/graphql/schema/` referencing
`internal/database/repositories` types — fine; new files
referencing `internal/oauth` from somewhere unrelated — flag). The
recent registry pattern (#87 #88 #89) all live in
`internal/graphql/schema/` — verify they don't leak into the SQL
layer.

**API / contract quality.** Pinned-description text on the
registry entries. The drift-detection tests cover byte-equality;
this lens reads them for actual accuracy + helpful examples.
Especially: do the descriptions explain what's NOT possible (gaps
that would otherwise be discovered by trial-and-error)?

**Operational.** Log volume + cardinality on hot paths
(`slog.WarnContext` per request is fine; `slog.Info` per record
ingested is not). Prometheus metrics coverage of the new code
(#87 #88 #89 added zero metrics — is that a real gap or a
deliberate "no oncall signal needed" call?). Migration index
validity recovery (the operator note in migration 030 references a
RUNBOOK section that doesn't exist yet per #89 R2.11).

### Out of scope (and why)

- **UX/UI.** The Next.js admin client in `client/` is a separate
  package not under the indexer's review surface; the only
  HTML the indexer serves is the GraphiQL widget (third-party).
- **Documentation.** AGENTS.md (1,139 lines), RUNBOOK,
  behavioral-tests.md, per-issue review docs are all in good
  shape. Not the weak spot.
- **Architecture refactors of the schema builder.** The
  consolidation of #87/#88/#89 into a generic abstraction is
  explicitly deferred per each plan's §9.4. Doing it tonight is
  exactly the "introducing new patterns to solve problems you
  cannot point to" that the directive warns against — there is
  no concrete current pain.
- **OAuth / DPoP redesign.** The 17-file OAuth subsystem has its
  own deep-flow review history; nothing on the diagnostic horizon
  suggests it needs a wholesale revisit.

## Wave ordering

1. **Wave 1 (parallel, ~50 min budget):** Security + Correctness/
   Robustness. Most likely to surface critical or high findings;
   the rest of the plan adapts to what they find.

2. **Wave 2 (parallel, ~40 min budget):** Reuse/Consistency +
   Performance. These inform each other (a duplicated SQL pattern
   that's also slow is a different priority than either alone).

3. **Wave 3 (sequential, ~30 min):** Code quality + Testing. Look
   specifically at the largest files (builder.go, records.go,
   filter.go, main.go) for dead code + missed deletion
   opportunities. Testing focuses on failure-mode coverage of the
   paths Wave 1/2 surfaced.

4. **Wave 4 (sequential, ~20 min):** API/contract quality +
   Architecture + Operational — synthesises the prior waves'
   findings into the consumer-facing surface health and the
   operator-facing health. Single review doc that integrates
   these three because they're each small.

Total review budget: ~2.5 hours. Acceptable padding for spin-up
and synthesis: ~30 min. Hard ceiling for diagnostic: 3 hours wall
clock.

## Time budget

- Phase 0 (orientation): done — ~45 min wall.
- Phase 1 (plan): ~15 min, ending now.
- Phase 2 (diagnostic): ~2.5–3h.
- Phase 3 (triage + plan): ~30 min.
- Phase 4 (implement + verify): ~3h, including mini re-reviews.
- Phase 5 (stopping rule per directive).
- Phase 6 (final re-review): ~45 min.
- Phase 7 (Draft PR + CI): ~15 min plus CI wait.

Total: ~7.5h plus CI wait. Within the 8h target with margin for
discovery work in Phase 4.

## Stopping rule (Phase 5)

Stop when any of:

1. All MUST FIX items from Phase 3 are landed AND mini-reviewed.
2. Implementation budget (~3h) is spent and the in-flight commit
   has been completed AND its quality gates pass.
3. The last full review round produced no critical/high findings
   and only nits.
4. I hit a problem that requires operator input (a decision on
   product behaviour, a destructive operation, an API contract
   choice). Document in `02b-late-findings.md` and stop.

In all four cases: leave `staging` at a green-gate state, all
in-flight work either committed or reverted, no uncommitted
changes in the working tree.

## Deletion bias commitments (per directive)

Bias toward removing things. Specifically scanning for:

- Defensive checks for impossible states (per directive: prime
  deletion target).
- Dead code (unused exports, deprecated helpers, branches that
  cannot execute).
- Comments that explain what well-named identifiers already say.
- Tests that don't actually test anything (tautological assertions,
  setup-only tests).

If something is "good but could be more flexible / abstract /
testable" with no current pain, **leave it alone**.
