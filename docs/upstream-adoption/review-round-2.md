# Implementation review — round 1 (post-implementation)

**Plan**: [`plan.md`](./plan.md)
**Plan review**: [`review-round-1.md`](./review-round-1.md)
**Date**: 2026-05-13
**Reviewers**: three parallel lenses on the committed diff — Go correctness + Track E semantics; destructive-op safety + security; client UX correctness.

Quality gates at review time: `go build`, `go vet`, `golangci-lint` clean; `go test -race` passes everywhere except the postgres-dependent packages (same set as `baseline.md`).

---

## Real defects found

### D.1 OAuth callback: `returnTo` open-redirect via `//`

> Reviewer (security): `"//evil.com".startsWith("/")` is `true`, so `${origin}//evil.com` would scheme-relatively redirect to `evil.com`. `returnTo` is server-set today, but a defensive check is the right shape.

**ACCEPT.** Tighten to `returnTo.startsWith("/") && !returnTo.startsWith("//")`.

---

## Cosmetic / quality-of-life items accepted

### Schema-level `requireAdmin` shim for parity

> Reviewer (Go): The two new purge mutations rely solely on resolver-level `requireAdmin`. Every other admin mutation also has the shim at the schema-resolver layer. Inconsistent → foot-gun for a future refactor.

**ACCEPT.** Belt + braces.

### Malformed `tokenExpiresAt` shows wrong error

> Reviewer (UX): `new Date("garbage").getTime()` is `NaN`, the countdown collapses to 0 and surfaces "Token expired" — wrong message.

**ACCEPT.** Detect `Number.isNaN(expiresAtMs)`; surface "Invalid token response — preview again".

### Operator-readable purge error codes

> Reviewer (UX): Stale-token errors surface raw GraphQL sentinels.

**ACCEPT.** Map the three sentinels to human-readable copy in `onError`.

### `aria-live` on the per-second countdown

**ACCEPT.** Drop. The preview card carries the same info without per-second SR announcements.

### `aria-live` on the batch lexicon list

**ACCEPT.** Move to a small "N of M processed" status line, not the entire `<ul>`.

### Settings page "Read-only" flicker during auth load

**ACCEPT.** Gate the banner on `!authLoading && !isAdmin`.

### SECURITY.md clarifications

**ACCEPT.** Three short additions: in-memory used-sig set is cleared on restart (5-min TTL is the hard replay bound); audit log line can be lost if the process dies between commit and `slog`; recount-defense assumes no concurrent attacker-controlled writes.

---

## Items deferred

- Resolver-path unit test with fake repos + `TapRemover` (Go reviewer, marked optional). Defer.
- Pre-existing `useState(initFn)` bug at `settings/page.tsx:111-119` (UX reviewer). Out of scope.
- Operator CLI for retry-Tap-cleanup-after-purge. Out of scope.

---

## Round 2 needed?

**No.** The single defense-in-depth fix (D.1) plus cosmetic items land in one follow-up commit. None compromise the security model of the destructive op.
