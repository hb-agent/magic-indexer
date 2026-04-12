#!/usr/bin/env bash
# setup-env.sh — create a fresh .env from .env.example with a real
# SECRET_KEY_BASE filled in. Idempotent: refuses to overwrite an
# existing .env so you can't clobber a working config by accident.
#
# Usage:
#   ./scripts/setup-env.sh
#
# Or via the Makefile:
#   make setup
set -euo pipefail

cd "$(dirname "$0")/.."

if [[ -f .env ]]; then
  echo ".env already exists; not overwriting. Delete it first if you want a fresh copy." >&2
  exit 1
fi

if [[ ! -f .env.example ]]; then
  echo ".env.example is missing; cannot template a .env from it." >&2
  exit 1
fi

if ! command -v openssl >/dev/null 2>&1; then
  echo "openssl is required to generate a secret; install it and re-run." >&2
  exit 1
fi

secret=$(openssl rand -base64 64 | tr -d '\n' | head -c 88)
placeholder='CHANGE_ME_TO_A_RANDOM_64_CHARACTER_STRING_USE_OPENSSL_RAND'

# Use awk so we don't rely on GNU-vs-BSD sed in-place semantics.
awk -v secret="$secret" -v placeholder="$placeholder" '
  $0 ~ placeholder { sub(placeholder, secret); print; next }
  { print }
' .env.example > .env

chmod 600 .env
echo "Wrote .env with a freshly generated SECRET_KEY_BASE."
echo "Edit it to set DATABASE_URL, LABELER_DIDS, ADMIN_DIDS, and ADMIN_API_KEY as needed."
