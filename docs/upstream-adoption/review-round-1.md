# Plan review — round 1 decisions

**Plan**: [`plan.md`](./plan.md)
**Date**: 2026-05-13
**Reviewers**: four parallel lenses — GraphQL schema correctness (A/B); destructive-op safety (E, D quick-check); ops + env-var ergonomics (C/D/H); admin-UX (F/G + E client surface).

Decisions below. ACCEPT items flow into the plan as-is. MODIFY items rewrite the relevant clause. REJECT items remove the work from scope.

---

## Track A → REJECT WHOLE TRACK

> Reviewer (schema correctness): `internal/lexicon/validator.go:225` and `internal/graphql/schema/builder.go:860,910` already implement the **opposite** policy — skip records with missing required fields. Today's policy is more defensible. Coercing a CID/datetime/blob/integer to `""`/`0` produces records that pass GraphQL validation but **lie to clients** (a `0` timestamp, an empty CID).

Drop. The reference behaviour exists because upstream doesn't have our `SanitizeRecord`. Our skip-on-missing is the right answer. Constraints for any future revisit logged in `docs/upstream-pull-candidates.md` § Dropped.

## Track B → ACCEPT with one extra test

- Zero-property `ObjectDef` carve-out → ACCEPT.
- Add regression test for `{populated, populated}` to confirm the carve-out doesn't poison real unions → ACCEPT.

## Track C → ACCEPT with four modifications

- Loopback hosts default to `http://` (`localhost`, `127.0.0.1`, `::1`) → ACCEPT.
- Lowercase the scheme; also update `cmd/hypergoat/main.go` HSTS gate to lowercase before `HasPrefix` → ACCEPT.
- Trim trailing slash → ACCEPT.
- Acceptance covers empty / whitespace / trailing-slash / mixed-case cases → ACCEPT.

## Track D → MODIFY: reuse `PUBLIC_URL`

> Reviewer (ops): `PUBLIC_CLIENT_URL` (reference) conflicts with our existing `PUBLIC_URL` used at `client/src/lib/env.ts:31`, `auth/client.ts:166`, `client-metadata.json/route.ts:7`. Introducing a second var splits source-of-truth and would silently fail on Vercel deploys that already have `PUBLIC_URL` set.

**Critical fix.** Read `env.PUBLIC_URL`, fall back to `requestUrl.origin` for local dev.

## Track E — MULTIPLE MODIFY

- 60s TTL → **300s.** Long enough for human-paced confirmation against a multi-thousand-record preview; short enough to defeat abandoned-tab replay.
- Token binding: `(admin_did, target_did, record_count, exp)`. Constant-time HMAC compare → ACCEPT.
- Storage: **HMAC-signed token** over `(admin_did, target_did, count, exp)` keyed by `SECRET_KEY_BASE`. No in-memory state to lose on restart. Single-use enforced via in-memory used-set keyed by signature.
- Transaction: SQL-only. Tap removal best-effort after commit. Reference `135bd49` discovered this and removed Tap from the transaction; we adopt the same shape.
- Auth: reuse existing `requireAdmin`. No second allow-list — strength from token binding and audit.
- Audit: structured logs only; record retention contract (≥90d GDPR-minimum, 1y recommended) in `SECURITY.md`.
- Rate limit: existing 60 req/min/IP on `/admin/graphql` is sufficient.
- UX: counts + handle + `latestIndexedAt` preview; retyped-DID confirm; visible token-expiry countdown.

## Track F → ACCEPT with modifications

- `<textarea>` (auto-grow); Enter submits one NSID, Shift+Enter newlines for batch.
- Pre-validate with `isValidNsid`; trim, dedupe.
- Serialize mutations (backend hits DNS).
- Fixed status list with `aria-live="polite"`, persistent until dismissed.

## Track G → MODIFY substantially

- Gate on `session.did ∈ settings.adminDids` (server-known list), NOT `NEXT_PUBLIC_ADMIN_DIDS`.
- Disabled-with-tooltip for non-destructive sections (preserves diagnostic value); hide destructive controls.
- Acceptance: server-side enforcement is the security boundary; non-admin direct GraphQL is still rejected.
- First-deploy admin-bootstrap note in `client/.env.example`.

## Track H → MODIFY

- File path: `client/src/lib/env.ts` (NOT `config.ts`).
- Detect at module load on the server, not via `window.location.origin`.
- Compare `getOrigin(PUBLIC_URL || NEXT_PUBLIC_VERCEL_BRANCH_URL)` to `getOrigin(HYPERGOAT_URL)`.
- Refuse in dev, warn in production.

## Cross-cutting

- Commit order: B → C → G → E → F → D → H (G before E so destructive UI is born inside the gate).
- Env-var crosswalk in the plan (reference names → ours).
- Accessibility per track.
- SECURITY.md note records operator log-retention contract.

## Round 2?

No. Round 1 surfaced enough material to reshape the plan, but every item has a clear decision with cited code paths.
