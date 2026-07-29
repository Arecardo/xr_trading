#!/usr/bin/env bash

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
GITLEAKS="${GITLEAKS_BIN:-gitleaks}"

cd "$ROOT"

python3 -m unittest tests/test_security_policy_check.py
python3 "$ROOT/scripts/security_policy_check.py"

command -v "$GITLEAKS" >/dev/null 2>&1 || {
  printf 'gitleaks is required; install it or set GITLEAKS_BIN\n' >&2
  exit 1
}

if ! git diff --quiet HEAD --; then
  git diff --no-ext-diff --binary HEAD -- |
    "$GITLEAKS" stdin \
      --no-banner \
      --no-color \
      --redact=100 \
      --log-level=error
fi

while IFS= read -r -d '' path; do
  "$GITLEAKS" dir \
    --no-banner \
    --no-color \
    --redact=100 \
    --log-level=error \
    "$ROOT/$path"
done < <(git ls-files --others --exclude-standard -z)

printf 'working tree secret scan passed\n'

"$GITLEAKS" git \
  --no-banner \
  --no-color \
  --redact=100 \
  --verbose \
  --log-opts="--all"

printf 'git history secret scan passed\n'
