# 02 — Quality findings (overnight 2026-05-18)

Scope: the 7 selective, non-stylistic lenses in the operator brief. Bias
toward deletion. Honest empties valued. Hard ceiling 15; calibrated low.

Findings most-important first.

---

### Q-1: `internal/oauth/errors.go` is entirely dead production code (157 LOC)

**Severity:** high
**Location:** `internal/oauth/errors.go:1-158` — the whole file.
**Problem:** The file exports the `OAuthError` struct, its three methods
(`Error`, `JSON`, `HTTPStatus`), two constructors (`NewOAuthError`,
`NewOAuthErrorWithState`), twelve RFC 6749-named factory helpers
(`InvalidRequest`, `UnauthorizedClient`, `AccessDenied`,
`UnsupportedResponseType`, `InvalidScope`, `ServerError`,
`TemporarilyUnavailable`, `InvalidClient`, `InvalidGrant`,
`UnsupportedGrantType`, `InvalidDPoPProof`, `UseDPoPNonce`), and a
`WriteErrorResponse` HTTP helper. Confirmed with a full-tree grep:
**not one of these is referenced outside `errors.go` and its own
`errors_test.go`.** Production OAuth error emission happens inside
`internal/server/oauth_handlers.go:1104-1110` via a private
`writeOAuthError(w, status, errorCode, description)` helper that
constructs a `map[string]string{"error":..., "error_description":...}`
and bypasses the typed error machinery entirely. The `redirectWithError`
path at line 1117-1129 does the same with `url.Values`.
**Why it matters:** This is the brief's "actively confusing readers"
case. A future contributor adding an OAuth endpoint will reach for
`oauth.InvalidRequest("...")` because it's idiomatic, named exactly
right, and *exists* — only to find the production path is the parallel
private helper next door. Either the private helper grows out of step
with the typed errors, or the contributor produces a third path. The
two paths emit semantically equivalent JSON today, so the divergence
isn't even visible at runtime. The R-1/R-3 deletion gauntlet picked
off three repo functions; this is the same class of finding, one
order of magnitude bigger.
**Proposed fix:** Pick one of:
(a) **Delete `errors.go` entirely** (and `errors_test.go`). The
    `writeOAuthError` private helper already serves every emission
    site. Cleanest reduction.
(b) **Replace `writeOAuthError` with `oauth.WriteErrorResponse`** and
    use the typed constructors at the ~30 emission sites in
    `oauth_handlers.go`. This is the inverse: keep `errors.go`, kill
    the private parallel. More edits, but the typed errors then earn
    their keep.
The cost of (a) is 290 LOC of source+test gone, zero behavioural
change. The cost of (b) is ~60 line edits and a real consumer for
the typed API. My recommendation: (a). The codebase has matured past
the point where the typed API was going to find a caller — three
years in, the private helper is the canonical path. Same external-API
check as R-1: grep certified-app + admin client for `oauth.OAuthError`
before deleting (the type is exported).
**Effort:** S (option a) / M (option b)
**Risk:** low (option a — pure deletion of unreferenced code)
**Reversibility:** easy

---

### Q-2: `internal/oauth/token_generator.go` is 80% dead — 7 of 9 generators have no production callers

**Severity:** high
**Location:** `internal/oauth/token_generator.go:23-71`.
**Problem:** Cross-package usage scan shows that of the nine
`Generate*` functions defined in this file, only three reach a
production path:
- `GenerateState` (called from `oauth_handlers.go:329`)
- `GenerateSessionID` (called from `oauth_handlers.go:300`)
- `GenerateAuthorizationCode` (called from `oauth_handlers.go:515`)
- `GenerateAccessToken` (called from `oauth_handlers.go:680, 848`)
- `GenerateRefreshToken` (called from `oauth_handlers.go:685, 853`)

The remaining four — `GenerateClientID`, `GenerateClientSecret`,
`GenerateDPoPNonce`, `GeneratePARRequestURI` — have **zero callers
outside their own `token_generator_test.go`**. The
`GenerateClientID/Secret` pair would be reasonable to keep if the
indexer issued client credentials for downstream consumers; it
doesn't (admin auth is API-key or OAuth bridged to upstream). The
`GenerateDPoPNonce` shape pairs with the unenforced nonce endpoint
flagged at S-6 in the security pass — same exposure, doubled up.
The `GeneratePARRequestURI` belongs to a Pushed-Authorization-Request
flow the indexer never implements (the bridge talks to the upstream
auth server which handles PAR).

Additionally, `IsExpiredWithSkew` (line 98) has zero callers outside
its own test, and the per-function comments (`// Returns a 43-character
URL-safe base64 string (32 random bytes encoded).`) are the exact
"WHAT not WHY" pattern called out in Lens 4 — but the bigger problem
is the surrounding dead code, so this is a folded note rather than a
separate finding.
**Why it matters:** Same class as Q-1 — a contributor wiring a future
client-registration endpoint will find `GenerateClientID` and assume
it's the canonical helper. It isn't; nothing calls it. Worse, the
adjacent helpers that ARE canonical (`GenerateAccessToken`,
`GenerateState`) sit in the same file, so the contributor can't tell
which are load-bearing from which are aspirational.
**Proposed fix:** Delete the four unused generators and
`IsExpiredWithSkew`. Keep `generateRandomToken` (private), the five
production-used generators, `CurrentTimestamp`, `ExpirationTimestamp`,
`IsExpired`. Net reduction ~40 LOC + test pruning. Same external-API
check as Q-1.
**Effort:** S
**Risk:** low
**Reversibility:** easy

---

### Q-3: `internal/oauth/scopes.go` is entirely dead production code (137 LOC)

**Severity:** high
**Location:** `internal/oauth/scopes.go:1-138` — the whole file (modulo
`ParseScopes` which is used in two `middleware.go` sites at lines
198, 240).
**Problem:** Same shape as Q-1: the file exports three scope constants
(`ScopeAtproto`, `ScopeTransitionGeneric`, `ScopeTransitionChatBsky`),
`JoinScopes`, `ValidateScopeFormat`, `ContainsScope`, `IsScopeSubset`,
`FilterScopes`, and a private `validateSingleScope` /
`isValidScopeChar` pair. Production grep shows only `ParseScopes` is
called outside the file's own test (`middleware.go:198, 240`). The
other six exports have no production callers — and crucially, the
scope-handling sites in `oauth_handlers.go` deal with scopes as
plain strings without ever consulting these helpers.

The three scope constants are never referenced anywhere — neither
within the package nor outside it. The string values are hard-coded
inline in `oauth_handlers.go:215` (`"atproto transition:generic"`)
as a literal, bypassing the constant.
**Why it matters:** Same trap as Q-1 and Q-2. A future contributor
adding scope validation will reach for `oauth.IsScopeSubset` (correct
implementation, well-tested) — and may not realise that the production
admin path doesn't enforce scopes at all today, so wiring it would
also need to update the call site that *should* be there but isn't.
The dead helpers obscure the "we don't enforce scopes" architectural
fact. Better to remove them so the future scope-enforcement work is
obviously additive rather than "fix the existing helpers."
**Proposed fix:** Move `ParseScopes` into `middleware.go` (its only
caller) as a private helper. Delete `scopes.go` and `scopes_test.go`.
Same external-API check.
**Effort:** S
**Risk:** low
**Reversibility:** easy

---

### Q-4: `AuthMiddleware.RequireAuth`, `RequireScope`, `WithMaxDPoPAge` and the four `*FromContext` helpers are dead production code

**Severity:** medium
**Location:** `internal/oauth/middleware.go:73-76` (`WithMaxDPoPAge`),
:252-267 (`RequireAuth`), :311-336 (`RequireScope`), :373-388
(`AccessTokenFromContext`, `ScopesFromContext`), :441-445
(`UseJTI`), :450-475 (`CleanupExpiredJTIs`).
**Problem:** The only production caller of `AuthMiddleware` is via
`OptionalAuth` (admin handler — `internal/graphql/admin/handler.go:256,
261`). Six surrounding helpers are exercised only by
`middleware_test.go`:
- `WithMaxDPoPAge` (no caller sets a custom age — the default 300s
  is what production uses)
- `RequireAuth` (no path requires OAuth-only auth; admin endpoint uses
  the OptionalAuth + downstream API-key check)
- `RequireScope` (no scope enforcement anywhere)
- `AccessTokenFromContext` (no resolver reads back the typed token —
  `UserIDFromContext` is what production reads at line 364)
- `ScopesFromContext` (no resolver reads scopes)
- `UseJTI` and `CleanupExpiredJTIs` (the DPoP JTI replay store is
  consulted directly through the `JTIStore` interface at
  `validateDPoPToken:144`; the free functions are a parallel surface
  with no callers)

The `validateBearerToken` private helper (line 209-249) is also
unreachable because no production path mounts `RequireAuth` (which
is the only entry into the bearer/DPoP dispatcher). The DPoP path
is genuinely reached today because `OptionalAuth` calls
`ValidateRequest` which dispatches to it — but the bearer arm at
line 209-249 has no live path.
**Why it matters:** The brief calls out "extending R-12/R-15/R-16"
defensive-check pruning. This is a structural variant: half of
`AuthMiddleware`'s public surface is aspirational. Combined with
Q-1, Q-2, Q-3 the picture is clear: `internal/oauth/` was designed
as a general-purpose OAuth server module, but the production
deployment is "OAuth bridge to an upstream PDS" — the surface kept
expanding to fit the original mental model, the deployment kept
narrowing. A reader looking at `middleware.go` today reasonably
assumes `RequireAuth` is the canonical "lock this endpoint" tool;
it isn't called from anywhere.
**Proposed fix:** Delete the seven helpers above and the
`validateBearerToken` private method. Keep `OptionalAuth`,
`ValidateRequest`, `validateDPoPToken`, `writeError`, the
`AuthResult`/`AuthMiddleware` types, and `UserIDFromContext` /
`NewAuthMiddleware` / `JTIStore` / `AccessTokenStore`. Reduces
`middleware.go` from ~480 LOC to ~280 LOC; the resulting surface is
exactly "OptionalAuth and the context-DID read", which matches the
production model.
**Effort:** S
**Risk:** low
**Reversibility:** easy

---

### Q-5: Three small helpers are dead production code — `DecodeError`, `IsDIDPLC`/`IsDIDWeb`, `IsValidNSID`

**Severity:** medium
**Location:** `internal/database/executor.go:84-86` (`DecodeError`);
`internal/oauth/did.go:371-378` (`IsDIDPLC`, `IsDIDWeb`);
`internal/lexicon/nsid.go:76-99` (`IsValidNSID`).
**Problem:** Three independent dead-code instances surfaced by the
exported-function audit:

1. **`database.DecodeError`** — sibling of `ConnectionError`,
   `QueryError`, `ConstraintError`. The other three are used by
   `internal/database/postgres/executor.go` to wrap pgx errors
   (lines 122, 145, 160, 174, 197, 199). `DecodeError` exists for
   symmetry but has no caller. The decode failure path in pgx surfaces
   through `QueryError` instead.
2. **`oauth.IsDIDPLC` / `oauth.IsDIDWeb`** — typed predicates over a
   DID string. No caller anywhere in the tree. The production code
   either uses the strict `did.IsValid` (which doesn't distinguish
   method) or pulls the prefix inline via `strings.HasPrefix`.
3. **`lexicon.IsValidNSID`** — pure utility, no caller. NSID validity
   is enforced at lexicon-load time via the parser's structural
   checks, not via this helper.
**Why it matters:** Lower individual leverage than Q-1/Q-2/Q-3, but
same class — readers find these on grep, assume they're canonical,
write new code against them, and now there's a fork that the
production path doesn't exercise.
**Proposed fix:** Delete each (and the matching tests). Combined ~50
LOC including tests. Same external-API check.
**Effort:** S
**Risk:** low
**Reversibility:** easy

---

### Q-6: Lens 1 (natural seams in large files) — no findings

**Severity:** N/A
**Location:** `internal/graphql/schema/builder.go` (1166 LOC),
`internal/database/repositories/records.go` (1177 LOC),
`internal/database/repositories/filter.go` (1084 LOC),
`cmd/hypergoat/main.go` (1372 LOC).
**Result:** None of the four warrant a split.

- **builder.go**: Contains a logically-distinct ~250 LOC block of
  package-level `var` GraphQL type declarations (`recordEventType`,
  `collectionStatType`, `timeSeriesPointType`,
  `collectionTimeSeriesType`, `genericRecordType`,
  `genericRecordEdgeType`, `genericRecordConnectionType`). Moving them
  to `types_static.go` would save ~250 LOC in builder.go, but the
  declarations reference the resolver closures at the call sites
  inside `buildSubscriptionType` and `createGenericRecordsResolver`,
  so splitting introduces a cross-file dependency. Not worth it.
- **records.go**: Cohesive — one repository type, one file, one
  package. The 4-method `CreateFieldIndex` / `DropFieldIndex` /
  `fieldIndexName` cluster could move to `field_index.go` but it's
  ~40 LOC; not worth a separate file.
- **filter.go**: The bottom 200 LOC are per-`Kind` SQL helpers
  (`buildContributorFilter`, `buildBadgeAwardSubjectFilter`,
  `buildStringSubjectFilter`, `extractInValues`); they could move to
  `filter_kinds.go`. But these are exactly the helpers R-2 is
  proposing to *collapse*; splitting before collapsing would just add
  churn. Wait for R-2 to land, then re-evaluate.
- **main.go**: Already organized as a top-down narrative with named
  phase functions (`initServices` → `setupRouter` → `setupOAuth` →
  `setupAdmin` → `setupGraphQL` → `startWorkers` → `startJetstream` →
  `startLabeler` → `startBackfill` → `serve`). Each is the right
  granularity. Splitting into `setup.go` / `start.go` would add files
  without subtracting confusion — the reader benefits from seeing the
  full boot order in one scroll.

The size is genuine cohesion in each case, not accidental
accumulation.

---

### Q-7: Lens 3 (defensive checks for impossible states in OAuth + labeler) — no new findings beyond R-12/R-15/R-16

**Severity:** N/A
**Location:** scanned `internal/oauth/` (17 files) and
`internal/labeler/` (8 files).
**Result:** Every `== nil` check in these subsystems is either:
1. **A pointer to a semantically-nullable value** (e.g.,
   `accessToken.DPoPJKT *string` is genuinely nil for non-DPoP
   tokens — middleware.go:182, 215, 233; `accessToken.UserID *string`
   for service-owned tokens — middleware.go:191, 233).
2. **An external-data shape check** (e.g., `doc.AtprotoSigningKey()`
   returns nil when the DID document doesn't have a signing key —
   serviceauth.go:228; the parsed key's `P256`/`Secp256k1` is nil
   when the alg/curve don't match — serviceauth.go:238, 243).
3. **A programmer-error panic guard** (e.g., `ServiceAuthMiddleware`
   panics if `verifier == nil` — serviceauth_middleware.go:38; same
   shape for `expectedLxm == ""`). These are appropriate.
4. **A genuine "optional" configuration guard** (e.g.,
   `b.signingKey == nil` in bridge.go:391 — the `SigningKey` field
   is documented as optional and the wiring path supports nil).
5. **A guard against a caller-passed nil element** (e.g.,
   `l == nil` in `validateLabel` consumer.go:623 — the labels slice
   is iterated and individual entries may be nil from the upstream
   labeler frame).

The R-12/R-15/R-16 "impossible state" pattern (redundant validation
after the constructor contract guarantees the field is set) doesn't
recur in the OAuth or labeler code. Both subsystems consistently
either trust the constructor or check pointers that have a real
nil meaning.

---

### Q-8: Lens 4 (comments that explain WHAT instead of WHY) — only the dead-code zones have egregious offenders

**Severity:** N/A
**Location:** the affected comments are clustered in the files Q-1, Q-2,
Q-3, Q-5 propose deleting.
**Result:** Spot-checked `records.go`, `builder.go`, `filter.go`,
`main.go`, the bulk of `internal/oauth/` — and the comment hygiene
is consistently *why*, not *what*. Examples of good comments:
`records.go:92-96` (the `recordColumns()` comment explains *why*
the `r.` prefix is always emitted — every read JOINs the actor
table); `records.go:124-128` (the `Insert` wrapper notes which path
is canonical and why it's kept); `filter.go:223-227` (the
load-bearing SQL-emission security warning is repeated at every
call site by R-20's deliberate convention).

The blatant offenders (`// Returns a 43-character URL-safe base64
string (32 random bytes encoded).` next to `func GenerateAccessToken()
(string, error)` — token_generator.go:11-13) are concentrated in
the dead-code files. Deleting the dead code is the simpler win.

---

### Q-9: Lens 5 (test code quality) — no findings worth filing

**Severity:** N/A
**Location:** scanned `internal/database/repositories/records_filter_test.go`
(1971 LOC), `internal/database/repositories/filter_unit_test.go`
(1684 LOC), `internal/graphql/schema/where_test.go` (679 LOC).
**Result:** The test code is dense but legitimately so. Three classes
of suspicion the brief calls out, and what was actually found:

1. **Tautological tests.** None. Every test exercises a behaviour;
   none assert "the value I set equals the value I read back without
   any intermediate function call." The closest are the
   `TestBuildSingleFilter_*_ShapeAndLowering` tests in
   `filter_unit_test.go` which assert exact SQL output strings — but
   those are *intentional drift-guards* for the migration-coupled
   SQL (the partial index expressions must match byte-for-byte). The
   security pass's lens explicitly preserved this pattern.
2. **Setup-only tests.** None. Even the largest setup blocks
   (`records_filter_test.go:170-220` for keyset-pagination stability)
   exercise a non-trivial path through `GetByCollectionFiltered`.
3. **Mock-not-code tests.** None — the tests run against a real
   Postgres (`testutil.SetupTestDB`), so they cannot test "the mock".
   The pure-SQL builder tests in `filter_unit_test.go` use no mock,
   just direct calls to the emitter functions.
4. **Brittle implementation-pinning assertions.** The SQL-string
   assertions are pinning by design (the brief's preserved category).
   The error-message-text assertions (e.g.,
   `TestValidate_Ini_EmptyListRejected` requires the string `"1 to 50"`
   appears) are slightly brittle, but these messages are the contract
   to the consumer — changing them is a change to the API and
   *should* break tests.

Honest empty result: the test code in this repo is well-handled.

---

### Q-10: Lens 6 (dependency cycles / surprising imports) — no findings

**Severity:** N/A
**Location:** scanned cross-package imports of `internal/oauth`.
**Result:** Three packages outside `internal/oauth/` and outside
`internal/server/` import `internal/oauth`:
- `internal/ingestion/processor.go` — for `oauth.DIDCache` (PDS
  resolution).
- `internal/backfill/backfill.go` — for `oauth.DIDCache`,
  `oauth.NewDIDResolver`, `oauth.WithPLCDirectoryURL`,
  `oauth.WithResolver`, `oauth.WithCacheTTL` (same PDS resolution).
- `internal/graphql/admin/handler.go` and
  `internal/graphql/admin/resolvers_oauth_clients.go` — admin OAuth
  client management.

The first two are a naming-organization smell — the DID cache lives
in `internal/oauth/` because that's where it was first needed, but
it's a generic utility used by ingestion and backfill which have no
real OAuth concern. Moving `DIDCache` + `DIDResolver` to
`internal/atproto/did/` would fix the smell but is the kind of churn
the brief deprioritises. Not flagged — the import is honest, and
neither package imports types they shouldn't see.

`internal/database/repositories/oauth_*.go` files all import
`internal/oauth` for the row-shape types (`*oauth.AccessToken`,
`*oauth.RefreshToken`, etc.) — that's a normal repository →
domain-model dependency direction.

No cycles. `go list -deps` and `go vet ./...` would have caught
import cycles structurally; the import graph is clean.

---

### Q-11: Lens 7 (error message quality on operator paths) — no findings

**Severity:** N/A
**Location:** scanned `cmd/hypergoat/main.go` startup path,
`internal/database/migrations/migrations.go`,
`internal/config/config.go`.
**Result:** The operator-facing error messages are consistently
well-tagged.

- **Startup errors** propagate from `run()` lines 189-194 as bare
  `return err`, but the underlying `config.Load()` and `config.Validate()`
  produce already-tagged messages (`"SECRET_KEY_BASE must be at least
  64 characters"`, etc. — config.go:272). The shell sees a clear
  message.
- **Migration errors** include the migration version in every wrap
  (`"failed to apply migration %s: %w"`, `"failed to record migration
  %s: %w"`, `"failed to commit migration %s: %w"` — migrations.go:107,
  113, 117). The brief's hypothetical "which migration failed" is
  exactly what these print. The "manual fix needed" hint in the
  non-transactional partial-failure path (line 144) is operator-grade.
- **DB connect errors** flow through `database.ConnectionError`
  wrappers in `internal/database/postgres/executor.go:122, 145, 160`
  with parsed-URL / open-DB / ping-DB tags.

No new finding. The error-message hygiene matches the rest of the
codebase's quality.

---

## Summary

| Severity | Count |
|----------|------:|
| High     | 3 (Q-1, Q-2, Q-3) |
| Medium   | 2 (Q-4, Q-5) |
| Low      | 0 |
| Honest empties (lenses scanned, no finding) | 6 (Q-6, Q-7, Q-8, Q-9, Q-10, Q-11) |
| **Total entries** | 11 |

Five findings, six honest-empty lens reports. Well under the 15
ceiling.

**The cluster.** Q-1 through Q-5 are all the same shape: the
`internal/oauth/` subsystem was designed as a general-purpose OAuth
server, then the deployment narrowed to "OAuth bridge to upstream
PDS." The server-shaped helpers were never deleted. Combined deletion
delta: ~400 LOC of source + ~400 LOC of corresponding tests. Every
piece is structurally simple to remove (no callers to update). The
external-API check (certified-app, admin client) is the only thing
that gates the deletion; assuming it comes back clean, this is
straight subtraction.

**The honest empties.** Lens 1 (big-file seams), Lens 3 (defensive
checks in OAuth/labeler), Lens 4 (WHAT-not-WHY comments), Lens 5
(test code quality), Lens 6 (import cycles), Lens 7 (operator error
messages) — all came up clean. The brief explicitly values these as
useful results; the codebase has been audited well, and most of the
brief's hypothetical concerns don't materialize.

**Highest-leverage fix.** Q-1 (delete `errors.go`) — pure subtraction,
no logic change, ~290 LOC of source+test gone, eliminates the most
likely "wrong-tool reach" trap a future contributor will hit.
