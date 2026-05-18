# 02 — Security findings (overnight 2026-05-18)

Scope: a single-pass security audit of `staging` per the lenses in the
operator brief, with a focus on the three nested-where features that
shipped in PRs #90/#91/#92. Twelve findings; the bottom of the file
records the lenses that produced no material results.

Severity calibration follows the brief: critical = exploitable as-is;
high = real, fix soon; medium = worth fixing, won't hurt to defer;
low/nit = preference (not reported here).

Most-important first.

---

## Findings (severity-ordered)

### S-1: `CreateFieldIndex` interpolates the `collection` argument into SQL without validation — admin-authenticated SQL injection

**Severity:** high
**Location:** `internal/database/repositories/records.go:819-834` (`CreateFieldIndex`); admin entry point at `internal/graphql/admin/schema.go:825-831` and `internal/graphql/admin/resolvers_lexicons.go:255-262`.
**Problem:** The `field` argument is validated with `ValidateFieldName`, but the `collection` argument is interpolated raw into the SQL string with single-quote wrapping:
```go
sqlStr := fmt.Sprintf(
    `CREATE INDEX CONCURRENTLY IF NOT EXISTS %s ON record ((json->>'%s')) WHERE collection = '%s'`,
    idxName, field, collection,
)
```
A `collection` value containing a single quote terminates the literal; an admin-shaped attacker can append arbitrary SQL (e.g. `collection: "x'; DROP INDEX something; --"`). The same `collection` value also flows into the generated index name via `fieldIndexName` (which only rewrites `.` to `_` — a `;` survives).
**Why this matters here (not just a generic smell):** Per AGENTS.md the admin surface accepts both an OAuth user and an `ADMIN_API_KEY` bearer; in either case the boundary between "admin can purge" and "admin can rewrite the schema" matters. The `ValidateFieldName` precedent (one argument over) shows the team already understands the contract. The omission is the kind of thing a future hardening sweep should catch. The `DropFieldIndex` variant at line 837-848 is the same shape (only `field` validated; the `idxName` is then a string the DB sees raw — for IF EXISTS that's bounded but still wrong-shaped).
**Proposed fix:** Validate `collection` against a canonical NSID predicate (the lexicon registry already knows what collections exist — reject anything not in `registry.Keys()`). Belt-and-braces: change `WHERE collection = '%s'` to use a parameterised path. Postgres rejects placeholders in DDL, so the right shape is `pgx.Identifier{...}.Sanitize()` for the index name plus a quoted-literal helper for the collection clause; the cleanest fix is to drop the WHERE entirely and let the inner expression-index do its job, OR move the partial-index predicate to a curated allowlist.
**Effort:** S
**Risk of fix:** low
**Reversibility:** easy

---

### S-2: `/notifications/graphql` has no body-size cap and no query-depth guard

**Severity:** medium
**Location:** `internal/server/notifications_xrpc.go:46-79`.
**Problem:** The handler reads the body directly into a JSON decoder with no `http.MaxBytesReader`, and executes the GraphQL query with no `depth.Check` pre-execution guard. Compare with `/graphql` (`internal/graphql/handler.go:135-152`) and `/admin/graphql` (`internal/graphql/admin/handler.go:160-174`), which both have both controls.
**Why this matters here (not just a generic smell):** the service-auth verifier (`internal/oauth/serviceauth.go`) bounds the *token* at 8 KiB, but the request body is independent of the token. Any AT Protocol identity with a valid signing key can mint a token (no allowlist beyond `aud` + `lxm`) and then ship a megabyte of GraphQL at the indexer. Layer 1 (`statement_timeout` 30s) is the only ceiling — that's a long time for a recursive selection set to burn CPU on resolver planning before SQL even fires. AGENTS.md explicitly carves notifications behind service-auth as "any AT Protocol identity can call this" so the threat model includes hostile clients.
**Proposed fix:** Add `r.Body = http.MaxBytesReader(w, r.Body, 1<<20)` before the JSON decode and call `depth.Check(body.Query, 15)` (matching the public surface) after parse. Both are two-line changes copied from the existing handlers.
**Effort:** S
**Risk of fix:** low
**Reversibility:** easy

---

### S-3: `ALLOWED_ORIGINS` unset silently allows all origins in production

**Severity:** medium
**Location:** `internal/server/cors.go:170-214` (`CORSMiddleware`); config plumbing in `internal/config/config.go:126` and `cmd/hypergoat/main.go:358-367`; the contract in `SECURITY.md` line 21 says "Do not leave unset in production."
**Problem:** When `ALLOWED_ORIGINS` is empty the middleware emits `Access-Control-Allow-Origin: *` for every request. `config.Validate()` does not warn or reject this state; the only signal is the `slog.Info "Configuration loaded" allowed_origins=""` line at boot. A production deploy that forgets to set `ALLOWED_ORIGINS` (the env var name is easy to typo, and Railway env-var scoping is per-branch — see global CLAUDE.md) silently exposes the admin GraphQL surface and the OAuth flows to every browser origin. The admin endpoint is API-key protected so it's not a credential-exposure, but `/oauth/token` and `/oauth/authorize` being cross-origin-callable from any site is meaningful — a malicious origin can mount a CSRF-aware OAuth confusion attack against logged-in users of any admin UI.
**Why this matters here (not just a generic smell):** SECURITY.md treats this as an operator-contract item, but the binary doesn't enforce the contract. A misconfigured boot succeeds. The pattern elsewhere (`SECRET_KEY_BASE` placeholder, `OAUTH_LEGACY_DPOP_JKT_CUTOFF` unset, `GRAPHQL_PUBLIC_QUERY_TIMEOUT_MS` < min) is to fail-closed at `Validate()`. Allowed-origins should match.
**Proposed fix:** In `config.Validate()` reject unset `ALLOWED_ORIGINS` whenever `ExternalBaseURL` starts with `https://` (i.e. real deploy mode); keep the dev permissiveness behind `http://` or an explicit `ALLOW_CORS_DEV=true` opt-in. Alternatively, log a `Warn` at startup with the explicit "running in dev-CORS mode" string so operators see it in their boot logs.
**Effort:** S
**Risk of fix:** medium — operators will hit it on first real deploy after the change.
**Reversibility:** easy (revert in one line) — but you'd need to walk back the bricked production boot.

---

### S-4: Two `slog.Warn` sites in the OAuth handler log raw user-supplied input without `logsafe.String`

**Severity:** medium
**Location:** `internal/server/oauth_handlers.go:282` (`slog.Warn("handle resolution failed", "handle", loginHint, "error", err)`) and `internal/server/oauth_handlers.go:294` (`slog.Warn("auth server resolution failed", "did", did, "error", err)`).
**Problem:** `loginHint` arrives directly from the `/oauth/authorize` query/form parameter — a public, unauthenticated, attacker-supplied string. The `did` at line 294 is either the same `loginHint` (when it passed `did.IsValid`) or the resolver's output (post-resolution from a handle that the attacker chose). Neither is run through `internal/logsafe.String`. The codebase has the pattern wired (`internal/graphql/admin/handler.go:217`, `internal/graphql/admin/purge.go:535-559`, etc.) but missed these two pre-OAuth-flow log sites.
**Why this matters here (not just a generic smell):** `logsafe`'s docstring identifies the threat model as forged audit lines via newline / U+2028 injection. The admin-event lines are the hardened ones because those are the GDPR-90d retained audit trail; the OAuth pre-flow lines are still ingested by the same log shipper. An attacker can spray malformed handles like `evil\nevent=actor_purge target_did=did:plc:victim ts=...` through `/oauth/authorize` to forge an `event=actor_purge` line that the alerting rule for `wrong_admin` keys on. Low severity because the forged line lacks the structured `actor_did` field that the SECURITY.md-recommended alert filters on, but it's still log pollution that could mislead an incident responder. Same fix discipline as the rest of the codebase — apply at every emission site, not just the audit ones.
**Proposed fix:** Wrap both values: `"handle", logsafe.String(loginHint)` and `"did", logsafe.DID(did)`. Two-character imports added; no behavioural change.
**Effort:** S
**Risk of fix:** low
**Reversibility:** easy

---

### S-5: `ADMIN_DIDS` env-var bootstrap stores the value un-validated

**Severity:** medium
**Location:** `cmd/hypergoat/main.go:294-303` (env-var → config table bootstrap); `internal/database/repositories/config.go:92-160` (`GetAdminDIDs` / `AddAdminDID` / `SetAdminDIDs`); SECURITY.md line 19 says every entry MUST be validated against `did.IsValid`.
**Problem:** When the indexer boots and the `admin_dids` config row is empty, `cmd/hypergoat/main.go:297` writes the raw `cfg.AdminDIDs` env-var value into the DB:
```go
if err := svc.config.Set(ctx, "admin_dids", adminDIDs); err != nil { ... }
```
No `did.IsValid` per entry; no `SplitCSV` filter for empty values; no upper bound on entries. The downstream `requireAdmin` check (`internal/graphql/admin/schema.go:896-902`) does an exact string-equality match between `userDID` and each `adminDID`, so an invalid entry (e.g. `"did:plc:abc\nadmin@victim"`) can't actually grant admin to a victim — but it does land a forged-shape value in the CSV that `AddAdminDID` would then carry forward unmodified. The runtime `AddAdmin` / `RemoveAdmin` resolvers DO validate via `did.IsValid` (resolvers.go:304 / 360), so the bootstrap path is the only un-checked write. The mismatch is the bug: SECURITY.md asserts strict validation as a security guarantee; the boot path quietly violates it.
**Why this matters here (not just a generic smell):** the operator who reads SECURITY.md and trusts "the canonical strict predicate guards every input path (admin endpoints, labeler reset/pause, service-auth JWT issuer, audit headers)" has been told something that isn't true today. Low immediate impact because the attacker can't influence `ADMIN_DIDS` env (operator-only). Higher long-term cost because future code that *reads* `admin_dids` and trusts it (a feature shipping in `staging` next month, say) will inherit the un-validated assumption.
**Proposed fix:** Validate each entry of `cfg.AdminDIDs` in `config.Validate()` via the strict predicate before the bootstrap call; reject the boot on malformed entries with a clear error. Same change brings the contract into line with SECURITY.md and matches the discipline of the runtime `AddAdmin` path.
**Effort:** S
**Risk of fix:** low (boot-time fail-fast)
**Reversibility:** easy

---

### S-6: DPoP nonces are served but never verified

**Severity:** low
**Location:** `internal/server/oauth_dpop_nonce.go:13-29` mounts a fresh-random nonce endpoint at `/oauth/dpop/nonce`. `internal/oauth/dpop.go:251-352` (`VerifyDPoPProof`) extracts `claims.Nonce` via the typed claim struct (line 66) but never asserts anything about it — never checks against the persisted nonce store, never matches against `internal/database/repositories/oauth_dpop_nonces.go`.
**Problem:** The endpoint exists and returns a fresh nonce on every call, so a client that opts to bind nonces to its proofs has a way to obtain them. The verifier ignores the nonce entirely. The result is a `DPoP-Nonce`-shaped response header with no enforcement teeth — a client can send proofs without nonce, with stale nonce, with attacker-controlled nonce, and the verifier accepts all three identically.
**Why this matters here (not just a generic smell):** RFC 9449 §8 makes the nonce flow OPTIONAL but mandates that if the server demands one (via the `use_dpop_nonce` error), it must reject proofs whose nonce doesn't match. The current shape doesn't demand nonces, so technically it's spec-conformant. The hazard is that a future reviewer reading the code sees "we have a nonce endpoint and the claim struct has a Nonce field" and assumes the binding is enforced. Either remove the endpoint (and the `Nonce` field on `DPoPClaims`) or wire it in. The existing `OAuthDPoPNoncesRepository` already has Insert/Exists/Delete; what's missing is the call site in `VerifyDPoPProof` and the error response that triggers retry.
**Proposed fix:** Pick one — (a) delete `oauth_dpop_nonce.go`, the `Nonce` field, and the `OAuthDPoPNoncesRepository` migration if unused, OR (b) wire the verifier to require a fresh nonce when the request is to `/oauth/token` and respond with `use_dpop_nonce` + a stored nonce when missing. (a) is the smaller change and matches the AT Protocol ecosystem's current laxness.
**Effort:** S (option a) / M (option b)
**Risk of fix:** low / medium
**Reversibility:** easy

---

### S-7: Dead-code branch in `Handler.validAPIKey` would silently allow auth if reached via a future refactor

**Severity:** low
**Location:** `internal/graphql/admin/handler.go:267-278`:
```go
func (h *Handler) validAPIKey(r *http.Request) bool {
    if h.adminAPIKey == "" {
        return true // no key configured — allow (backwards-compatible)
    }
    ...
}
```
**Problem:** The outer caller (line 119) already guards `validAPIKey` behind `h.adminAPIKey != ""`, so the `== ""` branch is unreachable today. But the branch returns `true` ("no key configured means allow"), which is the inverse of what `cmd/hypergoat/admin_http.go:27-30` does for the raw HTTP admin endpoints ("no key configured means 403"). A future refactor that adds a third call site to `validAPIKey` and forgets the outer guard would silently grant admin access to anyone.
**Why this matters here (not just a generic smell):** the comment says "backwards-compatible" which suggests the author considered it intentional, but the comment is now wrong — there is no current call path that exercises this branch, and the production contract (per SECURITY.md and the raw-HTTP admin handler) is "ADMIN_API_KEY unset = admin disabled." Two helpers with opposite semantics for the same env-var state is the bug, not the dead branch.
**Proposed fix:** Change the `== ""` branch to `return false`. The comment becomes "no key configured — admin auth disabled" matching the raw-HTTP handler. This is fully behaviour-preserving today and fail-closed for future refactors.
**Effort:** S
**Risk of fix:** low
**Reversibility:** easy

---

### S-8: `extractInValues` returns `nil` for non-list types but the SQL emitter still binds an `ANY(...::text[])` clause with a nil parameter

**Severity:** low
**Location:** `internal/database/repositories/filter.go:1067-1084`:
```go
func extractInValues(v interface{}) ([]string, bool) {
    var values []string
    var hasNull bool
    switch list := v.(type) {
    case []interface{}: ...
    case []string: ...
    }
    return values, hasNull   // both nil/false for any other type
}
```
The `OpIn` arm in `buildSingleFilter` (line 716-727) then emits `<expr> = ANY($N::text[])` with `values` as the parameter — which pgx serializes as a null array. Postgres evaluates `<expr> = ANY(NULL)` as NULL, which in a WHERE clause is treated as false. So the query silently returns zero rows instead of producing a meaningful error.
**Why this matters here (not just a generic smell):** the `f.Validate()` path (line 527-531) does enforce `validateInListShape` for `OpIn`, which rejects unknown types with `"in operator requires a list value"`. So the only way to reach the SQL emitter with an unsupported type is to skip `Validate()` — which the recursive builder at line 376 explicitly does NOT do. So this is defence-in-depth, not currently reachable. But the contract of "the SQL emitter assumes Validate already ran" is implicit; the existing comment at line 369-378 even acknowledges this is the historical bug being fixed. The extractor in the `KindArrayContributor` / `KindUnionSubject` / `KindStringSubject` arms at lines 917, 960, 1004 discards the `hasNull` return — also implicit-contract reliance.
**Proposed fix:** Either make `extractInValues` return an `error` when the input type is unsupported, or have `buildSingleFilter` defensively check `if len(values) == 0 { return "false", nil, paramIdx, nil }` at the top of the `OpIn` arm. Both make the unreachable-today failure mode loud the moment a future bug reaches it. Or add a regression test that calls `buildSingleFilter` with an unvalidated `IsNull == nil, Operator == OpIn, Value == 42` and asserts the error.
**Effort:** S
**Risk of fix:** low
**Reversibility:** easy

---

### S-9: `/admin/label-chain` uses a raw `uri` query parameter without validation, surfacing in the SQL parameter and an error log

**Severity:** low
**Location:** `cmd/hypergoat/admin_http.go:147-169` (`newLabelChainHandler`); the downstream query at `internal/database/repositories/labels.go` `GetAllForURI` is parameterised, so SQL injection is not the concern.
**Problem:** `uri` comes from `req.URL.Query().Get("uri")` and is passed verbatim into `svc.labels.GetAllForURI(req.Context(), uri)` and (on error) into `slog.Error("label-chain lookup failed", "uri", uri, "error", err)`. The `uri` is not validated for AT-URI shape (no `at://...` prefix check) and not run through `logsafe.String`. An admin-key holder can request `uri=$(payload)` and get the raw payload echoed into the structured log line.
**Why this matters here (not just a generic smell):** admin-key holder is "trusted" in the threat model, so this isn't an injection path against the index itself. But the audit trail is the operator's only forensic record (per SECURITY.md) — accepting hostile-shape input there muddies that record. The pattern in the rest of the file (`newLabelerResetHandler`, `newLabelerPauseHandler`) validates DID format before use; URI validation has no equivalent helper today.
**Proposed fix:** Reject `uri` values that don't match `^at://did:[a-z]+:[a-zA-Z0-9._:-]+/[a-zA-Z0-9._-]+/[a-zA-Z0-9._~-]+$` (the AT-URI shape) at the handler level, and wrap the `slog.Error` value via `logsafe.String`. Two-line change.
**Effort:** S
**Risk of fix:** low
**Reversibility:** easy

---

### S-10: Service-auth rejection logs include `err.Error()` which can carry attacker-influenced `iss` content

**Severity:** low
**Location:** `internal/oauth/serviceauth_middleware.go:85` — `slog.Warn("service-auth rejected", "reason", reason, "lxm", lxm, "error", err.Error())`. The `err` here is built by `internal/oauth/serviceauth.go` paths that `fmt.Errorf("%w: malformed iss %q", ErrServiceAuthUnsupportedDIDMethod, claims.Iss)` (line 220) and similar — `claims.Iss` is the attacker's chosen string when the JWT signature hasn't yet been verified.
**Problem:** The `%q` formatter escapes ASCII control characters, but a determined attacker can still ship a 2 KB `iss` claim full of misleading content that lands in the log line. The `reason` and `lxm` labels are bounded (sentinel sets / per-endpoint config); only the `error` is unbounded. The format-quoting helps, but the line is still long-form attacker-controlled content.
**Why this matters here (not just a generic smell):** AGENTS.md identifies log-injection as a real concern (it's the documented reason the admin mutation handler logs variable *keys*, not values, per the round-3 fix). The OAuth/service-auth surface inherited the same threat model but not the same hygiene. Bounded blast radius because the metric label set is already correct — the `reason` is what dashboards key on; the `error` is for triage. Still: a log line per failed token verify is a free amplification channel for an attacker who can churn invalid tokens.
**Proposed fix:** Drop `err.Error()` from the slog call entirely and rely on `reason` for the per-attempt forensics. If the operator needs the detailed error for debugging, gate it behind a `DEBUG`-level log call: `slog.Debug("service-auth rejected: detail", "reason", reason, "error", err.Error())`. Production logs ship at Info; debug stays in dev.
**Effort:** S
**Risk of fix:** low
**Reversibility:** easy

---

### S-11: `synthReplayKey` uses the raw signature bytes as the cache key — works for the lifetime of the cache, but the comment overstates the guarantee

**Severity:** nit (would be culled per the 30-finding cap but worth noting as it's the only doc/code mismatch in the OAuth subsystem I found)
**Location:** `internal/oauth/serviceauth.go:343-355` (`synthReplayKey`); the cache itself is `internal/oauth/jti_replay.go`.
**Problem:** The doc-comment claims ECDSA non-determinism makes the synthetic key "fresh per token." That's true for replay-detection-within-cache, but the cache is bounded at 100k entries (`jtiCacheCapacity = 100_000`). The eviction policy is by-earliest-expiry; under sustained burst above 100k tokens-per-MaxLifetime (60s), legitimate already-seen tokens can be evicted and a replay-window briefly reopens. SECURITY.md documents this trade-off explicitly ("Move to Postgres-backed storage before scaling past one replica"), so the operator-facing contract is fine — the code-internal comment isn't.
**Why this matters here (not just a generic smell):** the comment in serviceauth.go says the synthetic key "matches the spirit of jti (nonce)." That's true within the cache window — but if the cache evicts under pressure, the synthetic-key fallback degrades silently with no metric signal beyond the generic "jti cache size" counter. The current `metrics.go` doesn't expose the eviction-by-pressure count separately from the eviction-by-expiry count.
**Proposed fix:** Add a metric `hypergoat_service_auth_jti_evicted_under_pressure_total` and increment it in `jti_replay.go:checkAndSet` when the size>=capacity branch fires. The metric lets the operator alert before the synthetic-key replay window reopens.
**Effort:** S
**Risk of fix:** low
**Reversibility:** easy

---

### S-12: `processor.go` Warn log on non-object JSON includes the raw URI string

**Severity:** nit
**Location:** `internal/ingestion/processor.go:122-124`:
```go
slog.Warn("Skipping non-object JSON record",
    "uri", op.URI, "json_prefix", fmt.Sprintf("%q", truncate(trimmed, 20)))
```
**Problem:** `op.URI` is logged un-scrubbed. URIs come from Jetstream and are constructed as `at://did:.../collection/rkey`. The DID and collection are typed by the firehose; the `rkey` is producer-controlled. AT Protocol's rkey grammar (`[A-Za-z0-9._:~-]+`) excludes the control chars logsafe scrubs, so this isn't currently exploitable — but again, the contract is "every user-controlled string flows through logsafe at the slog boundary."
**Why this matters here (not just a generic smell):** the `json_prefix` value is already wrapped in `%q` so it can't carry newlines. The URI isn't. Consistent application of `logsafe.String` makes the contract obvious to the next reader.
**Proposed fix:** Wrap the URI: `"uri", logsafe.String(op.URI)`. Imported helper, two characters added.
**Effort:** S
**Risk of fix:** low
**Reversibility:** easy

---

## Lenses that produced no material findings

These are the explicit empties from the brief's 10 attack vectors. They
are noted here so a future reviewer knows the surface was scanned and
came up clean (or: was already audited at a depth I'm not adding to).

- **SQL string interpolation in `filter.go`'s `buildSingleFilter` /
  `buildFilterGroupRecursive`.** The alias parameter (`r`/`d`/`e`) is
  passed through three internal call sites only, and every emitter
  call originates from a code-defined constant — no path from request
  data reaches the alias position. The `KindArrayContributor` /
  `KindUnionSubject` / `KindStringSubject` arms each hardcode `r.`
  prefixes and reject any non-`r` alias at line 664-669 (the
  "lexicon-specific filter kind cannot be used inside a nested
  subquery" sentinel). The `JoinExpr` and `ArrayPath` fragments in
  the joined- and array-where registries are package-level
  `map[...]Descriptor` values with package-visible mutation only —
  no runtime path mutates them, and the comment-level security
  contract is enforced by reviewer attention rather than code, which
  is the right shape for "treat as SQL diff" content. The `JSON path
  fieldName` interpolation runs through `ValidateFieldName` at every
  emitter site (line 357, 376, 499, 614, 659), and the field-name
  validation regex (`[a-zA-Z_][a-zA-Z0-9_]*`) is strict enough that
  nothing matching it can interact with SQL escape semantics.

- **`/admin/graphql` auth bypass.** The handler (`internal/graphql/admin/handler.go`) requires `userDID != "" || apiKeyAuth` before any work happens (line 152-155); the `validAPIKey` check uses `subtle.ConstantTimeCompare`; the `X-User-DID` header is `did.IsValid`-validated before being trusted; and the schema's per-field guards (`requireAdmin` at every Resolve in `internal/graphql/admin/schema.go`) provide a second layer if the context-injection ever regresses. The raw-HTTP admin handlers (`cmd/hypergoat/admin_http.go`) do the same constant-time check via `checkAdminBearer` and refuse to register when `ADMIN_API_KEY` is empty. The only stylistic flaw is S-7 above.

- **Public endpoint exposing admin functionality.** The schema split is clean: the public `/graphql` builds from `internal/graphql/schema.NewBuilder(registry)` (no admin types), the admin endpoint builds from `internal/graphql/admin.SchemaBuilder` (admin-only types). The `statistics`/`activityBuckets`/`recentActivity`/`collectionOverview` aggregate-data fields are deliberately admin-gated per SECURITY.md, and the GraphQL schema enforces it.

- **DPoP `aud` / `htu` verification.** `internal/oauth/middleware.go:122-125` constructs the expected resource URL as `m.resourceURL + r.URL.Path` (+ `?RawQuery`) where `resourceURL` is `EXTERNAL_BASE_URL`-derived at startup. `VerifyDPoPProof` then asserts `claims.HTU != url` (line 322-324) and `claims.HTM != method` (line 319-321). Both checks are unconditional — no header an attacker controls can skip them.

- **JTI replay protection (both DPoP and service-auth).** DPoP-JTI: `OAuthDPoPJTIRepository.InsertIfNew` is an atomic `INSERT ... ON CONFLICT DO NOTHING` (`internal/database/repositories/oauth_dpop_jti.go:44-59`); the middleware at `internal/oauth/middleware.go:141-147` uses the boolean return as the replay signal. Service-auth JTI: `jtiReplayCache.checkAndSet` (`internal/oauth/jti_replay.go:57-91`) holds the mutex for the entire check-and-record sequence. Both are race-safe; both are checked before token validity is asserted. The synth-key fallback for tokens without `jti` is keyed on signature bytes (`internal/oauth/serviceauth.go:343-355`), which ECDSA non-determinism makes unique per re-signing.

- **Path traversal (LEXICON_DIR, lexicon upload).** `LEXICON_DIR` is operator-controlled config, not request data — `loadLexiconsFromDir` in `cmd/hypergoat/main.go:1329-1352` is read at boot, no request path reaches it. The admin lexicon-upload path (`internal/graphql/admin/resolvers_lexicons.go:73-174`) doesn't write any files to disk — it parses the ZIP in-memory and stores the contents in the `lexicon` DB table keyed by the `id` field from inside each parsed JSON. ZIP entry filenames are referenced only in error messages, not in any filesystem operation. Zip-slip is structurally not possible here.

- **Race conditions in `oauth_dpop_nonces.go`.** Repository methods are stateless wrappers around parameterised SQL; concurrency is managed by Postgres's transaction semantics. No in-process state to race.

- **Hardcoded secrets in `_test.go`.** Spot-checked the OAuth and admin test files; constants like `"test-secret-key-base"` are clearly test-only and never reach production paths (test setups always pass them explicitly to constructors that production wiring doesn't call with literals).

---

## Counts

| Severity | Count |
|----------|-------|
| Critical | 0 |
| High     | 1 |
| Medium   | 4 |
| Low      | 5 |
| Nit      | 2 |
| **Total** | **12** |

Well under the 30-finding ceiling. The codebase's audit history (23
review rounds + the 2026-04-13 comprehensive audit, per AGENTS.md
"Review history") shows in the density of correct work: the load-bearing
hardening (constant-time compares, logsafe wrappers at every audit-log
site, parameterised SQL across the repository layer, did.IsValid at
every input gate, the JTI replay primitives) is already in place. The
findings above are gaps in coverage, not architectural holes.
