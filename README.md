<p align="center">
  <img src="hypergoat.png" alt="Hyperindex" width="600">
</p>

# Hyperindex (hi)

**A Go AT Protocol AppView server that indexes records and exposes them via GraphQL**

*Formerly known as Hypergoat.*

Hyperindex (hi) connects to the AT Protocol network, indexes records matching your configured Lexicons, and provides a GraphQL API for querying them. It's a Go port of [Quickslice](https://github.com/quickslice/quickslice).

## Quick Start

```bash
# Clone and run
git clone https://github.com/GainForest/hypergoat.git
cd hypergoat
make setup          # creates .env with a freshly generated SECRET_KEY_BASE
go run ./cmd/hypergoat
```

Open http://localhost:8080/graphiql/admin to access the admin interface.

`make setup` calls `scripts/setup-env.sh`, which refuses to overwrite
an existing `.env` and fails fast if `openssl` isn't installed. For a
production deployment, see [SECURITY.md](SECURITY.md) — it spells out
the required environment variables, the rate-limiting expectations
at the reverse proxy, and the HTTPS / HSTS / admin-auth contract.

## Usage

### 1. Register Lexicons

Lexicons define the AT Protocol record types you want to index. Register them via the Admin GraphQL API at `/graphiql/admin`:

```graphql
mutation {
  uploadLexicons(files: [...])  # Upload lexicon JSON files
}
```

Or place lexicon JSON files in a directory and set `LEXICON_DIR` environment variable.

**Example lexicons:**
- `app.bsky.feed.post` - Bluesky posts
- `app.bsky.feed.like` - Likes
- `app.bsky.actor.profile` - User profiles

### 2. Start Indexing

Once lexicons are registered, Hyperindex automatically:
- **Connects to Jetstream** for real-time events
- **Indexes matching records** to your database

To backfill historical data, use the admin API:

```graphql
mutation {
  triggerBackfill  # Full network backfill for registered collections
}

# Or backfill a specific user
mutation {
  backfillActor(did: "did:plc:...")
}
```

### 3. Query via GraphQL

Access your indexed data at `/graphql`:

```graphql
# Query records by collection
query {
  records(collection: "app.bsky.feed.post") {
    edges {
      node {
        uri
        did
        value  # JSON record data
      }
    }
  }
}

# With typed queries (when lexicon schemas are loaded)
query {
  appBskyFeedPost(first: 10, where: { did: { eq: "did:plc:..." } }) {
    edges {
      node {
        uri
        text
        createdAt
      }
    }
  }
}
```

## Endpoints

| Endpoint | Description |
|----------|-------------|
| `/graphql` | Public GraphQL API (POST body capped at 1 MiB, query depth ≤ 15) |
| `/graphql/ws` | GraphQL subscriptions (WebSocket, max 64 subs/client) |
| `/admin/graphql` | Admin GraphQL API (POST-only, bearer token or OAuth required) |
| `/admin/labeler/reset?did=...` | Clear subscription + backfill cursors for one labeler (POST, bearer) |
| `/admin/labeler/pause?did=...` | Stop a single labeler consumer without restart (POST, bearer) |
| `/admin/label-chain?uri=...` | Diagnostic: every label on a URI (GET, bearer) |
| `/graphiql` | GraphiQL playground (public API, CSP-restricted) |
| `/graphiql/admin` | GraphiQL playground (admin API) |
| `/health` | Health check (pings the DB, 503 on degraded) |
| `/stats` | Server + consumer statistics |
| `/metrics` | Prometheus text exposition format |
| `/.well-known/oauth-protected-resource` | OAuth 2.0 protected-resource metadata |
| `/.well-known/oauth-authorization-server` | OAuth 2.0 server metadata |
| `/oauth-client-metadata.json` | OAuth 2.0 client metadata |
| `/oauth/authorize` | OAuth authorization endpoint |
| `/oauth/token` | OAuth token endpoint |
| `/oauth/register` | OAuth client registration endpoint |
| `/oauth/par` | OAuth Pushed Authorization Request endpoint |
| `/oauth/dpop/nonce` | DPoP nonce rotation endpoint |
| `/oauth/jwks` | JSON Web Key Set |

## Configuration

Create a `.env` file or set environment variables:

```bash
# Database (SQLite or PostgreSQL)
DATABASE_URL=sqlite:data/hypergoat.db
# DATABASE_URL=postgres://user:pass@localhost/hypergoat

# Server
HOST=127.0.0.1
PORT=8080
EXTERNAL_BASE_URL=http://localhost:8080

# Admin access (comma-separated DIDs)
ADMIN_DIDS=did:plc:your-did-here

# Security — required for session encryption (min 64 chars)
SECRET_KEY_BASE=your-secret-key-at-least-64-characters-long-generate-with-openssl-rand

# Admin API key — shared secret for admin authentication.
# When set, the X-User-DID header is trusted only if the request
# also carries a matching Authorization: Bearer <key> header.
# Generate with: openssl rand -base64 32
# ADMIN_API_KEY=your-secret-key-here

# WebSocket origins — comma-separated allowed origins for subscriptions.
# Empty = same-origin only. Set to "*" for development.
# ALLOWED_ORIGINS=https://your-frontend.vercel.app

# Jetstream (real-time indexing)
# Collections are auto-discovered from registered lexicons
# Or specify manually:
# JETSTREAM_COLLECTIONS=app.bsky.feed.post,app.bsky.feed.like

# Backfill
BACKFILL_RELAY_URL=https://relay1.us-west.bsky.network

# Labeler subscriptions (optional). Comma-separated DIDs.
# The server connects to each labeler's subscribeLabels endpoint,
# does a one-time queryLabels backfill on first start, then
# streams live. Cursors are persisted per-labeler in the config
# table so restarts resume cleanly. The first DID in this list
# is the default used by label-filtered GraphQL queries; clients
# can override with `labelerDids`.
# LABELER_DIDS=did:plc:5rw6of6lry7ihmyhm323ycwn
# LABELER_DRY_RUN=false
# LABELER_CURSOR_FLUSH_INTERVAL=5
```

## Labeler subscriptions

When `LABELER_DIDS` is set, the server resolves each DID to its
AtprotoLabeler service endpoint (via did:plc / did:web) and runs
one `labeler.Consumer` per DID. Each consumer:

1. Reads its persisted cursor from the `config` table.
2. On first start (no cursor), runs a `queryLabels` backfill
   paginated through the labeler's HTTP endpoint.
3. Opens a `com.atproto.label.subscribeLabels` websocket and
   streams labels into the `label` table.
4. Flushes the cursor every 5 seconds (configurable via
   `LABELER_CURSOR_FLUSH_INTERVAL`).
5. Auto-creates `label_definition` rows for every new `(src, val)`
   pair it sees, scoped by the per-labeler composite primary key
   added in migration 009.
6. On websocket drop, reconnects with exponential backoff.

A labeler that emits an `#info` frame with name `OutdatedCursor`
triggers a cursor clear + re-backfill on the next reconnect.

Each per-labeler consumer is panic-recovered — a bad label from
one labeler cannot take down the process.

You can inspect a record's full label chain (including negations
and expired labels) via:

```bash
curl -H "Authorization: Bearer $ADMIN_API_KEY" \
  "http://localhost:8080/admin/label-chain?uri=at://did:plc:abc/app.bsky.feed.post/xyz"
```

And query records filtered by labels:

```graphql
query {
  records(
    collection: "app.bsky.feed.post",
    labels: ["high-quality"],
    excludeLabels: ["!takedown"],
    labelerDids: ["did:plc:5rw6of6lry7ihmyhm323ycwn"]
  ) {
    edges { node { uri labels } }
  }
}
```

Note: **takedown enforcement is opt-in**. The indexer is intentionally
labeler-neutral and does not automatically hide takedown-labeled
records. Clients that want to honour takedowns must pass
`excludeLabels: ["!takedown"]` explicitly.

## Docker

```bash
docker compose up --build
```

Or build manually:

```bash
docker build -t hyperindex .
docker run -p 8080:8080 -v ./data:/data hyperindex
```

## Admin API

The admin API at `/admin/graphql` provides:

**Queries:**
- `statistics` - Record, actor, lexicon counts
- `lexicons` - List registered lexicons
- `activityBuckets` / `recentActivity` - Jetstream activity data
- `settings` - Server configuration

**Mutations:**
- `uploadLexicons` - Register new lexicons
- `deleteLexicon` - Remove a lexicon
- `backfillActor` - Backfill a specific user
- `triggerBackfill` - Full network backfill
- `populateActivity` - Populate activity from existing records
- `updateSettings` - Update server settings (URLs validated as https, DIDs validated)
- `createLabelDefinition` - Add a label definition (bounded: val ≤ 128, description ≤ 1024, src ≤ 512)
- `createLabel` / `negateLabel` - Apply or retract labels via the admin UI
- `resetAll` - Clear all data (requires confirmation)

**Auth:**
Every `/admin/graphql` request must present either a validated
OAuth user (via Authorization: Bearer <token>) or a valid
`ADMIN_API_KEY` bearer token together with a validated `X-User-DID`
header. GET is rejected with 405; only POST is accepted to keep
mutations out of access logs. Mutation logs redact variable
**values** — only operation name and variable keys are logged.

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                   Hyperindex (hi) Server                  │
├─────────────────────────────────────────────────────────┤
│                                                         │
│  Jetstream ──→ Consumer ──→ Records DB ──→ GraphQL API │
│                    │                                    │
│              Activity Log ──→ Admin Dashboard           │
│                                                         │
│  Backfill Worker ──→ AT Protocol Relay ──→ Records DB  │
│                                                         │
└─────────────────────────────────────────────────────────┘
```

**Key Components:**
- **Jetstream Consumer** - Subscribes to real-time AT Protocol events
- **Backfill Worker** - Imports historical data from relays
- **GraphQL Schema Builder** - Generates schema from Lexicons
- **Activity Tracker** - Logs all indexing activity for monitoring

## Development

```bash
# Run with hot reload
make dev

# Run tests
make test
go test -v -run TestName ./...  # Single test

# Lint
make lint

# Build binary
make build
```

## Database Support

- **SQLite** - Default, great for development and small deployments
- **PostgreSQL** - Recommended for production

Migrations run automatically on startup.

## History

Hyperindex was incubated and created by [GainForest](https://gainforest.earth) and [Claude Opus 4.5](https://www.anthropic.com/claude) (Anthropic), originally under the name *Hypergoat*. It has since been moved to [hypercerts-org](https://github.com/hypercerts-org) for community maintenance.

## License

Apache License 2.0

## Acknowledgments

- [GainForest](https://gainforest.earth) & [Claude Opus 4.5](https://www.anthropic.com/claude) - Original creators
- [Quickslice](https://github.com/quickslice/quickslice) - Original Gleam implementation
- [AT Protocol](https://atproto.com/) - The underlying protocol
