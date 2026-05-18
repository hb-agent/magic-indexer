# 02 — Findings index

Per-lens findings live in sibling files. This document is the
index + the cross-cutting view used for triage.

## Per-lens documents

| File | Lens | Total | High | Medium | Low / nit |
|---|---|---|---|---|---|
| [`02-findings-security.md`](02-findings-security.md) | Security | 12 | 1 | 4 | 7 |
| [`02-findings-correctness.md`](02-findings-correctness.md) | Correctness / robustness | 16 actionable (+14 empties/cross-refs) | 4 | 6 | 6 |
| [`02-findings-reuse.md`](02-findings-reuse.md) | Reuse / consistency | 20 | 0 | 6 | 14 |
| [`02-findings-performance.md`](02-findings-performance.md) | Performance | 10 actionable (+5 empties) | 2 | 4 | 4 |
| [`02-findings-quality.md`](02-findings-quality.md) | Code quality (selective) | 5 actionable (+6 empties) | 3 | 2 | 0 |
| [`02-findings-testing.md`](02-findings-testing.md) | Testing | 15 | 2 | 8 | 5 |
| [`02-findings-synthesis.md`](02-findings-synthesis.md) | Architecture + API + ops | 3 actionable (+4 empties) | 0 | 1 | 2 |

## Frozen list — every actionable finding by severity

### Critical (0)

None. The audit history density shows — the load-bearing
hardening (constant-time compares, `logsafe` at audit sites,
parameterised SQL on the user-facing paths, `did.IsValid` at
input gates, the JTI replay primitives) is already in place.

### High (12)

| # | Title | Source | Effort | Risk |
|---|---|---|---|---|
| S-1 | Admin SQL injection in `CreateFieldIndex` / `DropFieldIndex` — `collection` interpolated raw | security | S | low |
| C-1 | Activity-cleanup worker never runs after startup (`defer ticker.Stop()` outside the goroutine) | correctness | S | low |
| C-2 | Jetstream cursor-flusher goroutine leak per reconnect | correctness | S | low |
| C-3 | WebSocket `Stop()` close-on-events races send → panic with no recover (jetstream + labeler) | correctness | S | med |
| C-4 | UploadLexicons partial-success leaves DB out of sync with running schema | correctness | M | med |
| P-1 | `GetCollectionCount` ignores filters — totalCount returns the unfiltered count (**correctness defect**) | performance | M | low |
| P-2 | `jetstream_activity` duplicates full record JSON on every ingest (compounds with C-1) | performance | M | med |
| Q-1 | `internal/oauth/errors.go` — 157 LOC of entirely dead production code | quality | S | low |
| Q-2 | `internal/oauth/token_generator.go` — 80% dead (4 of 9 `Generate*` functions + `IsExpiredWithSkew`) | quality | S | low |
| Q-3 | `internal/oauth/scopes.go` — 137 LOC dead except `ParseScopes` | quality | S | low |
| T-1 | `TestActivityCleanupWorker_StartStop` is a pure tautology that masked C-1 | testing | S | low |
| T-2 | Jetstream / Tap / labeler consumers have zero unit tests beyond pure event-parsing | testing | L | med |

### Medium (31)

Listed by source file (each entry's full detail is in the
per-lens doc); MUST-FIX-tonight subset triaged in
[`03-implementation-plan.md`](03-implementation-plan.md).

- **Security:** S-2 (notifications endpoint missing body/depth caps),
  S-3 (`ALLOWED_ORIGINS` unset silent), S-4 (oauth-handlers log
  raw `loginHint`), S-5 (`ADMIN_DIDS` env-var path skips `did.IsValid`).
- **Correctness:** C-5/6/7/8/9/10/11 (panic-recover gaps, TOCTOU on
  lexicon upload, slow-subscriber drop-only, populateActivityIfEmpty
  races shutdown, Tap dispatch stall, activity orphan accumulation,
  others — see file).
- **Reuse:** R-1/2/3/5/6/8 (concrete duplication, ~150 LOC
  consolidatable + ~80 LOC deletable).
- **Performance:** P-3 (4 sequential DB round-trips per ingest),
  P-4 (UpsertActor per-record), P-5 (`awardCount` doc fix), P-6
  (per-record string/JSON double-parse).
- **Quality:** Q-4 (half of `AuthMiddleware` dead), Q-5 (3 small
  dead helpers).
- **Testing:** T-3 (`BatchInsert` partial-failure), T-4
  (`buildFilterGroupRecursive` depth-cap defence-in-depth), T-5
  (Insert malformed-JSON / cancelled-context), T-6 (acknowledged
  tautologies), T-7 (consumer unit-test gap), T-8 (totalCount
  filter-ignoring regression), T-11 (UploadLexicons), T-15 (Tap
  consumer test gap).
- **Ops:** O-1 (RUNBOOK missing INVALID-CONCURRENTLY procedure).

### Low / nit (38)

Most of these are explicit deferrals — preferences, optimisations
behind concrete thresholds, things the operator should triage in
the morning rather than touch tonight.

## Cross-cutting themes

Three themes emerged across multiple lenses:

### Theme 1 — Activity-table system is broken end-to-end

- C-1 makes the cleanup worker silently dead.
- P-2 stores the full record JSON in `jetstream_activity` (duplicates
  what's in `record.json`), retained for 7 days, in a table whose
  cleanup never runs.
- C-15/16 (medium) note the orphan-accumulation window.
- T-1 is a tautology test that re-implements the worker instead of
  exercising production, masking C-1 from CI.

**Triage:** fix C-1 + T-1 tonight (small, safe, restores broken
behaviour). P-2 + C-15/16 ride along in the same PR if time
permits — they share a single coherent fix (do the activity
write AFTER the record insert succeeds, drop the duplicated
JSON body, restore the cleanup worker that then has something to
do).

### Theme 2 — OAuth subsystem carries ~400 LOC of dead production code

The OAuth package was scaffolded as a general-purpose OAuth server
but the actual deployment is "OAuth bridge to upstream PDS." The
server-shaped helpers were never deleted:

- Q-1 (`errors.go` 157 LOC), Q-2 (`token_generator.go` ~80% dead),
  Q-3 (`scopes.go` 137 LOC dead except one helper), Q-4 (half of
  `AuthMiddleware` public surface dead).

**Triage:** delete tonight — pure removal, no behaviour change,
significantly easier reading. Each deletion is one commit. Risk
gated on the "is this called by an external client?" check —
the certified-app + admin client repos may import some of these
symbols (the `OAuthError` constructors look like a public API
shape). Resolution: only delete things that have zero call sites
in any reachable repo AND no reason an external caller would
construct them (e.g. unexported helpers; functions whose names
imply request-side use in a flow the indexer doesn't run).

### Theme 3 — `totalCount` is a stealth correctness defect

P-1 is severity-high in the perf file but is really a
correctness bug: every filtered GraphQL query that requests
`totalCount` gets the unfiltered count. Consumers building
pagination UI ("page 1 of 47") are showing wrong numbers. The
P-5 doc fix on `awardCount` is the visible-but-small version of
this; P-1 is the invisible-but-large version.

**Triage:** fix tonight. The smallest correct fix is a filter-
aware `CountByCollectionFiltered(filter)` repo method that reuses
the existing WHERE-builder — same shape as the existing
filter-aware `GetByCollectionFiltered`. Add a regression test
(T-8) in the same commit.

## Frozen as of Phase 2 close

This document and the per-lens files do not change again. New
discoveries during Phase 4 implementation go in
`02b-late-findings.md`.
