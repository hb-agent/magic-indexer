# Implementation review — round 1 (post-implementation)

**Commits reviewed**:

  - `5920fc8 docs(case-insensitive-eq): plan + round-1 review trail`
  - `9b93c67 feat(filter): case-insensitive string operators eqi/ini`
  - `039eb1d test(filter): Postgres-backed integration for eqi/ini`

**Reviewers**: SQL/safety (R1), GraphQL/API ergonomics (R2),
test coverage (R3). **Decisions recorded**: 2026-05-16.

A follow-up commit applies every accepted item as one atomic
patch. No further review round needed — accepted items are
small (one wording fix, one ergonomic reorder, several test
additions). The single P0 (R3 #8) is straightforward.

## R1 — SQL / safety

1. **P2 — `asciiToLower` two-pass scan redundancy.** **Reject.**
   The pre-scan exits at the first uppercase byte and returns
   the input unmodified when no work is needed (zero allocation
   for the common case). The second loop re-scanning from index
   0 is intentional and more readable than threading the
   pre-scan index through. Performance-only; the inner Postgres
   `lower()` call dwarfs the Go scan.

2. **P2 — IsNull double-validate.** **Reject (misread).** The
   new `Validate()` call sits AFTER the IsNull `continue` block
   and only runs on non-IsNull filters. IsNull filters still
   only call `Validate()` once at the original site.

3. **P2 — Triple field-name validation.** **Defer.** Field
   name validation runs three times for the recursive path
   (`Validate()` calls `ValidateFieldName`, then
   `buildSingleFilter` calls it again at line 405). All three
   are pure functions over short strings; cost is negligible.
   Skipping the inner one would mean documenting the precondition
   tightly across two call sites — not worth the churn.

4. **P2 — CHANGELOG note for contributor/subject empty IN
   tightening.** **Accept.** One sentence added to the
   "Behaviour change" note clarifying that the `OpIn`
   tightening applies to all FilterKinds, not just KindScalar.

5. **P2 — Absent / non-string `type` field coverage.**
   **Accept.** Combine with R3 #6a/b into a single fixture
   extension.

6. **P2 — Unify `in` and `ini` error message phrasing.**
   **Accept.** Use `"%s list must contain 1 to %d values"` for
   both lower-bound and upper-bound rejections.

7. **nit — OrComposition test wraps OR in artificial outer
   AND.** **Reject.** The wrapping mirrors what the GraphQL
   parser produces (`where: { _or: [...] }` → outer
   `FilterGroup{AND}` with one OR child). Accurate to the
   runtime shape.

8. **nit — `Eqi_RejectsEmptyIni` misnamed.** **Accept.** Rename
   to `Ini_RejectsEmpty`. (It tests OpIni, not OpEqi.)

9–11. **Positive observations.** No action.

12. **nit — Observability gap on new validate-on-recurse path.**
    **Defer.** Errors propagate through the GraphQL resolver
    layer which already logs them at the query-failure site.
    Adding `slog.Warn` here would double-log.

## R2 — GraphQL / API ergonomics

1. **P1 — Misleading "no-op for content hashes / TIDs" in
   `stringEqiDescription`.** **Accept.** R2 is right: the SQL
   runs for these types — the operator is redundant, not a
   no-op. Reword the operator description to say "for
   spec-case-sensitive identifiers (cid, cid-link, lowercase
   TIDs, DID authorities inside at-uri values) prefer `eq` for
   the cheaper, GIN-indexable comparison; `eqi` will still
   evaluate correctly but provides no semantic benefit."
   Also remove the stale "(unused ops are just ignored)"
   comment on the `cid` / `cid-link` switch arm in
   `FilterInputForLexiconType`.

2. **P2 — at-uri footgun.** **Defer.** Routing `at-uri` to a
   dedicated filter input that omits `eqi` is the right
   long-term shape but it's a bigger architectural change
   (would need a new `URIFilterInput`, registry wiring, and
   migration of all at-uri properties). The reworded
   description (item 1 above) closes the immediate
   informational gap. Captured as a follow-up.

3. **P1 — CHANGELOG self-contradiction on schema-diff impact.**
   **Accept.** Add: "Schema-diff tools (Apollo Rover, GraphQL
   Inspector) will NOT flag the `in: []` tightening — it is a
   runtime-only change; consumers must audit call-sites
   manually."

4. **nit — `parseOperator` cases out of order.** **Accept.**
   Move `case "eqi"` directly under `case "eq"`, and
   `case "ini"` directly under `case "in"`.

5. **P2 — Builder test uses stale testdata NSID.** **Defer.**
   Tracked in issue #68. The test still pins the production
   contract (builder generates `eqi`/`ini` on a string-typed
   property of a real lexicon). Refreshing the testdata is
   out of scope.

6. **nit — `ini` vs `isNull` typo collision risk.** **Defer.**
   `parseOperator` silently returns `("", false)` for
   unknown operators today; that's existing parser behaviour.
   Changing it touches every GraphQL filter consumer.

7. **nit — `stringEqiDescription` length.** **Defer.** Apollo
   Studio (primary consumer) shows full descriptions.
   Terminal-CLI truncation is downstream.

8. **nit — DIDFilterInput description should state WHY.**
   **Accept.** Append "(per W3C DID Core §3.1 — case folding
   would change identifier identity)".

9. **nit — AGENTS.md operator ordering note.** **Defer.** The
   existing AGENTS.md text says "future additions should
   follow this shape" — adequate.

10. **nit — `_or` / `_and` composition not pinned at builder
    level.** **Accept.** Extend
    `TestStringFilterInput_CaseInsensitiveOperators_OnGeneratedWhereInput`
    to assert `_or`/`_and` element types expose `eqi`/`ini`.

## R3 — test coverage

1. **P1 — Adversarial test is literal-match only.** **Accept
   with adaptation.** Add a generic `strings.Contains` check
   for each adversarial input — but skip single-byte cases
   that overlap with our literal SQL keywords (the `"` byte is
   in `COLLATE "C"`). The literal-match assertion catches
   shape changes; the per-input substring check catches
   smuggling.

2. **P1 — `TestAsciiToLower` missing boundary bytes.**
   **Accept.** Add `@` (0x40), `[` (0x5B), `` ` `` (0x60),
   `{` (0x7B), digits, an all-256-byte sweep, and a >128-byte
   input.

3. **P2 — `MaxInListSize` boundary doesn't cover `[]string`
   branch.** **Accept.** Parameterize the test over both list
   types.

4. **P1 — `OrComposition` test is count-only.** **Accept.**
   Build the expected URI set and verify membership.

5. **P2 — `MatchesAllCasings` uses `Errorf` not `Fatalf` for
   the count.** **Accept.** Downgrade to `Fatalf` so a 0-result
   regression fails fast.

6. **P1 — Missing-coverage gaps.** **Partial accept.**
   - Absent `type` field: **accept** (combine with R1 #5).
   - Non-string `type` value (numeric, null): **accept**
     (combine with R1 #5).
   - Nested-path `eqi`: **defer.** No testdata lexicon has a
     nested string property with `metadata__type`-style path
     today; the JSON-extractor support is exercised elsewhere.
   - Single-element `ini` equivalence to `eqi`: **accept.**
   - `_and` with two `eqi` on different fields: **accept.**

7. **P1 — `Eq_Regression_CaseSensitive` wouldn't catch a
   `lower()` regression on the containment path.** **Accept.**
   Add directional test: `eq:"Project"` matches r2 *only*
   (not r1 lowercase). That pins case-sensitivity in both
   directions, not just the "no broader match" axis.

8. **P0 — Validate-tightening only tests the empty-list
   branch.** **Accept.** This is the single P0 finding. Add a
   table-driven `TestGetByCollectionFiltered_RecursiveValidate_RejectsBadShapes`
   that hands `BuildFilterGroupClause` each invalid shape
   (oversize `in`, non-scalar element, eqi-on-column, eqi
   with non-string, ini empty list, ini oversize, contains
   under min) and asserts the Validate error surfaces from
   `GetByCollectionFiltered` rather than from a SQL execution
   failure.

9. **P2 — Fixture data leakage risk.** **Defer.**
   `resetBetweenTests` runs per-test; no current leakage. The
   shared `did:plc:alice` / `did:plc:bob` namespace is a
   fixture-hygiene observation, not a bug.

10. **nits.**
    - `uriList` helper position: **accept.** Move to top of
      the case-insensitive test section.
    - Test naming consistency
      (`Eqi_AndDid_ProjectsOfUser` vs `Eqi_OrComposition`):
      **accept.** Rename `Eqi_AndDid_ProjectsOfUser` →
      `Eqi_AndComposition_ProjectsOfUser` for symmetry.
    - `NullElementMirrorsIn` documents-by-side-effect: **defer.**
      The emitter parity has value; the comment already
      explains the unreachability.
    - Description substring brittleness: **defer.** Asserting
      that the description carries the case-insensitivity word
      is a deliberate UX guard.

---

## Follow-up commit shape

One commit, scope tag `fix(filter)`: applies items
- R1 #4, #5, #6, #8
- R2 #1, #3, #4, #8, #10
- R3 #1, #2, #3, #4, #5, #6 (subset), #7, #8, #10 (subset)

Single round. No round-3 needed.
