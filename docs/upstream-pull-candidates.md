# Upstream Hyperindex — fixes considered for Magic Indexer

**Date:** 2026-05-13
**Reference:** `hypercerts-org/hyperindex` (the public source we forked from)

Upstream `main` sits at the same commit Magic Indexer forked from; all upstream activity is on unmerged feature branches (`tap-feature`, `filter-feature`, `production`, `admin-secret`, `url-fix`). This document catalogues the commits worth carrying into Magic Indexer and our verdict on each.

Adopted in PR `feat/upstream-adoption`: Tracks **B, C, D, E, F, G, H** + a SECURITY.md note. Dropped: **Track A** (NonNull coercion — see § A below for why).

---

## Adopted

### Track B — Mixed-union zero-property `ObjectDef`
- Reference: `5826485` (filter-feature, `hyperindex-4jy.2`)
- Bug: a lexicon `{"type": "object"}` with no properties parses to an `ObjectDef` with `len(Properties)==0`; `buildUnionType` then passes it to `BuildObjectType` which graphql-go refuses to register, panicking the schema build.

### Track C — `EXTERNAL_BASE_URL` normalization
- Reference: `b926892`, `3562648` (production)
- Bugs: schemeless values double the host in browser-resolved URLs; mixed-case `HTTPS://` defeats the HSTS gate; trailing slashes produce `//oauth/callback` that mismatches OAuth redirect URIs.
- Magic-Indexer extension: loopback hosts (`localhost`, `127.0.0.1`, `::1`) default to `http://` instead of `https://` so bare-host dev configs don't get pinned to TLS.

### Track D — OAuth callback public-origin fallback
- Reference: `7bc790b` (production, but adapted)
- Reference uses `PUBLIC_CLIENT_URL`; we already have `PUBLIC_URL` wired through the OAuth client and client metadata. **Reuse the existing var** — split source-of-truth would silently fail on existing Vercel deploys.

### Track E — Admin actor purge with preview-then-confirm
- Reference: `de7ec98`, `13a40b4`, `135bd49` (production)
- Replaces SQL-by-hand for takedowns / GDPR / test cleanup.
- Magic-Indexer extension: HMAC-signed `confirmToken` bound to `(admin_did, target_did, record_count, exp)` with 5-min TTL, constant-time compare, single-use via in-memory used-set. Reference uses a literal `confirm: "PURGE"` string with no TTL.
- Transaction shape: SQL-only; Tap removal best-effort after commit (Tap is an HTTP sidecar and cannot enlist in `sql.BeginTx`). Reference `135bd49` already discovered this and removed Tap from the transaction.

### Track F — Batch lexicon registration UI
- Reference: `6275449` (tap-feature, `hyperindex-2rz`)
- Pasting many NSIDs (comma- / newline-separated) registers them serially with per-item status.

### Track G — Settings UI admin gate
- Reference: `7536679`, `593fcc9` (production)
- Hide destructive controls from non-admins. Server is still the security boundary.
- Magic-Indexer extension: gate on `session.did ∈ settings.adminDids` (server-known list via iron-session), NOT a `NEXT_PUBLIC_ADMIN_DIDS` env var — single source of truth.

### Track H — Fail fast on self-referential backend URL
- Reference: `88663c0` (production)
- Detect when `HYPERGOAT_URL` points at the client's own origin at module load. Refuse in dev, warn in production.

---

## Dropped

### Track A — NonNull coercion for required lexicon fields
- Reference: `7fc19ad`, `fb5c723`, `ae64fae`, `e706083` (tap-feature, `hyperindex-vz7`)
- Reference behaviour: when an indexed record is missing a lexicon-required field, coerce the field to a type-default zero value so GraphQL's NonNull contract holds.
- **Our position:** Magic Indexer already solves this *better* via `SanitizeRecord` (`internal/lexicon/validator.go:225`, `internal/graphql/schema/builder.go:860,910`) — it **skips** records with missing required fields. A coerced record (`""` for CID, `0` for datetime, etc.) would pass GraphQL validation but **lie to clients**. The reference's choice exists because upstream lacks our sanitizer; we don't need it.
- If we ever revisit, the design must be format-aware, only coerce when no `minimum` / `enum` / `format` constraints exist, handle nested-required and array-required, preserve keyset-cursor correctness, and emit `graphql_required_field_coerced_total{collection,field}` for observability.

---

## P3 — skipped

- Make `statistics` / `activityBuckets` / `recentActivity` public (`9e8acfc`). We're stricter than upstream by design.
- Relative paths in GraphiQL (`df322f4`).
- CodeRabbit config (`c8d76bd`, `dbe0b56`).
- `NEXT_PUBLIC_API_URL` → `NEXT_PUBLIC_HYPERINDEX_URL` rename (`6c7d2fc`).
- `golangci-lint v2` issue fixes (`be7492a`, `5716275`, `cd6f9ce`).
- Beads (`.beads/`) dev tooling.
