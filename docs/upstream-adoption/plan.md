# Indexer improvements — eight tracks → seven, per round-1 review

**Status**: PLAN (frozen — see `review-round-1.md` and `review-round-2.md` for decisions).
**Drives from**: [`docs/upstream-pull-candidates.md`](../upstream-pull-candidates.md) (audit of `hypercerts-org/hyperindex` feature branches, 2026-05-13).
**Target branch**: `feat/upstream-adoption` (cherry-picked from earlier work after upstream `staging` reset; see PR description).
**Engagement with Hyperindex**: none. Their public source is reference material; we ship our own work.

> **Round-1 amendments**: Track A (NonNull coercion) dropped — our existing `SanitizeRecord` skip policy is better than the reference's coerce policy. Track D rewritten to reuse `PUBLIC_URL` instead of inventing `PUBLIC_CLIENT_URL`. Track E substantially revised: HMAC-bound 300s confirm-token, SQL-only purge transaction with best-effort post-commit Tap removal (Tap can't enlist in `sql.BeginTx`). Track G gates on `session.did ∈ settings.adminDids`, not a `NEXT_PUBLIC_ADMIN_DIDS` env var. Track H file path corrected to `client/src/lib/env.ts`. Commit order reshuffled to G → E → F so destructive UI never lands ahead of its gate.

---

## 1. Larger goal this serves

Seven specific changes were identified by reading public source code that runs in roughly the same shape as ours. One is a GraphQL correctness bug, two are deployment paper-cuts, one is a meaningful operator capability (actor purge), three are admin-client polish. All are small or well-bounded; none are speculative.

Two outcomes:

1. **Correctness.** Track B fixes mixed unions where one variant is a zero-property `ObjectDef`. Track D fixes the OAuth callback redirect when the admin client is behind a reverse proxy.
2. **Operability.** Track E ships a first-class actor purge — replaces SQL-by-hand for takedowns / GDPR / test cleanup. Track C kills an `EXTERNAL_BASE_URL` paper-cut. Tracks F / G / H polish the admin client.

---

## 2. Scope

**One PR.** Atomic commits per track so each can be reverted independently. Commits land in the order: B → C → G → E → F → D → H → docs(security) → round-2 polish.

### Tracks

| # | Track | Files |
|---|---|---|
| **B** | Mixed-union zero-property `ObjectDef` | `internal/graphql/types/object.go`, `internal/graphql/types/types_test.go` |
| **C** | `EXTERNAL_BASE_URL` normalization | `internal/config/config.go`, `internal/config/config_test.go`, `cmd/hypergoat/main.go` (HSTS check) |
| **D** | OAuth callback reuses existing `PUBLIC_URL` | `client/src/app/api/oauth/callback/route.ts` |
| **E** | Admin actor purge: preview → HMAC-bound confirm → SQL-only purge → best-effort Tap | server: `internal/graphql/admin/{purge.go,purge_test.go,resolvers,schema,types}.go`, `internal/database/repositories/{actors,records}.go`, `cmd/hypergoat/main.go`; client: `client/src/app/settings/page.tsx`, `client/src/lib/graphql/mutations.ts`, `client/src/types/index.ts` |
| **F** | Batch lexicon registration | `client/src/app/lexicons/page.tsx` |
| **G** | Settings UI admin gate | `client/src/app/settings/page.tsx`, `client/.env.example` (admin bootstrap note) |
| **H** | Fail fast on self-referential backend URL | `client/src/lib/env.ts` |

---

## 3. Decisions

1. One PR, atomic commits per track.
2. No engagement with Hyperindex. Reference commits cited as breadcrumbs only.
3. Track A dropped (see audit doc).
4. Track E confirm token: HMAC-signed payload `(admin_did, target_did, record_count, exp)`, 300s TTL, single-use enforced via in-memory used-token set.
5. Track E purge transaction: SQL-only. Tap removal best-effort after commit.
6. Track E audit: structured logs only; ≥90d retention recorded in `SECURITY.md`.
7. Track E rate limit: existing 60 req/min/IP on `/admin/graphql`.
8. Track H: refuse in dev, warn in production.
9. Track G: gate on `session.did ∈ settings.adminDids`. No `NEXT_PUBLIC_ADMIN_DIDS`.
10. Track D: reuse `PUBLIC_URL`. No `PUBLIC_CLIENT_URL`.
11. Statistics / activity endpoints remain admin-only. Recorded in `SECURITY.md`.

---

## 4. Acceptance criteria

Each track is gated on:
- `go build ./...`, `go vet ./...`, `golangci-lint run ./...` clean.
- `go test -race ./...`: same packages green as the pre-implementation baseline (see `baseline.md`).
- `npx tsc --noEmit` clean for the client.

See `review-round-2.md` for the detailed acceptance items applied as a final polish commit.

---

## 5. Out of scope

- Track A (NonNull coercion) — dropped; constraints for any future revisit documented in the audit doc.
- P3 items from the audit (public statistics, GraphiQL relative paths, CodeRabbit, env-var renames, golangci-lint v2 upgrade).
- Beads tooling.
- Purge audit-trail database table — structured logs only.
- Database migrations — none.

---

## 6. Rollback

Each track is one atomic commit; revert it individually. No DB migrations → no schema rollback.

---

## 7. Next steps

PR `feat/upstream-adoption` → `staging`. CI green. Operator merges.
