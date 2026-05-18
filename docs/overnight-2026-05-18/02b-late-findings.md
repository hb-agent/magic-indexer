# 02b — Late findings

Discoveries during Phase 4 implementation that should have been
in `02-findings.md` but weren't surfaced by the diagnostic pass.

## Triage rules (per directive)

- Critical / high AND safe to fix → fix now.
- Everything else → defer to the operator; do not expand scope at 3am.

## Findings

### L-1: `buildFilteredWhereForCount` is a duplicate, not an extraction (F-2 from phase 6)

**Triage:** DEFER to operator.

**What:** The M9 commit (3ea2c55) introduced
`buildFilteredWhereForCount` claiming it was extracted from
`GetByCollectionFiltered`'s inline WHERE-builder so SELECT and
COUNT stay "in lockstep." In practice, `GetByCollectionFiltered`
was NOT refactored to call the new helper — its original inline
~95-LOC WHERE-builder is unchanged. The new helper is a
shape-identical duplicate.

**Why it's a real concern:** the two copies will drift on the
next filter-axis addition. A new filter implemented in
`GetByCollectionFiltered` won't participate in totalCount, and
the symmetric mistake (only in the helper) won't show up in
SELECTs. The drift won't be caught by current tests because
they exercise the SELECT path and the COUNT path independently,
not in a "they emit the same SQL" cross-check.

**Why deferred:** the proper fix is to refactor
`GetByCollectionFiltered` to use `buildFilteredWhereForCount`
and then layer on its SELECT-only concerns (keyset cursor,
ORDER BY) afterward. That touches the hottest query path in
the codebase and warrants a careful PR of its own — not a 3am
patch.

**Compensating control (landed in commit "chore(repo):
correct CountByCollectionFiltered comments"):** the helper's
search-emission no longer fires its own metric, so at least
that one observable double-count is out. A more durable
drift-detection test would walk the two implementations and
assert they produce identical SQL for the same FilterGroup —
worth landing as part of the proper extraction PR.

**Owner:** operator. Suggested next step: open an issue
titled "Refactor: GetByCollectionFiltered to use
buildFilteredWhereForCount (extract not duplicate)" with this
finding's body pasted in.
