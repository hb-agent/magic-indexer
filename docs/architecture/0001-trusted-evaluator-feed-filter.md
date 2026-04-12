# ADR 0001: Trusted-Evaluator Feed Filtering

**Status**: Accepted
**Date**: 2026-04-12
**Scope**: Magic Indexer + certs.social
**Related files**: `internal/graphql/query/connection.go`, `internal/database/repositories/records.go`

## Context

certs.social is adding a feature where a user can filter their activity feed
to only show records authored by users who have been "endorsed" by a
configurable set of trusted evaluators. The requirements shaping the design:

1. **Toggleable multi-evaluator set.** The app ships with a list of known
   evaluators. The user can toggle individual evaluators on and off, and the
   feed filter must reflect any subset of that list in real time.
2. **Server-side filtering.** The feed uses infinite scroll with keyset
   pagination and an existing server-side label filter. The trusted-evaluator
   filter must compose with those cleanly — no fill-loop, no short pages, no
   loss of "end of feed" signal.
3. **Live revocation.** If an evaluator deletes an endorsement record, the
   filter must update within seconds. Jetstream propagation is the existing
   live-update channel.
4. **No new operational infrastructure where reasonable.** Running another
   service (new deploy, new database, new uptime story) must be justified by
   a benefit this feature specifically requires.

This document records the alternatives we evaluated and why we chose a
purpose-built filter primitive on Magic Indexer over the ATProto-native
patterns that exist for similar feature shapes. The options are explained at
enough length that readers unfamiliar with the ATProto ecosystem can follow
the tradeoffs.

## Decision

Add a generic `authors: [String!]` filter argument to Magic Indexer's record
connections. The filter is not endorsement-specific — it means "records
authored (published under) by DIDs in this list" and is available on every
record collection via `ConnectionArgs`. The certs.social client maintains a
committed list of trusted-evaluator DIDs, tracks per-user toggles in
localStorage, derives the set of endorsed-DIDs from the active evaluators by
querying the endorsement collection through the same filter, and passes the
resulting DID set to the activities query through the same filter.

The full reasoning is in the "Options considered" section below. The
implementation plan is in
[`0001-trusted-evaluator-feed-filter-implementation.md`](./0001-trusted-evaluator-feed-filter-implementation.md).

## The critical constraint: multi-evaluator toggling

One requirement drove the most options off the table faster than the others:
the feed filter must reflect an arbitrary subset of a known list of
evaluators, toggled dynamically by the end user, with no backend reconfiguration.

Most ATProto primitives designed for "filter a feed to a curated set of
users" assume the curation is static at the server — the server operator
picks the filter, publishes it as a named feed or a named labeler, and
clients subscribe by name. Our requirement inverts that: the curation is
static at the app level, but the filter is dynamic at the user level, down to
the granularity of individual evaluators. That mismatch is the single most
important reason we ended up where we did.

The reader should keep this requirement in mind as each alternative is
evaluated — it is what eliminates most of them.

## Options considered

### Option A: Bluesky Feed Generators (`app.bsky.feed.generator`)

**What it is.** Feed generators are the canonical ATProto primitive for
"third parties can publish custom-ordered lists of content that clients can
subscribe to." A feed generator is a web service that:

- Consumes the ATProto firehose (or a specific relay) to see everything
  happening in the network.
- Maintains its own derived indexes optimized for whatever rule the feed
  implements — trust graph, topic classification, machine-learned
  relevance, social graph distance, keyword search, or anything else.
- Exposes an XRPC endpoint, `app.bsky.feed.getFeedSkeleton`, that returns
  an ordered list of post URIs (just URIs — not full post data).
- The Bluesky AppView (`bsky.social`) then hydrates those URIs into full
  post records before returning them to the client.

Bluesky's own "Discover" and "Following" algorithmic feeds are feed
generators. Third-party feeds like Skygaze, Graze, and Skyfeed also use this
primitive. A user in the main Bluesky app can pin any feed generator as a
tab in their feeds, and the Bluesky client handles the hydration layer
transparently.

A feed generator is essentially "build your own recommendation system and
plug it into ATProto." The primitive is extraordinarily flexible.

**What this would look like for us.** We would build a new service — call it
`certs-trusted-feed` — that consumes the firehose, tracks
`app.certified.temp.graph.endorsement` records to maintain an endorsed-DID
set, and serves a `getFeedSkeleton` endpoint returning activity URIs whose
authors are in that set. certs.social would call the skeleton endpoint and
hydrate via Magic Indexer.

**How it handles toggleable evaluators.** This is where the primitive
doesn't fit. The `getFeedSkeleton` contract accepts a `feed` parameter
identifying which pre-configured feed to return, plus standard pagination
(`cursor`, `limit`). It does not accept arbitrary filter parameters like
"evaluators = {Alice, Bob}" or "evaluators = {Bob, Charlie}". Supporting
dynamic subset selection would require one of:

1. **Pre-register every combination as a separate feed.** With N evaluators,
   that's 2^N feeds, most of them never accessed. Infeasible beyond a
   handful of evaluators.
2. **Add a custom query parameter extension** to the feed skeleton endpoint.
   This works technically, but it breaks the standard contract. Any client
   that talks to the feed generator must know about the extension, and we
   lose the primary benefit of using a feed generator in the first place —
   generic ATProto clients picking up the feed without knowing anything
   about certs.social.
3. **Build an "all-evaluators" feed and filter client-side.** This defeats
   the server-side filtering advantage and reintroduces the
   client-side-filter-under-infinite-scroll problem (fill loops, short
   pages, broken end-of-feed signal) that we explicitly want to avoid.

**Other costs.** A new service is a real operational commitment: its own
deployment, its own database, its own firehose consumer, its own health
checks and monitoring. In addition, the primary benefit of feed generators
— cross-client reuse — does not materialize for certs.social because the
content it serves is in custom lexicons (`org.hypercerts.*`,
`app.certified.*`). The Bluesky AppView does not know how to hydrate those
records. A feed generator that returned `org.hypercerts.claim.activity` URIs
would be unrenderable by the Bluesky app, so any claim that users could
"pin certs.social's trusted feed in Bluesky proper" is false until Bluesky
gains lexicon-agnostic hydration (not planned).

**Verdict.** Right primitive for generic, statically-configured content
feeds. Wrong primitive for dynamically-toggleable filters over custom
lexicons.

### Option B: Labelers (`com.atproto.label.*`)

**What it is.** Labelers are ATProto services that emit signed label records.
A label is a name-value pair targeting a URI or a DID, optionally with
expiration and negation semantics. Example labels include `nsfw`, `spam`,
`verified`, and custom values. Labelers publish their assertions via a
websocket stream (`com.atproto.label.subscribeLabels`). AppViews that want
labels ingest the stream, store the labels in their database, and apply them
to feed queries.

Users configure which labelers they trust in their ATProto client
preferences. The Bluesky app uses labelers for content moderation — users
who subscribe to a moderation labeler see NSFW content hidden, spam
filtered, and so on.

Magic Indexer already consumes a labeler: the existing
`high-quality` / `standard` / `draft` / `likely-test` quality labels come
from `hyperlabel-production`, are ingested via Magic Indexer's existing
labeler subscription pipeline, and power the existing `labels` /
`excludeLabels` filter on record connections. This is important context —
the labeler path is already load-bearing in our stack.

**What this would look like for us.** Run (or extend) a labeler service that
emits endorsement labels on DIDs whenever an evaluator makes an endorsement.
Either one label value per evaluator (`endorsed-by-alice`,
`endorsed-by-bob`, ...), or one label value for "endorsed" with the labeler
DID acting as attribution (`src: did:plc:alice val: endorsed`). Magic
Indexer ingests the labels. The existing `labels` filter serves
"records where the author has label `endorsed` from any labeler in the
configured set."

**How it handles toggleable evaluators.** Partially. If we use the "one
value per evaluator" scheme, the existing `labels: [String!]` filter
already accepts "any of" semantics — passing
`labels: ["endorsed-by-alice", "endorsed-by-bob"]` returns records whose
authors have at least one of those labels. Toggling evaluators maps to
toggling values in the filter list. From the indexer's perspective, this
would genuinely work.

The awkwardness is elsewhere:

1. **Endorsement semantics change.** In the current lexicon, "Alice endorses
   Bob" is a record that *Alice* writes to *Alice's* PDS. Alice owns the
   record, can delete it from her PDS, and the assertion is verifiable
   against Alice's signing key. In the labeler model, endorsement becomes
   "the labeler service, acting on Alice's behalf, emits a label targeting
   Bob." Evaluators cannot write endorsements directly from their own PDS;
   they must route through the labeler operator. That is a meaningfully
   different social contract — the primitive stops being a first-person
   action and becomes a delegated action. For a moderation labeler this is
   fine; for first-person endorsement it feels wrong.
2. **Label values are static at the labeler.** Every new evaluator requires
   a new label value baked into the labeler's configuration. Adding an
   evaluator means coordinating with the labeler operator, not just
   updating a client-side list.
3. **Cross-labeler composition is not a native operation.** Combining
   "endorsements from labeler X" with "endorsements from labeler Y"
   requires either one labeler operator emitting all of them
   (centralization, single point of failure, single point of policy) or
   client-side union (back to client-side logic). The existing labeler
   ecosystem assumes a labeler represents one coherent perspective —
   moderation, NSFW detection, a specific curator — not a dynamic set of
   independent trust-asserters.
4. **Our existing labeler is misconfigured upstream.** The
   `hyperlabel-production` labeler that the quality-label system uses has
   known PLC-document issues (stale localhost URLs, label-signing key
   mismatches between the DID that owns the HTTP endpoint and the DID that
   signs label records) that we do not currently have permission to fix.
   Building the trust-graph feature on top of the labeler path would
   require resolving those issues first, which is blocked on work outside
   this project.
5. **Revocation is first-class in labelers** (via `neg: true` negation
   records propagating through the subscribeLabels stream), which is a
   genuine positive over the current "delete the record" approach. But
   issuing a negation still routes through the labeler service, not from
   the evaluator's PDS directly.

**Verdict.** Correct primitive if endorsement were a collective moderation
signal where one labeler operator acts on behalf of many users according to
a policy. Uncomfortable fit for endorsement as a first-person social action,
made worse by the upstream infrastructure issues we cannot fix.

### Option C: Lists (`app.bsky.graph.list` + `app.bsky.graph.listitem`)

**What it is.** ATProto lists are curated collections of DIDs. A list is
created by one user (the owner), who adds individual entries via
`listitem` records. Lists can be shared, pinned, and subscribed to. Bluesky
uses lists for two built-in purposes: **mute lists** and **block lists**.
When a Bluesky user subscribes to a block list (via an
`app.bsky.graph.listblock` record), Bluesky's AppView filters out posts from
DIDs in that list when serving that user's feed.

Lists are the closest existing ATProto primitive to "a set of DIDs with
social meaning."

**What this would look like for us.** Each trusted evaluator maintains a
list — `app.bsky.graph.list` — of the DIDs they endorse. Each endorsement is
a `listitem` record adding a DID to the list. certs.social reads the lists
owned by the currently-active evaluators, unions the `listitem` entries,
and uses the resulting DID set to filter the feed.

**How it handles toggleable evaluators.** This is actually the closest fit
among ATProto-native primitives. Toggling an evaluator on/off maps cleanly
to including or excluding their list from the union. The list primitive is
designed for exactly this shape: "a set of DIDs with a common curator."

But:

1. **The Bluesky-native list model is owner-centric.** A list is a single
   curator's single viewpoint. This matches the "one evaluator, one trust
   graph" structure reasonably well, but some of the assumed workflows
   (lists are relatively stable; users don't edit them constantly) don't
   perfectly match endorsement (evaluators might endorse new users
   frequently).
2. **Lists don't have a native "include-only" filter semantic in Bluesky's
   AppView.** Block/mute lists have built-in support for negative filtering
   (hide members from your feed) because the use cases Bluesky prioritized
   were moderation. There is no standard "positive list filter" primitive
   — no `app.bsky.graph.listinclude`. To filter a feed to only users in a
   list, we still need a custom filter path in our own AppView, meaning
   lists don't save us the Magic Indexer work.
3. **The data shape is slightly awkward for aggregation.** A `listitem`
   references its parent list, so the query to compute "all DIDs in the
   lists of active evaluators" is a two-level join. Our custom endorsement
   lexicon flattens this: each endorsement record directly references the
   subject DID. For the specific query pattern we need, the custom lexicon
   is actually less complex.
4. **Cross-ecosystem portability is a mixed picture.** Lists are a Bluesky
   primitive, so Bluesky-aware tooling could read evaluator lists in
   principle. But this only matters if Bluesky-aware tooling also cares
   about certs.social's custom activity lexicons — and it doesn't. Nothing
   in the Bluesky ecosystem today has a reason to look at a list owned by a
   certs.social evaluator, so the portability benefit is theoretical.

**Verdict.** Functionally workable. Would end up in approximately the same
architectural place as our chosen approach (custom indexer filter + client
derivation), routed through a Bluesky-native primitive. The additional
complexity of the list/listitem join and the loss of the flat endorsement
record shape don't unlock meaningful wins for our specific use case.

### Option D: Client-side filter only (no backend changes)

**What it is.** The client fetches the unfiltered activity feed from Magic
Indexer, fetches endorsement records separately, derives the endorsed-DID
set, and filters each page of activities client-side before rendering.
No backend work.

**What this would look like for us.** Straightforward — a new endorsement
hook plus minor changes to the existing feed hook to apply an in-memory
filter after each page load.

**How it handles toggleable evaluators.** Trivial on the client. Toggling an
evaluator changes the local `Set<endorsedDid>`, the filter reapplies on the
next render, visible records change.

**Why we rejected it.** The infinite-scroll pagination problem. When the
client fetches a page of 50 raw activities but only (say) 2 of them are from
endorsed users, the user sees a nearly-empty page. The scroll trigger fires
again — because the viewport isn't full — and fetches another page, which
might yield one match, and another page, and another. The hook re-introduces
a "fetch until we have enough visible results" loop that was deliberately
removed from `useGlobalFeed` when the server-side label filter was adopted.
The UX regression is directly proportional to how sparse the trust graph is,
and is worst exactly when the trust graph is smallest — at launch.

Additionally:

1. **`hasNextPage` loses its honest meaning.** It becomes "the indexer has
   more raw records, but whether any of them are visible under your filter
   is unknown."
2. **"End of feed" detection requires paginating through the entire
   underlying feed**, which can be thousands of records for a sparse trust
   graph. The UI either shows "end of feed" prematurely (lie) or never
   (broken trailing spinner).
3. **A runaway fetch loop is a real hazard** when the scroll-to-load-more
   trigger fires faster than pages can be filtered. This specific pattern
   has caused production incidents at more than one company.
4. **Bandwidth and indexer load scale with trust-graph sparsity**, not with
   what the user sees. One scroll event can trigger a dozen raw-page
   fetches.

This option ships fastest and costs nothing upfront, but adopts a UX
regression we already worked to eliminate. We rejected it as a permanent
solution. We considered it as an intermediate stepping stone en route to a
better solution and rejected that too, in favor of shipping the best version
directly.

### Option E: Purpose-built AppView filter on Magic Indexer (chosen)

**What it is.** Magic Indexer is a custom AppView for certs.social — a Go
service that consumes the ATProto firehose, maintains its own indexes, and
exposes a GraphQL API. Unlike `bsky.social`, it is not a generic
multi-client AppView; it is a purpose-built backend for one application. As
such, its API surface can include filter primitives specific to what
certs.social needs, in the same way the `labels` filter is already
specific to what certs.social needs.

We add a generic `authors: [String!]` filter to the existing
`ConnectionArgs` used across all record connections. The filter semantic is
"records whose publishing DID is in the supplied list." It composes with
the existing label filter and with keyset cursor pagination without
special-casing.

**What this looks like for us.** The certs.social client maintains a list
of trusted-evaluator DIDs, committed to the client's GitHub repository. A
user setting, persisted in localStorage, tracks which evaluators are
currently toggled on. On feed load:

1. The client fetches endorsement records authored by the currently-active
   evaluators via `appCertifiedTempGraphEndorsement(authors: $active)`.
2. The client derives `endorsedDids = Set<subject>` from the results.
3. The client fetches activities via
   `orgHypercertsClaimActivity(authors: $endorsedDids, labels: $labels)`.
4. The feed renders normally. Each activity card shows an attribution chip
   indicating which active evaluators endorsed that author.

**How it handles toggleable evaluators.** Directly. Toggling an evaluator
changes the `activeEvaluators` set, which changes the cache key on the
endorsements query, which triggers a refetch, which produces a new
`endorsedDids` set, which changes the cache key on the activities query,
which triggers a refetch. Pagination remains server-side and honest. No
client-side filter loop.

**Tradeoffs.**

- **Pro:** Cleanest path to a well-paginated shipped feature. No new
  services. Composes with existing `labels` filter. The `authors`
  primitive is generic — reusable for any future "records from a specific
  DID set" feature, not endorsement-specific. Attribution data for the UI
  is already on the client from the endorsement query, so "Endorsed by
  @alice" costs nothing beyond the render.
- **Pro:** Honors the existing Magic Indexer architecture. The indexer is
  already a purpose-built AppView with one custom filter primitive
  (`labels`) that is not part of the standard Bluesky AppView API. Adding
  `authors` is an incremental step, not a new pattern.
- **Pro:** Trust-graph logic stays on the client, which means iterating on
  "what counts as trusted" — stake-weighted endorsements, time decay,
  transitive trust, category-specific evaluators — is a client-only change.
  The indexer's filter remains a stable generic primitive.
- **Pro:** The toggleable-evaluators requirement, which was the hard
  constraint on every other option, reduces to a cache key on two
  GraphQL queries. There is no combinatorial explosion, no new service per
  combination, no delegation of curation to an external operator.
- **Con:** The `authors` filter is not a standard ATProto primitive.
  Bluesky-native clients cannot use this feature without speaking to Magic
  Indexer's GraphQL API. For certs.social this is a non-issue — the custom
  lexicons already require a custom AppView to be renderable at all — but
  it means the feature is not portable to the broader ATProto ecosystem
  without additional work.
- **Con:** We carry another filter axis on the indexer's API surface. If
  the indexer's filter set grows indefinitely over time, the repository
  layer risks a combinatorial mess of method variants
  (`GetByCollectionWithAAndKeysetCursor`, `...WithAAndBAndKeysetCursor`,
  and so on). We mitigate this by refactoring the existing sibling-method
  pattern into a unified `GetByCollectionFiltered(ctx, ..., RecordFilter)`
  as part of the same PR. Adding a third filter axis later becomes a
  struct field, not a method explosion.
- **Con:** The empty-array filter semantic (`authors: []` → "zero results")
  is load-bearing. A client bug that silently produces an empty set could
  unintentionally suppress the entire feed if the semantic were reversed
  ("empty means no filter"). We make this distinction explicit at every
  layer — the repo method checks for empty-but-not-nil, the resolver logs a
  warning when it fires, and a metric counts the events for alerting.

## Decision rationale

We chose Option E because it is the only option that cleanly handles the
toggleable-multi-evaluator requirement without either introducing new
operational infrastructure (Options A and B) or accepting a UX regression
(Option D). Option C comes close but adds join complexity without unlocking
benefits we actually care about.

Secondary reasons:

1. **Pattern consistency.** Magic Indexer already exposes one bespoke
   filter primitive (`labels`) that is not part of the generic ATProto
   AppView API. Adding a second one is incremental, not a departure.
2. **Generic primitive.** `authors` is not endorsement-specific. Any future
   feature that needs "records from a specific DID set" — curated feeds by
   topic curators, moderator-authored records, records from the user's
   social graph — uses the same primitive with no additional backend work.
3. **Operational surface.** Zero new services. The backend change is a
   migration, a repository refactor, and resolver wiring — all in the
   same repository that is already deployed and monitored.
4. **Trust-graph iteration speed.** Keeping trust logic on the client means
   experimenting with "what counts as trusted" is a client-only change,
   not a coordinated backend/client release. This matters because the
   semantics of trust are the part most likely to change based on product
   feedback.
5. **Honest pagination.** Server-side filtering preserves the keyset
   cursor's contract. `hasNextPage` means "more matching records exist."
   End-of-feed detection is trivial. No fill loops.

## Consequences

### What this locks us into

- Magic Indexer's API surface gains one new axis (`authors`). The
  unified `RecordFilter` refactor prevents future axes from causing
  method-variant explosion, but the surface itself still grows.
- The trusted-feed feature is not accessible from generic ATProto
  clients. A Bluesky-native client cannot render it because it does not
  speak Magic Indexer's GraphQL API and does not understand
  `org.hypercerts.*` lexicons.
- The trust graph itself — `app.certified.temp.graph.endorsement` records
  in evaluators' PDSes — is portable and can be indexed by any interested
  party. Only the filter logic is proprietary to Magic Indexer.

### What it keeps open

- The feature can later be wrapped in a feed generator for cross-Bluesky
  reuse if that becomes a priority. The feed generator would be a thin
  XRPC service that calls Magic Indexer's GraphQL API internally and
  exposes `getFeedSkeleton`. The `authors` filter remains the underlying
  primitive. This is a strict extension of this ADR, not a revision of it.
- The trust-graph logic can iterate on the client (time decay, transitive
  trust, stake weighting, per-category evaluator subsets) without
  re-touching the backend.
- The existing `labels` filter composes with `authors` without special
  casing. Any future filter axis should compose the same way.

### What it forecloses

- A "universal trusted feed" that works in the main Bluesky app is not
  possible with this approach alone. It requires the feed-generator
  extension described above, plus Bluesky's AppView eventually gaining
  lexicon-agnostic hydration (not planned, not on our roadmap).
- Endorsement as a labeler-mediated primitive — where the labeler operator
  issues labels on evaluators' behalf — is incompatible with this design.
  The current design treats endorsement as a first-person PDS record.
  Switching later would require schema migration and a labeler operator
  commitment.

## Prior art and related patterns

- **Pinksea** (ATProto image-sharing app): runs a custom AppView with
  tag-based filter arguments on its feed query. Architecturally the same
  shape as what we propose — purpose-built AppView with custom filter
  primitives, not generic feed generators.
- **frontpage.fyi** (ATProto Hacker News clone): custom AppView with
  custom sort and filter primitives on its post query. Also the same
  shape.
- **whtwnd.com** (ATProto long-form blogging): custom AppView with its
  own filter API.

All of these run their own AppViews and expose their own filter primitives
rather than trying to shoehorn custom lexicons into Bluesky-native
primitives. The pattern of purpose-built AppViews with bespoke filter
surfaces is established in the ATProto ecosystem for apps whose content
shape doesn't map cleanly onto `app.bsky.feed.post`.

The generic Bluesky AppView, `bsky.social`, does not expose this kind of
filter surface — its API is intentionally narrow because it serves many
clients with conflicting needs. Purpose-built AppViews optimize for their
one client's UX and trade narrow-API discipline for app-specific
expressiveness.

## References

- Implementation plan:
  [0001-trusted-evaluator-feed-filter-implementation.md](./0001-trusted-evaluator-feed-filter-implementation.md)
- Lexicon: `app.certified.temp.graph.endorsement` (uploaded to
  magic-indexer-dev; see deployment runbook)
- Related ATProto specs:
  - [Feed Generators](https://atproto.com/specs/feed-generator)
  - [Labelers](https://atproto.com/specs/label)
  - [Lexicons](https://atproto.com/specs/lexicon)
- Known open issue about schema hot-reload after `uploadLexicons`:
  [hb-agent/magic-indexer#22](https://github.com/hb-agent/magic-indexer/issues/22)
