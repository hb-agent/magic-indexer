# Plan review — round 1

Three reviewers ran in parallel on the 8-proposal plan:
- **A** — correctness + test coverage
- **B** — build, imports, types, cross-package effects
- **C** — external surface area + operational risk

## Decisions

### Accepted as-is

- **#1 Delete `database.Row`/`Rows`** — A & B both confirmed zero callers, no import fallout.
- **#2 Delete `GetByCollectionWithLabelFilterAndKeysetCursor` + 6 tests** — A confirmed `records_filter_test.go` covers the same label-filter paths via `TestGetByCollectionFiltered_AuthorsWithLabelInclude` / `_AuthorsWithLabelExclude`. Wrapper is a literal 1-line forward.
- **#3 Delete `/xrpc/*` placeholder** — A & B accept. C initially flagged a possible conflict with a notifications XRPC endpoint, but verification showed the notifications endpoint mounts at `/notifications/graphql` (not under `/xrpc/*`); no real XRPC is served and the placeholder is genuinely dead.
- **#4 Inline 3 single-call helpers** — A confirmed `escapeHTML` is character-set-identical to `html.EscapeString` (both escape `<>&'"`). B noted: must also remove `import "strings"` and add `import "html"` in `graphiql.go`. C: no security regression since the page title is a server-controlled config value rendered in `<title>` text context.

### Accepted with modifications

- **#5 Collapse `RecordHooks`** — B caught that `RecordHook` is a struct (not a function type), so collapsing to `RecordHook RecordHook` would create a field-shadows-type ergonomics issue. **Adjustment**: rename the field to `Hook` and make it a pointer (`*RecordHook`) so the nil check is unambiguous. The existing loop's `Policy == HookAbortTx` and `Name` per-hook attributes are preserved.

- **#6 Unify comma-list parsing** — A noted that the two `AllowedOrigins` parsers keep empty elements (no per-element filter), while `parseDIDs`, `TapCollectionFilters`, and `atproto.ParseCollections` all drop empties. C confirmed CORS treats `nil` and `[]string{}` identically (`len(cfg.AllowedOrigins) == 0` triggers "allow all"). **Adjustment**: `config.SplitCSV` drops empties uniformly — an empty CORS entry is never useful and the change is observationally equivalent for the only consumer.

- **#7 Unify PLCDirectoryURL plumbing** — A & B both flagged that the entire override mechanism is dead in production: `ConfigRepository.SetPLCDirectoryOverride` is called once (main.go:280), `GetPLCDirectoryURL` has zero production callers (only `config_test.go`), and `plcDirectoryOverride` is read only by `GetPLCDirectoryURL`. **Adjustment**: scope expands to delete `plcDirectoryOverride` field + `SetPLCDirectoryOverride` + `GetPLCDirectoryURL` + the relevant `config_test.go` cases + the main.go:280 caller, on top of unifying the 3 resolver-option construction sites.

- **#8 Archive superseded docs** — C flagged that `AUDIT_REPORT_2026-04-13.md` still has three open items (`F-DEP-001` golang.org/x/crypto CVE-2024-45337, `F-LABELER-001` label signature verification, `F-DOS-001` WebSocket subscription DoS) and is still the authoritative posture doc. A also noted AGENTS.md and RUNBOOK.md link to several of the archive candidates. **Adjustment**: exclude `AUDIT_REPORT_2026-04-13.md` from the move. Archive only `REVIEW-Feb5.md`, `IMPLEMENTATION_PLAN.md`, and `docs/reviews/`. Update AGENTS.md and RUNBOOK.md references in the same commit.

### Rejected (none)

No proposal was rejected outright in this round.

## Skipped findings (not changed)

- "Field-shadows-type" naming under Go's lexical rules is legal but reads poorly — addressed by the `Hook` rename above.
- The DID-prefix lint rule was previously flagged by the upstream complexity review as "speculative"; that was incorrect (the rule cleanly bans only the bare `"did:"` literal, never matched the method-discriminator usages). Not changed.
- `didWebHost` manual prefix slicing in `cmd/hypergoat/main.go:1471-1477` — stylistic, not complexity. Skipped.

## Implementation order

Independent commits, in this order (least cross-cutting first):

1. `refactor(database): drop unused Row/Rows wrappers`
2. `refactor(records): drop deprecated label-only keyset wrapper and its tests`
3. `refactor(server): drop /xrpc 501 placeholder`
4. `refactor(server): inline single-use helpers (DPoP nonce, PAR URI, escapeHTML)`
5. `refactor(ingestion): collapse RecordHooks slice to single optional Hook`
6. `refactor(config): centralize comma-list parsing as SplitCSV`
7. `refactor(oauth): unify DIDResolver construction; drop unused PLC override path`
8. `docs: archive superseded reviews + IMPLEMENTATION_PLAN to docs/archive/`
