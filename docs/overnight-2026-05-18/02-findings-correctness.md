# 02 — Correctness / Robustness Findings

Date: 2026-05-18 (overnight pass).
Scope: 12 lenses listed in the brief. Cap: 30 findings.
Calibration: critical = data loss / crash loop / hang. high = real bug under
load or specific failure path. medium = worth fixing, low daily impact.
nits omitted.

Per-finding format: title, severity, location, problem (with excerpt),
why-it-matters, proposed fix, effort, fix-risk, reversibility.

---

### C-1: Activity-cleanup worker never runs after startup (stopped ticker)
**Severity:** high
**Location:** `internal/workers/activity_cleanup.go:33-60`
**Problem:**
```go
func (w *ActivityCleanupWorker) Start(ctx context.Context) {
    ...
    w.cleanup(ctx)            // <-- one immediate run

    ticker := time.NewTicker(w.interval)
    defer ticker.Stop()       // <-- fires the moment Start returns

    go func() {
        defer close(w.done)
        for {
            select {
            case <-ctx.Done():
                return
            case <-w.stop:
                return
            case <-ticker.C:     // <-- never fires again
                w.cleanup(ctx)
            }
        }
    }()
}
```
The `defer ticker.Stop()` is registered on `Start`, not on the goroutine. `Start`
returns the moment the goroutine is launched, so `ticker.Stop()` fires
immediately. The inner select then waits on a `ticker.C` that will never
receive again. Only the immediate `w.cleanup(ctx)` at boot ever runs.

**Why this matters here:** the worker is the only thing that purges old
`jetstream_activity` rows (7 days retention) AND the only thing that
flips orphaned `pending` activity rows to `orphaned` (10-minute
threshold). With it dead, the activity table grows unboundedly and any
`pending` row left after a crash between `LogActivity` and
`UpdateStatus` stays `pending` forever. The admin UI's activity view
shows growing zombie rows; storage costs creep up; the partial
unique index from migration 028 has nothing trimming dead entries.

**Proposed fix:** move the ticker construction (and its `defer
ticker.Stop()`) into the goroutine. Alternatively, hoist the ticker
into a struct field so `Stop()` can call `ticker.Stop()` explicitly
without relying on goroutine-scope defer. Smallest diff:
```go
go func() {
    ticker := time.NewTicker(w.interval)
    defer ticker.Stop()
    defer close(w.done)
    for { ... }
}()
```
Also: `main.go`'s `startWorkers` calls `Start` and stores a context
cancel but never calls `worker.Stop()` on shutdown — the cancellation
path covers it, but if `worker.Stop()` is meant to be the contract,
wire it in too.

**Effort:** S
**Risk of fix:** low (one-line move; existing tests should still pass).
**Reversibility:** easy.

---

### C-2: Jetstream consumer leaks one cursor-flusher goroutine per reconnect
**Severity:** high
**Location:** `internal/jetstream/consumer.go:196-202` (and the leaked
goroutine body in `internal/cursor/flusher.go:47`)
**Problem:**
```go
// startInternal is called by RunWithReconnect on every reconnect
func (c *Consumer) startInternal(ctx context.Context) error {
    ...
    if !c.config.DisableCursor {
        go c.cursorFlusher.Run(ctx)     // <-- never explicitly stopped
    }
    go c.processEvents(ctx, c.client)
    return c.client.Run(ctx)
}
```
`Flusher.Run` only exits on `ctx.Done()`. `ctx` here is the consumer's
outer `c.ctx`, not a per-generation context. Every reconnect spawns a
new flusher goroutine that lives for the rest of the consumer's
lifetime. After N reconnects (which is essentially "uptime / mean time
between disconnects"), there are N flushers all ticking on the same
`time.Ticker(5s)`, all calling `c.cursorFlusher.Save(...)` on every
tick where the atomic cursor advanced.

The labeler consumer got this right (per-generation `genDone` channel
in `internal/labeler/consumer.go:720-747`); jetstream did not.

**Why this matters here:** under a flaky upstream (jetstream2 has
occasional bounces; Tap has more), reconnects are frequent. A
week-long process with one reconnect/hour has 168 zombie flusher
goroutines, each doing a 5s tick and a DB `INSERT ... ON CONFLICT`
against `config`. Modest goroutine + heap leak (~6 KiB each) and N×
the cursor-save write rate. Memory grows linearly with reconnect
count.

**Proposed fix:** create the flusher inside `startInternal` and tie it
to the per-connection lifetime, mirroring labeler's pattern. Either
(a) construct a fresh `cursor.Flusher` per startInternal (atomic
state then lives per-generation; needs a hand-off through
`SetCurrent`), or (b) keep one Flusher and give it a per-generation
stop channel, then start it as `go c.cursorFlusher.RunWithStop(ctx,
genDone)`. (b) is the smaller surface diff.

**Effort:** M
**Risk of fix:** low (the leak path is independent of the hot ingest
path; the cursor semantics are unchanged).
**Reversibility:** easy.

---

### C-3: WebSocket clients' `Stop()` close(events) races concurrent send → panic
**Severity:** high
**Location:** `internal/jetstream/client.go:254-272` and
`internal/labeler/client.go:271-290`
**Problem (jetstream — labeler is identical):**
```go
// Run() is sending to c.events:
select {
case c.events <- event:
case <-ctx.Done():
    return ctx.Err()
}
// Stop() does (concurrently, from another goroutine):
func (c *Client) Stop() {
    c.stopOnce.Do(func() {
        close(c.done)
        c.mu.Lock()
        ... close conn ...
        c.mu.Unlock()
        close(c.events)        // <-- send-on-closed-channel panic
    })
}
```
`Consumer.Stop()` calls `c.ctxCancel()` and then `c.client.Stop()` in
sequence with no synchronisation that `Run()` has actually returned.
If `Run()` is blocked on `c.events <- event` (channel buffer full)
when Stop fires, both `c.events <- event` and `<-ctx.Done()` become
ready in the select. Go's select picks randomly; if the send wins,
the closed channel panics the goroutine.

Recovery: `cmd/hypergoat/main.go:1180` has a `recover` on the labeler
goroutine; jetstream has none. A jetstream panic from this path
takes the process down.

**Why this matters here:** the window is small but it's exactly the
window that opens at every shutdown and at every `UpdateCollections`
call (which fires on every lexicon upload, then a process restart
follows — so it's not the dominant exposure, but it is a real one).
Process crash mid-shutdown skips the cursor flush, skips ack-back to
any subscriber, and confuses the orchestrator (exit 2 vs. exit 0 vs.
exit 42 — only 42 is "restart requested").

**Proposed fix:** wait for `Run` to return before `close(events)`.
Cleanest pattern: have `Run()` `defer close(c.events)` on its own
return (so the goroutine that writes is the one that closes), and
remove `close(c.events)` from `Stop()`. `Stop()` still closes
`c.done` and the websocket — those unblock the read loop, which then
exits and the deferred close fires. Same approach as in
labeler/consumer.go:283's `client.Stop()`-after-`client.Run()`-return
pattern in `runOnce`, but applied at the client layer.

**Effort:** S
**Risk of fix:** low (single-writer closes is idiomatic Go).
**Reversibility:** easy.

---

### C-4: UploadLexicons partial-success leaves DB out of sync with running schema
**Severity:** high
**Location:** `internal/graphql/admin/resolvers_lexicons.go:153-173`
**Problem:**
```go
count := 0
for id, body := range proposed {
    if err := r.repos.Lexicons.Upsert(ctx, id, body); err != nil {
        return count, fmt.Errorf("failed to save lexicon %s: %w", id, err)
        // <-- returns WITHOUT calling notifyLexiconChange / restart
    }
    count++
}
...
if count > 0 && r.processRestartCallback != nil {
    r.processRestartCallback(...)
}
```
Schema validation in Stage 2 validates the full proposed set. Stage 3
upserts each row in a loop. If row N fails (transient DB error,
constraint, statement timeout), rows 0..N-1 are already committed.
The function returns the error without firing
`notifyLexiconChange` and without `processRestartCallback`. Result:

- DB has N new lexicons. The in-memory registry does not (no
  restart). The Jetstream consumer's `wantedCollections` is not
  updated. So the indexer has the new lexicons in DB but is not
  ingesting them.
- The schema validation in Stage 2 was against the FULL proposed
  set. The partial set in DB may build a different schema (e.g., if
  the new lexicons reference each other via `ref`). On the next
  unrelated restart, the schema builder may fail and the process
  cannot boot.

**Why this matters here:** lexicon uploads are operator actions during
maintenance windows. Partial success silently strands the deploy in
an inconsistent state until the next restart, which may then refuse
to come up. The 23-round review history mentions deployment
reliability heavily; this is the residual "deploy can wedge"
exposure.

**Proposed fix:** wrap Stage 3 in a single transaction
(`r.repos.Lexicons.BeginTx`+ a `BatchUpsert` repo method that takes
the tx). All-or-nothing matches the validation contract. The
`Lexicons` repo is single-table so the tx is bounded. Alternatively,
on any Stage 3 error, roll the loop forward (continue persisting the
rest), then fire restart + notifyLexiconChange unconditionally —
strictly worse semantics, only mentioned as a non-fix for contrast.

**Effort:** M
**Risk of fix:** low (atomic upsert of N rows is straightforward).
**Reversibility:** easy.

---

### C-5: Tap consumer goroutine has no panic recovery; jetstream + labeler do
**Severity:** medium
**Location:** `cmd/hypergoat/main.go:1010-1018` (Tap launcher) vs.
`cmd/hypergoat/main.go:1172-1190` (labeler launcher) and the
`recover`s inside `internal/tap/consumer.go:200-208` (per-event,
not whole-loop)
**Problem:** the Tap consumer is started with:
```go
go func() {
    slog.Info("Starting Tap consumer", ...)
    if err := tapConsumer.Start(tapCtx); err != nil {
        slog.Error("Tap consumer error", "error", err)
    }
}()
```
No outer `defer recover()`. The Tap consumer does have a per-event
panic recover (`internal/tap/consumer.go:199-208`), but a panic
outside `dispatch` (e.g., in `ParseEvent`, in `conn.ReadMessage`
wrapper, in the ack-write path, in the reconnect loop, in
`metrics.TapEventDispatchObserved`) will propagate out of `Start`
and unwind through `tapConsumer.Start` to this anonymous goroutine,
which is then unrecovered. Go runtime panics an unrecovered
goroutine → process crash.

By contrast: `chi/middleware.Recoverer` covers HTTP only, not these
background goroutines. The labeler launcher (line 1179) wraps the
goroutine body in `defer func() { if rec := recover(); ... }()`.
Jetstream's launcher (line 1038) doesn't — same exposure there.

**Why this matters here:** the documented policy ("a panic in one
subsystem should not take the whole process down") is enforced for
labeler but not for jetstream or tap. With the project's lexicon
work and #87/#88/#89 deltas in the schema layer, panics in the
ingestion hot path are non-zero-probability. A panic crash-loops
the orchestrator, which then exits 42 once a lexicon upload tries
to restart — confusing log output during incident response.

**Proposed fix:** add the same `defer recover()` wrapper to the
jetstream and tap launcher goroutines in main.go. Two ~6-line diffs.

**Effort:** S
**Risk of fix:** low.
**Reversibility:** easy.

---

### C-6: PurgeTokenSigner sweeper goroutine is never stopped on shutdown
**Severity:** low
**Location:** `internal/graphql/admin/purge.go:106` (start) /
`internal/graphql/admin/purge.go:154-162` (Close) vs.
`cmd/hypergoat/main.go:784` (no Close call)
**Problem:** `NewPurgeTokenSigner` unconditionally starts
`go s.runSweeper()`. `Close()` exists and cleanly stops it via
`close(s.stopSweeper)`. `cmd/hypergoat/main.go`'s
`configurePurgeMutations` builds the signer and never wires a
`Close()`:
```go
signer, err := admin.NewPurgeTokenSigner(...)
...
adminHandler.Resolver().SetPurgeTokenSigner(signer)   // <-- no Close hooked
```
At shutdown the sweeper continues running until the process exits.

**Why this matters here:** purely cosmetic in production (process is
about to exit anyway; the goroutine dies with it). Matters slightly
in tests that spin up the admin handler multiple times — leaks
goroutines across test runs. Also a minor `-race` cleanliness
issue.

**Proposed fix:** capture the signer on `backgroundServices`, call
`signer.Close()` from `bg.Stop()`. ~5 lines.

**Effort:** S
**Risk of fix:** low.
**Reversibility:** easy.

---

### C-7: `populateActivityIfEmpty` goroutine racing the deferred `svc.db.Close()`
**Severity:** medium
**Location:** `cmd/hypergoat/main.go:306` (`go
populateActivityIfEmpty(ctx, svc)`) vs. `cmd/hypergoat/main.go:203`
(`defer svc.db.Close()`)
**Problem:** The goroutine is started from `initServices` with
`ctx := context.Background()` (the local variable on line 289). It
iterates ALL records via `IterateAll` and writes activity rows. On
a large DB this can run for minutes. If the operator SIGTERMs the
process while it's still running:

1. `serve()` returns
2. `bg.Stop()` runs (defer in run())
3. `svc.db.Close()` runs (defer in run())

There is no synchronisation with the background activity-populate
goroutine. It will see `pool closed` errors mid-batch and log them
at warn — generally benign — but `IterateAll` may also crash on a
nil cursor if the pool was torn down mid-iteration. Also, the
goroutine writes via `LogActivityWithStatus` directly to the pool
with `context.Background()` (the function takes ctx but the caller
passes the package-level background).

**Why this matters here:** this only runs once per process when the
activity table is empty (first boot after a migration, or after a
manual truncation). On a Railway dev environment with sub-1s startup,
it's never observed. On a large prod backfill recovery, it's a
multi-minute window where shutdown becomes ugly.

**Proposed fix:** thread the run()-level shutdown into this
goroutine. Easiest: add a `populateCtx, populateCancel :=
context.WithCancel(context.Background())` on `backgroundServices`,
fire `populateCancel` from `bg.Stop()` BEFORE
`oauthCleanupCancel`. Or use a `sync.WaitGroup` to drain it after
`bg.Stop()` and before `svc.db.Close()`.

**Effort:** S
**Risk of fix:** low.
**Reversibility:** easy.

---

### C-8: Schema-validate callback ignores in-flight concurrent uploads (TOCTOU)
**Severity:** medium
**Location:** `cmd/hypergoat/main.go:799-820` (SchemaValidateCallback)
+ `internal/graphql/admin/resolvers_lexicons.go:73-174`
(UploadLexicons)
**Problem:** `SchemaValidateCallback` snapshots the IN-MEMORY
registry (`registry.GetAllLexicons()`), overlays the proposed
lexicons, and tries to build a schema. Returns success if the
resulting schema compiles. Then Stage 3 of UploadLexicons writes
to DB.

The in-memory registry is the boot-time snapshot; it does NOT see
prior successful uploads from this process lifetime (the process
restarts on success to pick them up). That's normally fine.

But: two concurrent uploads racing into the admin endpoint each
validate against the same boot-time registry + their own proposed
set. Schema validation cannot see the OTHER upload's proposed
lexicons. If A proposes `lexA` and B proposes `lexB` and `lexB`
contains a `ref` to `lexA`, B's validation sees the boot-time
registry without `lexA` and rejects B as an unresolved ref — even
though A is about to land. Less catastrophic but: if A proposes
something that breaks the schema and B proposes a compatible
overlay, A's restart fires; the next boot's registry has both A's
broken lexicons and B's overlay; schema build fails; deploy
wedged.

**Why this matters here:** lexicon uploads are gated by admin auth,
operator-driven, and typically not concurrent. But the project
has automation around lexicon updates (npm + admin GraphQL); two
runners (e.g., CI + manual) could plausibly collide.

**Proposed fix:** serialise uploads with a process-level
`sync.Mutex` on the resolver (the existing `backfillActive`
atomic is the closest analogue). At the admin resolver level,
acquire the mutex before Stage 1 and release after Stage 3 (or
after the restart signal). One-line addition + a defer.

**Effort:** S
**Risk of fix:** low.
**Reversibility:** easy.

---

### C-9: `c.ctxCancel` nil-check in Consumer.Stop hides Stop-before-Start race
**Severity:** low
**Location:** `internal/labeler/consumer.go:295-309`
**Problem:**
```go
func (c *Consumer) Stop() {
    c.stopOnce.Do(func() {
        c.clientMu.Lock()
        if c.ctxCancel != nil {       // <-- silently no-ops if Start hasn't run
            c.ctxCancel()
        }
        ...
    })
}
```
If `Stop()` is called before `Start()` completes the `c.ctxCancel`
assignment (line 144), `c.ctxCancel` is nil and Stop is a no-op.
But `stopOnce.Do` consumed its one chance, so subsequent Stop
calls are also no-ops. If Start later sets up ctx and runs, the
consumer is now unstoppable — no in-process cancel path will
work.

**Why this matters here:** in the main.go flow, Stop is only
called from `bg.Stop()` during shutdown, well after Start
finished. Edge case. But the labeler pause endpoint
(`newLabelerPauseHandler` in admin_http.go) fires `paused.Stop()`
on operator request — if pause fires during an unusually-slow
DID resolve in `Start`, the race window opens.

**Proposed fix:** either (a) move the `c.ctxCancel = nil`
defensive check, or (b) make Start atomic about
setting up state under the same lock Stop uses. Cleanest is to
construct ctx in the constructor (`NewConsumer`) instead of in
Start — then `c.ctxCancel` is always non-nil. That changes the
parent-context semantics (constructor doesn't have the ctx);
acceptable trade-off given the consumer is single-shot per
process today.

**Effort:** S
**Risk of fix:** medium (changes ctx wiring; needs careful test).
**Reversibility:** easy.

---

### C-10: Reaped knownVals slice retains backing array indefinitely
**Severity:** low
**Location:** `internal/labeler/consumer.go:705-712`
**Problem:**
```go
if len(c.knownVals) >= MaxKnownVals && len(c.knownValsOrder) > 0 {
    oldest := c.knownValsOrder[0]
    c.knownValsOrder = c.knownValsOrder[1:]   // <-- slice-front leak
    delete(c.knownVals, oldest)
}
```
Slicing the front off `knownValsOrder` advances the slice header
but does not release the backing array. On steady-state with a
labeler emitting more than `MaxKnownVals` distinct vals, the
backing array grows once (via the `append` two lines later) and
the slice header walks the front. Eventually GC can't reclaim
because the slice still references the array.

**Why this matters here:** memory bounded by 2× `MaxKnownVals`
worth of `cacheKey` strings; modest waste, not a leak that grows
indefinitely. Cosmetic.

**Proposed fix:** use a ring buffer or a true FIFO. Smallest:
periodically rebuild the slice (`c.knownValsOrder =
append([]string(nil), c.knownValsOrder...)`) when its capacity
exceeds 2× len. Even simpler: switch to `container/list` for
O(1) front-pop without backing-array pinning.

**Effort:** S
**Risk of fix:** low.
**Reversibility:** easy.

---

### C-11: Dynamic Jetstream restart reparents context to Background, losing operator cancel
**Severity:** low
**Location:** `cmd/hypergoat/main.go:1054-1088` (lexicon change
callback) + `internal/jetstream/consumer.go:229-266`
(UpdateCollections)
**Problem:** When a lexicon upload fires the change callback and
the existing jsConsumer is non-nil, the callback calls
`bg.jsConsumer.UpdateCollections(context.Background(),
updatedCollections)`. UpdateCollections then:
```go
c.ctx, c.ctxCancel = context.WithCancel(parent)   // parent = Background
...
go func() { _ = consumer.RunWithReconnect(ctx, c.startInternal, ...) }()
```
The new RunWithReconnect goroutine is rooted at
`context.Background()`. The OLD `bg.jsCancel` (set in
`startJetstream` from `jsCtx`) no longer governs anything — the
new goroutine ignores it because its ctx isn't in the jsCtx
tree.

This is functionally fine for shutdown because `bg.Stop()` calls
`bg.jsConsumer.Stop()` which cancels via `c.ctxCancel` (the
current one). But if anything else holds onto `bg.jsCancel`
expecting it to govern the jetstream consumer, it doesn't.

**Why this matters here:** today nothing else holds `bg.jsCancel`
— only `bg.Stop()` uses it. So this is a footgun for future
refactors, not a live bug. Worth a comment at minimum.

**Proposed fix:** pass the original `jsCtx` (or its ancestor)
through to the callback so UpdateCollections re-parents to it
instead of Background. Requires plumbing a `parentCtx
context.Context` field through to the callback. Mild surface
change.

**Effort:** M
**Risk of fix:** low.
**Reversibility:** easy.

---

### C-12: GraphQL subscription `Subscribe` race vs. `Publish` (slow subscriber drop)
**Severity:** medium
**Location:** `internal/graphql/subscription/pubsub.go:91-111`
**Problem:** `Publish` does a non-blocking send and drops the event
if the subscriber's buffer is full:
```go
select {
case sub.Events <- event:
default:
    slog.Debug("Subscription event dropped, buffer full", ...)
}
```
This is the correct policy. But there is no policy that disconnects
a persistently-slow subscriber. The `wsClient` only disconnects on
read errors or client-side complete; if the client opens a
subscription and stops reading, the pubsub silently drops every
event for that subscriber but the wsClient goroutine + the
subscription goroutine remain alive forever (the per-message read
deadline catches the silent client, but only if they also stop
sending pings — and graphql-transport-ws clients do send
keep-alives).

Worse: if a single subscriber matches a high-volume collection and
the public surface is exposed to anonymous traffic over the public
`/graphql/ws`, an attacker can open N connections and never read,
costing N goroutines + N × buffer-size events of memory until the
60s idle read deadline catches them (which it does — but only if
they're also silent).

**Why this matters here:** combined with the per-client subscription
cap of 64 (`wsMaxSubsPerClient`) this is bounded but not free.
Bigger concern: dropped events are silent at debug level — a
legitimate slow subscriber sees a lossy stream and has no way to
detect it.

**Proposed fix:** two parts:
1. Bump the dropped-event log to warn (rate-limited) and add a
   per-subscriber counter exposed via metrics.
2. Add a policy: after N consecutive drops, force-disconnect the
   subscriber and let it reconnect (this is the Bluesky pattern).

**Effort:** M
**Risk of fix:** medium (changes observed behaviour for slow
clients).
**Reversibility:** easy.

---

### C-13: `cursorFlusher` concurrent runs share `lastFlushed` state per-goroutine, defeating dedup
**Severity:** medium
**Location:** `internal/cursor/flusher.go:51-71` (interacts with C-2)
**Problem:** Each `Run` goroutine declares its own `lastFlushed`
local. If C-2 is unfixed and N flushers are alive simultaneously,
each tracks its own `lastFlushed` and each saves whenever
`f.cursor.Load() > local.lastFlushed`. On the first tick after
spawn, the new flusher's `lastFlushed` is zero, so it always
saves the current value once even if a sibling flusher just
saved the same value. With 168 flushers (one week of hourly
reconnects), that's 168 redundant writes on every reconnect's
first tick.

**Why this matters here:** scales with C-2's leak. Even after C-2
is fixed, this stays as defensive design — `lastFlushed` should
arguably be a struct field (atomic) so re-entrant Runs (which
shouldn't exist post-fix) at least share state.

**Proposed fix:** depend on C-2. Once Run is called once per
generation, this is moot. If a structural fix isn't desired, hoist
`lastFlushed` into `Flusher` as `atomic.Int64`.

**Effort:** S (folded into C-2)
**Risk of fix:** low.
**Reversibility:** easy.

---

### C-14: `OAuthHandlers.StartCleanupWorker` ticker.Stop is inside the goroutine — but no `defer`
**Severity:** low
**Location:** `internal/server/oauth_handlers.go:1176-1215`
**Problem:**
```go
func (h *OAuthHandlers) StartCleanupWorker(ctx context.Context, interval time.Duration, wg *sync.WaitGroup) {
    ticker := time.NewTicker(interval)
    ...
    go func() {
        ...
        for {
            select {
            case <-ctx.Done():
                ticker.Stop()    // <-- explicit, not deferred
                return
            case <-ticker.C:
                ...
            }
        }
    }()
}
```
Compared to C-1, this version *does* let the ticker live in the
goroutine's lifetime. But `ticker.Stop()` is called only inside
the `case <-ctx.Done()` arm; if the function returned for any
other reason (it can't today, but a future panic / break / refactor
might add one), the ticker would not be stopped. Also: `ticker` is
declared OUTSIDE the goroutine, which means it's a closure capture
and not a local — minor style smell, easy to confuse with the C-1
pattern.

**Why this matters here:** strictly cosmetic today; correct
behaviour. Worth flagging only because the same shape elsewhere
in the codebase (C-1) IS a real bug; consistency matters.

**Proposed fix:** move `ticker := time.NewTicker(interval)` inside
the goroutine and use `defer ticker.Stop()`.

**Effort:** S
**Risk of fix:** low.
**Reversibility:** easy.

---

### C-15: Tap consumer dispatch is single-threaded; one slow handler stalls the firehose
**Severity:** medium
**Location:** `internal/tap/consumer.go:144-195` (Run loop) +
`internal/tap/consumer.go:198-242` (dispatch with 30s per-event
timeout + up-to-3 retries)
**Problem:** the Tap Run loop:
```go
for {
    ... ReadMessage ...
    dispatchErr := c.dispatch(ctx, event)
    ... ack ...
}
```
`dispatch` blocks up to `30s × (1 + MaxRetries) = 30s × 4 = 120s`
in the worst case (per-attempt 30s timeout + 1s/2s/4s backoffs).
While dispatch is running, no other events are read. Tap uses
ack-based delivery so the upstream waits, but the in-process
ingestion stalls.

This is documented as intentional ("Synchronous dispatch (backpressure
via the WebSocket itself is the correct signal for ack-based
protocols)" in AGENTS.md). Fair. But the 4× retry × 30s combination
is a long stall, and the per-event timeout is set at the dispatch
level, not at the protocol level — if a single record hangs the DB
for >30s, all 4 retries hit and we lose 2 minutes of progress.

**Why this matters here:** combined with `DB_STATEMENT_TIMEOUT_MS`
default of 30000ms, a stuck DB query times out at 30s, retries 3
more times at 30s each (each retry also times out), so dispatch
gives up at ~120s. Meanwhile no other events flow. Cursor doesn't
advance; on reconnect, we re-process. Generally fine, but means a
slow DB query has a 2-minute halt on Tap ingestion, vs ~0 for
jetstream (which has the buffer + processes events concurrently
via channel).

**Proposed fix:** cap total dispatch budget to (e.g.) 60s with
fewer retries (1+2 = 3 attempts), or drop and log on first
DB-timeout. The "ack-based protocol = backpressure" argument
still stands; tighten the retry math.

**Effort:** S
**Risk of fix:** medium (changes documented dispatch semantics).
**Reversibility:** easy.

---

### C-16: Activity log + record insert are not atomic; orphan janitor depends on C-1
**Severity:** medium
**Location:** `internal/ingestion/processor.go:135-160` (LogActivity
then Insert separately) + `internal/workers/activity_cleanup.go`
(janitor)
**Problem:** the pipeline runs in this order:
1. `LogActivity(...)` returns `activityID` (status = "pending")
2. `Records.InsertWithParams(...)` writes the record
3. `updateStatus("success" | "error" | "rejected", ...)`

If the process is killed between (1) and (3) the activity row is
stuck in `pending`. The orphan janitor in
`workers/activity_cleanup.go` flips stale pending rows to
`orphaned` after 10 minutes. But the janitor only runs once per
hour AND, per C-1, never actually runs after startup. So pending
rows accumulate.

Even with C-1 fixed (so the janitor runs hourly), the design
trades atomicity for observability. That's a stated trade-off,
fine in principle. Mention here because the chain (C-1 → orphan
accumulation) is the actual exposure.

**Why this matters here:** purely an admin-UI visibility issue;
the record table itself is consistent. Mentioned to make the
chain visible: fix C-1 to recover the orphan cleanup behaviour.

**Proposed fix:** see C-1. No additional change needed here.

**Effort:** N/A (subsumed by C-1)
**Risk of fix:** low.
**Reversibility:** easy.

---

### C-17: Jetstream non-commit events advance the cursor unconditionally
**Severity:** low
**Location:** `internal/jetstream/consumer.go:285-349`
**Problem:** `processEvents` only branches into the ProcessRecord
path for `event.IsCommit()`. Non-commit events (identity,
account) fall through to `c.cursorFlusher.SetCurrent(event.TimeUS)`
without any processing on our side.

This is the intended behaviour for events we don't care about —
advancing the cursor means we don't re-deliver them on reconnect.
But: if a future change adds processing for identity events and
that processing can fail, the same loop structure means a fail
still advances the cursor (silent loss).

**Why this matters here:** cosmetic today (we genuinely don't
process those events). Trap for future refactor.

**Proposed fix:** add a comment at the cursor-advance line spelling
out the contract: "cursor advances iff the event was a commit
that succeeded, OR the event was not a commit at all." Adding a
guard test would be even better.

**Effort:** S (comment only)
**Risk of fix:** low.
**Reversibility:** easy.

---

### C-18: Migration partial-apply on non-transactional version-record failure leaves DDL+missing-row
**Severity:** low
**Location:** `internal/database/migrations/migrations.go:130-148`
**Problem:**
```go
func applyMigrationNoTx(ctx, exec, m) error {
    ...
    if _, err := exec.DB().ExecContext(ctx, m.UpSQL); err != nil {
        return fmt.Errorf("failed to apply non-transactional migration %s: %w", m.Version, err)
    }
    if _, err := exec.DB().ExecContext(ctx,
        "INSERT INTO schema_migrations (version) VALUES ($1)", m.Version); err != nil {
        return fmt.Errorf("migration %s DDL succeeded but failed to record version (manual fix needed): %w", m.Version, err)
    }
    return nil
}
```
If `INSERT INTO schema_migrations` fails after DDL succeeded, the
function returns an error that fails-fast in main. On next boot,
`getAppliedMigrations` doesn't see this version, so the migration
runs again. All non-transactional migrations in the repo use
`IF NOT EXISTS` (`CREATE INDEX CONCURRENTLY IF NOT EXISTS`), so
re-running is safe. The DROP-INDEX rollback uses `IF EXISTS`.
So idempotent today.

But: if a future non-transactional migration omits `IF NOT EXISTS`
(easy mistake), the second-boot re-run will fail with "relation
already exists" and the deploy is wedged.

**Why this matters here:** lint-time hazard, not a live bug.
Worth a developer-facing comment or a test that grep-fails for
non-transactional migrations missing `IF NOT EXISTS`.

**Proposed fix:** add a unit test in
`migrations_indexnames_test.go` (the existing pattern-guard
test) that walks every `-- no-transaction` up.sql and asserts
every CREATE statement uses `IF NOT EXISTS`. Two-line greppy
test.

**Effort:** S
**Risk of fix:** low.
**Reversibility:** easy.

---

### C-19: TriggerBackfill goroutine uses Background ctx — can't be cancelled by shutdown
**Severity:** low
**Location:** `internal/graphql/admin/resolvers_backfill.go:17-39`
**Problem:**
```go
go func() {
    defer r.backfillActive.Store(false)
    if err := r.fullBackfillCallback(context.Background()); err != nil { ... }
}()
```
The full-network backfill is launched with a fresh
`context.Background()`. There is no cancellation path. On
shutdown, `defer svc.db.Close()` will eventually fire while the
backfill is mid-write. The backfill sees pool-closed errors and
returns; the `backfillActive` atomic resets.

Visible failure mode: ugly log spam during shutdown when an
operator-triggered backfill is in flight. The CompareAndSwap
guards against concurrent triggers, but not against
shutdown-during-backfill.

**Why this matters here:** operator action, rarely-triggered. Edge
case. Worth wiring `bg`'s shutdown into the backfill ctx so the
log lines are clean.

**Proposed fix:** add a `backfillCtx, backfillCancel :=
context.WithCancel(...)` on `backgroundServices`, fire
`backfillCancel()` from `bg.Stop()`. Pass it through to the
fullBackfillCallback. Already mostly done for the boot-time
backfill (`bg.backfillCancel`); generalise.

**Effort:** S
**Risk of fix:** low.
**Reversibility:** easy.

---

### C-20: Notifications-XRPC handler error fails silently if schema build fails — admin keeps booting
**Severity:** low
**Location:** `cmd/hypergoat/main.go:617-643`
**Problem:** the notifications XRPC endpoint registration path:
```go
xrpcHandler, err := server.NewNotificationsXRPCHandler(notifResolver)
if err != nil {
    slog.Warn("Notifications XRPC endpoint disabled: schema build failed", "error", err)
} else {
    ... register handler ...
}
```
If `NewNotificationsXRPCHandler` fails (schema build for the
notifications surface broken), the admin path keeps coming up
WITHOUT the notifications XRPC. That's the documented behaviour
("must not kill the admin path"). But operators get a single
warn-level line on boot — no metric, no health-check signal. A
silently-disabled notifications endpoint will be discovered only
when downstream consumers fail.

**Why this matters here:** boot-time only, easy to miss. Pure
observability.

**Proposed fix:** expose a Prometheus gauge
`hypergoat_notifications_xrpc_enabled` (1 / 0) so
silently-disabled state is visible to alerting.

**Effort:** S
**Risk of fix:** low.
**Reversibility:** easy.

---

### C-21: `validateLabel` accepts URIs of exactly "at://" + ε (off-by-one boundary)
**Severity:** nit (omitted from severity tally; logged for completeness)
**Location:** `internal/labeler/consumer.go:631-635`
**Problem:**
```go
if !strings.HasPrefix(l.URI, "at://") || len(l.URI) <= len("at://") {
    slog.Debug("Labeler: skipping label with non-record URI", ...)
    return false
}
```
The `<=` reads correctly ("must have at least one character past
`at://`"). Fine. Mentioning only because the next bound
(`MaxLabelURILen`) compares against `len(l.URI)`, not a
post-prefix length. With `MaxLabelURILen = 8192` and the at://
prefix consumed, the actual repo/collection/rkey budget is 8187.
That's not a bug — just worth confirming the constant matches the
ATProto URI spec ceiling (which is 8192 INCLUDING the scheme).

**Why this matters here:** doesn't. Skip.

---

### C-22: Slot-allocation race: `bg.jsCancel` overwritten by lexicon-change callback can orphan original cancel
**Severity:** low
**Location:** `cmd/hypergoat/main.go:1054-1090`
**Problem:** The lexicon-change callback:
```go
adminHandler.Resolver().SetLexiconChangeCallback(func(updatedCollections []string) error {
    if bg.jsConsumer == nil {
        bg.jsConsumer = jetstream.NewConsumer(...)
        ...
        bg.jsCancel = dynCancel   // <-- overwrites the original (which was nil here)
        go func() { ... bg.jsConsumer.Start(dynCtx) ... }()
        return nil
    }
    return bg.jsConsumer.UpdateCollections(context.Background(), updatedCollections)
})
```
The branch where `bg.jsConsumer == nil` reassigns `bg.jsCancel`.
If startJetstream's initial path also fires (e.g., if collections
became non-empty between the boot and the callback), there is a
small window where both paths race to set `bg.jsCancel`.

In the actual code flow, the initial branch only runs if
`len(collections) > 0` at boot AND the callback only sees
`bg.jsConsumer == nil` if the initial branch didn't run. The two
paths are exclusive in practice. So this is a latent race that
isn't triggered today.

**Why this matters here:** doesn't (in current code). Worth a
comment so a future refactor that adds a third callback site
notices the contract.

**Proposed fix:** add an assertion or sync primitive guarding
`bg.jsConsumer` and `bg.jsCancel` together — e.g., a
`bg.jsMu sync.Mutex` for the consumer + cancel pair. Or just
document the contract.

**Effort:** S (comment) or S (mutex)
**Risk of fix:** low.
**Reversibility:** easy.

---

### C-23: Lens 8 (boundary conditions on URIs / DIDs / json badge.uri) — no material findings
**Severity:** N/A
The new #87/#88/#89 work passes URIs and badge refs through
parameterised SQL and JSON path operators that null-propagate
gracefully. Empty `uri` short-circuits to `(0, nil)` in
`CountAwardsByBadgeURI`. Empty `did` in `BackfillActor` is
rejected by `didpkg.IsValid` before reaching the DB. Array-where
guards against non-array values with `CASE WHEN jsonb_typeof()
= 'array'` so a corrupt record doesn't brick the result set. Did
not surface a panic / silent-corruption path. The validators
themselves (`SanitizeRecord`, `Validate`) already null-out
invalid optional fields.

---

### C-24: Lens 9 (concurrent map access) — no material findings
**Severity:** N/A
Every package-level or struct-field map in the hot paths is
either RWMutex-protected (`lexicon.Registry`, `oauth.DIDCache`,
`subscription.PubSub.subscribers`,
`backgroundServices.labelerConsumers`, the JTI replay cache) or
single-writer-by-construction (per-request scope, schema build
time). Did not find a map written from multiple goroutines
without synchronisation. `-race` would have caught it.

---

### C-25: Lens 6 (context cancellation correctness) — no material findings beyond C-19
**Severity:** N/A
Spot-checked every `context.WithTimeout` / `context.WithCancel`
in `internal/`. Each has a matched `defer cancel()` in the same
scope or an explicit cancel call before the function returns.
The only outlier is the TriggerBackfill goroutine (C-19).
`cursor.Flusher.Run` does its own explicit `cancel()` after
each save call (line 70). HTTP middleware deferred-cancel
pattern in `internal/server/middleware/timeout.go` is correct.

---

### C-26: Lens 7 (database transaction handling) — no material findings beyond C-4
**Severity:** N/A
The four transaction sites (`migrations.applyMigrationTx`,
`migrations.Rollback`, `records.BatchInsert`,
`admin.purge.PurgeActor` + `RemoveAllActors`) all use the
`committed bool` + `defer Rollback` pattern correctly.
`migrations.applyMigrationTx` is canonical. `BatchInsert` uses
`defer tx.Rollback()` directly (no flag) — Rollback after
Commit is a no-op so the pattern is safe. Did not find a path
where a successful Commit is followed by additional fallible
work mutating shared state in a way that would corrupt on
failure. Purge's post-commit Tap cleanup is bounded
best-effort, with explicit operator-facing semantics.

---

### C-27: Migration crash mid-non-transactional run is recoverable today, brittle tomorrow
**Severity:** low (subsumed by C-18; folded for cross-reference)
See C-18.

---

### C-28: Lens 11 (jetstream reconnect / cursor) — cursor advance is correctly post-processing
**Severity:** N/A
The cursor only advances after `ProcessRecord` returns nil. On
record-insert failure, `continue` skips the cursor update. On
reconnect, the firehose redelivers from the last-saved cursor
and the `record (uri PK)` upsert + `jetstream_activity
(source_event_id partial unique index)` dedup catch the
re-delivery. The chain is sound for at-least-once → effectively-
once. No findings beyond C-2 (the flusher leak) and C-3 (the
events-close race).

---

### C-29: Notifications hook policy is `HookLogContinue` — a malformed record cannot stall ingestion
**Severity:** N/A
Confirmed `internal/notifications/service.go:36-42` registers the
hook with `HookLogContinue` and the runHook in
`internal/ingestion/processor.go:307-314` has a deferred
`recover()` that translates panics into errors. A panic in an
extractor is caught, logged, and the record insert is unaffected.
This is the correct policy for a hot-path hook.

---

### C-30: Shutdown ordering claim in `backgroundServices.Stop()` checks out
**Severity:** N/A (lens 1 finding)
Walked the documented order:

1. `oauthCleanupCancel` + Wait — cleanup goroutine exits before
   anything else can pull the DB pool out from under it. ✓
2. `workersCancel` — activity cleanup ticker stops (modulo C-1
   meaning it never started running properly). ✓
3. `jsConsumer.Stop()` — calls into the consumer's Stop, which
   cancels its context and stops the client. C-3 race risk noted.
4. `jsCancel` — already-cancelled-or-no-op (because UpdateCollections
   may have overwritten the original; see C-11). Redundant but safe.
5. `tapCancel` — cancels the Tap context.
6. `didCacheStop()` — closes the DID cache cleanup goroutine's done channel.
7. Snapshot labelers under lock, then Stop each — prevents the
   `c.Stop()` (which may take a flush lock) from being called
   while labelerMu is held, avoiding any lock-ordering deadlock
   with the /stats handler reading the slice. ✓
8. `labelerCancel` — parent cancel, redundant after each labeler's
   own Stop. ✓
9. `backfillCancel` — interrupts in-flight backfill.

The pattern is correct; the only material findings on top of
this audit are C-3 (close-on-events race during Stop) and
unrelated leaks (C-2, C-6).

---

## Severity tally

| Severity | Count |
|----------|------:|
| critical | 0     |
| high     | 4     |
| medium   | 6     |
| low      | 10    |
| (nits, suppressed) | — |
| (no-finding lenses, logged for transparency) | 6 |
| **total entries** | 30 |

## Highest-leverage fixes for the morning

If picking three:
1. **C-1** (activity cleanup never runs) — 5-line move, restores a
   subsystem that's silently broken in production.
2. **C-2** (jetstream cursor-flusher leak per reconnect) — bounded
   memory leak under flaky upstream, fix is small.
3. **C-3** (client Stop close-on-events race) — once-per-shutdown
   panic risk on jetstream + labeler; lift `close(events)` into
   the Run-deferred path.

C-4 (UploadLexicons partial commit) is the highest-impact "wedged
deploy" risk but is gated on operator action.
