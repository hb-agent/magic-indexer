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

### Restart-on-exit contract (orchestrator dependency)

Lexicon upload triggers a full GraphQL schema rebuild. That rebuild
only happens on a fresh boot — `setupGraphQL()` runs once at
process start, not on demand. To rebuild, the upload handler
signals `serve()` to gracefully shut down and the process exits
with code **42** (`cmd/hypergoat/main.go:55-68`). The supervising
orchestrator is then expected to restart the process, which builds
the new schema from the updated lexicons.

**This means magic-indexer requires a restart-on-exit supervisor
in production.** Confirmed-good supervisors:

- **Railway** — restarts on any non-zero exit. Current production
  setup. Nothing extra to configure.
- **Docker / Docker Compose** with `restart: unless-stopped` or
  `restart: always`. Both work; exit 42 is treated as a normal
  failure and triggers the policy.
- **Kubernetes** with the default `restartPolicy: Always`. Any
  non-zero exit triggers a pod restart.
- **systemd** with `Restart=on-failure` or `Restart=always`.

**What breaks without such a supervisor.** Lexicon upload returns
`{"data":{"uploadLexicons":<count>}}` and looks like it succeeded.
The process then exits 42 and stays down. The service is offline
and the schema is never rebuilt. There is no in-process fallback —
hot-reload of the GraphQL schema is deliberately not attempted
because the `graphql-go` schema is immutable once built and
swapping it under live traffic would require holding requests
through a full re-resolution.

Code 42 is chosen to be distinguishable from other exit codes in
logs and dashboards — operators can alert on "exit 42 without a
preceding restart" as a sign the orchestrator is misconfigured.

If you are evaluating a new deploy target, the test is: post a
trivial lexicon upload, verify the process exits 42, and verify
it comes back up within your acceptable downtime window. If it
doesn't come back, no other feature works either.

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

### Recovering from an INVALID `CONCURRENTLY` index

Several migrations (026, 029, 030, plus any `createFieldIndex` mutation) use `CREATE INDEX CONCURRENTLY IF NOT EXISTS`. The `IF NOT EXISTS` guard normally protects against double-runs, but it has a foot-gun: if the initial `CONCURRENTLY` build fails partway through (lock acquisition timeout, signal, replica lag), Postgres leaves a `INVALID` index entry in `pg_index`. The next `CREATE INDEX CONCURRENTLY IF NOT EXISTS` then SEES the INVALID entry, treats it as "already exists," and skips creation. The migration completes successfully, the planner refuses to use the INVALID index, and queries silently degrade to sequential scans.

Detection: connect via `railway connect Postgres` and run
```sql
SELECT i.relname, ix.indisvalid
FROM pg_class i
JOIN pg_index ix ON i.oid = ix.indexrelid
WHERE i.relname LIKE 'idx_record_%' AND NOT ix.indisvalid;
```
Any rows returned are stuck INVALID indexes.

Recovery (one-shot, online — safe to run against production):
```sql
DROP INDEX CONCURRENTLY <name>;   -- e.g. idx_record_award_badge_uri
```
Then re-run the migration via `make db-migrate` (which is a no-op for migrations already at the latest version — for an admin-mutation index, re-issue the `createFieldIndex` call).

Both DROP and CREATE must be `CONCURRENTLY` to avoid taking an exclusive lock on the `record` table; both must also run outside a transaction (`-- no-transaction` directive on the migration file, or via a direct connection).

---

## Notifications

Enable per-service via Railway env var:
```
NOTIFICATIONS_ENABLED=true
```

When enabled:
- A `RecordHook` is attached to `RecordProcessor.RecordHooks` with `HookLogContinue` policy. A failing extractor cannot stall firehose ingestion.
- Three GraphQL fields are merged into `/admin/graphql`: `notifications`, `unreadNotificationCount`, `updateNotificationsSeen`.
- Migration 015 creates `notification`, `notification_participant`, `actor_state`.

### Reason catalog (v1)

| Reason | Collection | Recipient | Aggregation |
|---|---|---|---|
| `endorsement` | `app.certified.temp.graph.endorsement` | `subject.did` | Yes — collapse by `endorsement:<subject.uri>` |
| `activity-contributor` | `org.hypercerts.claim.activity` | each `contributors[].contributorIdentity` if it's a DID string | No — each activity is a distinct notification per contributor |

### Caps and limits

| Constant | Value | Purpose |
|---|---|---|
| `MaxFanOutPerRecord` | 100 | Maximum notifications from a single record |
| `UnreadCountCap` | 50 | Unread badge shows "50+" above this |
| `SortAtMaxPast` | 7 days | `sort_at` clamped if record's `createdAt` is older |
| `MaxRecordBytesForNotifications` | 1 MiB | Records above this size are not processed for notifications |
| `MaxReasonSubjectBytes` | 512 | Cap on `reason_subject` value |
| `MaxContributorsBeforeReject` | 200 | Shallow JSON scan rejects before full unmarshal |

### Rollout

1. Merge the feature and deploy. Flag defaults to false — nothing happens.
2. Flip `NOTIFICATIONS_ENABLED=true` in the **staging** Railway service.
3. Monitor for 1 hour:
   - `hypergoat_notifications_hook_errors_total` rate stays low
   - `hypergoat_notifications_hook_panics_total` stays zero
   - Manual spot-check: create a test endorsement, verify a notification row appears via admin GraphQL
4. After 24-hour staging soak, flip the flag in the **prod** Railway service.
5. Run `ANALYZE notification` after first significant insert volume to populate planner stats for the partial unique index.

### Retention

Notifications older than 90 days are deleted hourly by a background worker. The worker uses a Postgres advisory lock (`pg_try_advisory_lock(7392745193)`) so only one indexer pod runs the cleanup at a time.

### Known limitations

- No per-notification mark-as-read (watermark only).
- No push / email / preferences / activity subscriptions.
- No historical backfill — the launch-day feed is empty until new records are indexed.
- `contributorIdentity` as a strongRef (not a plain DID) is not handled; only string DIDs produce notifications.
- Hook runs in a separate connection from record insert — a record can be indexed without its notification landing if the hook fails. Idempotent replay (unique `(record_uri, recipient_did)` on participant) makes this self-healing on re-ingestion.

### Client integration

See `hypercerts-org/certs-social#66` for the `/notifications` UI implementation plan. `hypercerts-org/certs-social#68` ported the client to AT Protocol service-auth (issue #57) and removed the admin-key path on that side.

---

## Service-auth on `/notifications/graphql` (issue #57)

### What it is

`/notifications/graphql` is mounted when `DOMAIN_DID` is set. Every request carries a per-user AT Protocol service-auth JWT; the indexer reads the acting DID from `iss` instead of a GraphQL arg. No shared admin key on this path.

### Switching it on

1. Set `DOMAIN_DID` on Railway (e.g. `did:web:magic-indexer-dev.up.railway.app`).
2. `railway up` to redeploy. Watch boot log for `Notifications XRPC endpoint enabled`.
3. Smoke test: `curl https://<host>/.well-known/atproto-did` returns the DID as `text/plain` (did:web: only; did:plc: publishes via PLC directory, not us).
4. Mint a synthetic JWT and hit the endpoint once — watch `hypergoat_service_auth_verified_total{lxm="com.hypergoat.notification.query"}` tick.

### Switching it off (rollback)

Unset `DOMAIN_DID` and redeploy — the endpoint unmounts. Clients fall back to `/admin/graphql`.

### Diagnosing a 401 spike

The `hypergoat_service_auth_rejected_total{reason,lxm}` metric carries one of the 18 reasons below. Map to operator action:

| `reason` | Likely cause | Action |
|---|---|---|
| `missing_header`, `malformed_header` | Client isn't attaching `Authorization: Bearer …` or mangled the header | Check the client; not our bug |
| `expired`, `future_iat` | Client/server clock skew > 30s | Check PDS / client clock; tolerance is tight |
| `bad_audience` | `DOMAIN_DID` mismatch | Client minted with wrong `aud`; confirm the client's env var matches our deploy |
| `bad_lxm` | Client minted without or with wrong `lxm` | Client bug — must pass `lxm: com.hypergoat.notification.query` to `getServiceAuth` |
| `missing_jti` | Token has neither `jti` nor `iat` | Client minter too minimal; one of the two is required |
| `unsupported_alg`, `unsupported_did_method`, `verification_method_not_found`, `malformed_multibase`, `key_parse_failed` | Key discovery / crypto mismatch | Usually a rotated key or non-standard DID doc; inspect `iss` DID doc |
| `did_resolve_timeout`, `did_resolve_network`, `did_resolve_not_found` | PLC / did:web upstream issue | Check PLC status page and the specific `iss` DID's PDS |
| `did_resolve_unavailable` | Reserved for the deferred serve-stale path | Not fired in current impl |
| `bad_signature` | Forged or corrupted token, OR key rotated since cache last refreshed | Correlate to one `iss` — frequent single-DID hits suggest rotation, broad spray suggests abuse |
| `replay` | Same `(iss, jti)` seen twice inside the 60s window | Either a legit retry inside the window, or a replay attack — check call patterns |
| `throttled` | Reserved for the deferred resolver throttle | Not fired in current impl |
| `too_large` | Token > 8 KiB | Client bug — service-auth JWTs should be well under 2 KiB |
| `other` | Catch-all for unexpected errors | Page on-call; grep logs for `service-auth rejected` with the timestamp |

### Rejection log

Every 401 emits a `slog.Warn` with `reason`, `lxm`, and the wrapped error message (never the token body). Log search:

```
jsonPayload.message:"service-auth rejected"
```

### Deferred hardening (not yet wired, sentinels exist)

- Per-`iss` + client-IP token-bucket throttle on DID resolution
- Negative cache (5s network / 30s not-found)
- Serve-stale on PLC outage (60s cap, disabled after bad-signature bust)
- Key-rotation retry on `bad_signature`
- `caller_hash` metric label on `notifications_request_total`
- Persistent `jti` store before scaling past one replica

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

### Notifications follow-ups

- Per-notification mark-as-read (`dismissed_at` column)
- Push notifications (mobile + web)
- Email digests
- Per-user preferences / muting
- Activity subscriptions ("notify me when X posts")
- Same-transaction hook (requires repo tx refactor)
- Count-drift reconciler
- Top-N authors (`latest_authors text[]`) for "Alice, Bob, and 3 others" rendering
- Public-endpoint migration when OAuth auth lands on `/graphql`
- `contributorIdentity` strongRef resolution

Everything else from the review process either landed in the codebase
or is recorded as a closed issue with a fix commit reference.

---

## See also

- [`AGENTS.md`](../AGENTS.md) — package layout, code style, dev workflow
- [`SECURITY.md`](../SECURITY.md) — required env vars, reverse-proxy rate limits, admin auth contract
- [`docs/archive/reviews/`](archive/reviews/) — the 23-round overnight review history
