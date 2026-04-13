# Changelog

## 2026-04-13 — Full-text search

**PR:** [#35](https://github.com/hb-agent/magic-indexer/pull/35)

Add a `search: String` parameter to all typed collection queries and the generic `records` query. Uses Postgres `tsvector` with a GIN index for fast, stemmed full-text search.

- **Searched fields**: title (A), shortDescription (B), description (C), workScope (D)
- **Behavior**: space-separated terms are implicitly ANDed, English stemming applied
- **Combinable with**: `authors`, `labels`, `excludeLabels` filters
- **Max query length**: 500 characters

### Breaking changes

None. The `search` parameter is optional — existing queries work unchanged.

### Deployment notes

- Migration 012 adds a `search_vector` generated column and GIN index. Runs automatically on startup. At ~5000 records, takes under a second.
- Requires an `immutable_to_tsvector` wrapper function (created by the migration) because `to_tsvector` is STABLE not IMMUTABLE.

---

## 2026-04-13 — Record validation against lexicon schemas

**PR:** [#28](https://github.com/hb-agent/magic-indexer/pull/28)

Records with missing required fields no longer crash GraphQL responses. Query-time sanitization (always on) filters out bad records silently. Ingestion-time validation is configurable via `VALIDATION_MODE` env var.

---

## 2026-04-12 — Deep Code Review: 42 fixes across security, concurrency, performance

**PR:** [#25](https://github.com/hb-agent/magic-indexer/pull/25)
**Deployed to:** Railway (`magic-indexer-dev`)

### Breaking changes

- **GraphQL HTTP status codes**: queries now always return HTTP 200 per the GraphQL-over-HTTP spec. Errors are in the response body `errors` array (unchanged). Clients that checked `status === 400` for error detection should check the `errors` field instead. Most GraphQL client libraries already do this.

### Non-breaking changes visible to clients

- **WebSocket subscriptions** stay alive longer. The server now sends periodic pings, so idle subscriptions no longer timeout after 60 seconds.
- **OAuth `redirect_uri`** matching is now exact-only. The prefix-match path was an open-redirect risk. All existing registered clients use exact matching, so no impact expected.

### Internal improvements (invisible to clients)

**Security (10 fixes)**
- PAR endpoint validates `redirect_uri` against registered URIs
- DPoP access token hash (ATH) verification uses the verified JWT parse instead of a fragile re-parse
- OAuth client registration body capped at 1 MiB
- DID document ID validated against queried DID
- Token exchange errors logged server-side, generic message to client
- Subscription queries depth-checked (prevents resource abuse)
- Backfill responses bounded with `io.LimitReader`
- Grant type value no longer echoed in error responses
- OAuth cleanup worker errors now logged
- ATP session update errors now logged

**Concurrency (7 fixes)**
- WebSocket write races fixed in Jetstream and Labeler clients (mutex held across writes)
- Subscription `close()` uses `sync.Once` to prevent double-close panic
- Subscription `event.Record` cloned before mutation (prevents concurrent map write panic with multiple subscribers)
- `Collections()` reads under mutex
- `conn.Close` moved inside mutex in subscription handler
- `processEvents` receives client as explicit parameter (eliminates data race)

**Performance (7 fixes)**
- DID cache uses `singleflight.Group` to collapse concurrent resolutions (no more thundering herd)
- Redundant SELECT before INSERT eliminated (`ON CONFLICT WHERE cid`)
- `BatchInsert` skips no-op overwrites via CID guard
- `ensureActor` uses direct Upsert (removed redundant pre-check SELECT)
- Per-event Info logs downgraded to Debug (significant I/O reduction under load)
- `PublishRecord` skips JSON unmarshal when zero subscribers
- Double `IsCommit()` check merged

**Resilience (7 fixes)**
- `UpdateCollections` wrapped in reconnection loop (dynamic lexicon changes no longer kill event ingestion)
- Exponential backoff resets after 30-second stable connection (Jetstream + Labeler)
- Labeler cursor not advanced on full-batch failure (prevents data loss during transient DB issues)
- Labeler backfill sentinel uses -1 instead of 1 (prevents OutdatedCursor infinite loop)
- Postgres connection pool increased 25 to 50
- Backfill pagination capped at 10k pages (prevents infinite loop from misbehaving relay)
- CORS config comment corrected

**Correctness (5 fixes)**
- `createdAt` returns int64 (Unix epoch) not RFC3339 string in OAuth client mutations
- `capitalizeFirst` uses `unicode.ToUpper` instead of ASCII arithmetic
- OAuth client create/update responses include `scope` field
- DPoP ATH field carried through validation result struct

**Postgres compatibility (6 fixes)**
- All boolean columns (`neg`, `revoked`, `used`, `require_redirect_exact`) use `database.Bool` and scan into `bool` instead of int
- `InsertNegation` uses dialect-correct boolean literal
- Test assertions use JSON-semantic comparison (Postgres JSONB reorders keys)
- Test cursor format uses microsecond precision for Postgres timestamps
- `resetBetweenTests` fixed: correct table names, all OAuth tables included

### Deployment notes

- Zero config changes required. All fixes are code-level.
- Migrations run automatically on startup (no new migrations in this release).
- Brief (~10-30s) downtime during Railway container restart. Jetstream resumes from last-flushed cursor.
- Existing OAuth sessions, tokens, and registered clients are unaffected.

### Known deferred items

- **[#24](https://github.com/hb-agent/magic-indexer/issues/24)**: DPoP refresh token key rebinding (requires DB migration)
- **[hypercerts-org/certs-social#45](https://github.com/hypercerts-org/certs-social/issues/45)**: Client `COOKIE_SECRET` defaults to public value (Next.js fix)
