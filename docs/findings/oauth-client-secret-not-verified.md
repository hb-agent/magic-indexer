# Finding — `client_secret` is generated but never verified

**Filed**: 2026-05-17 (surfaced during plan-review round 1 of
`docs/review-2026-05-17/`, item R4.7).

**Severity**: Medium. Largely mitigated by the discovery doc (see
"Why this is not P0" below), but the surface is misleading and
should be fixed.

**Status**: Filed for a follow-up deep-flow track. Not folded into
`review-2026-05-17` because the fix touches the `/oauth/token`
flow which that batch does not.

---

## What's broken

`internal/server/oauth_register.go` accepts `client_secret_basic` and
`client_secret_post` as values for `token_endpoint_auth_method` on
the dynamic-registration endpoint. When it does, the handler:

1. Marks the client as `ClientConfidential` (`oauth_register.go:118-121`).
2. Generates a `client_secret` (`oauth_register.go:127, 243-245`).
3. Stores it (`oauth_register.go:148, 164`).
4. Returns it in the registration response (`oauth_register.go:181`).

But the token endpoint never reads or checks it:

- `handleAuthorizationCodeGrant` (`internal/server/oauth_handlers.go:582-770`)
  reads `code`, `client_id`, `redirect_uri`, `code_verifier` from
  `r.Form` (`:586-589`) — but **never reads `client_secret`** and
  never compares against `client.ClientSecret`.
- Same handler fetches the client at `:605` but only uses it to
  confirm existence; the `ClientSecret` field is ignored.
- `handleRefreshTokenGrant` (skim the file around the
  `refresh_token` grant) — same story; refresh by `client_id` and
  the token alone.

So a caller in possession of a confidential client's `client_id`
plus a valid authorization code (or refresh token) can complete the
grant without presenting the secret. The secret is decorative.

## Why this is not P0

The server's authorization-server metadata advertises only `"none"`
and `"private_key_jwt"` as supported token-endpoint auth methods
(`internal/server/oauth_handlers.go:146`):

```go
TokenEndpointAuthMethodsSupported: []string{"none", "private_key_jwt"},
```

A correctly-implemented AT Protocol OAuth client reads the discovery
doc and won't pick `client_secret_basic` / `client_secret_post`.
PKCE plus DPoP cover the realistic flows. So in practice, the dead
code path doesn't get exercised.

The reason it's still worth fixing:

- Dynamic registration *accepts* `client_secret_*` values and returns
  a populated `client_secret`. An operator inspecting the registration
  response would reasonably believe the secret is enforced.
- A client implementer who deliberately ignores the discovery doc and
  picks `client_secret_basic` (e.g. expecting full OAuth 2.1
  compliance) silently downgrades to PKCE-only without warning.
- The `ClientType: "confidential"` field is returned in the
  registration response (`oauth_register.go:189`), which is a lie.

## Recommended fix

Two options, both small:

- **Option A — reject at registration**: if the request specifies
  `client_secret_basic` or `client_secret_post`, return a
  registration error pointing at the supported list in the discovery
  doc. Don't generate a secret. Adjust the `parseAuthMethod` switch
  at `oauth_register.go:290-296` to error on those values. Smallest
  diff; matches AT Protocol's public-clients-only posture.

- **Option B — actually verify the secret on `/oauth/token`**: read
  `client_secret` from the Authorization header (basic) or form body
  (post), constant-time-compare against `client.ClientSecret` when
  the client's `TokenEndpointAuthMethod` requires it. Larger diff;
  brings the server to OAuth 2.1 conformance for these methods, but
  the discovery doc would also need to advertise the methods.

**Recommendation: Option A.** This server is an AT Protocol OAuth
provider; AT Protocol's OAuth profile assumes public clients with
PKCE/DPoP. Adding real `client_secret` support is scope creep with
no consumer asking for it.

## Acceptance criteria for the follow-up fix

1. Posting `token_endpoint_auth_method=client_secret_basic` (or
   `_post`) to `/oauth/register` returns `400 Bad Request` with
   `error_description` pointing to the supported list.
2. No `client_secret` is ever stored for `client_secret_*` clients
   (because none get registered).
3. Existing public-client and `private_key_jwt` paths unchanged.
4. Migration: any existing rows with non-NULL `ClientSecret`
   get their secret nulled out (or the row deleted, per operator
   call). Likely zero rows in practice.
5. Tests cover both rejection paths and confirm the discovery doc
   still advertises the unchanged supported list.

## Out of scope for this finding

- The audit's other OAuth defense-in-depth items (constant-time
  compares, JTI cleanup floor, shutdown WaitGroup) — those live in
  Track 3 of `review-2026-05-17`.
- The R4.6 JTI-burning DoS at `middleware.go:131` — separate
  follow-up.
- Any redesign of the OAuth provider's posture (public/confidential
  mix, DPoP-only mode, etc.).

## Owner

Operator decision: prioritise vs. other backlog. No CVE assigned;
the surface is reachable only via a deliberately-non-compliant
client choice and the secret-bearing response is misleading rather
than exploitable in the deployed configuration.
