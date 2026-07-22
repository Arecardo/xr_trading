#!/usr/bin/env bash

set -euo pipefail

SERVICE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${MARKET_INFO_COMPOSE_ENV_FILE:-$SERVICE_DIR/.env.example}"
POSTGRES_USER="${POSTGRES_USER:-postgres}"
POSTGRES_DB="${POSTGRES_DB:-xr_trading}"
TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
OUTPUT="${1:-$SERVICE_DIR/backups/${POSTGRES_DB}-${TIMESTAMP}.dump}"

compose() {
  if [[ -n "${MARKET_INFO_COMPOSE_PROJECT:-}" ]]; then
    docker compose --project-name "$MARKET_INFO_COMPOSE_PROJECT" --project-directory "$SERVICE_DIR" --env-file "$ENV_FILE" "$@"
  else
    docker compose --project-directory "$SERVICE_DIR" --env-file "$ENV_FILE" "$@"
  fi
}

checksum() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

command -v docker >/dev/null 2>&1 || { printf 'docker is required\n' >&2; exit 1; }
[[ "$OUTPUT" != "-" ]] || { printf 'backup output must be a file path\n' >&2; exit 1; }
mkdir -p "$(dirname "$OUTPUT")"
OUTPUT_DIR="$(cd "$(dirname "$OUTPUT")" && pwd)"
OUTPUT="$OUTPUT_DIR/$(basename "$OUTPUT")"
PARTIAL="$OUTPUT.partial"
trap 'rm -f "$PARTIAL"' EXIT

compose exec -T postgres pg_isready -U "$POSTGRES_USER" -d "$POSTGRES_DB" >/dev/null
compose exec -T postgres pg_dump \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
  --format custom \
  --compress 6 \
  --no-password >"$PARTIAL"

[[ -s "$PARTIAL" ]] || { printf 'backup archive is empty\n' >&2; exit 1; }
mv "$PARTIAL" "$OUTPUT"
checksum "$OUTPUT" >"$OUTPUT.sha256"
trap - EXIT
printf 'backup=%s\nchecksum=%s\n' "$OUTPUT" "$OUTPUT.sha256"
