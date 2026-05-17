# Implementation review — round 2

Three reviewers ran in parallel over the 9 commits between `1d4ff13` and `aeee4d7`:

- **A** — correctness (behavior preservation)
- **B** — test coverage (gaps opened by deletions / migrations)
- **C** — external surface area (HTTP, GraphQL, log, env, docs)

## Verdicts

**A — correctness:** ALL 9 COMMITS PASS. Every refactor preserves observable behavior. The lint-fix commit (`aeee4d7`) correctly resolves the `html` package/variable shadow introduced by `ec8a726`.

**C — external surface:** ZERO UNINTENDED OBSERVABLE CHANGES. The only observable delta is `d1d061d` — `/xrpc/*` now returns chi's 404 instead of the JSON 501 placeholder, which is the intended behavior (we are not an XRPC server, the placeholder was a lie). All other commits are internal-only or update docs.

**B — test coverage:** Three gaps flagged:

1. **`config.SplitCSV` has no unit test** (commit `df338ad`). The helper is exercised transitively via call sites, but direct tests covering the drop-empty + whitespace + edge cases would be worth adding.

2. **`SetPLCDirectoryURL` (DB-row typed setter)** now has no dedicated test (commit `49b0246`). One test that called it was removed alongside the deleted override mechanism. The method is a one-line forward over the generic `Set(ctx, "plc_directory_url", url)`, which IS exercised by `TestConfigRepository_InitializeDefaults` round-trip.

3. **PAR `request_uri` format and GraphiQL HTML title escaping** are not directly unit-tested (commit `ec8a726`). Both are stdlib usage in handler paths.

## Decisions

- **Add a `TestSplitCSV` unit test** covering: empty input, single entry (with and without surrounding whitespace), multiple entries, trailing/leading/interior empty entries, only-commas, only-whitespace, duplicates. Doubles as documentation for the drop-empty semantic.
- **Accept the `SetPLCDirectoryURL` gap.** The method is now a 1-line wrapper over a tested path; adding a dedicated test would mostly be testing `Set`. If the wrapper is removed in a future cleanup, no test needs to change.
- **Accept the PAR / GraphiQL title gaps.** Both predate this PR — neither commit introduced new untested logic; both inlined an existing untested helper. Adding handler-level tests would be useful work, but it is independent of this complexity-reduction PR and would expand scope.

No round 3.
