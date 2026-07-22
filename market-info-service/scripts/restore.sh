#!/usr/bin/env bash

set -euo pipefail

SERVICE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="${MARKET_INFO_COMPOSE_ENV_FILE:-$SERVICE_DIR/.env.example}"
POSTGRES_USER="${POSTGRES_USER:-postgres}"
SOURCE_DB="${POSTGRES_DB:-xr_trading}"
ARCHIVE="${1:-}"
TARGET_DB="${2:-}"
EXPECTED_ASSET_CODE="${3:-}"
EXPECTED_MIGRATION_VERSION="${MARKET_INFO_EXPECTED_MIGRATION_VERSION:-5}"
CREATED=false

usage() {
  printf 'usage: %s <archive.dump> <new_database> [expected_asset_code]\n' "$0" >&2
}

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

cleanup_failed_restore() {
  if [[ "$CREATED" == "true" ]]; then
    compose exec -T postgres dropdb --username "$POSTGRES_USER" --if-exists "$TARGET_DB" >/dev/null 2>&1 || true
  fi
}

[[ -n "$ARCHIVE" && -n "$TARGET_DB" ]] || { usage; exit 2; }
[[ -f "$ARCHIVE" && -s "$ARCHIVE" ]] || { printf 'backup archive is missing or empty: %s\n' "$ARCHIVE" >&2; exit 1; }
[[ "$TARGET_DB" =~ ^[A-Za-z][A-Za-z0-9_]{0,62}$ ]] || { printf 'invalid target database name\n' >&2; exit 1; }
[[ "$TARGET_DB" != "$SOURCE_DB" ]] || { printf 'refusing to restore over source database %s\n' "$SOURCE_DB" >&2; exit 1; }

if [[ -f "$ARCHIVE.sha256" ]]; then
  EXPECTED_CHECKSUM="$(awk 'NR == 1 {print $1}' "$ARCHIVE.sha256")"
  ACTUAL_CHECKSUM="$(checksum "$ARCHIVE")"
  [[ "$EXPECTED_CHECKSUM" == "$ACTUAL_CHECKSUM" ]] || { printf 'backup checksum mismatch\n' >&2; exit 1; }
fi

command -v docker >/dev/null 2>&1 || { printf 'docker is required\n' >&2; exit 1; }
compose exec -T postgres pg_isready -U "$POSTGRES_USER" -d "$SOURCE_DB" >/dev/null
ROLE_COUNT="$(compose exec -T postgres psql --username "$POSTGRES_USER" --dbname "$SOURCE_DB" --tuples-only --no-align --command "SELECT count(*) FROM pg_roles WHERE rolname IN ('xr_core_owner','xr_market_data_owner','xr_market_data_runtime')")"
[[ "$ROLE_COUNT" == "3" ]] || { printf 'required database roles are missing; initialize PostgreSQL first\n' >&2; exit 1; }
EXISTS="$(compose exec -T postgres psql --username "$POSTGRES_USER" --dbname "$SOURCE_DB" --tuples-only --no-align --command "SELECT 1 FROM pg_database WHERE datname = '$TARGET_DB'")"
[[ -z "$EXISTS" ]] || { printf 'target database already exists: %s\n' "$TARGET_DB" >&2; exit 1; }

trap cleanup_failed_restore EXIT
compose exec -T postgres createdb --username "$POSTGRES_USER" "$TARGET_DB"
CREATED=true
compose exec -T postgres pg_restore \
  --username "$POSTGRES_USER" \
  --dbname "$TARGET_DB" \
  --exit-on-error \
  --single-transaction <"$ARCHIVE"

MIGRATION_VERSION="$(compose exec -T postgres psql --username "$POSTGRES_USER" --dbname "$TARGET_DB" --tuples-only --no-align --command "SELECT max(version_id) FROM market_data.schema_migrations WHERE is_applied = true")"
[[ "$MIGRATION_VERSION" == "$EXPECTED_MIGRATION_VERSION" ]] || { printf 'unexpected restored migration version: got %s, want %s\n' "$MIGRATION_VERSION" "$EXPECTED_MIGRATION_VERSION" >&2; exit 1; }
compose exec -T postgres psql --username "$POSTGRES_USER" --dbname "$TARGET_DB" --set ON_ERROR_STOP=1 --command "SET ROLE xr_market_data_runtime; SELECT count(*) FROM core.assets; SELECT count(*) FROM market_data.providers;" >/dev/null
if [[ -n "$EXPECTED_ASSET_CODE" ]]; then
  ASSET_COUNT="$(printf "SELECT count(*) FROM core.assets WHERE code = :'expected_asset';\n" | compose exec -T postgres psql --username "$POSTGRES_USER" --dbname "$TARGET_DB" --tuples-only --no-align --set expected_asset="$EXPECTED_ASSET_CODE")"
  [[ "$ASSET_COUNT" == "1" ]] || { printf 'expected restored asset was not found: %s\n' "$EXPECTED_ASSET_CODE" >&2; exit 1; }
fi

CREATED=false
trap - EXIT
printf 'restored_database=%s\nmigration_version=%s\n' "$TARGET_DB" "$MIGRATION_VERSION"
