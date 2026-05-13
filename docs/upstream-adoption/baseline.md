# Quality-gate baseline (pre-implementation, 2026-05-13)

Captured before any Track B–H code changes land.

## Toolchain note

The dev env is `aarch64` but `go env GOARCH` defaults to `amd64`. Cross-compile via cgo then fails (`gcc: -m64`). For local gates, prefix Go invocations with `GOARCH=arm64 CGO_ENABLED=1`. CI runs linux/amd64 with native gcc.

## Gates

| Gate | Command | Baseline |
|---|---|---|
| build | `GOARCH=arm64 CGO_ENABLED=1 go build ./...` | clean |
| vet | `GOARCH=arm64 CGO_ENABLED=1 go vet ./...` | clean |
| lint | `golangci-lint run ./...` | **0 issues** |
| test (-race) | `GOARCH=arm64 CGO_ENABLED=1 go test -race -count=1 ./...` | DB-dependent tests fail; everything else passes |

## DB-dependent failures (postgres unavailable locally; no docker in this env)

Pre-existing failures, **all** with `connection refused` on `localhost:5432`:

- `internal/database/repositories` (Records + Reports test suites)
- `internal/notifications` (Apply_* tests)

All failures are environmental, not code defects. CI runs against a Postgres service container so the full suite passes there.

## Acceptance for post-implementation gates

- `go build`, `go vet`, `golangci-lint` clean (0 issues).
- `go test -race`: no new failure outside the documented DB-dependent set.
