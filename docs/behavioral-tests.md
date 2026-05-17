# Behavioral test catalogue

System-level tests that verify magic-indexer's user-visible
contract end-to-end. Complementary to (not a replacement for) the
Go unit and integration suites under `internal/`. Each test here
describes a behavior the deployed service must exhibit, with
runnable steps so a person or an AI agent can conduct it.

Intent of this doc:

- **Define the contract** in one place so a change that violates a
  behavior is loudly visible at review time, not in production.
- **Be runnable today** — every step is a real command, not pseudo-code.
- **Extend over time** — when a new feature ships, add a numbered
  test here following the template at the bottom.

If you find a behavior the indexer should guarantee that isn't
covered here, add it. If a test goes stale (commands changed,
endpoint moved), update it in place rather than letting it rot.

---

## How to run

### Targets

Each test declares a `Target`:

- **`LOCAL`** — requires a fresh local stack:
  ```bash
  make setup            # one-time: generates .env
  docker compose -f docker-compose.postgres.yml up -d
  make db-migrate
  make run              # in another terminal
  # default: http://localhost:8080
  ```
  Use for tests that are destructive (purge, resetAll, exit-42
  restart) or that need a known-clean DB. Tear down with
  `docker compose -f docker-compose.postgres.yml down -v` between runs.

- **`DEV-DEPLOYED`** — runs against the live development instance at
  `https://magic-indexer-dev.up.railway.app`. Use for read-only or
  state-additive tests where shared state is acceptable. Never run
  destructive tests here without operator approval — there is no
  separate staging deployment.

- **`EITHER`** — works against either target; behavior is independent
  of the deployment's existing state.

### Required tooling

- `curl` (every test).
- `gh` (only the admin tests that read PR / release state — none today).
- `psql` (some tests need to inspect or seed the DB directly).
- `ab` (the agent-browser CLI documented in `AGENTS.md`) for tests
  that need to verify browser-visible behavior (post-hydration
  errors, CORS, JS errors). The dev container has it installed.
- `python3` + `requests` (or Node + `fetch`) for the OAuth flow
  tests that need to follow redirects and handle DPoP signing.

### Conventions

- Replace `$BASE_URL` with either `http://localhost:8080` (LOCAL) or
  `https://magic-indexer-dev.up.railway.app` (DEV-DEPLOYED).
- Replace `$ADMIN_DID` and `$ADMIN_API_KEY` with operator-controlled
  values. In LOCAL these come from `.env`; in DEV-DEPLOYED, from the
  operator. Never paste live tokens into a log line — use
  `read -s` shell-side or environment variables.
- A test FAILS if any single step produces output that doesn't
  match the expected shape. Treat "almost right" as a fail and
  investigate.

---

## Test catalogue (checklist)

| ID | Name | Target | Notes |
|----|------|--------|-------|
| **A — Boot and basic surface** |
| [A1](#a1) | Service boots with valid config | LOCAL | |
| [A2](#a2) | `/health` returns 200 with expected body shape | EITHER | |
| [A3](#a3) | `/metrics` serves the Prometheus exposition format | EITHER | |
| [A4](#a4) | `/stats` returns operator-friendly counts | EITHER | |
| **B — Lexicons and GraphQL schema** |
| [B1](#b1) | Lexicon upload triggers exit code 42 | LOCAL | destructive (process exits) |
| [B2](#b2) | After restart, public GraphQL schema includes the new types | LOCAL | depends on B1 |
| [B3](#b3) | Schema-invalid lexicon upload rejected pre-commit | LOCAL | |
| **C — Ingestion (Jetstream)** |
| [C1](#c1) | Live Jetstream connection produces records in the DB | DEV-DEPLOYED | |
| [C2](#c2) | Restart resumes from cursor; activity log doesn't duplicate | LOCAL | tests Track 4 |
| [C3](#c3) | Buffer-depth metric updates | EITHER | tests Track 4 |
| [C4](#c4) | Rate-limited error logging on sustained DB failure | LOCAL | tests Track 4 |
| **D — Ingestion (Tap)** |
| [D1](#d1) | Tap consumer connects, processes, acks | LOCAL | requires `docker-compose.tap.yml` |
| [D2](#d2) | Tap ack-failure redelivery doesn't duplicate activity rows | LOCAL | tests Track 4 |
| **E — GraphQL queries** |
| [E1](#e1) | Query records by collection returns most-recent first | EITHER | |
| [E2](#e2) | `authors` filter narrows result set | EITHER | |
| [E3](#e3) | Empty `authors: []` returns zero records (load-bearing semantic) | EITHER | |
| [E4](#e4) | `labels` filter include + exclude | EITHER | |
| [E5](#e5) | `search` full-text filter | EITHER | |
| [E6](#e6) | Keyset cursor returns deterministic pagination | EITHER | |
| [E7](#e7) | Contributor filter is index-served, not O(collection) | EITHER | guards Track 1 regression test |
| [E8](#e8) | BadgeAward `subject` filter matches `defs#did` object shape | EITHER | issue #65 |
| [E9](#e9) | GraphFollow `subject` filter returns followers; index-served | EITHER | issue #86 |
| [E10](#e10) | BadgeAward nested-where on `badge` filters by joined definition | EITHER | issue #87 |
| **F — GraphQL subscriptions** |
| [F1](#f1) | Subscription receives a live create event | EITHER | needs ws client (wscat) |
| **G — OAuth (admin auth)** |
| [G1](#g1) | End-to-end client registration → auth code → DPoP token issuance | LOCAL | |
| [G2](#g2) | Refresh-token DPoP key binding is enforced | LOCAL | issue #24 |
| [G3](#g3) | DPoP JKT mismatch returns `invalid_token`, not `invalid_dpop_proof` | LOCAL | Track 3d |
| [G4](#g4) | JTI replay rejected | LOCAL | |
| **H — Service-auth JWT (notifications)** |
| [H1](#h1) | Valid AT Protocol service-auth JWT lets the request through | LOCAL | |
| [H2](#h2) | Invalid signature / wrong aud / wrong lxm rejected | LOCAL | |
| **I — Labeler** |
| [I1](#i1) | Labels arrive from a labeler subscription | DEV-DEPLOYED | |
| [I2](#i2) | `!takedown` filters records out of public queries | LOCAL | |
| [I3](#i3) | `/admin/labeler/{pause,reset}` and `/admin/label-chain` | LOCAL | Track 6 |
| **J — Admin operations** |
| [J1](#j1) | `updateSettings` mutation persists and is read back | LOCAL | |
| [J2](#j2) | `purgeActor` preview-then-confirm removes record + actor atomically | LOCAL | destructive |
| [J3](#j3) | `resetAll` preview-then-confirm wipes user data only | LOCAL | destructive |
| **K — Operability** |
| [K1](#k1) | Misconfigured DB URL: rejection error redacts the password | LOCAL | Track 7.Z |
| [K2](#k2) | SIGTERM drains background goroutines without "pool closed" errors | LOCAL | Track 3c |

---

## Test details

Each spec follows the same shape:

- **Coverage** — what feature / contract this guards.
- **Target** — `LOCAL`, `DEV-DEPLOYED`, or `EITHER`.
- **Preconditions** — what state must exist before running.
- **Steps** — numbered commands.
- **Expected** — what to check.
- **Cleanup** — how to restore state (if destructive).
- **Refs** — related code paths, audit items, PR numbers.

---

### A1
**Service boots with valid config.**

- **Coverage**: configuration validation rejects placeholder secrets
  and requires the OAuth cutoff date; the validator is wired before
  any other startup work runs.
- **Target**: LOCAL.
- **Preconditions**: `.env` exists (`make setup` if not), Postgres
  running (`docker compose -f docker-compose.postgres.yml up -d`),
  migrations applied (`make db-migrate`).
- **Steps**:
  1. `make build`
  2. `./bin/hypergoat 2>&1 | tee /tmp/boot.log &`
  3. Wait 5 seconds.
- **Expected**:
  - `/tmp/boot.log` contains `"Starting Hypergoat - AT Protocol AppView Server"`.
  - The next non-debug line is `"Connecting to Postgres"` with
    `dialect` *absent* (removed in Track 7.Z) and the URL redacted
    via `config.RedactPassword`.
  - No `slog.Error` or fatal lines before the `"OAuth endpoints enabled"`
    line.
  - The process is still running (`pgrep -f hypergoat`).
- **Cleanup**: `kill %1`.
- **Refs**: `cmd/hypergoat/main.go:179` (`run()`),
  `internal/config/config.go` (`Validate()`).

---

### A2
**`/health` returns 200 with the expected body shape.**

- **Coverage**: basic liveness; minimum surface required for Railway's
  health check to pass.
- **Target**: EITHER.
- **Preconditions**: the service is running.
- **Steps**:
  1. `curl -sw '\nHTTP %{http_code}\n' $BASE_URL/health`
- **Expected**:
  - HTTP 200.
  - Body contains `"status"` and a value that is or maps to
    "ok" / "healthy" — see `setupRouter` for the exact shape.
  - Response is JSON (`Content-Type: application/json`).
- **Cleanup**: none.
- **Refs**: `cmd/hypergoat/main.go` `setupRouter()` (search for `"/health"`).

---

### A3
**`/metrics` serves the Prometheus exposition format.**

- **Coverage**: scrape contract; counter naming follows the
  `hypergoat_<subsystem>_<thing>_<unit>` convention.
- **Target**: EITHER.
- **Preconditions**: service running.
- **Steps**:
  1. `curl -s $BASE_URL/metrics > /tmp/metrics.txt`
  2. Verify these series exist (one per line; ignore HELP/TYPE lines):
     - `hypergoat_http_requests_total`
     - `hypergoat_jetstream_events_total`
     - `hypergoat_records_inserted_total`
     - `hypergoat_jetstream_event_buffer_depth` *(Track 4)*
     - `hypergoat_jetstream_event_buffer_capacity` *(Track 4)*
     - `hypergoat_activity_log_failed_total` *(Track 4)*
     - `hypergoat_tap_event_dispatch_seconds_bucket` *(Track 4 histogram)*
  3. Confirm no metric label embeds raw user input (DID, URI, IP):
     `grep -E 'did:plc|at://|^[0-9]{1,3}\.[0-9]{1,3}\.' /tmp/metrics.txt`
     should return zero lines.
- **Expected**:
  - All series in step 2 appear.
  - Step 3 returns no matches (cardinality discipline).
- **Note on absent CounterVec series**: labelled counters
  (CounterVecs) do not appear in `/metrics` output until the first
  `.WithLabelValues(...)` call. So `hypergoat_ingestion_error_total{consumer=jetstream|tap}`
  is *expected* to be absent on a healthy deployment that has not
  yet seen an ingestion error. Treat its absence as "no errors
  yet," not as a missing metric. To force-initialise it, run
  test [C4](#c4) (LOCAL — induces a DB outage).
- **Cleanup**: `rm /tmp/metrics.txt`.
- **Refs**: `internal/metrics/metrics.go`.

---

### A4
**`/stats` returns operator-friendly counts.**

- **Coverage**: the read-only operator dashboard endpoint (no auth)
  that powers the public landing page count.
- **Target**: EITHER.
- **Preconditions**: service running.
- **Steps**:
  1. `curl -s $BASE_URL/stats | jq .`
- **Expected**:
  - JSON response with at least these top-level keys: `records`,
    `actors`, `lexicons`, `labelers` (exact names may evolve;
    inspect to confirm shape on the deployment under test).
  - Numeric values, no exceptions / errors in the body.
- **Cleanup**: none.

---

### B1
**Lexicon upload triggers exit code 42.**

- **Coverage**: the restart-on-exit contract documented in
  `RUNBOOK.md` §"Restart-on-exit contract" — without exit 42 the
  GraphQL schema can never rebuild on a new lexicon.
- **Target**: LOCAL (destructive — the process exits).
- **Preconditions**: A1 passes. Service running in the foreground
  (`make run`, not via supervisor). `$ADMIN_API_KEY` and `$ADMIN_DID`
  set. A valid lexicon ZIP staged at `/tmp/lexicons.zip` (one or
  more `.json` lexicon files; use `testdata/lexicons/` for a
  ready-made set).
- **Steps**:
  1. In a terminal, watch the running service's logs.
  2. In another terminal:
     ```bash
     base64 -w0 /tmp/lexicons.zip > /tmp/lexicons.zip.b64
     python3 -c "import json,sys; print(json.dumps({
       'query': 'mutation($zip:String!){ uploadLexicons(zip:$zip) }',
       'variables': {'zip': open('/tmp/lexicons.zip.b64').read().strip()}
     }))" > /tmp/upload.json
     curl -X POST $BASE_URL/admin/graphql \
       -H "Authorization: Bearer $ADMIN_API_KEY" \
       -H "X-User-DID: $ADMIN_DID" \
       -H "Content-Type: application/json" \
       --data-binary @/tmp/upload.json
     ```
- **Expected**:
  - Response: `{"data":{"uploadLexicons":<count>}}` where `<count>`
    matches the number of lexicons in the zip.
  - In the running-service terminal: a log line
    `"Exiting for orchestrator restart" code=42` appears within
    ~5 seconds.
  - The service process exits with status 42 (`echo $?` in the
    service terminal).
- **Cleanup**: bring the service back up: `make run`.
- **Refs**: `cmd/hypergoat/main.go:55-68`, `docs/RUNBOOK.md`
  §"Restart-on-exit contract".

---

### B2
**After restart, public GraphQL schema includes the new types.**

- **Coverage**: end-to-end lexicon upload → schema rebuild path. B1
  proves the process exits; B2 proves the schema then includes the
  new types after restart.
- **Target**: LOCAL.
- **Preconditions**: B1 just ran; service has been restarted.
- **Steps**:
  1. Identify a type name expected to appear from the uploaded
     lexicons (e.g. `OrgHypercertsClaimActivity`).
  2. Query introspection:
     ```bash
     curl -s -X POST $BASE_URL/graphql \
       -H 'Content-Type: application/json' \
       -d '{"query":"{ __schema { types { name } } }"}' \
       | jq -r '.data.__schema.types[].name' \
       | grep -E 'OrgHypercerts|Activity'
     ```
- **Expected**:
  - The expected type name(s) appear in the introspection output.
  - A query against the new type (e.g.
    `{ orgHypercertsClaimActivity(first: 1) { totalCount } }`)
    returns without a "Cannot query field" error.
- **Cleanup**: none.
- **Refs**: `cmd/hypergoat/main.go` `setupGraphQL()`,
  `internal/graphql/schema/builder.go`.

---

### B3
**Schema-invalid lexicon upload rejected pre-commit.**

- **Coverage**: the pre-commit schema validator added for issue #22 —
  catches lexicons that would produce an invalid GraphQL schema
  *before* writing them to the DB, so the next restart doesn't fail
  to boot.
- **Target**: LOCAL.
- **Preconditions**: service running, admin credentials.
- **Steps**:
  1. Stage an intentionally-broken lexicon at `/tmp/bad-lexicon.json`:
     a lexicon that references an undefined ref or uses a
     reserved GraphQL keyword as a field name.
  2. Build a zip with just that file and upload it via the same
     mutation shape as B1.
- **Expected**:
  - Response: `{"data":null,"errors":[{...}]}` with an error
    message naming the schema-validation failure.
  - Service does NOT exit 42.
  - DB row count for the broken lexicon does not increase
    (verify with `psql -c "SELECT COUNT(*) FROM lexicon"`).
- **Cleanup**: none — the bad lexicon was never written.
- **Refs**: `internal/graphql/admin/resolvers_lexicons.go`
  `UploadLexicons`, `SchemaValidateCallback`.

---

### C1
**Live Jetstream connection produces records in the DB.**

- **Coverage**: the firehose ingestion happy path — the project's
  primary purpose.
- **Target**: DEV-DEPLOYED (the live instance is already connected
  to Jetstream; a fresh LOCAL takes minutes to see traffic).
- **Preconditions**: at least one lexicon registered whose
  collection produces traffic on Bluesky (e.g.
  `app.bsky.feed.post`).
- **Steps**:
  1. Capture the current per-collection event counts:
     ```bash
     curl -s $BASE_URL/metrics \
       | grep '^hypergoat_jetstream_events_total{' \
       > /tmp/jetstream-before.txt
     ```
  2. Wait 60 seconds (a popular collection produces multiple
     records/sec on Jetstream; a low-traffic deployment may only
     see one event per ~30s).
  3. Re-capture and diff:
     ```bash
     curl -s $BASE_URL/metrics \
       | grep '^hypergoat_jetstream_events_total{' \
       > /tmp/jetstream-after.txt
     diff /tmp/jetstream-before.txt /tmp/jetstream-after.txt
     ```
- **Expected**:
  - `diff` shows at least one series whose value increased.
- **Note**: `/stats records` is a softer signal — update-only
  events and create-then-delete cycles process correctly but
  don't change the row count. `hypergoat_jetstream_events_total`
  is the load-bearing counter; trust it over `/stats records` for
  this test.
- **Cleanup**: `rm /tmp/jetstream-*.txt`.
- **Refs**: `internal/jetstream/consumer.go`,
  `internal/ingestion/processor.go`.

---

### C2
**Restart resumes from cursor; activity log doesn't duplicate.**

- **Coverage**: the source_event_id dedup landed in Track 4
  (review-2026-05-17). Without it, a crash between LogActivity and
  the record insert produces a duplicate activity row on restart.
- **Target**: LOCAL.
- **Preconditions**: LOCAL stack running with at least one lexicon
  registered. Some baseline traffic has been ingested.
- **Steps**:
  1. Note the activity-row count:
     ```bash
     PSQL='docker compose -f docker-compose.postgres.yml exec -T postgres psql -U hypergoat -d hypergoat -tA'
     BEFORE=$($PSQL -c "SELECT COUNT(*) FROM jetstream_activity")
     CURSOR_BEFORE=$($PSQL -c "SELECT value FROM config WHERE key='jetstream_cursor'")
     ```
  2. SIGINT the running service (Ctrl+C).
  3. Restart it: `make run`.
  4. Wait 10 seconds.
  5. Re-check:
     ```bash
     AFTER=$($PSQL -c "SELECT COUNT(*) FROM jetstream_activity")
     DUP=$($PSQL -c "SELECT COUNT(*) FROM (SELECT source_event_id FROM jetstream_activity WHERE source_event_id IS NOT NULL GROUP BY source_event_id HAVING COUNT(*) > 1) d")
     echo "before=$BEFORE after=$AFTER duplicate_source_event_ids=$DUP"
     ```
- **Expected**:
  - `DUP == 0` — no source_event_id appears more than once. The
    partial unique index on `idx_jetstream_activity_source_event_id`
    enforces this; the SQL UPSERT in `LogActivityWithStatus` returns
    the existing id on conflict so the orphan janitor doesn't mark
    a redelivered-but-successful row as orphaned.
  - `AFTER >= BEFORE` (ingestion may have continued during the
    test).
- **Cleanup**: none.
- **Refs**: migrations 027 + 028; `internal/database/repositories/jetstream_activity.go`;
  `docs/review-2026-05-17/plan.md` Track 4.

---

### C3
**Buffer-depth metric updates.**

- **Coverage**: observability over Jetstream backpressure — added in
  Track 4 because the audit found no way for an operator to know how
  close the channel was to saturation.
- **Target**: EITHER.
- **Preconditions**: service running and ingesting (Jetstream
  connected).
- **Steps**:
  1. `curl -s $BASE_URL/metrics | grep -E 'jetstream_event_buffer_(depth|capacity)'`
  2. Wait 10 seconds.
  3. Run the same command again.
- **Expected**:
  - Both `_depth` and `_capacity` series appear.
  - `_capacity` is `1000` (the `EventChannelBufferSize` constant).
  - `_depth` is a non-negative number ≤ 1000. The value should
    typically be close to 0 (consumer keeps up) but is not
    constant — running the command twice 10s apart should not
    always return exactly the same number.
- **Cleanup**: none.
- **Refs**: `internal/jetstream/client.go` `bufferDepthLoop`,
  `internal/metrics/metrics.go`.

---

### C4
**Rate-limited error logging on sustained DB failure.**

- **Coverage**: the audit found that a multi-hour DB outage would
  produce millions of identical log lines; Track 4 added rate-
  limited error logging (first 5 loud, then 1/min) to keep logs
  observable.
- **Target**: LOCAL (requires inducing a DB outage).
- **Preconditions**: LOCAL stack running and ingesting.
- **Steps**:
  1. Tail the service log: `tail -f /tmp/boot.log`.
  2. Stop Postgres without a graceful shutdown:
     `docker compose -f docker-compose.postgres.yml kill postgres`.
  3. Watch the log for 90 seconds.
  4. Restart Postgres:
     `docker compose -f docker-compose.postgres.yml start postgres`.
- **Expected**:
  - First few errors (up to `defaultErrLogLoudLimit = 5`) appear as
    full `slog.Error` lines with `did`, `collection`, `error` fields.
  - Subsequent errors are suppressed; one summary line per minute
    appears with the `(rate-limited)` suffix and an
    `occurrences_since_last_log` field showing the suppressed
    count.
  - `hypergoat_ingestion_error_total{consumer="jetstream"}` counter
    increases by the actual number of failures (not just the
    visible log count).
- **Cleanup**: Postgres restarted in step 4; ingestion resumes
  automatically.
- **Refs**: `internal/jetstream/consumer.go` `rateLimitedErrLogger`,
  `internal/tap/consumer.go` (mirrored).

---

### D1
**Tap consumer connects, processes, acks.**

- **Coverage**: the Tap sidecar consumer is the project's alternative
  to Jetstream — ack-based delivery with per-repo ordering.
- **Target**: LOCAL (requires `docker-compose.tap.yml`).
- **Preconditions**: `docker compose -f docker-compose.tap.yml up -d`
  has started the Tap sidecar; service `TAP_URL` env var points at
  it; `JETSTREAM_URL` is empty so the consumer chooses Tap.
- **Steps**:
  1. Watch the service log for `"Connected to Tap"`.
  2. Wait 30 seconds, then check `/stats`.
  3. Inspect the Tap sidecar log:
     `docker compose -f docker-compose.tap.yml logs tap --tail 50`
     — the sidecar should not be reporting "ack timeout" errors.
- **Expected**:
  - Service log shows the connection succeeded.
  - `/stats` records count increased.
  - `/metrics` includes the
    `hypergoat_tap_event_dispatch_seconds_bucket` histogram with
    sample counts > 0.
- **Cleanup**: shut down the Tap sidecar when done.
- **Refs**: `internal/tap/consumer.go`, `docker-compose.tap.yml`.

---

### D2
**Tap ack-failure redelivery doesn't duplicate activity rows.**

- **Coverage**: same as C2 but for Tap. The audit found the Tap
  consumer acks *after* dispatch returns; a network failure on the
  ack causes redelivery. The source_event_id index added in
  migrations 027/028 closes this for both consumers.
- **Target**: LOCAL.
- **Preconditions**: D1 passed; Tap connected and ingesting.
- **Steps**:
  1. Note `BEFORE` count of `jetstream_activity` rows.
  2. Inject a network glitch between the service and the Tap
     sidecar — easiest: `docker compose ... restart tap` while the
     service has events in-flight.
  3. Wait 30 seconds for ingestion to settle.
  4. Re-check counts plus run the dup-detection query from C2.
- **Expected**:
  - Same as C2: zero duplicate `source_event_id` values, no
    `pending → orphaned` transitions caused by the redelivery.
- **Cleanup**: none.
- **Refs**: same as C2; `internal/tap/handler.go` populates
  `op.SourceEventID = &event.ID`.

---

### E1
**Query records by collection returns most-recent first.**

- **Coverage**: the default sort + keyset cursor contract — the only
  default ordering the API promises.
- **Target**: EITHER.
- **Preconditions**: at least one collection with > 2 records.
- **Steps**:
  1. List populated collections and pick one:
     ```bash
     curl -s -X POST $BASE_URL/graphql -H 'Content-Type: application/json' \
       -d '{"query":"{ collectionStats { collection count } }"}' | jq
     ```
  2. Query the most-recent 5 via the generic `records` resolver
     (works for any registered collection):
     ```bash
     curl -s -X POST $BASE_URL/graphql -H 'Content-Type: application/json' \
       -d '{"query":"{ records(collection:\"org.hypercerts.claim.activity\", first:5) { edges { node { uri did rkey } } } }"}' | jq
     ```
- **Expected**:
  - Five records returned.
  - Each `uri` is unique.
  - Each `did` is a plausible DID.
- **Notes**:
  - The polymorphic `GenericRecord` type exposes only the
    cross-collection scalars: `uri, did, rkey, cid, collection,
    labels, pds`. The per-record `value` (the lexicon-typed
    record body) is on the *auto-generated per-collection* types
    (e.g. `orgHypercertsClaimActivity { value }`). Use the
    per-collection resolver if you need to inspect or filter on
    record-body fields.
  - To verify the most-recent-first ordering, use the
    per-collection resolver: it exposes `indexedAt` /
    `createdAt` so the timestamps are visible. The generic
    `records` resolver doesn't expose those scalars.
- **Cleanup**: none.
- **Refs**: `internal/database/repositories/records.go`
  `GetByCollectionWithKeysetCursor`.

---

### E2
**`authors` filter narrows result set.**

- **Coverage**: per-DID filtering, deduped + sorted before binding.
- **Target**: EITHER.
- **Preconditions**: identify a DID with records in the target
  collection.
- **Steps**:
  1. Get a small unfiltered baseline from the per-collection
     resolver:
     ```bash
     curl -s -X POST $BASE_URL/graphql -H 'Content-Type: application/json' \
       -d '{"query":"{ orgHypercertsClaimActivity(first:10) { edges { node { did } } } }"}' | jq
     ```
  2. Pick the DID of the first record from the baseline.
  3. Filter by that DID:
     ```bash
     curl -s -X POST $BASE_URL/graphql -H 'Content-Type: application/json' \
       -d '{"query":"{ orgHypercertsClaimActivity(authors:[\"did:plc:...\"], first:5) { edges { node { did } } } }"}' | jq
     ```
- **Expected**:
  - Every returned record has `did` matching the filter value.
  - Count is less than or equal to the baseline.
- **Notes**: `authors` is an argument on the *per-collection*
  auto-generated resolvers (e.g. `orgHypercertsClaimActivity`),
  not on the polymorphic `records` resolver. The polymorphic
  resolver only takes `collection, search, labels, labelerDids,
  excludeLabels, excludePds, first, after` — author filtering
  requires the per-collection path.
- **Cleanup**: none.

---

### E3
**Empty `authors: []` returns zero records.**

- **Coverage**: the load-bearing distinction between "no authors
  filter" (returns all) and "empty authors filter" (returns
  nothing). A bug here would degrade an empty client-side trust set
  to "show everything."
- **Target**: EITHER.
- **Steps**:
  1. Query a per-collection resolver with `authors: []` (empty
     list, NOT omitted):
     ```bash
     curl -s -X POST $BASE_URL/graphql -H 'Content-Type: application/json' \
       -d '{"query":"{ orgHypercertsClaimActivity(authors:[], first:10) { edges { node { uri } } pageInfo { hasNextPage } } }"}' | jq
     ```
- **Expected**:
  - `edges` is empty.
  - `pageInfo.hasNextPage` is `false`.
  - Service log (when checkable on LOCAL) shows
    `"records filter short-circuited on empty authors"` — the
    short-circuit avoids a DB round trip. Not observable on
    DEV-DEPLOYED; the wire-level contract is the load-bearing
    assertion.
- **Notes**: same as E2 — use the per-collection resolver. The
  polymorphic `records` resolver does not accept `authors`, so
  the empty-vs-omitted distinction is not testable through it.
- **Cleanup**: none.
- **Refs**: `internal/database/repositories/records.go`
  `RecordFilter.Authors` doc, `GetByCollectionFiltered`.

---

### E4
**`labels` filter include + exclude.**

- **Coverage**: the include/exclude label filter — both arms exist
  and a record can be matched by include AND missed by exclude in
  the same query.
- **Target**: EITHER.
- **Preconditions**: a labeler is enabled with at least two
  distinct label values present on records.
- **Steps**:
  1. Get a baseline of 20 records and grab one's URI.
  2. Use the admin label-chain endpoint (I3) to identify a label
     value present on that URI.
  3. Query with `labels: { include: ["that-value"] }`.
  4. Query with `labels: { exclude: ["that-value"] }`.
- **Expected**:
  - Step 3 includes the baseline URI.
  - Step 4 does NOT include the baseline URI.
- **Cleanup**: none.

---

### E5
**`search` full-text filter.**

- **Coverage**: `search_vector` GIN index, `plainto_tsquery('english', ...)`.
- **Target**: EITHER.
- **Preconditions**: a collection whose records contain English
  text — pick any populated collection from `collectionStats`.
- **Steps**:
  1. Note baseline of `hypergoat_records_search_applied_total`
     in `/metrics`.
  2. Query with a common word:
     ```bash
     curl -s -X POST $BASE_URL/graphql -H 'Content-Type: application/json' \
       -d '{"query":"{ orgHypercertsClaimActivity(search:\"climate\", first:3) { edges { node { uri did } } } }"}' | jq
     ```
  3. Re-check the metric.
- **Expected**:
  - Results returned (`edges` non-empty, assuming the term has
    matches in the chosen collection).
  - `hypergoat_records_search_applied_total` increased by 1.
- **Notes**: per-collection types expose the lexicon-typed `value`
  field (e.g. `{ value { description } }` on
  `OrgHypercertsClaimActivity`), but field names vary per
  collection — inspect with introspection
  (`__type(name: "OrgHypercertsClaimActivity") { fields { name } }`)
  before querying it. The minimum verifiable contract is "search
  is wired and returns *some* result" — content relevance is
  better checked by a per-collection test that knows its schema.
- **Cleanup**: none.

---

### E6
**Keyset cursor returns deterministic pagination.**

- **Coverage**: composite (sort_at|indexed_at, uri) cursor — no
  duplicates and no skips across page boundaries.
- **Target**: EITHER.
- **Preconditions**: > 20 records in the target collection.
- **Steps**:
  1. Fetch page 1 with `first: 10`.
  2. Fetch page 2 with `first: 10, after: "<endCursor from page 1>"`.
  3. Collect URIs from both pages into a list.
- **Expected**:
  - 20 distinct URIs.
  - Each page's `pageInfo.hasNextPage` is `true` (assuming > 20
    records).
  - Page 2's first URI sorts strictly after page 1's last URI.
- **Cleanup**: none.

---

### E7
**Contributor filter is index-served, not O(collection).**

- **Coverage**: migration 024's partial GIN index on
  `record_contributor_identities(json)` — the Track 1 regression
  test in `filter_unit_test.go` guards the constant ↔ migration
  byte-level coupling; this behavioral test verifies the planner
  actually picks the index.
- **Target**: LOCAL for the EXPLAIN check (needs psql); the wire-
  level half (`contributor` filter returns matching records) is
  EITHER.
- **Preconditions**: at least 1000 records in
  `org.hypercerts.claim.activity` with various contributors.
- **Steps**:
  1. Issue a contributor-filtered query that returns a small
     result set (note: the field is `contributor` *singular*, not
     `contributors`):
     ```graphql
     {
       orgHypercertsClaimActivity(
         where: { contributor: { eq: "did:plc:..." } }
         first: 10
       ) { edges { node { uri } } }
     }
     ```
     Verify the response includes records authored both BY the
     filtered DID and by other DIDs that LIST the filtered DID as
     a contributor — the filter matches contributors, not
     authors.
  2. (LOCAL only — needs psql) Connect to Postgres and run
     `EXPLAIN ANALYZE` for the equivalent SQL (reconstruct:
     `SELECT ... WHERE
     record_contributor_identities(r.json) @> ARRAY['did:plc:...']::text[]`).
- **Expected**:
  - Step 1: matching records returned, including both
    authored-by and contributor-of records for the filter DID.
  - Step 2: `EXPLAIN ANALYZE` shows
    `Bitmap Index Scan on idx_record_contributor_identities`.
  - Step 2: Execution time is under 100ms (vs. multiple seconds
    for a sequential scan on a populated table).
- **Cleanup**: none.
- **Refs**: migration 024,
  `internal/database/repositories/filter.go` `buildContributorFilter`,
  Track 1 regression test.

---

### E8
**BadgeAward `subject` filter matches `defs#did` object shape.**

- **Coverage**: issue #65 — the BadgeAward `subject` field is a
  union whose `app.certified.defs#did` arm uses key `did`, not
  `identity`; the partial btree index on `subject_did` (migrations
  025/026) must be picked.
- **Target**: EITHER.
- **Preconditions**: a populated `app.certified.badge.award`
  collection with both bare-string and defs#did-shaped subjects.
- **Steps**:
  1. Query with a `subject` value matching a known badge-award
     record:
     ```graphql
     {
       appCertifiedBadgeAward(
         where: { subject: { eq: "did:plc:..." } }
         first: 5
       ) { edges { node { uri } } }
     }
     ```
  2. `EXPLAIN ANALYZE` the resulting SQL.
- **Expected**:
  - Records returned regardless of which subject shape they use.
  - Plan includes `Index Scan using idx_record_subject_did` (the
    partial btree from migration 026).
- **Cleanup**: none.
- **Refs**: migrations 025 + 026, issue #65,
  `internal/database/repositories/filter.go`
  `buildBadgeAwardSubjectFilter`.

---

### E9
**GraphFollow `subject` filter returns followers; index-served.**

- **Coverage**: issue #86 — the certified-app `/profile/[handle]?tab=followers`
  reads from the indexer via `appCertifiedGraphFollow(where: {
  subject: { eq: <did> } }, first, after)`. Without an index, the
  query degrades to a sequential scan over the follow collection;
  migration 029's partial expression index
  `idx_record_follow_subject` plus `KindStringSubject` filter SQL
  are what makes it serve the read pattern.
- **Target**: LOCAL for the `EXPLAIN ANALYZE` check (needs psql);
  the wire-level half (`subject` filter returns matching records)
  is EITHER once the lexicon is registered on the target.
- **Preconditions**:
  - Lexicon `app.certified.graph.follow` registered on the
    target. On LOCAL: upload `testdata/lexicons/app/certified/graph/follow.json`
    via the admin upload mutation (see B1 for the upload curl).
    On DEV-DEPLOYED: requires the upstream `hypercerts-lexicon`
    PR to have merged and a fresh upload — confirm with the
    introspection query in step 1.
  - At least a few follow records present on the target. On
    LOCAL: insert directly via psql, or wait for Jetstream to
    ingest some once the lexicon is up.
- **Steps**:
  1. Verify the schema includes the resolver and the field:
     ```bash
     curl -s -X POST $BASE_URL/graphql -H 'Content-Type: application/json' \
       -d '{"query":"{ __type(name:\"AppCertifiedGraphFollowWhereInput\") { inputFields { name } } }"}' \
       | jq -r '.data.__type.inputFields[].name' | sort
     ```
     Expect `_and _or createdAt did subject via` (or a superset).
  2. Issue the canonical followers query for a DID that has at
     least one follower:
     ```bash
     curl -s -X POST $BASE_URL/graphql -H 'Content-Type: application/json' \
       -d '{"query":"{ appCertifiedGraphFollow(where: { subject: { eq: \"did:plc:...\" } }, first: 100) { totalCount edges { node { uri cid did createdAt } } pageInfo { hasNextPage endCursor } } }"}' \
       | jq
     ```
  3. (LOCAL only — needs psql) Run `EXPLAIN ANALYZE` for the
     equivalent SQL (reconstruct from the
     internal/database/repositories/records.go query path or
     enable slog at debug):
     ```sql
     EXPLAIN ANALYZE
     SELECT r.uri, r.cid, r.did, r.json
     FROM record r LEFT JOIN actor a ON r.did = a.did
     WHERE r.collection = 'app.certified.graph.follow'
       AND r.json->>'subject' = 'did:plc:...'
     ORDER BY r.indexed_at DESC, r.uri DESC
     LIMIT 100;
     ```
- **Expected**:
  - Step 1: input fields include both `did` (auto-added record-
    author filter) and `subject` (registry-injected).
  - Step 2: returns follow records authored by other DIDs whose
    `node.value.subject` (if queryable per the lexicon) or
    underlying JSON subject equals the filter DID. Each `node.did`
    is a follower. No GraphQL errors.
  - Step 3: plan includes
    `Index Scan using idx_record_follow_subject on record r` (or
    `Bitmap Index Scan`). Execution time well under 100ms even on
    a populated `record` table.
- **Cleanup**: none for steps 1–2. For step 3, no state change.
- **Composition check (optional)**: the pinned description
  documents an `_or` shape for "I follow OR am followed by." Run
  it once to confirm the schema accepts the recursive composition:
  ```graphql
  { appCertifiedGraphFollow(where: { _or: [
      { did: { eq: "did:plc:me" } },
      { subject: { eq: "did:plc:me" } }
  ] }, first: 5) { edges { node { uri did } } } }
  ```
  Expect both authored-by-me and pointed-at-me follow records.
- **Refs**: issue #86; migration 029
  (`internal/database/migrations/postgres/029_add_follow_subject_index.up.sql`);
  `buildStringSubjectFilter` in
  `internal/database/repositories/filter.go`; registry entry in
  `internal/graphql/schema/where.go`; regression test
  `TestStringSubjectFilter_IndexExpressionMatchesMigration029`.

---

### E10
**BadgeAward nested-where on `badge` filters by joined definition.**

- **Coverage**: issue #87 — the certified-app's Endorsements
  tab reads "what endorsements has this DID received?" in a
  single round-trip via
  `appCertifiedBadgeAward(where: { subject: { eq: $did },
  badge: { badgeType: { eq: "endorsement" } } })`. The nested
  `badge` filter translates to an EXISTS subquery over
  `app.certified.badge.definition` joined by the strongRef in
  `award.json.badge.uri`. Without this, the client makes two
  round-trips and joins them on the wire.
- **Target**: EITHER for the wire-level query; LOCAL for the
  `EXPLAIN ANALYZE` check.
- **Preconditions**:
  - `app.certified.badge.award` and `app.certified.badge.definition`
    lexicons registered on the target. On DEV-DEPLOYED these
    should already be present (badge.award has been there
    since #65); on LOCAL upload both before testing.
  - At least a few award records with a known subject DID and
    at least one corresponding definition with
    `badgeType = "endorsement"`. On dev: `appCertifiedBadgeAward
    { totalCount }` should be non-zero before running this test.
- **Steps**:
  1. Verify the schema includes the joined-where field:
     ```bash
     curl -s -X POST $BASE_URL/graphql -H 'Content-Type: application/json' \
       -d '{"query":"{ __type(name:\"AppCertifiedBadgeAwardWhereInput\") { inputFields { name type { name } } } }"}' \
       | jq '.data.__type.inputFields | map(select(.name == "badge"))'
     ```
     Expect a single entry with
     `type.name == "AppCertifiedBadgeDefinitionWhereInput"`.
  2. Issue the exact query the certified-app uses:
     ```bash
     curl -s -X POST $BASE_URL/graphql -H 'Content-Type: application/json' \
       -d '{"query":"{ appCertifiedBadgeAward(where: { subject: { eq: \"did:plc:...\" }, badge: { badgeType: { eq: \"endorsement\" } } }, first: 100) { edges { node { uri did createdAt } } pageInfo { hasNextPage } } }"}' \
       | jq
     ```
  3. Cross-check by issuing the 2-call workaround (subject
     filter only, then filter client-side by definition's
     badgeType) and confirming the result sets match.
  4. (LOCAL only) `EXPLAIN ANALYZE` the equivalent SQL:
     ```sql
     EXPLAIN ANALYZE
     SELECT r.uri, r.cid, r.did
     FROM record r LEFT JOIN actor a ON r.did = a.did
     WHERE r.collection = 'app.certified.badge.award'
       AND r.subject_did = 'did:plc:...'
       AND EXISTS (
         SELECT 1 FROM record d
         WHERE d.collection = 'app.certified.badge.definition'
           AND d.uri = r.json->'badge'->>'uri'
           AND d.json @> '{"badgeType":"endorsement"}'::jsonb
       )
     ORDER BY r.indexed_at DESC, r.uri DESC
     LIMIT 100;
     ```
- **Expected**:
  - Step 1: `badge` field present, typed as the definition's
    WhereInput.
  - Step 2: returns awards whose subject matches AND whose
    joined definition has `badgeType = "endorsement"`. No
    GraphQL errors.
  - Step 3: identical URIs in both result sets.
  - Step 4: plan includes an `Index Scan using record_pkey on
    record d` (or equivalent) for the inner subquery — the
    join probes `record.uri` (primary key). No sequential
    scan over the definition collection.
- **Composition check (optional)**: the pinned description's
  intersection+disjunction example —
  ```graphql
  { appCertifiedBadgeAward(where: {
      subject: { eq: "did:plc:..." },
      badge: { _or: [
        { badgeType: { eq: "endorsement" } },
        { badgeType: { eq: "verification" } }
      ] }
    }, first: 5) { edges { node { uri } } } }
  ```
  Expect results that satisfy both halves; confirms `_or` is
  wired through inside the inner.
- **Cleanup**: none.
- **Refs**: issue #87; `KindStringSubject` is unaffected here —
  the join uses `KindScalar` filters in the inner against
  badge.definition's JSON properties; the EXISTS subquery
  emission lives in
  `internal/database/repositories/filter.go`
  `buildFilterGroupRecursive` (search for "EXISTS");
  registry entry at
  `internal/graphql/schema/where.go` `joinedWhereRegistry`;
  extractor branch at `extractFieldFiltersRecursive`.

---

### F1
**Subscription receives a live create event.**

- **Coverage**: GraphQL subscription path via PubSub.
- **Target**: EITHER (LOCAL is easier to instrument).
- **Preconditions**: `wscat` installed (`npm install -g wscat`).
- **Steps**:
  1. Open a subscription to a high-traffic collection (Jetstream
     URL with `subscriptions-transport-ws` protocol — check
     `/graphql` headers or use a GraphQL client that handles the
     subprotocol).
  2. Wait for events for 60 seconds.
- **Expected**:
  - At least one create event message received with `uri`, `cid`,
    `did`, `collection`, and a `record` payload.
- **Cleanup**: close the WebSocket.
- **Refs**: `internal/graphql/subscription/`, the publish call in
  `internal/ingestion/processor.go`.

---

### G1
**End-to-end client registration → auth-code → DPoP token issuance.**

- **Coverage**: the OAuth flow the admin UI depends on (dynamic
  client registration, PKCE, DPoP, PAR optional).
- **Target**: LOCAL (avoid creating throwaway tokens in DEV).
- **Preconditions**: service running with OAuth endpoints enabled.
  A Python script with `requests` and a JWT library installed for
  the DPoP signature (the admin client uses this pattern; see
  `client/` for a reference).
- **Steps**:
  1. Register a new client:
     ```bash
     curl -X POST $BASE_URL/oauth/register \
       -H 'Content-Type: application/json' \
       -d '{
         "redirect_uris": ["http://localhost:9999/callback"],
         "token_endpoint_auth_method": "none"
       }'
     ```
     Note `client_id` from the response.
  2. Construct an authorization URL with PKCE parameters
     (`code_challenge`, `code_challenge_method=S256`,
     `response_type=code`, your `client_id`, `redirect_uri`).
  3. Visit the URL in a browser, complete the (test) login,
     follow the redirect to `/callback?code=...`.
  4. Generate a DPoP proof JWT (ES256, P-256 key).
  5. Exchange the code:
     ```bash
     curl -X POST $BASE_URL/oauth/token \
       -H "DPoP: <proof>" \
       -H 'Content-Type: application/x-www-form-urlencoded' \
       --data-urlencode "grant_type=authorization_code" \
       --data-urlencode "code=<from step 3>" \
       --data-urlencode "redirect_uri=http://localhost:9999/callback" \
       --data-urlencode "code_verifier=<from step 2>"
     ```
- **Expected**:
  - Token response is 200 with `access_token`, `refresh_token`,
    `token_type: "DPoP"`, `expires_in`.
  - The access token's row in `oauth_access_token` has `dpop_jkt`
    set to the JKT calculated from your DPoP key.
- **Cleanup**: `curl -X POST $BASE_URL/oauth/revoke -d "token=<at>"` then
  delete the client via the admin GraphQL `deleteOAuthClient`.
- **Refs**: `internal/server/oauth_handlers.go`,
  `internal/oauth/dpop.go`, `internal/oauth/pkce.go`.

---

### G2
**Refresh-token DPoP key binding is enforced.**

- **Coverage**: issue #24 — a refresh request must be signed by the
  same DPoP key that the refresh token is bound to, or the request
  is rejected.
- **Target**: LOCAL.
- **Preconditions**: G1 succeeded — you have a refresh token + the
  DPoP key.
- **Steps**:
  1. Refresh with the original DPoP key — expect success.
  2. Generate a fresh DPoP key (different JKT).
  3. Try to refresh the SAME refresh token with the new key.
- **Expected**:
  - Step 1: 200, new access + refresh tokens.
  - Step 3: 400 with `invalid_grant` and a metric tick on
    `hypergoat_oauth_refresh_jkt_mismatch_total`.
- **Cleanup**: revoke the test tokens.
- **Refs**: `internal/server/oauth_handlers.go`
  `checkRefreshTokenDPoPBinding`.

---

### G3
**DPoP JKT mismatch returns `invalid_token`, not `invalid_dpop_proof`.**

- **Coverage**: Track 3d response-shape collapse — removed the
  oracle that distinguished "your key was wrong" from "your key
  was right but something else failed."
- **Target**: LOCAL.
- **Preconditions**: G1 succeeded — you have a DPoP-bound access
  token and its DPoP key.
- **Steps**:
  1. Generate a fresh DPoP key (different JKT).
  2. Use the access token with the new key against a
     DPoP-protected endpoint (e.g. `/admin/whoami`):
     ```bash
     curl -i -X GET $BASE_URL/admin/whoami \
       -H "Authorization: DPoP <access_token>" \
       -H "DPoP: <fresh-proof-with-new-key>"
     ```
- **Expected**:
  - 401.
  - Response body contains `"error":"invalid_token"`.
  - Response body does NOT contain `"invalid_dpop_proof"` or
    `"DPoP key mismatch"`.
  - Service log shows `slog.Debug "DPoP JKT mismatch"` with
    `internal_error=invalid_dpop_proof` — the internal trace
    keeps the precise reason.
- **Cleanup**: none.
- **Refs**: `internal/oauth/middleware.go` validateDPoPToken,
  test `TestAuthMiddleware_RequireAuth_DPoP_KeyMismatch`.

---

### G4
**JTI replay rejected.**

- **Coverage**: in-memory + DB-backed JTI replay caches reject
  reused proofs within their windows.
- **Target**: LOCAL.
- **Preconditions**: G1 succeeded.
- **Steps**:
  1. Make a successful DPoP-authenticated request.
  2. Replay the exact same `DPoP:` header value (same JTI).
- **Expected**:
  - Second request: 401 with `invalid_dpop_proof`.
  - `hypergoat_oauth_*` replay metric (if one exists) ticks.
- **Cleanup**: none.
- **Refs**: `internal/oauth/middleware.go` `validateDPoPToken`,
  `oauth_dpop_jti.go` `InsertIfNew`.

---

### H1
**Valid AT Protocol service-auth JWT lets the request through.**

- **Coverage**: the third-party notifications endpoint that accepts
  ATProto service-auth JWTs (separate from the OAuth provider).
- **Target**: LOCAL.
- **Preconditions**: a labeler / external service is configured to
  sign JWTs with a known key; the service knows the issuer's
  DID and signing key.
- **Steps**:
  1. Sign a JWT with the issuer's signing key, `aud` set to the
     configured domain DID, `lxm` set to the expected lexicon
     method for the endpoint (see `serviceauth.go` for the exact
     value).
  2. Call the notifications endpoint with
     `Authorization: Bearer <jwt>`.
- **Expected**:
  - 2xx response.
  - `hypergoat_service_auth_verified_total` increments.
- **Cleanup**: none.
- **Refs**: `internal/oauth/serviceauth.go`,
  `internal/notifications/`.

---

### H2
**Invalid signature / wrong aud / wrong lxm rejected.**

- **Coverage**: the three primary rejection paths.
- **Target**: LOCAL.
- **Steps**:
  1. Send a JWT signed by an unknown key.
  2. Send a JWT with `aud` set to some other DID.
  3. Send a JWT with `lxm` set to a different method.
- **Expected**:
  - All three return 401 with distinct (internal) sentinels but
    the same body shape on the wire.
  - `hypergoat_service_auth_rejected_total{reason=...}`
    increments with the matching `reason` label.
- **Cleanup**: none.
- **Refs**: `internal/oauth/serviceauth.go` `Verify*` functions.

---

### I1
**Labels arrive from a labeler subscription.**

- **Coverage**: the labeler ingestion happy path.
- **Target**: DEV-DEPLOYED (LOCAL needs a running labeler).
- **Preconditions**: `LABELER_DIDS` includes a DID that emits labels.
- **Steps**:
  1. `curl -s $BASE_URL/stats | jq` — note any `labels` count.
  2. Wait 30 seconds.
  3. Re-check.
- **Expected**:
  - Count increased (assuming the labeler is active).
  - `hypergoat_labeler_labels_received_total{src="<labeler-did>"}`
    increments.
- **Cleanup**: none.
- **Refs**: `internal/labeler/consumer.go`.

---

### I2
**`!takedown` filters records out of public queries.**

- **Coverage**: trusted-evaluator-feed takedown filtering — public
  query results must hide records with an active `!takedown` from a
  trusted labeler.
- **Target**: LOCAL.
- **Preconditions**: at least one record + one labeler with a
  `!takedown` label.
- **Steps**:
  1. Query a record by URI — confirm it returns.
  2. Have the labeler emit a `!takedown` for that URI (insert
     directly into `label` via psql for the test).
  3. Re-query — should not return.
  4. Insert a negation (`neg=true`).
  5. Re-query — should return again.
- **Expected**:
  - Step 1: 1 result.
  - Step 3: 0 results.
  - Step 5: 1 result.
- **Cleanup**: delete the test label rows.
- **Refs**: `internal/database/repositories/labels.go`
  `HasTakedown`, `GetTakedownURIs`.

---

### I3
**`/admin/labeler/{pause,reset}` and `/admin/label-chain`.**

- **Coverage**: the three raw-HTTP admin endpoints extracted in
  Track 6.
- **Target**: LOCAL.
- **Preconditions**: a labeler is enabled and ingesting.
- **Steps**:
  1. `curl -X POST $BASE_URL/admin/labeler/reset?did=$LABELER_DID
        -H "Authorization: Bearer $ADMIN_API_KEY"`
     — expect 200 with `{"reset":true,...}`.
  2. `curl -X POST $BASE_URL/admin/labeler/pause?did=$LABELER_DID
        -H "Authorization: Bearer $ADMIN_API_KEY"`
     — expect 200 with `{"paused":true,...}`. Service log shows
     the consumer stopping.
  3. `curl $BASE_URL/admin/label-chain?uri=<some-uri>
        -H "Authorization: Bearer $ADMIN_API_KEY"`
     — expect a JSON array of labels (including negated and
     expired — this is a diagnostic view).
  4. Try each endpoint without the bearer header — expect 401.
  5. Try each endpoint with `did=garbage` (invalid DID) on the
     two labeler routes — expect 400 (the DID validation guards
     against config-key injection).
- **Expected**: all as listed.
- **Cleanup**: restart the labeler if it should keep running.
- **Refs**: `cmd/hypergoat/admin_http.go`.

---

### J1
**`updateSettings` mutation persists and is read back.**

- **Coverage**: the operator settings surface — change something
  via mutation, read it back via query.
- **Target**: LOCAL.
- **Preconditions**: service running, admin credentials.
- **Steps**:
  1. Query current settings.
  2. `mutation { updateSettings(relayUrl: "https://relay2.example.com") { relayUrl } }`.
  3. Query again to confirm the change.
  4. Confirm `hypergoat_admin_settings_changed_total{field="relay_url"}`
     increments.
  5. The service log line `event=admin_settings_changed`
     `field=relay_url before=... after=...` appears with `before`
     and `after` redacted via `logsafe.String`.
- **Expected**: change persists; metric ticks; log shape correct.
- **Cleanup**: reset to the original value via a second mutation.
- **Refs**: `internal/graphql/admin/resolvers.go` `UpdateSettings`.

---

### J2
**`purgeActor` preview-then-confirm removes record + actor atomically.**

- **Coverage**: the destructive actor-purge flow with preview +
  HMAC-signed token, scope checks, count-drift guard.
- **Target**: LOCAL (destructive).
- **Preconditions**: at least one actor with records exists. Pick
  one whose deletion is acceptable.
- **Steps**:
  1. `previewPurgeActor(did: "<did>")` — note `purgeToken` and
     `recordCount` in the response.
  2. `purgeActor(did: "<did>", confirmToken: "<token>")`.
  3. Query `record` and `actor` rows for that DID — should be 0.
  4. Try to reuse the same token — expect rejection with the
     `PurgeReasonReplayed` reason metric.
  5. Try a tampered token — expect rejection with
     `PurgeReasonSignatureInvalid`.
- **Expected**: deletion atomic (both tables empty); replay and
  tamper rejected.
- **Cleanup**: re-ingestion will reintroduce the actor if the DID
  is still active.
- **Refs**: `internal/graphql/admin/purge.go`.

---

### J3
**`resetAll` preview-then-confirm wipes user data only.**

- **Coverage**: the resetAll admin escape hatch — must wipe records,
  actors, labels, activity, notifications, etc., but preserve
  config (admin DIDs, relay URL), lexicons, schema_migrations, and
  oauth_client registrations.
- **Target**: LOCAL (destructive).
- **Preconditions**: stack is populated.
- **Steps**:
  1. `psql` — capture row counts for `config`, `lexicon`,
     `schema_migrations`, `oauth_client`, `label_definition`
     (these must be PRESERVED).
  2. `previewResetAll` — note `purgeToken` and the table list.
  3. `resetAll(confirmToken: "<token>")`.
  4. `psql` — verify preserved tables are unchanged.
  5. `psql` — verify `record`, `actor`, `label`,
     `jetstream_activity`, `notification`, `notification_participant`
     are empty.
- **Expected**:
  - Preserved tables: row counts unchanged.
  - Wiped tables: row counts 0.
- **Cleanup**: ingestion repopulates over time. Re-run migrations
  if anything looks off.
- **Refs**: `internal/graphql/admin/purge.go` `ResetAll`,
  `resetAllTables`.

---

### K1
**Misconfigured DB URL: rejection error redacts the password.**

- **Coverage**: pre-existing bug fixed in Track 7.Z — the
  `ConnectDatabase` rejection path used to log the raw URL with
  the password. Now uses `config.RedactPassword`.
- **Target**: LOCAL.
- **Preconditions**: ability to start the service with a custom
  `DATABASE_URL`.
- **Steps**:
  1. Set `DATABASE_URL="mysql://admin:secret@host/db"` and start
     the service.
  2. Capture the error output and the error message returned to
     stdout / stderr.
- **Expected**:
  - Error message contains `mysql://admin:****@host/db` or similar
    redaction.
  - Error message does NOT contain the literal string `secret`.
  - Service exits non-zero (boot failure).
- **Cleanup**: restore valid `DATABASE_URL`.
- **Refs**: `internal/server/database.go` `ConnectDatabase`.

---

### K2
**SIGTERM drains background goroutines without "pool closed" errors.**

- **Coverage**: Track 3c — `sync.WaitGroup` on `backgroundServices`
  ensures the OAuth cleanup goroutine exits before the deferred
  `db.Close()` fires.
- **Target**: LOCAL.
- **Preconditions**: service running, OAuth cleanup ticker started
  (it always does when OAuth is enabled).
- **Steps**:
  1. Send SIGTERM to the process: `kill -TERM <pid>`.
  2. Wait for the process to exit.
  3. Inspect the shutdown log lines.
- **Expected**:
  - Service exits with status 0.
  - No log lines containing
    `"pool closed"`, `"use of closed connection"`, or
    `"closed pool"` between "Shutdown" and the final line.
  - The cleanup goroutine's exit is visible (or implicit — its
    log lines stop appearing before the pool close).
- **Cleanup**: none.
- **Refs**: `cmd/hypergoat/main.go` `backgroundServices.Stop`,
  `internal/server/oauth_handlers.go` `StartCleanupWorker`.

---

## Adding a new behavioral test

Use this template. Pick the next free ID in the section's letter
range (or open a new section if the test doesn't fit any current
one). Add a row to the catalogue table at the top so it shows up
in the checklist.

```markdown
### <ID>
**<Short imperative name>.**

- **Coverage**: what user-visible behavior or system contract this
  guards.
- **Target**: `LOCAL` | `DEV-DEPLOYED` | `EITHER`.
- **Preconditions**: state that must be true before running.
- **Steps**:
  1. <command or action>
  2. <command or action>
- **Expected**:
  - <bullet per assertion>
- **Cleanup**: how to restore state (none if non-destructive).
- **Refs**: file paths, audit/PR/issue references.
```

Three-line guidance for tests that mix tools:

1. **Make every command paste-runnable.** No `<TODO>` placeholders
   except for genuinely user-specific values; even those should
   come with an example pattern.
2. **Assert against shape, not exact text** where the response can
   evolve (timestamps, IDs, dynamic counts). Use `jq` to pull the
   keys you care about and compare structure, not whole bodies.
3. **Always include the cleanup step** for destructive tests, even
   if it's "restart the service" or "re-run migrations." A test
   that leaves state behind is a test that won't be re-run.
