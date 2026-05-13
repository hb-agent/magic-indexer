#!/usr/bin/env bash
# Guards against re-introduction of prefix-only DID validation.
#
# The strict predicate `internal/atproto/did.IsValid` exists precisely
# because `strings.HasPrefix(s, "did:")` lets newline / control-char
# payloads into log lines, CSV admin lists, and downstream HTTP calls.
# The #64 migration retired every existing call site of the prefix
# check; this guard prevents recurrence.
#
# Exits 0 when no violations are found, 1 otherwise. Wire into
# `make lint` so CI fails on a regression.
#
# The `internal/atproto/did/` package itself is exempt — the predicate
# is implemented there.

set -euo pipefail

# Per-line opt-out marker: `// allow-did-prefix:<reason>`. Use sparingly
# for genuine format-discriminator cases (e.g. "is this URI an at:// or a
# did:?"). Validation cases must always use didpkg.IsValid.
hits=$(grep -rn 'strings\.HasPrefix(.*"did:"' \
  --include='*.go' \
  --exclude-dir=did \
  --exclude-dir=.git \
  --exclude-dir=node_modules \
  --exclude-dir=client \
  internal/ cmd/ 2>/dev/null | grep -v 'allow-did-prefix' || true)

if [ -n "$hits" ]; then
  echo "lint-no-did-prefix: prefix-only DID checks are banned." >&2
  echo "Use internal/atproto/did.IsValid instead. Violations:" >&2
  echo "" >&2
  echo "$hits" >&2
  exit 1
fi
