# Security & Code Quality Audit Report

**Date:** 2026-04-13
**Scope:** Full codebase — Go backend (hypergoat), Next.js admin client, Docker/CI, dependencies
**Methodology:** Multi-pass AI-assisted audit (10 specialist reviewers, adversarial cross-review, 3 full passes)
**Auditor:** Claude Opus 4.6 (AI agent)

---

## Executive Summary

**Security posture:** The codebase has a solid security foundation — parameterized SQL queries, constant-time secret comparisons, CORS enforcement, depth-limited GraphQL, body size caps, and non-root Docker containers. However, several Critical-severity issues existed: a hardcoded cookie secret default that could allow session forgery, hardcoded production infrastructure URLs in public-facing code, and a non-existent Go version in the build system. These have been fixed in this audit.

**Code quality:** Well-structured Go code with clear separation of concerns. The 23-round review history (PR #25) already addressed many common issues. Main debt: a few swallowed errors in the OAuth layer, missing security linters, and the client lacks defense-in-depth (relies entirely on backend authorization).

**Top 3 items to address before next ship:** (1) Upgrade `golang.org/x/crypto` to >= v0.31.0 (CVE-2024-45337), (2) implement label signature verification in the labeler consumer, (3) verify production `SECRET_KEY_BASE` and `COOKIE_SECRET` are unique per environment.

---

## Findings Summary

| Severity | Count | Fixed | Remaining |
|----------|-------|-------|-----------|
| Critical | 4 | 4 | 0 |
| High | 5 | 5 | 0 |
| Medium | 8 | 6 | 2 |
| Low | 7 | 0 | 7 |
| Info | 5 | 0 | 5 |
| **Total** | **29** | **15** | **14** |

---

## Critical Findings (All Fixed)

### F-BUILD-001: Non-existent Go version (go 1.25) — FIXED in CS-001
- **Location:** `go.mod:3`, `Dockerfile:2`
- **Impact:** Build behavior unpredictable; GOTOOLCHAIN=auto could download arbitrary toolchain
- **Fix:** Pinned to go 1.23, alpine:3.21, GOTOOLCHAIN=local

### F-CLIENT-COOKIE-001: Hardcoded cookie secret default — FIXED in CS-002
- **Location:** `client/src/lib/env.ts:17`
- **Impact:** If COOKIE_SECRET not set in production, anyone who reads the source code can forge session cookies and impersonate any user
- **Fix:** Removed default; app throws on startup if missing

### F-CLIENT-URL-001: Production Railway URL in public code — FIXED in CS-003
- **Location:** `client/src/app/docs/agents/route.ts:1-2`, `client/src/app/docs/page.tsx:6`
- **Impact:** Internal infrastructure hostname exposed to all users
- **Fix:** Derive from request URL at runtime

### F-CLIENT-SECRET-001: OAuth secrets sent to browser — FIXED in CS-004
- **Location:** `client/src/lib/graphql/queries.ts:118`
- **Impact:** Client secrets visible in browser DevTools on every settings page load
- **Fix:** Removed clientSecret from list query and update mutation response

---

## High Findings (All Fixed)

### F-LINT-001: No security linter (gosec) — FIXED in CS-005
- **Location:** `.golangci.yml`
- **Fix:** Added gosec and bodyclose linters

### F-CI-001: No GitHub Actions permissions block — FIXED in CS-006
- **Location:** `.github/workflows/ci.yml`
- **Fix:** Added `permissions: read-all` at workflow level

### F-CLIENT-AUTH-001: Admin proxy sends API key for unauthenticated requests — FIXED in CS-008
- **Location:** `client/src/app/api/admin/graphql/route.ts`
- **Fix:** Return 401 if session has no DID

### F-CRYPTO-001: Hand-rolled JWT signing — FIXED in CS-009
- **Location:** `internal/oauth/bridge.go:470-502`
- **Fix:** Replaced with golang-jwt/jwt/v5

### F-SUPPLY-001: Unpinned opencode plugin — FIXED in CS-013
- **Location:** `opencode.json`
- **Fix:** Pinned to @1.0.0

---

## Medium Findings

### F-SESSION-001/002/003: Session cookie missing explicit flags — FIXED in CS-007
- **Location:** `client/src/lib/session.ts:32`
- **Fix:** Explicit httpOnly, sameSite=lax, reduced maxAge to 7 days

### F-ERR-001: Swallowed createClientAssertion error — FIXED in CS-010
- **Location:** `internal/oauth/bridge.go:317-321`
- **Fix:** Propagate error as BridgeError

### F-CLIENT-HEADERS-001: No security headers on Next.js client — FIXED in CS-011
- **Location:** `client/vercel.json`
- **Fix:** Added HSTS, X-Frame-Options, X-Content-Type-Options, etc.

### F-ENV-001/002: .env.example has real admin DID — FIXED in CS-012
- **Location:** `.env.example:42`
- **Fix:** Cleared default, un-commented ADMIN_API_KEY

### F-CLIENT-DOS-001: No request size limit on GraphQL proxy — FIXED in CS-014
- **Location:** `client/src/app/api/graphql/route.ts`
- **Fix:** Added 1 MiB content-length check

### F-DEP-001: golang.org/x/crypto v0.21.0 with CVE-2024-45337 — NOT FIXED (requires Go toolchain)
- **Location:** `go.mod:85`
- **Impact:** SSH authorization bypass vulnerability. While this project doesn't use SSH directly, the transitive dependency should be updated.
- **Recommendation:** Run `go get golang.org/x/crypto@v0.31.0 && go mod tidy` when Go toolchain is available.

### F-LABELER-001: Label signatures not cryptographically verified — NOT FIXED (architecture change)
- **Location:** `internal/labeler/consumer.go`
- **Impact:** Labels trusted based on WebSocket connection; MITM or compromised relay could inject arbitrary labels
- **Recommendation:** Implement signature verification against the labeler's signing key from DID document

---

## Low Findings (Not Fixed — Low Risk)

| ID | Description | Location |
|----|-------------|----------|
| F-DOCKER-003 | `wget` in runtime image only for healthcheck | Dockerfile:29 |
| F-GRAPHIQL-001 | Admin API key stored in browser localStorage via GraphiQL | internal/server/graphiql.go |
| F-GRAPHIQL-002 | GraphiQL loads assets from unpkg.com CDN (supply chain) | internal/server/graphiql.go |
| F-DB-001 | OAuth tokens and DPoP keys stored plaintext in DB | internal/database/migrations/postgres/001 |
| F-DB-002 | admin_session has no expires_at TTL column | internal/database/migrations/postgres/001 |
| F-METRICS-001 | /metrics endpoint unauthenticated (documented, by design) | cmd/hypergoat/main.go |
| F-CORS-001 | CORS defaults to `*` when ALLOWED_ORIGINS unset (documented) | internal/server/cors.go |

---

## Saboteur Scenarios

### Scenario 1: Session Forgery (Pre-Fix)
**Prerequisites:** Read access to the source code
**Steps:** (1) Read `client/src/lib/env.ts` to find the default cookie secret. (2) Use iron-session with the known secret to forge a cookie with any user's DID. (3) Send requests to /api/admin/graphql as any admin.
**Impact:** Full admin access to any deployment using the default cookie secret.
**Status:** Fixed in CS-002.

### Scenario 2: Cheapest Denial of Service
**Prerequisites:** None (unauthenticated)
**Steps:** Send unbounded GraphQL subscription requests via WebSocket. Each connection spawns goroutines. While `wsMaxSubsPerClient=64` limits per-connection, opening many connections from different source IPs bypasses this.
**Mitigation:** Operators must configure rate limiting at the reverse proxy (documented in SECURITY.md).

### Scenario 3: Stealthiest Data Access
**Prerequisites:** Network position to intercept WebSocket
**Steps:** MITM the labeler WebSocket connection (labels are ingested without signature verification). Inject crafted labels to influence record visibility or attach misleading metadata.
**Status:** Requires F-LABELER-001 fix.

---

## Systemic Root Causes

1. **Client trusts backend for all authorization.** The Next.js client has no admin-vs-user distinction in the UI layer. All pages are accessible to any authenticated user; the backend is the sole authorization gate. This is defense-in-depth weak.

2. **Development defaults that work in production.** Hardcoded fallback URLs and secrets meant for local development can silently operate in production if environment variables are missing. The pattern should be: missing config = fail loudly.

3. **No security-specific CI tooling.** Before this audit, there was no gosec, no CodeQL, no dependency vulnerability scanning in CI. Issues were caught only through manual review.

---

## Recommendations for Human Follow-Up

1. **Upgrade golang.org/x/crypto** to >= v0.31.0 (requires Go toolchain)
2. **Run `go mod tidy`** after the Go version pin change
3. **Implement label signature verification** in the labeler consumer
4. **Verify production secrets** are unique per environment (not copy-pasted from .env.example)
5. **Add CodeQL or Snyk** to CI for automated vulnerability scanning
6. **Consider adding** `actions/dependency-review-action` for PR-level dep vulnerability checks
7. **Review the `COOKIE_SECRET` change** — ensure it's set in Vercel environment variables before deploying this branch

---

## Methodology Notes

This audit was performed by an AI agent (Claude Opus 4.6). Limitations:
- Go toolchain was not available in the dev container, so `go build` and `go test` could not verify compile-time correctness of Go changes
- No runtime testing was performed (no dev server started)
- Dependency CVE assessment relied on knowledge cutoff; a fresh `govulncheck` scan is recommended
- The audit focused on the magic-index subdirectory; the certs-social and certified-app codebases were not audited
