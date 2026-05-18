# 00 — Orientation

Date: 2026-05-18.
Working branch: `staging` (clean, up to date with `origin/staging`,
which is at parity with `origin/main` after PR #92 merged).

## What this project does

Magic Indexer (binary `hypergoat`) is a Go AT Protocol AppView. It
subscribes to live record streams (Jetstream by default, the
crypto-verified Tap sidecar optionally), persists matching records
into Postgres keyed by lexicon, and exposes a dynamically-generated
GraphQL surface over that data. The schema is built at startup from
the lexicons it knows about (uploaded via an admin mutation or seeded
from `LEXICON_DIR`), so the GraphQL surface is a direct projection of
whatever AT Protocol record types the operator wants to track.

Two production-facing endpoints sit on the same Chi router: a public
`/graphql` (read-only over the indexed records) and an admin-gated
`/admin/graphql` (label management, lexicon upload, actor purges,
labeler enable/disable). The dev deployment runs at
`magic-indexer-dev.up.railway.app` on Railway with Postgres 18 as the
backing store.

## Architectural shape

Single-binary monolith with cleanly-separated internal packages.
Entry point at `cmd/hypergoat/main.go` (1372 LOC), structured as:
`run()` → `initServices()` → `setupRouter()` → `serve()`. `services`
is the dependency-injection bag (`repositories.*` + database
executor); `backgroundServices` tracks cancellable goroutines for
clean shutdown (Jetstream consumer, Tap consumer, labeler consumers,
OAuth cleanup, backfill).

### Data flow at runtime

1. **Ingestion.** `internal/jetstream` (or `internal/tap`) opens a
   websocket to the configured firehose, filters events to the
   collections in the registered lexicon set, and hands valid record
   bodies to `internal/ingestion` which calls `RecordsRepository.Insert`
   (idempotent on `uri+cid`). Labelers run as parallel consumers in
   `internal/labeler` writing to `label`/`label_definition` tables.
2. **Persistence.** Postgres via pgx; everything funnels through
   `internal/database` (the `Executor` interface) and per-aggregate
   repositories in `internal/database/repositories` (records, actors,
   lexicons, labels, label definitions, label preferences,
   jetstream activity, reports, OAuth state, config).
3. **Schema construction.** `internal/graphql/schema/builder.go` walks
   the lexicon registry at boot and synthesises per-collection record
   types + connection/where/filter inputs from the lexicon shape.
   Three registries live alongside the property-driven generation and
   layer on per-lexicon special-cases:
   - `filterRegistry` — lexicon-specific filter kinds with bespoke
     SQL (`KindArrayContributor`, `KindUnionSubject`, `KindStringSubject`).
   - `joinedWhereRegistry` — strong-ref nested `where` (#87).
   - `arrayWhereRegistry` — array-element nested `where` (#88).
   - `derivedFieldRegistry` — synthetic record-level fields with a
     Resolve func (#89: `awardCount`).
4. **Query execution.** `internal/graphql/handler.go` accepts the
   request, runs depth analysis (`internal/graphql/depth`), wraps the
   handler context with `repositories.WithRepositories`, calls
   graphql-go. Resolvers consult `internal/database/repositories`;
   filter-aware paths use the `FilterGroup`-aware
   `GetByCollectionFiltered`.
5. **Subscriptions.** `internal/graphql/subscription` fans out an
   in-process pubsub over a websocket (`/graphql/ws`). Ingestion
   pushes record events to subscribers.
6. **OAuth.** Confidential ATProto OAuth for the admin UI lives in
   `internal/oauth` (17 files, the heaviest non-DB subsystem):
   DID resolution + cache, DPoP binding + nonce/jti replay,
   service-auth JWTs, scope enforcement.
7. **Notifications.** `internal/notifications` watches record creates
   and posts envelope payloads to the certified-app's notification
   inbox via service-auth JWT against `/notifications/graphql`.

### Stack

- **Language:** Go 1.25. Standard library + ~20 direct deps. Pinned
  transitives are heavy (~150) but driven by `bluesky-social/indigo`
  and the IPLD/IPFS dependency tree.
- **Web:** `go-chi/chi/v5` router + standard `net/http`.
- **GraphQL:** `graphql-go/graphql` v0.8.1.
- **DB driver:** `jackc/pgx/v5` (raw pgx — no ORM). Postgres-only
  schema management via the `internal/database/migrations` runner.
- **Tests:** stdlib `testing` + `testutil.SetupTestDB` against a
  shared local Postgres in CI.
- **CI:** `.github/workflows/ci.yml`. Four jobs: `test`,
  `build-docker`, `reproducible-build`, `lint` (`golangci-lint`).
  All four are required for PR merge.

## Constraints and idioms (what the code is betting on)

- **Lexicon-driven everything.** Almost no per-lexicon code in the
  generic paths — the lexicon shape produces the schema. The
  per-lexicon special-cases live in narrow registries (above).
  Adding a new lexicon is typically zero-code (just an admin upload).
- **One Postgres connection pool, two timeout layers.** Pool-level
  `statement_timeout` (`DB_STATEMENT_TIMEOUT_MS`, default 30s) +
  per-request middleware deadline on `/graphql` only
  (`GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS`, default 5s). `Validate()`
  enforces the inequality at startup.
- **Records are atomic JSONB blobs.** The `record` table has
  `(uri PK, cid, did, collection, json, indexed_at, rkey)` plus a
  handful of generated columns / partial expression indexes for the
  per-lexicon filter hot paths (subject_did on badge.award,
  follow.subject, badge.uri on award). Anything not directly indexed
  goes through `json->...` paths.
- **Filter abstraction is in code, not SQL strings.** A
  `FilterGroup` (with `Filters`, `Children`, `Joined`, `Arrays`)
  produced by the schema extractor is converted to parameterised SQL
  by `buildFilterGroupRecursive` with alias plumbing (`r` outer,
  `d` joined, `e` array element). The locked-kind sentinel rejects
  lexicon-specific kinds in non-`r` scopes.
- **Atomic commits, deep flow, mandatory `Co-Authored-By:` trailer.**
  AGENTS.md spells out the four quality gates (build/vet/test
  -race/lint) required before any commit. CI rejects on any one.
- **Registry-first for new GraphQL surfaces.** The pattern
  established in #87 (`joinedWhereRegistry`) was reused in #88
  (`arrayWhereRegistry`) and #89 (`derivedFieldRegistry`) — each
  adds a parallel registry, a pinned description, a builder hook,
  and an extractor branch. Plan reviewers explicitly weigh new
  features against this pattern.
- **Behavioral-test catalogue (`docs/behavioral-tests.md`) is the
  named contract for end-to-end probes.** Twelve entries (A1, A3,
  B*, C1, D*, E1–E12, F1) — when verifying a feature works
  end-to-end, the existing entries take precedence over inventing a
  new probe; new behaviour adds a new entry.

## Surface area inventory

- **Code (non-test):** 56,009 LOC across 198 Go files in
  `internal/` + 1,541 LOC in `cmd/hypergoat/`.
- **Tests:** 82 `_test.go` files.
- **Migrations:** 60 files (30 up + 30 down) in
  `internal/database/migrations/postgres/`. Latest: 030, added
  earlier today for #89.
- **Largest packages by LOC (non-test):**
  - `internal/database` — 23 files / 6,682 LOC (repositories +
    executor + migrations runner).
  - `internal/graphql` — 22 files / 8,573 LOC (handler, schema
    builder, query/connection plumbing, subscription).
  - `internal/oauth` — 17 files / 3,410 LOC (DPoP, service-auth,
    DID resolution).
  - `internal/server` — 11 files / 2,518 LOC (CORS, security
    headers, OAuth handlers, notifications XRPC bridge).
  - `internal/lexicon` — 6 files / 1,724 LOC.
  - `internal/backfill` — 2 files / 1,378 LOC.
- **Largest single files:**
  - `internal/graphql/schema/builder.go` — 1,166 LOC.
  - `internal/database/repositories/records.go` — 1,177 LOC.
  - `internal/database/repositories/filter.go` — 1,084 LOC.
  - `internal/database/repositories/records_filter_test.go` —
    1,971 LOC.
  - `internal/database/repositories/filter_unit_test.go` — 1,684 LOC.
  - `cmd/hypergoat/main.go` — 1,372 LOC.
- **Test coverage signal:** every package with non-trivial logic has
  a test file. No `coverage.out` checked in. Per-PR CI runs
  `go test -race ./...` against a managed Postgres service — the
  coverage signal is "tests pass" rather than a percentage.

## What I cannot determine without the operator

- **What dimensions of correctness matter most right now.** The repo
  has been through 23 review rounds (per AGENTS.md). The night's
  highest-leverage work depends on whether the operator's near-term
  focus is "harden the GraphQL surface" vs. "speed up ingestion" vs.
  "shore up OAuth" vs. "general refactoring." I will pick lenses in
  Phase 1 based on what the code itself surfaces; flagging this as
  the most likely place where my judgement could diverge from theirs.
- **Whether the 10 open GitHub issues are sleeping (not tonight's
  scope) or hot.** I'll treat them as sleeping by default — only
  touching one if it surfaces from the diagnostic pass as
  load-bearing for a bigger finding.
- **Whether the operator wants me to consolidate the registry
  patterns (#87/#88/#89) into a generic abstraction.** Plan §9.4
  in each issue explicitly defers this until a 2nd-or-3rd entry
  forces the question. I will NOT touch this tonight — it's the
  exact "introducing a new pattern" the directive warns against.
- **Whether the docs under `docs/` should be culled / reorganised.**
  18 sub-directories, ~12 of which are issue-specific archives. Some
  ARE consumer-facing (RUNBOOK, behavioral-tests, SECURITY); most
  are an archival audit trail. Will not touch tonight without a
  concrete win.

## Operating context (for the morning)

- Active branch on remote: `staging` = `origin/main` = `9f920dc`.
- PR #92 (issue #89) merged ~30 min ago; Railway should be mid-
  autodeploy of the awardCount field. A background poll
  (`task bwknhe3rh`) is watching for the new field to land on dev.
- Three "issue-issue" PRs (#90 #91 #92) have shipped this session,
  each via the deep-flow process. Findings, plans, and review docs
  archived under `docs/issue-87/`, `docs/issue-88/`, `docs/issue-89/`.
