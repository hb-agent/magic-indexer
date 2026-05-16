# Plan review — round 1

**Plan**: `docs/case-insensitive-string-eq/plan.md`
**Reviewers**: SQL/perf/security (R1), GraphQL/API ergonomics (R2), test coverage (R3).
**Decisions recorded**: 2026-05-16.

The plan has been updated in place with every accepted item folded in.
This file is the audit trail.

## Conflict resolved up front

**R1 item 5** wanted to refuse `eqi`/`ini` on `tid`, `at-uri`, `uri`
in addition to `cid`/`cid-link`, citing atproto/RFC case-sensitivity.

**R2 item 3** argued that the in-repo precedent is that
`contains` and `startsWith` (both case-insensitive ILIKE) are
already exposed on the same lexicon types with no per-type
opt-out — so a selective refusal on `eqi`/`ini` would produce an
asymmetric surface where case-insensitivity is allowed for some
operators and not others on the same field. R2 also pointed out
(implicitly) that `cid`/`cid-link` refusal creates an introspection-
vs-runtime mismatch the consumer would hit cold.

**Decision**: go further than either reviewer. Expose
`eqi`/`ini` on **every** `StringFilterInput`-bearing property,
**including** `cid`/`cid-link`. Document the trade-off in the
operator descriptions — for content hashes and lowercase TIDs,
`eqi` is identical to `eq` and harmless; for the authority
component of an `at-uri` it can produce false matches and the
consumer should use `eq` if exact identity is required. This
matches the existing precedent set by `contains`/`startsWith`,
avoids the schema/runtime mismatch R3 item 10 flagged, and trades
a tiny correctness footgun (mixed-case DIDs inside at-uris) for a
much simpler API. `DIDFilterInput` remains operator-restricted
(no `eqi`/`ini`) — that's the contract for spec-canonical DIDs.

---

## R1 — SQL / performance / security

1. **P1 — eqi loses GIN fast path.** **Accept.** Plan now documents
   the unindexed-on-its-own behaviour in the field description and
   captures "follow-up: functional index `lower(json->>'type')`"
   as a deferred item in Out-of-scope.

2. **P1 — `ini: []` policy unclear.** **Accept.** Plan now requires
   `len(values) >= 1` in `Validate()` for `OpIni`. (Also tightens
   `OpIn` to the same — currently it would accept an empty list
   that emits `= ANY('{}')` and never matches. Pinned in tests.)

3. **P2 — NULL members in `ini`.** **Accept.** Mirror `OpIn`
   exactly: when `extractInValues` reports `hasNull`, emit
   `(lower(expr) = ANY($n::text[]) OR expr IS NULL)`. In practice
   unreachable through the GraphQL `[String!]` surface, but parity
   in the emitter is cheap.

4. **P2 — `lower()` collation and IMMUTABLE.** **Accept with
   adaptation.** Emit `lower((expr) COLLATE "C")` so the expression
   is IMMUTABLE under any database collation and indexable in the
   follow-up index. Matches the plan's already-stated ASCII-only
   semantics. Equivalent to the Go-side `strings.ToLower` only on
   ASCII inputs — documented as a known limitation alongside the
   existing Turkish-İ note.

5. **P1 — refuse for more lexicon types.** **Reject** (see "Conflict
   resolved up front"). Expose everywhere except `DIDFilterInput`.

6. **nit — field-name injection test.** **Accept.** One unit test
   asserting `ValidateFieldName` runs before SQL emission for the
   new operators.

7. **nit — planner narrowing confirmation.** **Informational, no
   action.** Already captured in plan's Open Questions and the
   performance section.

8. **nit — `[]string` round-trip.** **Accept.** Unit test asserts
   the parameter slice is `[]string`, not `[]interface{}`.

9. **nit — pre-lower in Go and store on the FieldFilter.** **Accept.**
   `Validate()` lower-cases the value in place; the emitter
   binds the already-lowered string. Eliminates ToLower-vs-Postgres-
   lower drift for ASCII; combined with item 4 makes the contract
   "ASCII-fold on both sides" end-to-end.

10. **nit — `neqi` out-of-scope note.** **Accept.** Added to
    Out-of-scope.

## R2 — GraphQL schema / API ergonomics

1. **P1 — document the `-i` suffix convention.** **Accept.**
   `StringFilterInput`'s type-level description gets a sentence
   explaining the `-i` suffix is the going-forward convention for
   case-insensitive variants. AGENTS.md filter section gets the
   same note (so a future operator like `neqi` doesn't get
   bikeshedded).

2. **P1 — pin field descriptions verbatim.** **Accept.** Adopted
   R2's suggested text with minor edits to reflect the
   `COLLATE "C"` ASCII-fold contract and the all-types exposure
   (i.e. no "Not accepted for cid / cid-link" clause; instead a
   "no-op for content hashes" sentence). Final wording is in the
   plan.

3. **P1 — don't over-refuse per-type.** **Accept** (see conflict
   resolution).

4. **Confirmed — operator descriptions surface in introspection.**
   Plan's Open Question #2 closed.

5. **Confirmed — backwards compat with one CI-diff caveat.**
   **Accept.** CHANGELOG entry includes the "additive; existing
   query shapes unchanged" line so consumers running schema-diff
   gates see this is informational.

6. **P2 — touch AGENTS.md filter section.** **Accept.** Folded
   into item 1.

7. **P2 — don't split `StringFilterInput`.** **Accept** — matches
   the plan's chosen approach.

8. **nit — pin `DIDFilterInput` no-case-insensitive contract.**
   **Accept.** One sentence appended to `DIDFilterInput.Description`.

9. **nit — close open question #1 (refuse vs expose-as-noop).**
   **Closed via conflict resolution: expose-as-noop.**

## R3 — test coverage

1. **P1 — `_and` and nested composition.** **Accept.** Test plan
   now includes one `_and` test mixing `eqi` with `eq` on another
   field, one `_or`-inside-`_and` nested test, and one max-depth
   boundary test pinning `MaxFilterDepth`.

2. **P1 — `eq`/`in` regression coverage.** **Accept.** Two
   explicit regression assertions: `eq:"project"` does not match
   `"Project"`; `in:["project"]` does not match `"Project"`. Pins
   acceptance criterion 6 to runnable code.

3. **P1 — `MaxInListSize` boundary for `ini`.** **Accept.**
   N=50 (matches) and N+1=51 (rejected) tests added, mirroring
   the existing `MaxArrayContributorScan` boundary pattern.

4. **P1 — SQL safety adversarial table.** **Accept.** Concrete
   table: `'; DROP TABLE record; --`, `"`, `\`, `%`, `_`, embedded
   NUL, leading/trailing whitespace. Asserts (a) SQL string
   contains none of the payload bytes; (b) bound parameter equals
   `strings.ToLower(input)`; (c) SQL contains literal
   `lower((json->>'<field>') COLLATE "C")`.

5. **P2 — empty-string and whitespace policy.** **Accept.** Pinned:
   `""` matches a record whose field is literally empty string and
   nothing else; `" project "` (whitespace) does NOT match
   `"project"` (no implicit trim); NUL-containing values are
   accepted at validation and passed through verbatim (Postgres
   text type rejects them at write time — out of our hands).

6. **P2 — `ini` edge cases pinned.** **Accept with R1 item 2
   override.** Empty `ini: []` rejected at validation (mirrors
   `in`). Single-element behaves identically to `eqi`. Duplicates
   accepted (Postgres `ANY` handles). Mixed casing in the array
   is collapsed via server-side lowercasing in `Validate()`.

7. **P2 — error message content asserted.** **Accept.** Substring
   assertions on the refusal errors (item 2 above for empty list,
   the IsJSON-false guard message, the field-name injection
   message). Each error mentions the cap or the alternative
   operator.

8. **P2 — `eqi: 42` type mismatch.** **Accept.** Unit test on
   `Validate()` covers `OpEqi` with a non-string value.

9. **P2 — builder-level exposure test.** **Adapted accept.**
   Because of the conflict-resolution decision (expose everywhere),
   the builder test becomes simpler: assert `eqi`/`ini` appear on
   any `StringFilterInput`-typed property in a real lexicon's
   generated `WhereInput`, and assert they do NOT appear on
   `DIDFilterInput`-typed fields (`did`, `contributor`, `subject`).

10. **P1 — schema/runtime mismatch for `cid` fields.** **Closed
    via conflict resolution** — no per-type refusal, no mismatch.

11. **nit — Unicode no-fold pinning.** **Accept.** One unit test:
    record with Cyrillic `р` (U+0440), filter `eqi:"p"` (Latin
    U+0070). Result: no match. Pins that we do not perform
    Unicode confusable folding.

12. **nit — `neqi` parity test.** **Accept.** One test pins
    `neq:"project"` continues case-sensitive behaviour so the
    absence of `neqi` reads as deliberate, not an oversight.

---

## Net effect on the plan

The plan has gained:

- A `COLLATE "C"` clause in the emitted SQL.
- A `len ≥ 1` validation on `ini` (and tightened on `in`).
- Server-side lowercasing in `Validate()` so the emitter binds
  pre-lowered strings; `OpEqi`/`OpIni` carry no extra logic in
  the emitter beyond the SQL shape.
- No per-type refusal; instead descriptive guidance in the
  operator descriptions and a one-line addition to
  `DIDFilterInput.Description`.
- Verbatim field descriptions, pinned.
- Expanded test plan reflecting items above and the boundary /
  regression / SQL-safety / composition coverage R3 raised.
- AGENTS.md gets the `-i` suffix convention note.
- CHANGELOG entry calls out "additive; existing query shapes
  unchanged" for schema-diff consumers.

No follow-up review round required — every accepted item is
small and well-scoped; we'll see them all again at implementation
review.
