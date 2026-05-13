# Security notes for operators

This document collects the pieces of the deployment story that live
outside the binary — things the process cannot enforce on its own
because they belong at the reverse proxy, the load balancer, or the
platform. Everything in here is **required for a production deploy**.

## Required environment

These must be set before the process starts. `docker-compose.yml`
and `docker-compose.postgres.yml` fail fast (`:?` form) if the
first is missing; the rest are validated inside `config.Validate()`.

| Variable | Purpose | Notes |
|---|---|---|
| `SECRET_KEY_BASE` | Session / cookie signing key | ≥64 bytes. Generate with `openssl rand -base64 64`. The literal `development-secret-key-change-in-production-64chars` placeholder is rejected by Validate at startup. |
| `DATABASE_URL` | `sqlite:/path/…` or `postgres://…` | Validate the URL does not enable `sslmode=disable` in production. |
| `ADMIN_API_KEY` | Bearer token for `/admin/*` | Required for any admin access. Paired with `X-User-DID` header for audit. |
| `ADMIN_DIDS` | Comma-separated admin DIDs | Every entry is validated against the canonical strict `internal/atproto/did.IsValid` predicate — the only DID input-validation gate in the codebase. The earlier prefix-only `oauth.HasDIDMethodPrefix` was removed in this PR; the strict predicate now guards every input path (admin endpoints, labeler reset/pause, service-auth JWT issuer, audit headers). |
| `EXTERNAL_BASE_URL` | Public-facing `https://…` URL | HSTS is only emitted when this starts with `https://`. |
| `ALLOWED_ORIGINS` | CORS allow-list | Do **not** leave unset in production — the default "allow all" is for local dev only. |
| `LABELER_DIDS` | Comma-separated labeler DIDs | Empty disables labeler ingestion. |
| `DOMAIN_DID` | Indexer's own DID (`did:web:<host>` or `did:plc:…`) | Required to enable the `/notifications/graphql` service-auth endpoint (issue #57). Malformed values fail `Validate()` at startup; unset means the endpoint stays unmounted. Every service-auth JWT is validated against this as the `aud` claim. |
| `OAUTH_LEGACY_DPOP_JKT_CUTOFF` | Unix timestamp of the DPoP binding deploy | Required. Refresh tokens issued before this timestamp are accepted unbound; tokens after must carry a matching DPoP JKT. |
| `DB_STATEMENT_TIMEOUT_MS` | Pool-level Postgres `statement_timeout` (Layer 1 of issue #71). Default 30000. | Injected into every connection in the pool via `options=-c statement_timeout=`. Server-side hard kill — runs regardless of client liveness. Must be strictly greater than `GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS` (enforced at startup). If the operator already sets `statement_timeout` in `DATABASE_URL`, that value is preserved and the default is skipped (logged at INFO). `PGOPTIONS` env-var is overridden by the URL — operators relying on `PGOPTIONS` for `statement_timeout` must move the value to `DATABASE_URL`. |
| `GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS` | Per-request budget on `/graphql` (Layer 2 of issue #71). Default 5000. | Installed by middleware on the public endpoint only. `/admin/graphql` and `/graphql/ws` are bounded by Layer 1 instead. Timed-out queries return HTTP 200 with a `QUERY_TIMEOUT` GraphQL error and the `X-Query-Timeout` response header. |

## Query budgets (issue #71)

The indexer caps DB query duration at two layers, both fail-safe:

- **Layer 1 — `DB_STATEMENT_TIMEOUT_MS`** (default 30 s). Postgres-side
  `statement_timeout` injected on every connection. Catches truly
  stuck queries even when client-side cancellation fails (network
  blip, pgx misbehaviour). Applies to **every** path — public, admin,
  subscriptions, Jetstream.
- **Layer 2 — `GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS`** (default 5 s). HTTP
  middleware on `/graphql` wraps the request context with a tighter
  deadline. The handler shapes the response when the deadline fires:
  HTTP 200, header `X-Query-Timeout: <budget-ms>`, body with a
  `QUERY_TIMEOUT` GraphQL error including `extensions.budgetMs` and
  `extensions.retryable=false`.

**Operator contract:**

1. `DB_STATEMENT_TIMEOUT_MS` **must** be strictly greater than
   `GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS`. Enforced by `config.Validate()`
   at startup.
2. Reverse-proxy read timeout (`proxy_read_timeout` for nginx,
   `read_timeout` for Caddy, etc.) **must** be strictly greater than
   `GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS`. If the proxy times out first,
   the in-process budget never fires — you lose the metric signal
   (`hypergoat_graphql_query_timeout_total`), Layer 1 still bounds DB
   query duration but the client sees a generic proxy 504 instead of
   the canonical `QUERY_TIMEOUT` shape.
3. Tune Layer 2 from `httpRequestDuration{route="/graphql"}` p99 in
   `/metrics`.
4. Recommended Prometheus alert:
   `rate(hypergoat_graphql_query_timeout_total[5m]) > N` where N is
   your acceptable timeout rate.

## Rate limiting (**must** be handled at the reverse proxy)

The application intentionally does **not** implement in-process
rate limiting. Rate limiting in a long-running Go app competes with
every sliding-window / token-bucket implementation in every
reverse-proxy tier you already have. Pick one and enforce it there.

Recommended limits when Magic Indexer sits behind nginx, Caddy,
Traefik, HAProxy, Cloudflare, or a Vercel / Railway edge (the
`proxy_pass http://hypergoat` examples below reference whatever
upstream name you've chosen in your own reverse-proxy config —
the historical binary name is just the convenient label):

| Route | Limit | Reasoning |
|---|---|---|
| `POST /oauth/token` | 10 req / min / IP | Prevents refresh-token brute force. |
| `POST /oauth/authorize` | 30 req / min / IP | Noisy login flows still work; scripted enumeration does not. |
| `POST /oauth/register` | 5 req / min / IP | Client registration is a one-time operation. |
| `POST /graphql` | 300 req / min / IP | Generous; GraphiQL auto-complete fires frequent small queries. |
| `GET /graphql/ws` | 5 concurrent / IP | Each connection still caps at 64 subscriptions (enforced in-process). |
| `POST /admin/graphql` | 60 req / min / IP | Admin UIs are bursty; a single admin should never exceed this. |
| `POST /admin/labeler/*` | 10 req / min / IP | Incident-response endpoints. |
| `GET /admin/label-chain` | 60 req / min / IP | Diagnostic reads. |

### Example: nginx

```nginx
limit_req_zone $binary_remote_addr zone=oauth_token:10m rate=10r/m;
limit_req_zone $binary_remote_addr zone=graphql:10m rate=300r/m;
limit_req_zone $binary_remote_addr zone=admin:10m rate=60r/m;

location = /oauth/token     { limit_req zone=oauth_token burst=5 nodelay; proxy_pass http://hypergoat; }
location   /graphql         { limit_req zone=graphql     burst=50 nodelay; proxy_pass http://hypergoat; }
location   /admin/          { limit_req zone=admin       burst=20 nodelay; proxy_pass http://hypergoat; }
```

### Example: Caddy

```caddy
@oauth_token path /oauth/token
@graphql    path /graphql /graphql/*
@admin      path /admin/*

rate_limit @oauth_token 10r/m 5
rate_limit @graphql     300r/m 50
rate_limit @admin       60r/m 20
```

## HTTPS enforcement

The application emits `Strict-Transport-Security` **only** when
`EXTERNAL_BASE_URL` starts with `https://`. Behind TLS termination
at a proxy, set the env var accordingly and the process will do the
right thing.

Never run Magic Indexer on a public port without TLS — the OAuth flow,
admin API, and DPoP proofs all assume a trusted transport.

## Service-auth surface (`/notifications/graphql`, issue #57)

When `DOMAIN_DID` is set, `/notifications/graphql` is mounted behind a service-auth JWT middleware. There is no shared secret on this path — every request carries a per-user JWT minted by the caller's PDS.

- `alg` allowlist is `ES256` and `ES256K`. `alg=none` and HS256 are rejected before key resolution.
- `aud` must equal `DOMAIN_DID`. `lxm` must equal `com.hypergoat.notification.query`. Tokens missing both `jti` and `iat` are rejected (replay-key construction requires at least one).
- `exp` cannot be more than 60s past `now`; `iat` cannot be more than 30s in the future.
- Replay cache is bounded (100k entries) and evicts by nearest-expiry under pressure. Documented trade-off: a sustained burst beyond capacity can reopen a narrow replay window for an already-seen token. Move to Postgres-backed storage before scaling past one replica.
- `WWW-Authenticate: Bearer error="invalid_token"` is returned on failure. No `error_description` — rejection reasons are exposed only via the `hypergoat_service_auth_rejected_total{reason,lxm}` metric.
- `/.well-known/atproto-did` is served only when `DOMAIN_DID` is `did:web:<ourHost>`; did:plc publishes to the PLC directory, not here.
- Deferred: resolver throttle, negative cache, serve-stale on PLC outage, bad-signature retry. Sentinels and metric helpers exist but aren't wired yet — see AGENTS.md.

## Admin surface

- `/admin/graphql` is **POST-only**. GET is rejected with 405 to
  avoid leaking mutation variables into access logs and proxy
  caches.
- `/admin/graphql` requires **either** a validated OAuth user or a
  valid `ADMIN_API_KEY` bearer token + `X-User-DID` header.
- `/admin/labeler/reset`, `/admin/labeler/pause`, and
  `/admin/label-chain` all require the `ADMIN_API_KEY` bearer
  token (constant-time compare) and validate `did` format before
  use.
- Admin mutation logs redact variable **values**; only operation
  name and variable keys are logged. Do not re-introduce value
  logging without an audit.

### Statistics / activity endpoints

The GraphQL fields `statistics`, `activityBuckets`, `recentActivity`,
and `collectionOverview` are **admin-gated** in this codebase. They
return aggregate operational data (record / actor counts, recent
ingestion events, per-collection sizes) that we do not consider safe
to expose anonymously. Adopters coming from upstream projects with
laxer defaults should not assume these are publicly readable here —
they are not, and exposing them is a deliberate operator choice, not
a one-line config flip.

### Actor purge (`previewPurgeActor` / `purgeActor`)

Two admin mutations support GDPR-style takedowns and test-data
cleanup. Both are gated by the existing admin auth (above):

- `previewPurgeActor(did)` returns counts plus an HMAC-signed
  `confirmToken` bound to (requesting admin DID, target DID,
  record count at preview time, expiry). The signing key is
  `SECRET_KEY_BASE`; the TTL is 5 minutes; the token is
  single-use, enforced by signature in an **in-memory** set
  that is cleared on process restart. Within the 5-minute TTL
  window after a crash, a captured token could be redeemed
  once more — still bound to the same admin DID, target DID,
  and count. The TTL is the hard replay bound.
- `purgeActor(did, confirmToken)` re-counts records before
  verifying the bound token (so count drift between preview
  and purge rejects the token), then commits a SQL-only
  transaction deleting records and the actor row. Tap state is
  removed best-effort *after* the commit because Tap is an
  HTTP sidecar and cannot enlist in `sql.BeginTx`; a Tap
  failure does not roll back the SQL commit. The count-drift
  defense assumes no concurrent attacker-controlled writes to
  the target's records — out of threat model.

Every successful purge emits a structured log line:

```
event=actor_purge actor_did=… target_did=… record_count=… \
requested_by_did=… tap_status=removed|failed|skipped ts=…
```

**Operator contract**: this log line is the audit trail. Configure
your log shipper to retain `event=actor_purge` lines for **at
least 90 days** (GDPR-minimum) and prefer **one year** if you
anticipate takedown disputes. There is no DB-side audit table by
design — losing the log line means losing the audit. **A process
crash between the SQL commit and the `slog` call can also lose
the audit line** (low probability, no auto-recovery); a client-side
retry will see `recordsDeleted=0` and write its own audit, which
is how an operator detects this case.

The rate limit on `/admin/graphql` (see "Rate limiting" above —
default 60 req/min/IP) also governs `purgeActor` and is intended
to bound fat-finger damage. No additional limit is wired
specifically for purge; tighten the proxy rule if your threat
model requires it.

## Metrics

`/metrics` is unauthenticated and exposes Prometheus text format.
Every label is bounded — no user-controlled cardinality. Operators
who want to gate `/metrics` should do it at the reverse proxy.

## Reporting a vulnerability

If you find a security issue, open a private GitHub security
advisory on the repository or contact the maintainer out of band.
Do not file a public issue.
