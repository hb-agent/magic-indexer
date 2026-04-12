# Fixed 20-perspective roster for rounds 11–18

Each of these perspectives runs in every one of the 8 additional rounds.
Each round starts from fresh context. The roster does not change.

1.  **Labeler ingest** — client frame decoding, consumer upsert, cursor persistence, backfill handoff.
2.  **Jetstream ingest** — event parsing, commit handling, cursor advance, reconnect loop.
3.  **Records repository** — Insert/BatchInsert, keyset pagination, label-filter SQL, dialect correctness.
4.  **Labels repository** — Insert/InsertNegation, GetByURIs, HasTakedown, GetTakedownURIs, exp handling.
5.  **Label definitions** — composite (src, val) PK, ensureDefinition race, idempotency.
6.  **Migrations** — up/down symmetry, transactional application, idempotency, cross-dialect parity.
7.  **DB layer** — connection pool, WAL, busy timeout, placeholder generation, Dialect() branching.
8.  **GraphQL public schema** — resolver wiring, cursor encoding, non-null lists, batch-load paths.
9.  **GraphQL admin schema** — auth gating, mutation input validation, POST-only, pagination clamps.
10. **OAuth flow** — authorize, callback, token exchange, PAR, register, refresh rotation, PKCE.
11. **OAuth middleware + DPoP** — Bearer + DPoP proof, replay detection, nonce, alg pinning.
12. **DID resolution** — did:plc, did:web, redirect handling, private-host rejection, response bounds.
13. **HTTP server + middleware** — CORS, security headers, request timeouts, body caps, graceful drain.
14. **WebSocket subscriptions** — upgrade, idle timeout, max subs per client, fan-out, origin check.
15. **cmd/hypergoat lifecycle** — startup ordering, background services, tracked cancel contexts, Stop path.
16. **Config + env** — validation, malformed input handling, defaults, log redaction, docs drift.
17. **Lexicon registry + resolver** — parse, register, ref resolution, cycles, concurrent reads.
18. **Workers + backfill** — activity cleanup, oauth cleanup, DPoP cleanup, DID cache cleanup, backfill state.
19. **Observability + logs** — slog field naming, PII / secret leakage, log injection, error wrapping.
20. **Build / deploy / deps** — go.mod hygiene, Dockerfile, CI workflows, migration embed, reproducibility.
