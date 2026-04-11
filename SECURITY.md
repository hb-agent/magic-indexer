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
| `ADMIN_DIDS` | Comma-separated admin DIDs | Every entry is validated as a valid DID shape by `UpdateSettings`. |
| `EXTERNAL_BASE_URL` | Public-facing `https://…` URL | HSTS is only emitted when this starts with `https://`. |
| `ALLOWED_ORIGINS` | CORS allow-list | Do **not** leave unset in production — the default "allow all" is for local dev only. |
| `LABELER_DIDS` | Comma-separated labeler DIDs | Empty disables labeler ingestion. |

## Rate limiting (**must** be handled at the reverse proxy)

The application intentionally does **not** implement in-process
rate limiting. Rate limiting in a long-running Go app competes with
every sliding-window / token-bucket implementation in every
reverse-proxy tier you already have. Pick one and enforce it there.

Recommended limits when hypergoat sits behind nginx, Caddy, Traefik,
HAProxy, Cloudflare, or a Vercel / Railway edge:

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

Never run hypergoat on a public port without TLS — the OAuth flow,
admin API, and DPoP proofs all assume a trusted transport.

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

## Metrics

`/metrics` is unauthenticated and exposes Prometheus text format.
Every label is bounded — no user-controlled cardinality. Operators
who want to gate `/metrics` should do it at the reverse proxy.

## Reporting a vulnerability

If you find a security issue, open a private GitHub security
advisory on the repository or contact the maintainer out of band.
Do not file a public issue.
