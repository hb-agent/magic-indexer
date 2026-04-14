# Magic Indexer Operations Runbook

This is the operator's playbook for Magic Indexer (the
`hb-agent/magic-indexer` fork of `hypercerts-org/hyperindex`,
deployed as the `magic-indexer` Railway service in the `dev`
environment).

If you're a new contributor or a fresh AI session opening this
repo, **read [AGENTS.md](../AGENTS.md) first** for the dev
guide and project layout, then come back here for deployment
and operations.

---

## Live environment

| Item                | Value                                              |
|---------------------|----------------------------------------------------|
| Public URL          | https://magic-indexer-dev.up.railway.app           |
| GraphQL (public)    | https://magic-indexer-dev.up.railway.app/graphql   |
| GraphQL (admin)     | https://magic-indexer-dev.up.railway.app/admin/graphql |
| GraphiQL playground | https://magic-indexer-dev.up.railway.app/graphiql  |
| Health              | https://magic-indexer-dev.up.railway.app/health    |
| Stats               | https://magic-indexer-dev.up.railway.app/stats     |
| Metrics             | https://magic-indexer-dev.up.railway.app/metrics   |
| Railway dashboard   | https://railway.com/project/7d6c4e52-de61-439f-96c0-3ded4114b9be |
| GitHub repo         | https://github.com/hb-agent/magic-indexer          |
| Active branch       | `per-labeler-definitions`                          |
| Backing database    | Postgres 18 (Railway-managed, in same project)     |

**Secrets** (`SECRET_KEY_BASE`, `ADMIN_API_KEY`) live in:
1. The Railway dashboard → `magic-indexer` service → Variables tab
2. The operator's password manager (1Password / Bitwarden / Vault)

They are **not** in this repository, in `.env.example`, or in
any committed file. If you lose them and need to rotate, see
[Rotating secrets](#rotating-secrets) below.

---

## Build, test, lint (local development)

From a clean checkout:

```bash
git checkout per-labeler-definitions
make setup           # generates .env with a fresh SECRET_KEY_BASE
go run ./cmd/hypergoat
```

Quality gates that must pass before any PR:

```bash
go build ./...
go test ./...
go test -race ./...
golangci-lint run ./...
```

CI runs all four on every push to `main` (and on PRs targeting
`main`) plus a Postgres-variant test pass and a reproducible-build
diff. See `.github/workflows/ci.yml`.

---

## First-time deploy to Railway

This is the procedure that produced the live deployment. Follow
it from scratch only if you're standing up a new Railway project.
For routine code updates use [Routine deploy](#routine-deploy).

### Prerequisites

- A Railway account (sign in with GitHub)
- A Railway API token created at https://railway.com/account/tokens
- The Railway CLI (`curl -fsSL https://railway.com/install.sh | sh`)

### Steps

```bash
export RAILWAY_API_TOKEN='<your-token>'
export RAILWAY_NO_TELEMETRY=1

# 1. Sanity check the token
railway whoami

# 2. Create the project
railway init --name magic-indexer

# 3. Add Postgres (run once; the CLI prompt looks like it hangs
#    but the service is created)
railway add -d postgres
# wait ~5 seconds, then ctrl-C if it doesn't return on its own

# 4. Create the application service
railway add --service magic-indexer
# same — interactive prompt looks like a hang, but the service is created

# 5. Link this directory to the service
railway link --project magic-indexer --environment dev --service magic-indexer
railway service magic-indexer

# 6. Set environment variables. Generate the secrets first; never
#    reuse them across deployments.
SECRET_KEY_BASE=$(openssl rand -base64 64 | tr -d '\n')
ADMIN_API_KEY=$(openssl rand -base64 32 | tr -d '\n')
railway variables --service magic-indexer \
  --set "HOST=0.0.0.0" \
  --set "PORT=8080" \
  --set "DATABASE_URL=\${{Postgres.DATABASE_URL}}" \
  --set "SECRET_KEY_BASE=$SECRET_KEY_BASE" \
  --set "ADMIN_API_KEY=$ADMIN_API_KEY" \
  --set "ADMIN_DIDS=did:plc:<your-did-here>" \
  --set "ALLOWED_ORIGINS=https://certs.social,https://certs-social-*.vercel.app" \
  --skip-deploys

# 7. Push the build
railway up --service magic-indexer --detach

# 8. Generate a public domain. Railway auto-names it
#    <service>-<environment>.up.railway.app, so ours becomes
#    magic-indexer-dev.up.railway.app.
curl -s -X POST https://backboard.railway.com/graphql/v2 \
  -H "Authorization: Bearer $RAILWAY_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"query":"mutation { serviceDomainCreate(input: { environmentId: \"<env-id>\", serviceId: \"<service-id>\", targetPort: 8080 }) { domain } }"}'

# 9. Once the domain exists, set EXTERNAL_BASE_URL to enable HSTS
railway variables --service magic-indexer \
  --set "EXTERNAL_BASE_URL=https://magic-indexer-dev.up.railway.app"

# 10. Verify
curl https://magic-indexer-dev.up.railway.app/health
```

**Save `SECRET_KEY_BASE` and `ADMIN_API_KEY` in your password
manager immediately.** They are also stored in the Railway
dashboard (Variables tab on the `magic-indexer` service) so
you can re-read them later, but lose-then-redeploy without
them = the new deployment refuses to boot (the Round 1
`config.Validate()` rejects an empty / placeholder value).

### Things that will trip you up

- **Railway bans `VOLUME` in Dockerfiles.** Our Dockerfile used
  to declare `VOLUME ["/app/data"]` for SQLite users. The first
  Railway build failed because of it. The current Dockerfile has
  a comment explaining why we removed it. Don't put it back.
- **`OptionalAuth` middleware passes through on bad tokens.**
  `/admin/graphql` is mounted with `OptionalAuth` because the
  admin handler accepts both OAuth and `ADMIN_API_KEY` bearer
  tokens. If you mount it with `RequireAuth` instead, the API-key
  path breaks. See the comment in `internal/oauth/middleware.go:OptionalAuth`.
- **Railway CLI uses `RAILWAY_API_TOKEN`, not `RAILWAY_TOKEN`,
  for account-scoped tokens.** `RAILWAY_TOKEN` is for
  project-scoped tokens. Whoami fails silently with the wrong
  variable.
- **`railway add --database postgres` shows a prompt then
  succeeds anyway.** Don't double-run it; you'll create two
  Postgres services. If you do, delete the duplicate via the
  GraphQL API: `mutation { serviceDelete(id: "<dup-id>") }`.

---

## Routine deploy

Once the project exists and you're working on the same branch:

```bash
cd /path/to/magic-indexer
git checkout per-labeler-definitions
git pull
export RAILWAY_API_TOKEN='<your-token>'
railway up --service magic-indexer --detach
```

Watch the build:

```bash
railway logs --service magic-indexer --build --lines 100
```

Watch the runtime once it's deployed:

```bash
railway logs --service magic-indexer --deployment --lines 200
```

Or stream live:

```bash
railway logs --service magic-indexer --deployment
```

To force a redeploy of the *current* code (e.g. after changing
an env var without using `--skip-deploys`):

```bash
railway redeploy --service magic-indexer --yes
```

---

## Lexicon management

### Where to get lexicons (canonical source)

Magic Indexer's lexicons are managed by the upstream
[`hypercerts-org/hypercerts-lexicon`](https://github.com/hypercerts-org/hypercerts-lexicon)
project. **Do not read from `main` directly** — that branch is
unstable and may contain broken or unreleased schema changes.
Use the published npm package instead:

```bash
# Get the latest published version
curl -s https://registry.npmjs.org/@hypercerts-org/lexicon \
  | python3 -c "import json, sys; print(json.load(sys.stdin)['dist-tags']['latest'])"

# Download that version
curl -sL https://registry.npmjs.org/@hypercerts-org/lexicon/-/lexicon-<version>.tgz \
  -o /tmp/lexicon.tgz
mkdir -p /tmp/lexicon-pkg && tar -xzf /tmp/lexicon.tgz -C /tmp/lexicon-pkg
```

The lexicons are inside `/tmp/lexicon-pkg/package/lexicons/`,
organised by NSID prefix (`org/hypercerts/`, `app/certified/`,
`org/hyperboards/`, plus a few transitive deps from `pub/leaflet`,
`com/atproto`, `app/bsky`).

### Filter to the prefixes you want and upload

```bash
# Stage only the lexicons you need
cd /tmp/lexicon-pkg
mkdir -p upload-staging
find extracted/package/lexicons -name "*.json" | while read f; do
  id=$(python3 -c "import json; print(json.load(open('$f'))['id'])")
  case "$id" in
    org.hypercerts.*|app.certified.*|org.hyperboards.*)
      rel=${f#extracted/package/lexicons/}
      mkdir -p "upload-staging/$(dirname "$rel")"
      cp "$f" "upload-staging/$rel"
      ;;
  esac
done

# Build the ZIP
( cd upload-staging && zip -r ../lexicons.zip . )

# Base64-encode (no line wrap)
base64 -w0 lexicons.zip > lexicons.zip.b64

# Upload via the admin GraphQL mutation
ADMIN_API_KEY='<your-admin-api-key>'
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

When the upload succeeds, the Jetstream consumer **automatically
restarts** with the new union of `wantedCollections`. You will
see in the logs:

```
"Starting Jetstream consumer (dynamic)" collections=[...]
"Connecting to Jetstream" url="wss://...?wantedCollections=..."
"Connected to Jetstream"
```

### Verify uploaded lexicons

```bash
# Quick: count from /stats (no auth needed)
curl https://magic-indexer-dev.up.railway.app/stats

# Detailed: list every registered NSID via admin
curl -X POST https://magic-indexer-dev.up.railway.app/admin/graphql \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "X-User-DID: $ADMIN_DID" \
  -H "Content-Type: application/json" \
  -d '{"query":"{ lexicons { id } }"}'
```

---

## Labeler management

### Enable a labeler

Set `LABELER_DIDS` to a comma-separated list of labeler DIDs and
trigger a redeploy:

```bash
railway variables --service magic-indexer \
  --set "LABELER_DIDS=did:plc:abc...,did:plc:def..."
```

The labeler subsystem will:

1. Resolve each DID via PLC (or did:web) to find its
   `AtprotoLabeler` service endpoint.
2. Run a one-time `queryLabels` HTTP backfill to load any
   historical labels under that DID's `src`.
3. Open a `subscribeLabels` WebSocket for live streaming.
4. Persist a per-DID cursor in the `config` table so restarts
   resume cleanly.

### Disable all labelers

```bash
# CLI doesn't allow --set with empty values, use the GraphQL API
curl -s -X POST https://backboard.railway.com/graphql/v2 \
  -H "Authorization: Bearer $RAILWAY_API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"query":"mutation { variableUpsert(input: { projectId: \"<project>\", environmentId: \"<env>\", serviceId: \"<service>\", name: \"LABELER_DIDS\", value: \"\" }) }"}'
railway redeploy --service magic-indexer --yes
```

### Pause one labeler without redeploying

```bash
curl -X POST -H "Authorization: Bearer $ADMIN_API_KEY" \
  "https://magic-indexer-dev.up.railway.app/admin/labeler/pause?did=did:plc:..."
```

The matching consumer is removed from the active set, its cursor
is flushed, and its goroutine exits. To bring it back, redeploy
the service (in-process resume is not currently supported).

### Reset a labeler's cursor (force re-backfill)

```bash
curl -X POST -H "Authorization: Bearer $ADMIN_API_KEY" \
  "https://magic-indexer-dev.up.railway.app/admin/labeler/reset?did=did:plc:..."
```

This deletes both the subscription seq cursor and the in-progress
backfill checkpoint from the `config` table. The next process
restart will run a full backfill for this labeler from scratch.

### Common labeler failure modes

| Symptom                                              | Likely cause                                                  |
|-----------------------------------------------------|----------------------------------------------------------------|
| `connect: dial labeler: dial tcp: connection refused` | Labeler's PLC entry points at a non-public host (e.g. localhost). The operator must update their DID document. |
| `dial labeler: websocket: bad handshake`            | Labeler host serves the queryLabels HTTP endpoint but not the subscribeLabels WS endpoint. Backfill works, live stream doesn't. |
| `Labeler backfill complete ... received=0`          | The labeler has no labels published under this DID's `src`. Either it's a new labeler with no data, or the wrong DID is configured. |
| Cursor gap warning                                  | The labeler dropped frames. Check the labeler operator's logs. The indexer keeps going. |

The reconnect loop uses exponential backoff (1 s → 2 min cap),
so a permanently-broken labeler will produce one log line per
two minutes once it settles.

---

## Inspecting the system

### Health, stats, metrics

```bash
# Liveness — pings the database. Returns 503 on degraded.
curl https://magic-indexer-dev.up.railway.app/health

# Operational counters and per-labeler state
curl https://magic-indexer-dev.up.railway.app/stats

# Prometheus text format. Bounded label cardinality.
curl https://magic-indexer-dev.up.railway.app/metrics
```

### Logs

```bash
# Live stream
railway logs --service magic-indexer --deployment

# Last N lines
railway logs --service magic-indexer --deployment --lines 200

# Build logs (for the most recent build)
railway logs --service magic-indexer --build --lines 200
```

### Verifying the live deployment in a real browser

The dev container has `agent-browser` installed (with Playwright's
ARM64 Chromium) and a wrapper at `~/.local/bin/ab`. Use it to
catch hydration / CORS / runtime errors that SSR HTML and curl
probes will miss:

```bash
ab open https://magic-indexer-dev.up.railway.app/graphiql
ab snapshot                       # accessibility tree with refs
ab screenshot /tmp/page.png
ab eval 'fetch("/health").then(r => r.status)'
ab close
```

This is how the certs-social integration found a CORS bug that
all static checks (TypeScript, Next.js build, lint) missed.
See AGENTS.md for the install instructions if `ab` isn't there
in a fresh session.

### "Why is this record hidden?"

If a record is missing from a `records(excludeLabels: [...])`
query and you suspect a label is hiding it, hit the diagnostic
endpoint that returns *every* label on a URI (active, negated,
expired, with provenance):

```bash
curl -H "Authorization: Bearer $ADMIN_API_KEY" \
  "https://magic-indexer-dev.up.railway.app/admin/label-chain?uri=at://did:plc:abc/app.bsky.feed.post/xyz"
```

### Admin GraphQL queries from the command line

```bash
ADMIN_API_KEY='<your-key>'
ADMIN_DID='did:plc:<your-did>'

curl -X POST https://magic-indexer-dev.up.railway.app/admin/graphql \
  -H "Authorization: Bearer $ADMIN_API_KEY" \
  -H "X-User-DID: $ADMIN_DID" \
  -H "Content-Type: application/json" \
  -d '{"query":"{ statistics { recordCount actorCount lexiconCount } }"}'
```

Or use the GraphiQL UI at https://magic-indexer-dev.up.railway.app/graphiql/admin
— the credentials bar at the top accepts the API key + DID and
persists them in `localStorage`.

---

## Rotating secrets

### `SECRET_KEY_BASE`

```bash
NEW=$(openssl rand -base64 64 | tr -d '\n')
railway variables --service magic-indexer --set "SECRET_KEY_BASE=$NEW"
```

Railway auto-redeploys. Every existing OAuth session is
invalidated; users will need to re-authorize.

### `ADMIN_API_KEY`

```bash
NEW=$(openssl rand -base64 32 | tr -d '\n')
railway variables --service magic-indexer --set "ADMIN_API_KEY=$NEW"
```

Save the new value in your password manager **before** the
redeploy completes — you cannot retrieve it from anywhere else
once the old key stops working.

### Railway API token

If your token has been exposed:

1. Open https://railway.com/account/tokens
2. Delete the old token
3. Create a fresh one with the same name
4. Update wherever the token was used (CI secrets, your local
   `~/.bashrc` / `~/.zshrc`, etc.)

---

## Common gotchas (lifted from the review history)

- **`SECRET_KEY_BASE` dev placeholder**: the literal string
  `development-secret-key-change-in-production-64chars` is rejected
  at startup by `config.Validate()`. Don't try to short-circuit
  this — generate a real key.
- **`HOST=127.0.0.1` vs `0.0.0.0`**: defaults to 127.0.0.1 for
  local dev so you don't accidentally expose to your LAN. Set
  `HOST=0.0.0.0` in any deployed environment.
- **HSTS only emits when `EXTERNAL_BASE_URL` starts with
  `https://`**: a deployed instance with `http://` in that env
  var will not send HSTS, by design.
- **Jetstream auto-discovers collections from registered lexicons.**
  If `JETSTREAM_COLLECTIONS` is unset and you upload lexicons
  via the admin API, the consumer dynamically restarts with the
  new collection list. If `JETSTREAM_COLLECTIONS` *is* set, that
  list is authoritative and lexicon uploads do not change it.
- **Activity log orphans**: if the process crashes between
  `LogActivity` and `UpdateStatus`, an `activity` row is left in
  `pending` state. The activity cleanup worker (in
  `internal/workers/`) flips these to `orphaned` after 10 min.
- **CORS wildcards**: `ALLOWED_ORIGINS` supports glob-style
  wildcard patterns (e.g. `https://certs-social-*.vercel.app`)
  to cover Vercel preview deployments without listing each one.
- **Takedown is opt-in**: a record with an active `!takedown`
  label is *not* hidden by default. Clients have to pass
  `excludeLabels: ["!takedown"]` explicitly. This is a deliberate
  product decision (the indexer is labeler-neutral). See the
  closed-but-deferred [issue #13](https://github.com/hb-agent/magic-indexer/issues/13).

---

## Admin client

A Next.js admin UI is deployed at https://magic-indexer-admin.vercel.app.
It uses confidential ATProto OAuth — sign in with any DID listed in
`ADMIN_DIDS`. The client talks to the same `/admin/graphql` endpoint
documented above; no separate API key is needed when using OAuth.

---

## `authors` filter

Typed collection queries accept `authors: [String!]` to scope results
to specific author DIDs. Usage:

```graphql
{ orgHypercertsClaimActivity(first: 10, authors: ["did:plc:..."]) { edges { node { uri } } } }
```

- Cap: 500 DIDs per query (returns an error if exceeded).
- Empty list (`authors: []`) = no filter, returns all authors.

---

## Field filters, sorting, and pagination

Typed collection queries support:

- **`where`** argument with per-field filter inputs generated from the lexicon's scalar properties. Operators: `eq`, `neq`, `gt`, `lt`, `gte`, `lte`, `in`, `contains` (min 3 chars), `startsWith`, `isNull`.
- **`_and` / `_or`** boolean composition inside `where`. Max nesting depth 3, global cap of 20 total conditions across the whole tree.
- **`orderBy`** (field name string) and **`orderDirection`** (`ASC` or `DESC`, default `DESC`). Works for scalar lexicon properties and the built-in `indexed_at` column. `NULLS LAST` is applied; URI appended as tiebreaker.
- **Forward pagination** via `first` + `after`, **backward pagination** via `last` + `before`. Mixed modes are rejected.
- **`totalCount`** — lazy, only computed when the client selects it.

Cursors are base64-URL-encoded JSON arrays (`["sortField", "sortValue", "uri"]`). The decoder accepts the legacy pipe-delimited format for backward compatibility but only when `orderBy` is the default `indexed_at`.

**Performance note:** `eq` uses JSONB containment (`@>`) which is served by the GIN `jsonb_path_ops` index. Comparison (`gt`/`lt`/etc.) and pattern (`contains`/`startsWith`) operators use `json->>'field'` extraction which is **not** GIN-indexed — they do sequential scans. For hot filter fields, create a partial expression index:

```graphql
mutation {
  createFieldIndex(collection: "org.hypercerts.claim", field: "createdAt") {
    success
    indexName   # e.g. "idx_record_org_hypercerts_claim_createdAt"
  }
}
```

This generates `CREATE INDEX CONCURRENTLY ON record ((json->>'field')) WHERE collection = 'nsid'`. Drop it with `dropFieldIndex(collection, field)`. Both mutations require admin auth.

Nested JSON paths are supported at the SQL layer via the `__` separator (e.g., `metadata__source` → `json->'metadata'->>'source'`), but auto-generating nested WhereInput fields from lexicons is deferred (#40).

---

## Endorsement lexicon

The `app.certified.temp.graph.endorsement` lexicon has been uploaded
and is ingested by Jetstream. It is used by the certs-social frontend
for the trusted-evaluator feed filter feature
(see [`docs/architecture/0001-trusted-evaluator-feed-filter.md`](architecture/0001-trusted-evaluator-feed-filter.md)).

---

## Deferred work

Issues with deliberate deferral reasoning attached:

- **[#10 — Labeler signature verification](https://github.com/hb-agent/magic-indexer/issues/10)**.
  Re-open when a labeler we ingest starts shipping cryptographic
  signatures against a stable scheme.
- **[#13 — GDPR hard-delete endpoint](https://github.com/hb-agent/magic-indexer/issues/13)**.
  Re-open when there's a real erasure request or a legal requirement.
- **[#39 — Multi-column sort](https://github.com/hb-agent/magic-indexer/issues/39)**.
  Single-column sort-aware keyset pagination is done. Multi-column requires
  ROW() comparisons with NULL handling and mixed-direction support — deferred
  until there's concrete demand.
- **[#40 — Auto-generating nested WhereInput fields](https://github.com/hb-agent/magic-indexer/issues/40)**.
  SQL layer supports nested path extraction via `__` separator. Schema builder
  walking nested object types is the remaining work.
- **[#41 — Tap signature verification](https://github.com/hb-agent/magic-indexer/issues/41)**.
  Defer until Tap is actually deployed.
- **[#42 — Multi-relay Tap](https://github.com/hb-agent/magic-indexer/issues/42)**.
  Defer until ATProto has multiple production relays; run multiple
  magic-indexer instances sharing one Postgres as the workaround.

Everything else from the review process either landed in the codebase
or is recorded as a closed issue with a fix commit reference.

---

## See also

- [`AGENTS.md`](../AGENTS.md) — package layout, code style, dev workflow
- [`SECURITY.md`](../SECURITY.md) — required env vars, reverse-proxy rate limits, admin auth contract
- [`docs/reviews/`](reviews/) — the 23-round overnight review history
