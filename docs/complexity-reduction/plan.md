# Complexity reduction — May 2026

**Status**: Approved (after review-round-1). Implementation in progress on `staging`.
**Date**: 2026-05-17.

## Larger goal

Reduce surplus complexity that has accreted across the project's first three months without changing observable behavior. Targets are pure-deletion or single-call-site collapses with zero or near-zero risk. Out of scope: any change to GraphQL schema, OAuth wire shape, public CORS behavior, or the ingestion contract that consumers depend on.

## Scope — 8 changes

Each is implementable as one atomic commit; they are independent.

| # | What | File(s) | LOC delta | Risk |
|---|---|---|---|---|
| 1 | Delete `database.Row` / `database.Rows` wrappers | `internal/database/executor.go:134-162` | −30 | low |
| 2 | Delete `GetByCollectionWithLabelFilterAndKeysetCursor` + its 6 dedicated tests | `internal/database/repositories/records.go:705-719`, `records_labels_test.go` | −160 | low |
| 3 | Delete `/xrpc/*` 501 placeholder | `cmd/hypergoat/main.go:610-620` | −11 | low (404 instead of 501 for `/xrpc/<unmatched>`) |
| 4 | Inline 3 single-call helpers in `internal/server/` | `oauth_dpop_nonce.go`, `oauth_par.go`, `graphiql.go` | −16 | low |
| 5 | Collapse `RecordHooks []RecordHook` to `Hook *RecordHook` | `internal/ingestion/processor.go`, `cmd/hypergoat/main.go:1128` | −5 | low |
| 6 | Centralize comma-list parsing as `config.SplitCSV` | `internal/config/config.go`, 4 sites in `cmd/hypergoat/main.go` | −10 | low |
| 7 | Unify DIDResolver construction; delete dead PLC override mechanism | `cmd/hypergoat/main.go`, `cmd/backfill_pds/main.go`, `internal/database/repositories/config.go`, `config_test.go` | −80 | low |
| 8 | Archive superseded docs (REVIEW-Feb5, IMPLEMENTATION_PLAN, `docs/reviews/`) | `docs/archive/`, `AGENTS.md`, `docs/RUNBOOK.md` | move | low |

Total: ~310 LOC out, ~25 LOC in. Net −285 LOC of code, plus ~100 KB docs moved under `docs/archive/`.

## Alternatives considered

- **Collapse the notifications `Registry` plugin pattern into a switch** (`internal/notifications/registry.go` + `service.go`). Rejected by operator: more notification patterns are expected, the plugin shape is load-bearing for that direction.
- **Delete `populateActivityIfEmpty` boot-time backfill** (`cmd/hypergoat/main.go:301,308-334,1517-1535`). Rejected by operator: more migrations may need a similar shape; the helper is cheap on warm DBs (two `SELECT count(*)`), keeping it preserves option value.
- **Move AUDIT_REPORT to `docs/archive/`** (originally part of change 8). Rejected: the audit has three unresolved items (`F-DEP-001` x/crypto CVE, `F-LABELER-001` label signature verification, `F-DOS-001` WebSocket subscription DoS). Archival would mis-signal as "superseded".
- **Rewrite `didWebHost` to use `strings.HasPrefix`/`TrimPrefix`** (`cmd/hypergoat/main.go:1471-1477`). Skipped: stylistic only, not a complexity win.
- **Delete OAuth interface abstractions** (`AccessTokenStore`, `JTIStore`, `ServiceAuthDIDResolver`, `TapRemover`, tap `Connection`/`Dialer`/`EventHandler`). Skipped: each is exercised by tests via substitution.

## Acceptance criteria

- All four quality gates green: `go build ./...`, `go vet ./...`, `go test -race ./...`, `golangci-lint run ./...`.
- `scripts/lint-no-did-prefix.sh` still passes.
- `make test` exits 0 (Postgres-backed tests included when `TEST_DATABASE_URL` is set).
- No new entries appear in `git diff staging origin/main` for unrelated packages.
- AGENTS.md and RUNBOOK.md links to archived docs are updated.
- Public HTTP response shapes unchanged for `/health`, `/stats`, `/graphql`, `/admin/graphql`, `/oauth/*`, `/notifications/graphql`.

## Out of scope

- Any change to lexicon parsing, schema generation, ingestion semantics, or GraphQL response shape.
- Renaming `hypergoat` binary or Go module path (AGENTS.md says no).
- Touching the notifications extractor abstraction.
- Touching the activity-table boot-time backfill helper.
- Touching `didWebHost` style.
- Any change to OAuth, DPoP, PAR, or service-auth wire protocol.

## Rollback plan

Each change is a separate commit on `staging`. Per-change revert is `git revert <sha>` on `staging` followed by push. The Draft PR `staging → main` is not merged until the operator approves, so production is unaffected during iteration.

## Open questions

None — review-round-1 resolved all of them. See `review-round-1.md`.
