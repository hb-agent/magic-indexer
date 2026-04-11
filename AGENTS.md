# AGENTS.md - Magic Indexer Development Guide

Magic Indexer is an AT Protocol AppView server that indexes
Lexicon-defined records and exposes them via a dynamically-generated GraphQL API.
The compiled binary is still called `hypergoat` inside the container and the Go
module path is `github.com/GainForest/hypergoat` — historical names from when
the project was originally called Hypergoat. Every `go run ./cmd/hypergoat` /
`bin/hypergoat` reference below is pointing at that binary path, not a
different product.

**Status:** Core functionality complete (Phases 1-7). All tests passing.

Use `bd` for task tracking. Run `bd onboard` to get started.

## Build/Test Commands

```bash
# Build
make build                     # Build binary to bin/hypergoat
go build ./...                 # Build all packages (quick check)

# Run
make run                       # Build and run server
go run ./cmd/hypergoat         # Run directly
make dev                       # Run with hot reload (requires air)

# Test - ALL TESTS
make test                      # Run all tests with race detector
go test ./...                  # Run all tests (faster, no race)

# Test - SINGLE TEST
go test -v -run TestName ./...                    # Run test by name pattern
go test -v -run TestParseLexicon ./internal/lexicon/...  # Specific package
go test -v ./internal/graphql/admin/...           # All tests in package

# Test - WITH COVERAGE
make test-coverage             # Generate coverage.html report

# Lint & Format
make lint                      # Run golangci-lint
make fmt                       # Format with go fmt + gofumpt
go fmt ./...                   # Format only

# Install dev tools
make tools                     # Install air, golangci-lint, gofumpt, migrate
```

## Code Style Guidelines

### Imports
Group imports in this order with blank lines between:
```go
import (
    "context"           // 1. Standard library
    "fmt"

    "github.com/go-chi/chi/v5"  // 2. External packages

    "github.com/GainForest/hypergoat/internal/database"  // 3. Internal packages
)
```

### Package Documentation
Every package must have a doc comment:
```go
// Package config handles application configuration loading from environment variables.
package config
```

### Naming Conventions
- **Packages:** lowercase, single word (`lexicon`, `oauth`, `backfill`)
- **Files:** lowercase with underscores (`did_resolver.go`, `jetstream_activity.go`)
- **Types:** PascalCase (`Executor`, `RecordFetcher`, `WhereClause`)
- **Interfaces:** Noun or -er suffix (`Executor`, `Fetcher`, `Resolver`)
- **Constants:** PascalCase exported, camelCase private
- **Acronyms:** All caps (`URI`, `DID`, `HTTP`, `JSON`)

### Error Handling
Always wrap errors with context:
```go
if err != nil {
    return fmt.Errorf("failed to query records: %w", err)
}
```

For typed errors:
```go
type DBError struct {
    Code    string
    Message string
    Cause   error
}
func (e *DBError) Error() string { return e.Message }
func (e *DBError) Unwrap() error { return e.Cause }
```

### Context
Always pass context as the first parameter:
```go
func (r *RecordsRepository) GetByURI(ctx context.Context, uri string) (*Record, error)
```

### Repository Pattern
All database access goes through repositories in `internal/database/repositories/`:
```go
type RecordsRepository struct {
    db database.Executor
}

func NewRecordsRepository(db database.Executor) *RecordsRepository {
    return &RecordsRepository{db: db}
}

func (r *RecordsRepository) GetByURI(ctx context.Context, uri string) (*Record, error) {
    // Use r.db.Placeholder() for SQL parameters
    sqlStr := fmt.Sprintf("SELECT %s FROM record WHERE uri = %s", r.recordColumns(), r.db.Placeholder(1))
    // ...
}
```

### Testing
Use table-driven tests:
```go
func TestParseLexicon(t *testing.T) {
    tests := []struct {
        name    string
        input   string
        want    *Lexicon
        wantErr bool
    }{
        {name: "simple record", input: `{"lexicon":1}`, want: &Lexicon{}},
        {name: "invalid json", input: `{`, wantErr: true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := ParseLexicon(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
            }
        })
    }
}
```

### Logging
Use structured logging with `log/slog`:
```go
slog.Info("Starting backfill", "collections", collections, "count", len(repos))
slog.Warn("Failed to resolve DID", "did", did, "error", err)
slog.Error("Database connection failed", "error", err)
```

## Project Structure

```
cmd/hypergoat/          # Main entry point (server initialization, routing)
internal/
  backfill/             # Historical data backfill from AT Protocol relays
  config/               # Configuration loading from environment
  database/
    migrations/         # SQL migrations (auto-run on startup)
    repositories/       # Data access layer (records, actors, lexicons, oauth, etc.)
    sqlite/             # SQLite implementation (pure Go, no CGO)
    postgres/           # PostgreSQL implementation (pgx)
  graphql/
    admin/              # Admin API (schema.go, resolvers.go, handler.go, types.go)
    schema/             # Public schema builder (dynamic from lexicons)
    resolver/           # Public resolvers and context
    query/              # Connection types (Relay spec)
    depth/              # Pre-execution GraphQL query depth guard
    subscription/       # WebSocket subscriptions (graphql-transport-ws)
    types/              # GraphQL type mapping from lexicons
  integration/          # Integration tests (build tag: integration)
  jetstream/            # Real-time AT Protocol event consumer
  labeler/              # ATProto labeler subscribeLabels + queryLabels client
  lexicon/              # Lexicon parsing, registry, NSID utilities
  metrics/              # Prometheus counters + /metrics HTTP handler
  oauth/                # OAuth 2.0 + DPoP + PKCE + did:plc / did:web resolution
  server/               # HTTP handlers (GraphiQL with CSP, OAuth endpoints,
                        # security headers middleware, CORS middleware)
  workers/              # Background jobs (activity cleanup + orphan janitor,
                        # backfill state, OAuth cleanup)
docs/                   # Implementation plan and documentation
scripts/                # Deployment helpers (setup-env.sh)
testdata/               # Test fixtures and sample lexicons
```

## Labeler subsystem (internal/labeler/)

Mirrors `internal/jetstream/` but speaks the ATProto labeler protocol:

- `client.go` — websocket client for
  `com.atproto.label.subscribeLabels`. Uses `fxamacker/cbor/v2`
  for the two-CBOR-object frame format (`#labels`, `#info`,
  `#error`). `SetReadLimit` bounds frame size. Non-normal close
  codes are surfaced at Warn; empty-body `#labels` frames are
  dropped explicitly; `#info` decode failures are elevated to
  Warn so `OutdatedCursor` signals cannot be silently lost.
- `backfill.go` — one-time `com.atproto.label.queryLabels`
  paginated backfill via `hashicorp/go-retryablehttp`.
- `consumer.go` — lifecycle: load cursor → backfill if needed →
  connect → stream labels → flush cursor on a ticker. Exponential
  backoff on reconnect. Panic-recovered at the goroutine boundary
  in `cmd/hypergoat/main.go` so one labeler cannot take down the
  process. Logs cursor gaps at Warn.

Label definitions are auto-upserted via
`INSERT ... ON CONFLICT DO NOTHING` keyed on the composite
`(src, val)` PK added in migration 009, so concurrent labelers
cannot race a new `(src, val)` pair.

## Security headers middleware

`internal/server/security_headers.go` emits
`X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`,
`Referrer-Policy: no-referrer`, and conditionally
`Strict-Transport-Security` (only when `EXTERNAL_BASE_URL` is
https). `/graphiql` sets its own `Content-Security-Policy`
allowing the unpkg CDN for bootstrap assets; JSON API endpoints
keep the tighter default.

See [SECURITY.md](SECURITY.md) for the full operator contract
(rate limiting, required env vars, admin auth shape).

## Deployment

The dev deployment lives at **https://magic-indexer-dev.up.railway.app**,
running on Railway with a Postgres backing store. The full deploy
playbook — first-time provisioning, routine deploys, env vars,
secrets management, common gotchas — is in
[`docs/RUNBOOK.md`](docs/RUNBOOK.md). Read that **before** touching
the live environment.

Key facts an AI session should know up front:
- The Railway service is named `magic-indexer` in the `dev`
  environment of project `magic-index` (project ID
  `7d6c4e52-de61-439f-96c0-3ded4114b9be`).
- The active branch is `per-labeler-definitions`. Routine deploys
  are `railway up --service magic-indexer --detach` from a clean
  checkout of that branch.
- Secrets (`SECRET_KEY_BASE`, `ADMIN_API_KEY`, Railway API token)
  live in the operator's password manager and the Railway
  dashboard. **Never commit them.** `config.Validate()` rejects the
  dev placeholder string at startup.
- Railway bans `VOLUME` in Dockerfiles. Don't reintroduce it.
- The Railway CLI uses `RAILWAY_API_TOKEN` (not `RAILWAY_TOKEN`)
  for account-scoped tokens.

## Operations

Day-to-day operational tasks — uploading lexicons (always from
the npm package `@hypercerts-org/lexicon`, never from the
upstream main branch), enabling/disabling labelers, pausing a
single labeler without redeploying, inspecting why a record is
hidden, rotating secrets, common labeler failure modes — are
all in [`docs/RUNBOOK.md`](docs/RUNBOOK.md). It is the canonical
"how do I do X in production" reference.

## Review history

This branch went through **23 rounds of overnight review**
producing 59 fixes and 3 regression tests before the first
deploy. The per-round logs and final reports are in
[`docs/reviews/`](docs/reviews/). Read the index there if you
want to know what's already been audited (so you don't waste a
session re-finding the same things) or which items were
deferred with explicit reasoning.

Two open issues are deliberate deferrals:
- [#10 — Labeler signature verification](https://github.com/hb-agent/magic-indexer/issues/10) (waiting for upstream spec stability)
- [#13 — GDPR hard-delete endpoint](https://github.com/hb-agent/magic-indexer/issues/13) (waiting for product/legal trigger)

Don't re-discover them. If a real trigger appears, follow the
re-open criteria documented in each issue.

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below.

1. **Run quality gates** (if code changed):
   ```bash
   go build ./...
   go test ./...
   ```
2. **Commit and PUSH**:
   ```bash
   git add -A && git commit -m "feat: description"
   git push origin $branchname
   git status  # MUST show "up to date with origin"
   ```
3. **Verify** - Work is NOT complete until `git push` succeeds

**CRITICAL:** Never stop before pushing - that leaves work stranded locally.
