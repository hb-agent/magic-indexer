# AGENTS.md — Magic Indexer (single-source onboarding)

> **If you are an AI assistant or a new contributor opening this
> repo for the first time, this file is your complete onboarding.
> Reading just this file should be enough to be useful. The other
> docs (`docs/RUNBOOK.md`, `docs/reviews/`, `SECURITY.md`,
> `README.md`) are deeper references; this file is the digest.**

If, after reading this file, you are unsure about anything that
isn't covered here, that is a documentation bug — flag it to the
operator and they will tighten this file rather than re-explain.

---

## What this is

**Magic Indexer** is an AT Protocol AppView server that ingests
records from Jetstream + labels from ATProto labelers and exposes
both via a dynamically-generated GraphQL API.

It is the `hb-agent/magic-indexer` fork of the
`hypercerts-org/hyperindex` project. The compiled binary inside
the container is named `hypergoat`, and the Go module path is
`github.com/GainForest/hypergoat`. Both names are historical
artefacts from when the project was originally called Hypergoat.
Every command in this repo that mentions `hypergoat` or
`./cmd/hypergoat` is referring to that binary path — not a
different product. **Do not rename the module or the binary**;
it would touch ~80 files for a brand-only change.

The product name in user-facing documentation, configuration,
deployments, and conversation is **Magic Indexer**.

---

## Live deployment (the dev environment)

| Item                     | Value                                                                  |
|--------------------------|------------------------------------------------------------------------|
| Public URL               | `https://magic-indexer-dev.up.railway.app`                              |
| Public GraphQL           | `https://magic-indexer-dev.up.railway.app/graphql`                      |
| GraphQL subscriptions    | `wss://magic-indexer-dev.up.railway.app/graphql/ws`                     |
| Admin GraphQL            | `https://magic-indexer-dev.up.railway.app/admin/graphql`                |
| GraphiQL playground      | `https://magic-indexer-dev.up.railway.app/graphiql`                     |
| GraphiQL admin           | `https://magic-indexer-dev.up.railway.app/graphiql/admin`               |
| Health                   | `https://magic-indexer-dev.up.railway.app/health`                       |
| Stats                    | `https://magic-indexer-dev.up.railway.app/stats`                        |
| Prometheus metrics       | `https://magic-indexer-dev.up.railway.app/metrics`                      |
| Railway project ID       | `7d6c4e52-de61-439f-96c0-3ded4114b9be`                                  |
| Railway project name     | `magic-index`                                                           |
| Railway environment      | `dev`                                                                   |
| Railway service          | `magic-indexer`                                                         |
| Railway dashboard        | `https://railway.com/project/7d6c4e52-de61-439f-96c0-3ded4114b9be`      |
| GitHub repo              | `https://github.com/hb-agent/magic-indexer`                             |
| Active branch            | `per-labeler-definitions`                                               |
| Backing database         | Postgres 18, Railway-managed, in the same project                       |
| Admin UI                 | `https://magic-indexer-admin.vercel.app` (Next.js, confidential ATProto OAuth) |
| Currently ingesting from | Jetstream (24 lexicon-derived collections, including `app.certified.temp.graph.endorsement`) |

The 24 collections currently being ingested all start with one
of three NSID prefixes: `org.hypercerts.*`, `app.certified.*`,
`org.hyperboards.*`. The `app.certified.temp.graph.endorsement`
lexicon supports the trusted-evaluator feed filter
(see [`docs/architecture/0001-trusted-evaluator-feed-filter.md`](docs/architecture/0001-trusted-evaluator-feed-filter.md)). Lexicons are uploaded via the admin API
from the npm package `@hypercerts-org/lexicon` (see Operations
below).

### Full-text search

All typed collection queries and the generic `records` query support
a `search: String` parameter for full-text search across record
content. The search uses Postgres `tsvector` with a GIN index for
fast, stemmed queries.

**Searched fields** (weighted): title (A), shortDescription (B),
description (C), workScope (D, string variant only).

**Behavior**: terms are space-separated and implicitly ANDed.
English stemming is applied ("forest" matches "forests"). Special
characters are stripped by `plainto_tsquery` — no injection risk.
Max query length: 500 characters.

**Example**:
```graphql
{ orgHypercertsClaimActivity(search: "forest conservation", first: 10, authors: ["did:plc:..."]) {
    edges { node { uri title shortDescription } }
    pageInfo { hasNextPage }
} }
```

**Combinable with**: `authors`, `labels`, `excludeLabels`, `labelerDids`.

### Record validation

Records are validated against their lexicon schemas at two points:

- **Ingestion time** (Jetstream + backfill): controlled by
  `VALIDATION_MODE` env var (`disabled`/`warn`/`enforce`, default
  `disabled`). In `warn` mode, invalid records are logged but stored.
  In `enforce` mode, they are skipped.
- **Query time** (always on): `SanitizeRecord()` filters out records
  missing required fields, truncates over-long strings, and nulls
  invalid optional fields. This prevents NonNull propagation from
  killing entire query responses.

---

## Safety rules — read these first

These are non-negotiable. Apply before doing anything that
touches state.

1. **Never commit secrets.** `SECRET_KEY_BASE`, `ADMIN_API_KEY`,
   the Railway API token, OAuth signing keys, and `.env` files
   stay out of git. The repo's `.gitignore` excludes `.env` and
   `.env.local`. `config.Validate()` at startup rejects the
   literal `development-secret-key-change-in-production-64chars`
   placeholder so a misconfigured deploy fails fast instead of
   booting with a public key.

2. **Never echo secrets in unredacted form** in chat output, log
   files, or anything that could be persisted. When the operator
   pastes a secret to you, store it in `/tmp/...` with `chmod
   600` and reference the variable, don't reproduce the value.

3. **Confirm with the operator before destructive actions.** This
   includes (but isn't limited to): `railway down`, deleting
   Railway services, dropping the Postgres volume, force-pushing
   to `per-labeler-definitions`, deleting GitHub branches, mass
   issue closure, `git reset --hard`, `git rebase -i`,
   `gh repo delete`, dropping or truncating any database table,
   `railway redeploy` against a service whose latest commit you
   haven't built locally, rotating any secret without first
   confirming the operator has the new value, and any operation
   that affects the upstream `hypercerts-org/hyperindex` repo
   (this is a fork — your default scope is `hb-agent/magic-indexer`).

4. **Read-then-act, not act-then-explain.** When investigating a
   problem, read the relevant code with file:line evidence
   before proposing changes. Roughly half the "CRITICAL" findings
   in the 23 review rounds were false positives that disappeared
   after looking at the actual lines cited.

5. **Quality gates before commit.** No code change is "done"
   until all four pass:
   ```bash
   go build ./...
   go vet ./...
   go test -race ./...
   golangci-lint run ./...
   ```
   CI also runs Postgres tests via `TEST_DATABASE_URL` and a
   reproducible-build diff job. Both should stay green.

6. **Commit message convention.** Each commit ends with a
   `Co-Authored-By:` trailer naming the model/agent that wrote
   it. The repo's recent history follows this; match the style.

7. **`git push` is not optional.** A change you didn't push is
   work that doesn't survive the session. The "Landing the
   plane" checklist at the bottom of this file is mandatory.

---

## Browser automation in this dev container

This dev container has a working **agent-browser** install that
controls a real headless Chromium. Use it when you need to verify
something behaves correctly *in a real browser* — not just in
SSR HTML, not just in a curl probe. Examples: client-side
hydration errors, CORS rejections, React error boundaries,
post-hydration data fetches, or "does the user actually see X
on the page".

The two pieces that had to come together for this to work:

- **`agent-browser` CLI** (npm package, native Rust): installed
  globally via `npm install -g agent-browser`. Version `0.25.3`
  or later.
- **Chromium binary**: this dev container is Linux ARM64.
  Chrome for Testing has no ARM64 builds, so we use the
  Chromium that ships with Playwright instead. Install with
  `npx --yes playwright@latest install chromium --with-deps`.
  Lands at `~/.cache/ms-playwright/chromium-1217/chrome-linux/chrome`.
- **Wrapper script** at `~/.local/bin/ab` that always passes
  `--executable-path` pointing at the Playwright Chromium so
  you never have to remember it. **Use `ab` instead of
  `agent-browser` for everything in this repo.**

If `ab --version` doesn't work in a fresh session, both the
npm install and the Playwright Chromium download will need to
be re-run, then drop the wrapper back in:

```bash
npm install -g agent-browser
npx --yes playwright@latest install chromium --with-deps
mkdir -p ~/.local/bin
cat > ~/.local/bin/ab <<'EOF'
#!/usr/bin/env bash
exec agent-browser --executable-path "$HOME/.cache/ms-playwright/chromium-1217/chrome-linux/chrome" "$@"
EOF
chmod +x ~/.local/bin/ab
```

The chromium directory name (`chromium-1217`) is the Playwright
revision number and may differ on a fresh install. Update the
wrapper if needed.

### Common usage

```bash
ab open https://magic-indexer-dev.up.railway.app/graphiql
ab snapshot                       # accessibility tree with refs (best for AI)
ab screenshot /tmp/page.png       # raster image
ab eval '<javascript>'            # run JS in the page context
ab click @e10                     # click element by ref from snapshot
ab fill @e3 "search term"         # fill an input
ab close                          # close session
```

### What it caught last session

The integration test of `certs-social → magic-indexer` produced
the right SSR HTML, the right Vercel build, the right TypeScript,
and a passing `npm run build` — but the live page in a real
browser showed `Something went wrong / Failed to fetch` because
the magic-indexer CORS allowlist didn't include the Vercel
preview URL. Caught only because `ab open` + `ab snapshot`
exposed the post-hydration error state. None of the static
checks would have found it.

### What it can't do

`ab` controls a headless browser. It can't:

- Watch you click around interactively (use your own browser).
- Step through React DevTools.
- Show you the same console output you'd see in Chrome DevTools
  in detail (use `ab eval 'console errors are evaluated as JS'`
  workarounds, or open the page in your own browser for
  interactive debugging).

For deep interactive debugging, your local browser is still
the right tool. `ab` is for "verify the live deployment renders
correctly without me having to open a browser tab."

## Self-test for a fresh session

After you've read this file, test your understanding by
mentally answering these. If you can answer all of them without
re-reading, you're oriented:

1. What is the project called, and what does it do in one sentence?
2. Where is it deployed, on what platform, with what backing store?
3. What's currently in the database (records, actors, lexicons,
   labelers)?
4. What's the active branch and the last commit on it?
5. Which two issues are deliberately deferred and why?
6. What two facts about lexicons do you have to remember when
   uploading them?
7. What is the *one* command an operator runs to deploy a code
   change?
8. What's the difference between `RAILWAY_TOKEN` and
   `RAILWAY_API_TOKEN`?
9. What's the rule about the `VOLUME` keyword in `Dockerfile`?
10. Why is `OptionalAuth` middleware permissive on bad bearer
    tokens, and why is that not a security hole?

Answers are scattered through the rest of this file. If any of
the questions don't have a clear answer here, that's a
documentation bug — say so.

---

## Build, test, lint (local development)

From a clean checkout:

```bash
git clone https://github.com/hb-agent/magic-indexer.git
cd magic-indexer
git checkout per-labeler-definitions
make setup           # generates .env with a fresh SECRET_KEY_BASE
go run ./cmd/hypergoat
```

Quality gates that must pass before any commit:

```bash
go build ./...                   # also: make build
go vet ./...
go test ./...                    # also: make test (adds -race)
go test -race ./...
golangci-lint run ./...          # also: make lint
```

Single test patterns:

```bash
go test -v -run TestParseLexicon ./internal/lexicon/...
go test -v ./internal/graphql/admin/...
```

Coverage report:

```bash
make test-coverage               # writes coverage.html
```

To run the integration test suite (build tag `integration`):

```bash
go test -tags=integration ./internal/integration/...
```

CI runs all of the above on every push to `main` and every PR
targeting `main`, against both SQLite and Postgres (`TEST_DATABASE_URL`),
plus a reproducible-build diff job. See `.github/workflows/ci.yml`.

---

## Code style

### Imports
Three groups, blank lines between them, in this order:

```go
import (
    "context"           // 1. Standard library
    "fmt"

    "github.com/go-chi/chi/v5"  // 2. External packages

    "github.com/GainForest/hypergoat/internal/database"  // 3. Internal packages
)
```

### Package documentation
Every package has a doc comment on `package`:

```go
// Package config handles configuration loading from environment variables.
package config
```

### Naming
- Packages: lowercase, single word (`lexicon`, `oauth`, `backfill`).
- Files: lowercase with underscores (`did_resolver.go`).
- Types: PascalCase (`Executor`, `RecordFetcher`).
- Interfaces: noun or `-er` suffix (`Executor`, `Fetcher`).
- Acronyms: all caps (`URI`, `DID`, `HTTP`, `JSON`).

### Errors
Always wrap with context, prefer `%w`:

```go
if err != nil {
    return fmt.Errorf("failed to query records: %w", err)
}
```

For OAuth-style validation errors prefer package-level sentinel
vars (see `internal/oauth/dpop.go` for the canonical pattern
that came out of review Round 8).

### Context
Always pass `ctx` as the first parameter to any I/O method:

```go
func (r *RecordsRepository) GetByURI(ctx context.Context, uri string) (*Record, error)
```

### Repository pattern
Database access lives in `internal/database/repositories/`.
Constructors take a `database.Executor`. SQL is built with
`r.db.Placeholder(n)` for dialect-aware parameters. Every method
takes `ctx`. Don't reach into the executor from outside the
repositories layer.

### Logging
Use `log/slog` everywhere. Structured fields, never string
interpolation:

```go
slog.Info("Starting backfill", "collections", collections, "count", len(repos))
slog.Warn("Failed to resolve DID", "did", did, "error", err)
slog.Error("Database connection failed", "error", err)
```

The mutation log line in the admin handler logs **variable
keys, not values** — never reintroduce value logging without an
audit, it's a log-injection vector that Round 3 caught and
fixed.

### Testing
Table-driven tests, fresh setup per test, no shared state:

```go
func TestSomething(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        wantErr bool
    }{
        {"happy path", "valid", false},
        {"bad input",  "junk",  true},
    }
    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            _, err := DoThing(tc.input)
            if (err != nil) != tc.wantErr {
                t.Errorf("err = %v, wantErr %v", err, tc.wantErr)
            }
        })
    }
}
```

For DB tests, use `testutil.SetupTestDB(t)` which honours
`TEST_DATABASE_URL` and falls back to in-memory SQLite.

---

## Project structure

```
cmd/hypergoat/          # Main entry point (server init, routing, lifecycle)
internal/
  backfill/             # Historical record backfill from AT Protocol relays
  config/               # Configuration loading from environment + Validate()
  consumer/             # Shared reconnection backoff (RunWithReconnect) used by jetstream, labeler, tap
  cursor/               # Shared cursor persistence (atomic.Int64 Flusher) used by all consumers
  database/
    migrations/         # SQL migrations (auto-run on startup, transactional + "-- no-transaction" sentinel for CONCURRENTLY)
    repositories/       # Data access layer (records, labels, label_definitions, filter.go for field filters, etc.)
    sqlite/             # SQLite implementation (pure Go, modernc) — note: removed from production, Postgres-only
    postgres/           # PostgreSQL implementation (pgx)
  graphql/
    admin/              # Admin API (POST-only, bearer-or-OAuth gated) — createFieldIndex, dropFieldIndex, uploadLexicons, etc.
    schema/             # Public schema builder (dynamic from lexicons) + where.go for field filter extraction
    resolver/           # Public resolver wiring + repository context injection
    query/              # Connection types (Relay spec) + ClampPageSize + SortDirectionEnum
    depth/              # Pre-execution GraphQL query depth guard (max 15 / 20)
    subscription/       # WebSocket subscriptions (graphql-transport-ws)
    types/              # GraphQL type mapping from lexicon definitions + filters.go (per-type FilterInput types)
  ingestion/            # Shared RecordProcessor: ensure-actor → insert/delete → log-activity → publish-to-pubsub
  integration/          # Integration tests (build tag: integration)
  jetstream/            # Real-time AT Protocol event consumer (delegates to ingestion.RecordProcessor)
  labeler/              # ATProto labeler subscribeLabels + queryLabels client
  lexicon/              # Lexicon parsing, registry, NSID utilities
  metrics/              # Prometheus counters + /metrics HTTP handler
  notifications/        # Bluesky-pattern notifications: per-collection extractors, aggregation, seen watermark
  notifications/extractors/  # Per-collection notifier implementations (endorsement, activity-contributor)
  oauth/                # OAuth 2.0 + DPoP + PKCE + did:plc / did:web resolution
  server/               # HTTP handlers, security headers, CORS, GraphiQL UI
  tap/                  # Tap sidecar consumer (crypto-verified events, ack-based delivery) — alternative to Jetstream via TAP_ENABLED
  workers/              # Background jobs (activity cleanup + orphan janitor, etc.)
docs/                   # RUNBOOK + reviews + plans
scripts/                # Deployment helpers (setup-env.sh)
testdata/               # Test fixtures and sample lexicons
```

---

## Subsystem highlights (the things that bit us in review)

### Public GraphQL `authors` filter
Typed collection queries accept an `authors: [String!]` argument
to filter by author DID. Cap: 500 DIDs per query. An empty list
means "no filter" (returns all authors). Example:
`orgHypercertsClaimActivity(first: 10, authors: ["did:plc:..."])`.

### Field filter system (`internal/database/repositories/filter.go` + `internal/graphql/schema/where.go`)
Typed collection queries also accept a `where` argument with per-field
operators generated from the lexicon's scalar properties:

- Operators: `eq`, `neq`, `gt`, `lt`, `gte`, `lte`, `in`, `contains`, `startsWith`, `isNull`.
- `eq` uses `json @> $::jsonb` containment (hits the GIN `jsonb_path_ops` index).
  Other operators use `json->>'field'` extraction (seq scan unless an expression
  index exists — see admin mutations below).
- `neq` semantically means "not equal OR field absent" (includes NULLs).
- `contains` min 3 chars; `startsWith` min 1 char. Both escape `\`, `%`, `_` via `ESCAPE '\\'`.
- `in` uses `= ANY($::text[])` — single array param instead of expanded `IN (...)`.
- Nested paths via `__` separator (e.g., `metadata__source` →
  `json->'metadata'->>'source'`). Max 3 nesting levels. Auto-generating nested
  WhereInput fields from lexicons is deferred (issue #40); SQL layer supports it.

Composition via `_and` / `_or` fields on WhereInput (recursive, self-referential
via `graphql-go` `AddFieldConfig`):
- `FilterGroup` tree with `GroupAND`/`GroupOR` operators.
- `BuildFilterGroupClause` is the recursive SQL builder; proper parenthesization;
  global condition count capped at `MaxFilterConditions` (20) across the whole
  tree; max depth `MaxFilterDepth` (3).
- Field name validation (`[a-zA-Z_][a-zA-Z0-9_]*` per segment) runs before any
  string interpolation into SQL — this is defense-in-depth; names come from the
  lexicon registry, not user input.

### Sort-aware keyset pagination
`orderBy` (string, field name) and `orderDirection` (ASC/DESC, default DESC)
arguments on typed collection queries. The repository layer now honors these:
the `ORDER BY` clause uses `SortOption.BuildSortExpr()` and the keyset cursor
comparison uses the sort expression (previously always `indexed_at`).

- Direct columns (`indexed_at`, `uri`, `did`, `collection`, `cid`, `rkey`) use
  the column name; anything else becomes `json->>'field'` (with nested path
  support via `__`).
- `NULLS LAST` in ORDER BY for both ASC and DESC.
- URI tiebreaker appended in the same direction.
- **Fast-path guard**: when no filters/labels/search apply, the function
  delegates to `GetByCollectionWithKeysetCursor` which always sorts by
  `indexed_at DESC`. The `hasCustomSort` check (PR #50) prevents that path
  from silently ignoring a custom `orderBy` on an unfiltered query.
- Multi-column sort (orderBy as list) is deferred (issue #39).

### Cursor format (V2)
Cursors are base64-URL-encoded JSON arrays:
`["sortField", "sortValue", "uri"]`. The decoder also accepts the legacy
pipe-delimited format (`"timestamp|uri"`) for backward compatibility; legacy
cursors only work when `orderBy` is `indexed_at` (default). Sort-field
mismatch produces a clear error.

### Backward pagination
`last` + `before` arguments complement `first` + `after`. Mixed forward +
backward is rejected. Implementation: flip the sort direction + cursor
comparison, fetch `last+1`, reverse the slice in memory. `hasPreviousPage`
is true when we fetched more than `last`; `hasNextPage` for backward mode
reflects whether items exist after the returned window.

### Admin expression index mutations
`createFieldIndex(collection, field)` and `dropFieldIndex(collection, field)`
on the admin GraphQL API. Generates:
`CREATE INDEX CONCURRENTLY ON record ((json->>'field')) WHERE collection = 'nsid'`.
Partial index (filtered by collection) keeps size small. Runs outside a
transaction via the migration runner's `-- no-transaction` sentinel convention.
Use this to accelerate comparison/pattern filters that the GIN index can't serve.

### Shared consumer infrastructure (`internal/ingestion`, `internal/cursor`, `internal/consumer`)
Extracted from the original inline Jetstream consumer during the hyperindex port:

- `ingestion.RecordProcessor` — ensure-actor → insert/delete → log-activity → publish-to-pubsub. Used by both Jetstream and Tap consumers. Enforces an optional collection allowlist and rejects non-object JSON records.
- `cursor.Flusher` — `atomic.Int64` cursor value + ticker-based flush, skip-on-idle. Survives context cancellation via a bounded final flush.
- `consumer.RunWithReconnect` — exponential backoff (1s → 2min, reset after 30s of stable connection).

### Tap consumer (`internal/tap/`)
Alternative to Jetstream when `TAP_ENABLED=true`. Consumes crypto-verified
events from the Bluesky Tap sidecar with ack-based delivery and per-repo
ordering. Synchronous dispatch (backpressure via the WebSocket itself is the
correct signal for ack-based protocols). Panic-recovered, exponential retry
(1s/2s/4s) per event, then skip. `Connection` / `Dialer` interfaces abstract
gorilla/websocket for testability. Trust boundary: Tap verifies MST inclusion
proofs but not signing key vs DID document (#41 deferred).

### Notifications subsystem (`internal/notifications/`)
Bluesky-pattern notification system. Enabled via `NOTIFICATIONS_ENABLED=true`.

- **Data model**: `notification` (envelope, one row per displayed notification,
  optionally aggregated by `group_key`), `notification_participant` (one row
  per source record that contributed — unique on `(record_uri, recipient_did)`
  for idempotent replay and correct tombstone cascade), `actor_state`
  (per-user seen watermark, same as Bluesky).
- **Hook**: registered as a `RecordHook` on `RecordProcessor.RecordHooks`,
  policy `HookLogContinue`. A malformed record cannot stall firehose ingestion —
  hook errors are logged but don't abort the record insert. Panic-recovered
  per invocation. Runs on insert/update/delete.
- **Extractors** (`internal/notifications/extractors/`): one Go file per
  collection. Currently: `endorsement` (aggregates on subject URI) and
  `activity-contributor` (non-aggregating, fans out per contributor DID up to
  `MaxFanOutPerRecord=100`).
- **Idempotency**: the participant table's UNIQUE `(record_uri, recipient_did)`
  is the replay boundary. Re-processing the same record is a no-op.
- **Tombstone cascade**: record delete → `DeleteByRecordURI` removes
  participants, decrements envelope count, deletes the envelope at count 0,
  recomputes `latest_*` from remaining participants when the removed
  participant was the latest.
- **Update path**: delete-then-re-extract, to handle activity contributor
  list changes correctly.
- **Defense-in-depth**: `isValidDID` syntactic validation, `clampSortAt` bounds
  timestamps to `[now-7d, now]`, `MaxReasonSubjectBytes` caps subject URIs,
  `MaxContributorsBeforeReject` short-circuits oversized records via a shallow
  JSON scan before full unmarshal.
- **GraphQL (admin endpoint)**: `notifications(did, reasons, first, after)`,
  `unreadNotificationCount(did)` (capped at 50+), `updateNotificationsSeen(did, seenAt)`.
  Fields are merged into the admin schema via `admin.WithExtraQueries` and
  `admin.WithExtraMutations` options — no cyclic import between packages.
- **Cursor V1** for notifications: base64-URL JSON `["v1:notif", sort_at_iso, id]`.
- **Trust boundary**: public `/graphql` is unauthenticated, so notifications
  live on the admin endpoint and accept `did` as an argument. The certs-social
  proxy is the trust boundary (resolves session DID and forwards it). Public-
  endpoint migration is deferred until OAuth auth lands on `/graphql`.

### Labeler subsystem (`internal/labeler/`)
Mirrors `internal/jetstream/` but speaks the ATProto labeler
protocol:

- `client.go` — websocket client for `com.atproto.label.subscribeLabels`.
  Uses `fxamacker/cbor/v2` for the two-CBOR-object frame format
  (`#labels`, `#info`, `#error`). `SetReadLimit` bounds frame
  size. Non-normal close codes are surfaced at Warn; empty-body
  `#labels` frames are dropped explicitly; `#info` decode failures
  are elevated to Warn so `OutdatedCursor` signals cannot be
  silently lost.
- `backfill.go` — one-time `com.atproto.label.queryLabels`
  paginated backfill via `hashicorp/go-retryablehttp`.
- `consumer.go` — lifecycle: load cursor → backfill if needed →
  connect → stream labels → flush cursor on a ticker. Exponential
  backoff on reconnect. Panic-recovered at the goroutine boundary
  in `cmd/hypergoat/main.go` so one labeler cannot take down the
  process. Logs cursor gaps at Warn.

Label definitions are auto-upserted via `INSERT ... ON CONFLICT
DO NOTHING` keyed on the composite `(src, val)` PK from migration
009 — concurrent labelers cannot race a new `(src, val)` pair.

### Jetstream consumer (`internal/jetstream/`)
- `client.go` — websocket client for the Jetstream firehose.
  `SetReadLimit(8 MiB)` bounds per-frame memory.
- `consumer.go` — lifecycle + reconnect loop. The cursor is
  persisted to the `config` table every 5 s by default. Critical
  invariant from Round 14: `c.cursorDone`, `c.config`, and
  `c.ctxCancel` must all be mutated under `clientMu`; the
  `Start()` reconnect loop and `UpdateCollections()` both take
  the lock around their state writes.
- The lexicon change callback dynamically restarts the Jetstream
  consumer with a fresh `wantedCollections` list whenever
  lexicons are uploaded via the admin API. No process restart
  needed.

### Security headers middleware (`internal/server/security_headers.go`)
Emits `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`,
`Referrer-Policy: no-referrer`, and conditionally
`Strict-Transport-Security` (only when `EXTERNAL_BASE_URL` is
`https://`). `/graphiql` sets its own `Content-Security-Policy`
allowing the unpkg CDN for bootstrap assets; JSON API endpoints
keep the tighter default.

### `OptionalAuth` middleware (`internal/oauth/middleware.go`)
**Important contract**: when the `Authorization` header is
present but fails OAuth token validation, `OptionalAuth` passes
through with no user context — it does **not** return 401. This
is required because `/admin/graphql` is mounted with
`OptionalAuth` and the admin handler accepts two auth schemes:
a validated OAuth user, and an `ADMIN_API_KEY` bearer token.
Returning 401 in the middleware on a non-OAuth bearer (like the
admin API key) would prevent the API-key path from ever being
reached. The admin handler does its own 401 check on empty
userDID + no API key, so security posture is unchanged.
Round 8 introduced this behaviour live during the first deploy
when admin auth via `ADMIN_API_KEY` returned `invalid_token`.

### Activity log empty-event-json normalisation (`internal/database/repositories/jetstream_activity.go`)
The Jetstream consumer passes `string(commit.Record)` into
`LogActivity`. For delete operations `commit.Record` is nil and
the result is `""`. Postgres `JSONB NOT NULL` rejects empty
strings; SQLite stores them loosely as TEXT. The repository
normalises empty / whitespace-only payloads to the JSON literal
`null` so both dialects accept the row. Discovered live during
the Railway deploy.

### `config.Validate()` (`internal/config/config.go`)
- Refuses to start if `SECRET_KEY_BASE` is shorter than 64 bytes
  or matches the literal `development-secret-key-change-in-production-64chars`
  placeholder.
- Refuses to start on out-of-range `PORT`.
- Logs a Warn (not silent fallback) when `getEnvInt` is given a
  malformed integer value.

### Migrations (`internal/database/migrations/`)
- Each migration's `UpSQL` and the `schema_migrations` insert
  run inside a single transaction (`applyMigrationTx`). A crash
  in the middle leaves both rolled back.
- `Rollback` follows the same pattern.
- Migrations 001–009 are present in both `sqlite/` and `postgres/`
  variants and are tested for round-trip equivalence.

### Repositories that touch labels
- `LabelsRepository` — `Insert` / `InsertNegation` use
  `ON CONFLICT DO NOTHING` keyed on a partial unique index per
  migration 007. Active-set queries (`GetByURIs`, `HasTakedown`,
  `GetTakedownURIs`, plus the records label-filter subquery) all
  filter expired labels via `(l.exp IS NULL OR l.exp > nowLiteral())`.
- `LabelDefinitionsRepository` — composite `(src, val)` primary
  key from migration 009 so two labelers can both define
  `high-quality` with different semantics.
- `OAuthDPoPJTIRepository.InsertIfNew` — atomic `INSERT ... ON
  CONFLICT DO NOTHING` for race-safe DPoP replay detection.

---

## Deploying — the short version

The full deploy playbook (first-time provisioning, lexicon
upload, secret rotation, common gotchas) is in
[`docs/RUNBOOK.md`](docs/RUNBOOK.md). Read that **before**
touching the live environment for anything beyond a routine
code redeploy.

### Routine code deploy

```bash
cd /path/to/magic-indexer
git checkout per-labeler-definitions
git pull
export RAILWAY_API_TOKEN='<from-password-manager>'
railway up --service magic-indexer --detach
railway logs --service magic-indexer --deployment --lines 100
```

### Watch a deploy

```bash
railway logs --service magic-indexer --build         # build phase
railway logs --service magic-indexer --deployment    # runtime
```

### Railway gotchas (the things that broke our first deploys)

- **`VOLUME` is banned in Dockerfiles.** Railway rejects any
  `VOLUME` instruction. Don't reintroduce it. Use Railway's
  native volume mechanism via the dashboard if you need
  persistent storage.
- **Use `RAILWAY_API_TOKEN` for account-scoped tokens, not
  `RAILWAY_TOKEN`.** `RAILWAY_TOKEN` is for project-scoped
  tokens. Whoami fails silently with the wrong variable.
- **`railway add --database postgres` shows a prompt that looks
  like a hang but the service is created anyway.** Don't double-
  run; you'll get duplicate Postgres services. If you do, delete
  the duplicate via the GraphQL API: `mutation { serviceDelete(id: "<dup-id>") }`.
- **`railway variables --set NAME=` (empty value) is rejected by
  the CLI.** To clear a variable, use the GraphQL API:
  `mutation { variableUpsert(input: { ..., value: "" }) }`.
- **HSTS only emits when `EXTERNAL_BASE_URL` starts with
  `https://`**. A deployed instance with `http://` in that env
  var will not send HSTS. By design.
- **Railway auto-discovers + exposes `${{Postgres.DATABASE_URL}}`
  variable references** at runtime; use that form, not the raw
  resolved URL, so a Postgres credential rotation propagates
  automatically.

---

## Operations — the short version

The full operator playbook is in [`docs/RUNBOOK.md`](docs/RUNBOOK.md).
Two essential rules to remember:

### Lexicons come from npm, never from main

The canonical source for hypercerts/certified/hyperboards
lexicons is the npm package `@hypercerts-org/lexicon`. **Do not
read from the upstream `hypercerts-org/hypercerts-lexicon` main
branch directly.** The README of that repo says so explicitly,
and there's a good reason: main is unstable and contains
work-in-progress schema changes that may be broken or
incompatible. The npm package is the versioned, tested
distribution.

To upload lexicons matching a set of NSID prefixes:

```bash
# 1. Resolve latest version
VERSION=$(curl -s https://registry.npmjs.org/@hypercerts-org/lexicon \
  | python3 -c "import json,sys; print(json.load(sys.stdin)['dist-tags']['latest'])")

# 2. Download the tarball
curl -sL "https://registry.npmjs.org/@hypercerts-org/lexicon/-/lexicon-$VERSION.tgz" \
  -o /tmp/lexicon.tgz
mkdir -p /tmp/lexicon-pkg && tar -xzf /tmp/lexicon.tgz -C /tmp/lexicon-pkg

# 3. Filter to the prefixes you want
cd /tmp/lexicon-pkg
mkdir -p upload-staging
find package/lexicons -name "*.json" | while read f; do
  id=$(python3 -c "import json; print(json.load(open('$f'))['id'])")
  case "$id" in
    org.hypercerts.*|app.certified.*|org.hyperboards.*)
      rel=${f#package/lexicons/}
      mkdir -p "upload-staging/$(dirname "$rel")"
      cp "$f" "upload-staging/$rel"
      ;;
  esac
done

# 4. Zip + base64
( cd upload-staging && zip -r ../lexicons.zip . )
base64 -w0 lexicons.zip > lexicons.zip.b64

# 5. Upload via admin GraphQL
ADMIN_API_KEY='<from-password-manager>'
ADMIN_DID='did:plc:<your-did>'
python3 -c "
import json
print(json.dumps({
  'query': 'mutation Upload(\$zip: String!) { uploadLexicons(zipBase64: \$zip) }',
  'variables': {'zip': open('lexicons.zip.b64').read().strip()}
}))" > upload-payload.json

curl -X POST https://magic-indexer-dev.up.railway.app/admin/graphql \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "X-User-DID: $ADMIN_DID" \
  -H "Content-Type: application/json" \
  --data-binary @upload-payload.json
# expected: {"data":{"uploadLexicons":<count>}}
```

After upload, the Jetstream consumer **automatically restarts**
with the new union of `wantedCollections`. No human action needed.

### Labeler enable / disable / pause

```bash
# Enable: comma-separated DIDs
railway variables --service magic-indexer \
  --set "LABELER_DIDS=did:plc:abc...,did:plc:def..."

# Disable all: empty string via GraphQL (CLI doesn't allow empty values)
curl -X POST https://backboard.railway.com/graphql/v2 \
  -H "Authorization: Bearer $RAILWAY_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"query":"mutation { variableUpsert(input: { projectId: \"7d6c4e52-de61-439f-96c0-3ded4114b9be\", environmentId: \"<env-id>\", serviceId: \"<service-id>\", name: \"LABELER_DIDS\", value: \"\" }) }"}'
railway redeploy --service magic-indexer --yes

# Pause one labeler without restart (admin endpoint)
curl -X POST -H "Authorization: Bearer $ADMIN_API_KEY" \
  "https://magic-indexer-dev.up.railway.app/admin/labeler/pause?did=did:plc:..."

# Reset cursor (force re-backfill on next start)
curl -X POST -H "Authorization: Bearer $ADMIN_API_KEY" \
  "https://magic-indexer-dev.up.railway.app/admin/labeler/reset?did=did:plc:..."
```

### Diagnose "why is this record hidden?"

```bash
curl -H "Authorization: Bearer $ADMIN_API_KEY" \
  "https://magic-indexer-dev.up.railway.app/admin/label-chain?uri=at://did:plc:abc/app.bsky.feed.post/xyz"
```

Returns every label on the URI (active, negated, expired) with
provenance. Bypasses the public query path's filters because
this is a diagnostic view.

### Common labeler failure modes

| Symptom                                              | Likely cause                                                                                                  |
|------------------------------------------------------|---------------------------------------------------------------------------------------------------------------|
| `dial labeler: connection refused`                   | Labeler's PLC entry points at a non-public host (e.g. `http://localhost:4100`). Operator must update DID doc. |
| `dial labeler: websocket: bad handshake`             | Labeler host serves queryLabels HTTP but not subscribeLabels WS. Backfill works, live stream doesn't.         |
| `Labeler backfill complete ... received=0`           | No labels published under this DID's `src`. Either new labeler with no data, or wrong DID.                    |
| Cursor gap warning in logs                           | Labeler dropped frames upstream. Indexer keeps going.                                                          |

The reconnect loop uses exponential backoff (1 s → 2 min cap),
so a permanently-broken labeler settles to one log line per two
minutes per labeler.

---

## Review history (so you don't waste a session)

This branch went through **23 rounds of overnight review**
producing 59 fixes and 3 regression tests before the first
deploy. The per-round logs and final reports are in
[`docs/reviews/`](docs/reviews/). Read the index there if you
suspect something has already been audited.

A comprehensive security audit was performed on **2026-04-13**
(see [`docs/AUDIT_REPORT_2026-04-13.md`](docs/AUDIT_REPORT_2026-04-13.md)).
It identified 29 findings (4 Critical, 5 High, 8 Medium) and
fixed 15 of them across 14 commits. The remaining items are
low-severity or require architectural changes.

Combined totals:

| Rounds | Reviewers | Critical | Major | Minor | Nice | Fixed |
|--------|-----------|----------|-------|-------|------|-------|
| 1–10   | 200       | 35       | 100   | 95    | 19   | 55 fixes + 3 regression tests |
| 11–18  | 160       | 2        | 1     | 0     | 0    | 3 fixes (jetstream state races, Round 14) |
| 19–23  | 100       | 0        | 0     | 0     | 0    | 1 mid-deploy fix (`OptionalAuth` pass-through) |
| Audit  | 10+       | 4        | 5     | 8     | 12   | 15 fixes across 14 commits |
| **total** | **470+** | **41** | **106** | **103** | **31** | **74 fixes + 3 regression tests** |

### Items deliberately deferred (do not re-discover)

Two open issues are **deliberate** deferrals. Both have full
rationale + design questions documented as comments on the
GitHub issue. Read the comments before proposing work in either
area.

- **[#10 — Labeler signature verification](https://github.com/hb-agent/magic-indexer/issues/10)**.
  Re-open when a labeler we ingest starts shipping cryptographic
  signatures against a stable scheme.
- **[#13 — GDPR hard-delete endpoint](https://github.com/hb-agent/magic-indexer/issues/13)**.
  Re-open when there's a real erasure request or a legal
  obligation.

Other things that came up in review and were intentionally
**not** changed:
- The Go module path stays `github.com/GainForest/hypergoat`
  and the binary stays `hypergoat`. Renaming would touch ~80
  files for a brand-only change. The product is "Magic Indexer"
  in docs, the binary is `hypergoat` on disk.
- **Takedown is opt-in.** A record with an active `!takedown`
  label is *not* hidden by default. Clients must pass
  `excludeLabels: ["!takedown"]` explicitly. This is a
  deliberate product decision (the indexer is labeler-neutral).

---

## Landing the plane (mandatory checklist when ending a session)

1. **Quality gates** (if code changed):
   ```bash
   go build ./...
   go vet ./...
   go test -race ./...
   golangci-lint run ./...
   ```
   All four green or you have a real reason for the failure
   that's documented in the commit message.

2. **Commit with the right convention**:
   ```bash
   git add -A
   git commit -m "<area>: <one-line summary>

   <body>

   Co-Authored-By: <your model name> <noreply@anthropic.com>"
   ```
   Refer to closed issues with `Closes #N` where applicable.

3. **Push**:
   ```bash
   git push origin per-labeler-definitions
   git status                    # MUST show "up to date with origin"
   ```

4. **Verify**: nothing that affects the live deployment is
   considered "shipped" until `railway up && /health → 200` and
   the relevant log line you expected is visible in
   `railway logs --service magic-indexer --deployment`.

5. **Don't leave secrets in `/tmp`** if the session is ending
   without recovery. `shred -u /tmp/<file>` or leave them in
   place if the operator will be back to use them.

**Never stop before pushing.** Local-only work is work that
doesn't survive.

---

## See also (for the things this digest abbreviates)

- [`docs/RUNBOOK.md`](docs/RUNBOOK.md) — full operator playbook
  with first-time deploy, lexicon walkthrough, secret rotation,
  incident response, every gotcha worked out long-form.
- [`docs/reviews/README.md`](docs/reviews/README.md) — index of
  the 23-round overnight review history.
- [`SECURITY.md`](SECURITY.md) — required env vars, reverse-proxy
  rate limits, admin auth contract.
- [`README.md`](README.md) — high-level project intro and live URL.
- [`scripts/setup-env.sh`](scripts/setup-env.sh) — what `make setup` actually runs.
