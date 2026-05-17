# Record edit history v2 — crowdfunding-campaign evidence trail

**Status**: Proposal (supersedes `record-edit-history.md` and folds in `record-edit-history-review.md`).
**Date**: 2026-05-14.

---

## 0. Concrete consumer (the question § 5 of the original review demanded)

> **Crowdfunding partner.** Magic Indexer's operator runs an instance that backs a crowdfunding campaign. The campaign relies on the indexed record data (claims, evaluations, attestations under the `org.hypercerts.*` namespace) as the source of truth shown to donors. The partner running the campaign needs to:
>
> 1. **Prove the state of records over the campaign's full duration** — at any later date, be able to answer "what did record X look like on day Y of the campaign?"
> 2. **Export the full history for a specified list of DIDs** (the campaign's participating actors) to take to their own systems / present to donors / use in dispute resolution.
> 3. **Optionally self-serve the export** — the data is public; the partner should be able to pull what they need without operator round-trips.

This is the use case. It answers four of the six open questions from the review at once:

- **Use case shape** (Q1): legal-defence audit + bulk evidence export. NOT user-facing edit indicator, NOT operational audit of indexer state.
- **Layer** (Q2): Magic Indexer is correct. The partner needs one cross-DID query over the lexicon set; the PDS can't do that, and `jetstream_activity` at 7 days is far too short.
- **JSON body** (Q3): required, in full. CID-only is insufficient for evidentiary use.
- **Retention default** (Q6): essentially forever (campaign duration + dispute window, in years). The 90-day floor proposed in the review is wrong for this use case.

Two questions remain operator decisions:
- **Tamper-evidence depth** (new): CID-only, operator-signed manifest, or full Merkle commitment?
- **`purgeActor` interaction** (Q5): hard-block while a DID is in active campaign scope, or soft-warn?

---

## 1. Recommended shape

Keep Option C from the original proposal (append-only `record_history` sidecar) as the storage primitive. Layer two new things on top of it:

1. **Bulk export endpoint** — `/export/record-history` (HTTP, streaming JSONL), DID-list parameter, optional time range, gzip-compressed. Not GraphQL; the bulk shape doesn't fit a paged connection.
2. **Tamper-evidence column on the history row** — a server-computed integrity field, exact shape an operator decision (see § 4).

Plus all 30 accepted findings from the review (§ 4 of `record-edit-history-review.md`), with the retention default flipped from 90 days to "forever / operator-configurable in years."

---

## 2. Schema (revised from § 3.C.1 of the original)

```sql
CREATE TABLE record_history (
    id BIGSERIAL PRIMARY KEY,
    uri TEXT NOT NULL,
    cid TEXT,                   -- NULL for delete tombstone
    did TEXT NOT NULL,
    collection TEXT NOT NULL,
    rkey TEXT GENERATED ALWAYS AS (substring(uri from '[^/]+$')) STORED,
    operation TEXT NOT NULL CHECK (operation IN ('create', 'update', 'delete')),
    json JSONB,                 -- NULL for tombstone
    sort_at TIMESTAMP WITH TIME ZONE,
    indexed_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),

    -- NEW for v2: tamper-evidence column.
    -- Server-computed hash binding (uri, cid, indexed_at, json) at write time.
    -- For "operator-signed manifest" mode, this is an HMAC-SHA256 over the
    -- canonical concatenation, keyed by SECRET_KEY_BASE.
    -- For "CID-only" mode (cheapest), this is NULL — the partner verifies
    -- the export rows against the CID using DAG-CBOR canonicalization.
    integrity_tag TEXT
);

-- Pagination + retention indexes (per review F-DB-002).
CREATE INDEX idx_record_history_uri_indexed_at_id
    ON record_history (uri, indexed_at DESC, id DESC);
CREATE INDEX idx_record_history_indexed_at
    ON record_history (indexed_at);                       -- retention sweep
CREATE INDEX idx_record_history_did_indexed_at_id
    ON record_history (did, indexed_at DESC, id DESC);    -- export by DID list
```

The `idx_record_history_did_indexed_at_id` index is now load-bearing — it's what the export endpoint scans for the partner's DID list. (Per the review, the original proposal's `did` index was speculative; here it's the access pattern that justifies it.)

Drop the `collection` index for v1. The bulk export filters by DID list; collection narrowing inside the export is a server-side iterate-and-filter (cheap once we're inside a DID's rows).

---

## 3. Bulk export endpoint

### 3.1 — HTTP shape

```
POST /export/record-history
Content-Type: application/json
Authorization: Bearer <export-api-key>

{
  "dids": ["did:plc:abc...", "did:plc:def...", ...],
  "since": "2026-05-01T00:00:00Z",        // optional
  "until": "2026-05-14T23:59:59Z",        // optional
  "include_tombstones": true,             // default true; delete events count as evidence
  "include_json": true                    // default true; false for cid-only listing
}

→ 200 OK
Content-Type: application/x-ndjson
Content-Encoding: gzip
X-Export-Total-Estimate: 1500000          // server-side rough count; the stream may differ slightly
X-Export-Schema-Version: v1
Transfer-Encoding: chunked

{"uri":"at://did:plc:abc/.../1","cid":"bafyrei...","did":"did:plc:abc","collection":"org.hypercerts.claim.activity","operation":"create","json":{...},"sort_at":"2026-05-02T10:14:32Z","indexed_at":"2026-05-02T10:14:33.842Z","integrity_tag":"hmac-sha256:abc123..."}
{"uri":"at://did:plc:abc/.../1","cid":"bafyrei2...","did":"did:plc:abc","collection":"org.hypercerts.claim.activity","operation":"update","json":{...},"sort_at":"2026-05-05T09:00:00Z","indexed_at":"2026-05-05T09:00:01.221Z","integrity_tag":"hmac-sha256:def456..."}
{"uri":"at://did:plc:abc/.../2","cid":null,"did":"did:plc:abc","collection":"...","operation":"delete","json":null,"sort_at":null,"indexed_at":"2026-05-06T12:30:00Z","integrity_tag":"hmac-sha256:..."}
...
```

Why JSONL (newline-delimited JSON), not a JSON array: the payload can be gigabytes for a large campaign; the partner streams it through `jq -c` or a database loader without buffering. Standard format for bulk data exports.

Why HTTP not GraphQL: a 3 GB streaming download over a GraphQL pagination loop is wrong for both sides. The partner shouldn't have to track cursors for an evidence pull; the server shouldn't have to materialize JSON results in a graphql-go response shape. One pgx server-side cursor + chunked transfer + gzip = standard.

### 3.2 — Server implementation sketch

```go
func (h *ExportHandler) HandleRecordHistory(w http.ResponseWriter, r *http.Request) {
    // Auth: API key → operator-scoped export keys (see § 3.3).
    key, ok := h.validateExportKey(r)
    if !ok { http.Error(w, "unauthorized", 401); return }

    var req ExportRequest
    if err := json.NewDecoder(r.Body).Decode(&req); err != nil { ... }

    // Validate every DID via did.IsValid (per the May 13 lint rule).
    for _, did := range req.DIDs {
        if !didpkg.IsValid(did) {
            http.Error(w, "invalid DID in list", 400); return
        }
    }

    // Scope check: the API key may be restricted to a subset of DIDs.
    if !key.allows(req.DIDs) { http.Error(w, "key not authorized for one or more DIDs", 403); return }

    // Estimate count for the header (one cheap COUNT(*) with the same WHERE).
    estTotal, _ := h.repo.CountForExport(r.Context(), req)
    w.Header().Set("X-Export-Total-Estimate", strconv.FormatInt(estTotal, 10))
    w.Header().Set("X-Export-Schema-Version", "v1")
    w.Header().Set("Content-Type", "application/x-ndjson")
    w.Header().Set("Content-Encoding", "gzip")
    w.Header().Set("Transfer-Encoding", "chunked")

    // Stream via pgx server-side cursor; never load all rows into memory.
    gz := gzip.NewWriter(w)
    defer gz.Close()
    enc := json.NewEncoder(gz)

    err := h.repo.StreamExport(r.Context(), req, func(row HistoryRow) error {
        return enc.Encode(row)
    })
    if err != nil {
        // Mid-stream errors land in the body; the partner detects truncation
        // by comparing line count to X-Export-Total-Estimate.
        slog.Error("export stream failed", "key", key.id, "err", err)
        _, _ = gz.Write([]byte(`{"error":"stream truncated","reason":"server"}` + "\n"))
        return
    }
}
```

Server-side cursor (pgx `Conn.Query` with `QueryExecModeExec` + `FETCH`) is the right shape — `MaxOpenConns=50` and the per-query `statement_timeout=30s` make a long export problematic, so the export uses a dedicated long-running connection from a separate pool (or bypasses the public pool entirely).

### 3.3 — Authentication

The user said "the data is public." Three options, increasing rigour:

**Option E-1: open + rate-limited.**
No auth. Anyone can export anyone's history. Rate-limit at the proxy (existing pattern — see SECURITY.md). Simplest. Acceptable IF the data really is public and the operator is comfortable with unscoped scrapes.

**Option E-2: operator-issued API keys (recommended).**
Each partner gets an API key. Keys are scoped: either "all DIDs" (operator's own use) or "this specific DID list" (campaign-scoped). Stored in a new `export_api_key` table; the admin GraphQL exposes mutations to mint/revoke. Rate-limited per key.
- Pros: operator controls who pulls what; cheap to revoke; audit log per key.
- Cons: operator round-trip to provision the partner.

**Option E-3: ATProto service-auth JWT (the existing `/notifications/graphql` pattern).**
Partner mints a JWT signed by a DID's signing key. Export endpoint validates `aud=DOMAIN_DID, lxm=com.magicindexer.export-history`. Returns only the JWT-issuer DID's history.
- Pros: cryptographic, self-serve, no operator-issued credential.
- Cons: only works for "the DID's owner exports their own history." Doesn't fit "a third-party partner exports many DIDs' history" unless every campaign participant signs over a delegation to the partner. That delegation infrastructure doesn't exist in ATProto yet.

**My recommendation: ship E-2.** It matches the actual workflow ("partner identifies, operator authorises a list of DIDs"). Defer E-3 to a v2 when ATProto adds delegation. E-1 is too coarse for a system that the partner uses for legal evidence.

### 3.4 — Self-serve export

E-2 supports self-serve in the cleanest way:

1. Operator provisions an API key for the partner, scoped to the campaign's DID list. One admin GraphQL mutation, ~10 seconds.
2. Partner curls `/export/record-history` with the key, gets streaming JSONL, processes locally.
3. Partner can pull as often as they need until the operator revokes the key.

The partner never has to ask the operator for a fresh export. The operator never has to babysit a bulk download. The data is exactly what would be in a one-shot export, just on demand.

---

## 4. Tamper-evidence: three rungs

The partner's "prove what the record said at time T" claim requires SOMETHING beyond "trust the indexer's database." Three rungs of rigour:

### Rung 1: CID-only verification (zero new server cost)

Each export row carries `cid`. The CID is a hash of the canonical DAG-CBOR encoding of the record JSON; the partner can independently hash their copy of `json` and compare. If they match, the indexer's JSON IS the record the PDS published. No server signature needed.

The CID-only rung does NOT prove the indexer recorded this state at the claimed `indexed_at`. It only proves the JSON-vs-CID binding (which the PDS already guarantees). For a partner who only cares about "what did the record say," this is enough.

### Rung 2: HMAC-signed manifest (recommended)

For every history row, compute and store:
```
integrity_tag = HMAC-SHA256(
    key = SECRET_KEY_BASE,
    message = canonical_concat(uri, cid, indexed_at_rfc3339nano, json_canonical)
)
```

Export rows include `integrity_tag`. The operator publishes the HMAC's KEY ID and verification recipe in SECURITY.md. **Crucially: the operator does NOT publish the HMAC secret.** Verification is asymmetric in the sense that "the indexer claimed X" can be proven (the indexer-side signature exists), but third parties cannot forge new claims without the secret.

This is "trust-on-write" semantics: the partner doesn't need to verify in real time, but at dispute resolution can hand the indexer-signed export to a third party along with the indexer's public statement of the verification recipe, and ask the indexer to re-derive the HMACs. If they match, the export is exactly what the indexer's database said at write time. (The operator and partner sign a separate agreement that the operator will not retroactively re-sign rows.)

Caveat: this only proves "the indexer's database said X at row-write time." It doesn't prove the operator hasn't since deleted and re-inserted rows with new HMACs over fake content. For higher rigour, see rung 3.

### Rung 3: External anchoring (defer to v2)

Periodically (hourly? daily?) hash a range of `record_history` rows together (Merkle root over `(integrity_tag, indexed_at)` for the period), publish the root to an external append-only log (a Bluesky post, a blockchain, a transparency log). The partner can later prove that a row existed at the time of the published root, defeating retroactive tampering.

This is real money — Merkle hashing, anchoring infrastructure, the operator running a publisher. For a v1, **don't ship this**. Plan for it if Rung 2 turns out to be insufficient for a legal dispute.

### My recommendation: ship Rung 2.

The HMAC column costs 32 bytes per row (44 bytes in base64) and one HMAC call per write — sub-microsecond. The verification recipe lives in SECURITY.md. Rung 1 (CID verification) comes free along with it. Rung 3 is documented as "v2 if needed."

---

## 5. Retention policy

The defensive position is: **retain forever** for any DID that participated in any campaign, until the campaign's legal-retention window has fully elapsed.

But "forever for everyone" is operator-hostile (per Ops-R1 F-OPS-001). The compromise is **campaign-aware retention**:

### Schema:

```sql
CREATE TABLE record_history_retention_scope (
    did TEXT PRIMARY KEY,
    retained_until TIMESTAMP WITH TIME ZONE NOT NULL,
    scope_id TEXT NOT NULL,                 -- "campaign:hypercerts-spring-2026" or similar
    created_by_did TEXT NOT NULL,           -- operator DID that registered the scope
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW()
);

CREATE INDEX ON record_history_retention_scope (retained_until);
```

### Retention worker logic:

```sql
DELETE FROM record_history
WHERE indexed_at < NOW() - $retention_default_days * INTERVAL '1 day'
  AND did NOT IN (
    SELECT did FROM record_history_retention_scope
    WHERE retained_until > NOW()
  );
```

DIDs with an active retention scope are excluded from pruning. DIDs without one fall under the default retention (operator-configured, say 90 days, or 0=forever).

### Admin GraphQL surface:

```graphql
mutation RegisterRetentionScope($scopeId: String!, $dids: [String!]!, $until: DateTime!) {
  registerRetentionScope(scopeId: $scopeId, dids: $dids, until: $until) {
    addedCount, alreadyScopedCount
  }
}
mutation ExtendRetentionScope($scopeId: String!, $until: DateTime!) {
  extendRetentionScope(scopeId: $scopeId, until: $until) { updatedCount }
}
mutation EndRetentionScope($scopeId: String!) {
  # Doesn't delete history immediately — just removes the retention shield.
  # Next retention sweep prunes per default.
  endRetentionScope(scopeId: $scopeId) { releasedCount }
}
```

This neatly separates the policy decision ("retain these DIDs for this long") from the implementation (the retention worker). The operator declares "campaign-A is running until 2030-12-31; here are its 4,000 DIDs," and the indexer guarantees those DIDs' history survives. Other DIDs prune normally.

### `purgeActor` interaction

If a DID is in an active retention scope, `purgeActor` must either:
- (a) refuse, with a clear error: `"DID in retention scope 'campaign:hypercerts-spring-2026' until 2030-12-31; release the scope first"`, OR
- (b) require an operator-confirmation flag: `purgeActor(did, confirmToken, overrideRetentionScope: "campaign:...")` that explicitly opts out for that one call.

**My recommendation: (a) refuse by default, (b) available as a separate `forcePurgeActor` mutation with its own audit log line.** This treats retention scopes as the more authoritative claim (legal-defence > GDPR-erasure in the relevant window per Art. 17(3)(e)).

### `resetAll` interaction

`resetAll` is the nuclear option. It clears `record_history` per the review. But it should ALSO clear `record_history_retention_scope` — leaving scopes pointing at DIDs that no longer have history makes no sense.

---

## 6. Updated component inventory

### What was in v1 (still needed):

- `record_history` table (revised schema in § 2).
- Append-only write path with `BeginTx` wrap (per review F-DB-010).
- `recordHistory(uri)` + `recordVersionAt(uri, at)` GraphQL fields (per original § 3.C.4).
- Retention worker with batched-CTE DELETE (per review F-DB-006).
- Five Prometheus series + audit logs (per review F-OPS-004, F-OPS-005).
- SECURITY.md + RUNBOOK.md sections (per review F-OPS-006, F-OPS-011).

### New for v2:

- `integrity_tag` column on `record_history`; HMAC computed at write time (§ 4 Rung 2).
- `record_history_retention_scope` table + admin GraphQL mutations (§ 5).
- `/export/record-history` HTTP endpoint with API-key auth (§ 3).
- `export_api_key` table + admin GraphQL mutations for key mint/revoke/list.
- Forced-purge variant of `purgeActor` for retention-scope override (§ 5).
- Dedicated DB connection pool for long-running exports (§ 3.2).
- Per-export metrics: `hypergoat_export_record_history_total{outcome}`, `hypergoat_export_record_history_bytes`, `hypergoat_export_record_history_rows`.
- Verification-recipe documentation in SECURITY.md (§ 4 Rung 2).

---

## 7. Effort estimate

| Track | Effort |
|---|---|
| Schema + write-path tx wrap (original Track) | ~2 d |
| Tamper-evidence (HMAC column + write-path compute) | ~0.5 d |
| Retention-scope table + admin mutations + worker integration | ~1 d |
| Forced-purge variant of `purgeActor` | ~0.5 d |
| HTTP export endpoint + streaming + gzip + API-key auth | ~2 d |
| `export_api_key` table + admin mutations | ~0.5 d |
| Documentation (SECURITY.md, RUNBOOK.md, partner integration guide) | ~1 d |
| Tests (resolver-level + Postgres-backed + HTTP-level + scope-aware retention) | ~1.5 d |
| **Subtotal** | **~9 d** |
| Plan-review + impl-review (per AGENTS.md deep flow) | ~1 d |
| **Total** | **~10 days** |

Substantially larger than the v1 estimate (~2 days). The new export endpoint and retention-scope logic dominate. **If the operator wants to ship incrementally, the right phasing is:**

- **Phase 1** (~3 d): schema + write path + tamper HMAC + retention-scope table. Admin GraphQL for `recordHistory(uri)` + `recordVersionAt(uri, at)`. Manual SQL for exports.
- **Phase 2** (~5 d): HTTP export endpoint + API keys + admin mutations + partner docs.
- **Phase 3** (~2 d): hardening, metrics, runbook, partner integration test.

The partner can use Phase 1 via admin GraphQL (one URI at a time) immediately. Phase 2 unlocks the bulk-export workflow.

---

## 8. Open questions (operator, please answer)

1. **Tamper-evidence rung**: 1, 2, or 3? My recommendation is 2. Confirm.

2. **Retention default for non-scoped DIDs**: 90 days, 7 days (matches `jetstream_activity` precedent), or 0=forever? Crowdfunding-context argues "long" for ALL DIDs since you don't always know in advance which ones a partner will care about. My recommendation: 1 year default; campaign-scoped DIDs override to whatever the campaign's window is. Confirm.

3. **Auth model for the export endpoint**: E-1 (open), E-2 (operator-issued API keys), E-3 (service-auth JWT)? My recommendation: E-2. Confirm.

4. **`purgeActor` while in retention scope**: hard-refuse (recommended), or soft-warn? Confirm.

5. **Schema version pinning**: the export's `X-Export-Schema-Version: v1` is a contract. The partner's tooling will hardcode it. Operator commitment: how many years before a breaking change, and what's the deprecation window?

6. **Campaign lifecycle**: who creates retention scopes — operator only (admin auth) or the partner via their API key? I think operator-only is safer (the partner can't extend their own retention beyond what was agreed).

---

## 9. Comparison with the original review's recommendation

The review concluded "don't advance until you name the consumer" and lazily added "if the answer is 'longer audit window', Option A wins." That's now wrong:

- Option A (extend `jetstream_activity`) doesn't have the export endpoint, doesn't have tamper-evidence, doesn't have retention scoping, and uses wire-format JSON. For "operator wants 30-day audit", it's fine. For "legal-defence evidence trail with partner export," it's not.
- Option C with the v2 additions covers the use case end-to-end and at a cost (~10 days) that's a fraction of running a campaign on weaker evidence.

The review's tactical findings (cursor format, `DateTime` scalar fix, tx wrap, retention-with-scope, `purgeActor`/`resetAll` interaction) still apply, all 30 of them, and they're folded into this v2.

---

## 10. Next step

Advance to `docs/proposals/record-edit-history-v2/plan.md` per the project's deep-flow process. Plan-review reviewers should focus on:

1. Tamper-evidence — is HMAC over `(uri, cid, indexed_at, json)` the right shape? Bikeshed the canonicalisation rules.
2. Retention scope — is the table schema right? Should scopes be hierarchical (campaign → sub-campaign)?
3. Export endpoint — is JSONL streaming the right transport? Should we offer a Parquet alternative for partners doing analytics? Should there be a resumable-by-cursor variant for partners on flaky networks?
4. API-key shape — single secret per key, or rotating? What's the revoke story?
5. Partner integration guide — what should the partner's tooling look like? Should we ship a reference CLI?

If the operator confirms § 8 answers in my proposed defaults, the plan can land in a day and implementation in ~10 days as above.
