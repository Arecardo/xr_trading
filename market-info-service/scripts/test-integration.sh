#!/usr/bin/env bash

set -euo pipefail

SERVICE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
COMPOSE_FILE="$SERVICE_DIR/compose.yaml"
ENV_FILE="${MARKET_INFO_COMPOSE_ENV_FILE:-$SERVICE_DIR/.env.example}"
PROJECT="${MARKET_INFO_INTEGRATION_PROJECT:-xr-market-info-qa-$$}"
CONTROL_DB="market_info_qa_control"
TEST_DB="market_info_qa"
POSTGRES_USER="postgres"
POSTGRES_PASSWORD="qa-integration-local-only"
PACKAGES="${MARKET_INFO_INTEGRATION_PACKAGES:-./internal/database/integration}"
KEEP="${MARKET_INFO_INTEGRATION_KEEP:-0}"

compose() {
  POSTGRES_DB="$CONTROL_DB" \
    POSTGRES_USER="$POSTGRES_USER" \
    POSTGRES_PASSWORD="$POSTGRES_PASSWORD" \
    POSTGRES_PORT=0 \
    docker compose \
      --project-name "$PROJECT" \
      --project-directory "$SERVICE_DIR" \
      --env-file "$ENV_FILE" \
      --file "$COMPOSE_FILE" \
      "$@"
}

cleanup() {
  status=$?
  trap - EXIT
  if [[ "$KEEP" == "1" ]]; then
    printf 'integration PostgreSQL retained for debugging: project=%s database=%s\n' "$PROJECT" "$TEST_DB"
  else
    compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  fi
  exit "$status"
}

command -v docker >/dev/null 2>&1 || { printf 'docker CLI is required\n' >&2; exit 1; }
docker info >/dev/null 2>&1 || { printf 'Docker is not running; start Colima or another Docker runtime\n' >&2; exit 1; }
[[ -f "$ENV_FILE" ]] || { printf 'Compose environment file not found: %s\n' "$ENV_FILE" >&2; exit 1; }

trap cleanup EXIT

printf 'starting isolated PostgreSQL project %s\n' "$PROJECT"
compose up --detach --wait postgres

HOST_ENDPOINT="$(compose port postgres 5432 | head -n 1)"
HOST_PORT="${HOST_ENDPOINT##*:}"
[[ "$HOST_PORT" =~ ^[0-9]+$ ]] || { printf 'cannot determine PostgreSQL host port from %s\n' "$HOST_ENDPOINT" >&2; exit 1; }

compose exec -T postgres createdb --username "$POSTGRES_USER" --template template0 "$TEST_DB"
# Apply every deploy/postgres/init/*.sql script in lexicographic order, the
# same order docker-entrypoint-initdb.d would use on a truly fresh container.
# This must not hardcode just 001: 002 (core catalog seed), 003/004 (FX asset
# type + seed, BE-003a) and any later-numbered init script all seed rows that
# later goose migrations' hardcoded-ID INSERTs depend on via foreign key (see
# 00006_seed_provider_catalog.sql and 00007_seed_coingecko_fx_provider.sql).
for init_file in "$SERVICE_DIR"/deploy/postgres/init/*.sql; do
  printf 'applying core bootstrap script %s\n' "$(basename "$init_file")"
  compose exec -T postgres psql \
    --username "$POSTGRES_USER" \
    --dbname "$TEST_DB" \
    --set ON_ERROR_STOP=1 \
    <"$init_file" \
    >/dev/null
done

ADMIN_URL="postgres://$POSTGRES_USER:$POSTGRES_PASSWORD@127.0.0.1:$HOST_PORT/$TEST_DB?sslmode=disable"
MIGRATION_URL="$ADMIN_URL&options=-c%20role%3Dxr_market_data_owner"
RUNTIME_URL="$ADMIN_URL&options=-c%20role%3Dxr_market_data_runtime"

printf 'applying migrations to isolated database %s\n' "$TEST_DB"
MARKET_INFO_MIGRATION_DATABASE_URL="$MIGRATION_URL" \
  go run ./cmd/market-info-migrate

printf 'running integration packages: %s\n' "$PACKAGES"
MARKET_INFO_DATABASE_URL="$RUNTIME_URL" \
  MARKET_INFO_TEST_DATABASE_URL="$RUNTIME_URL" \
  MARKET_INFO_TEST_ADMIN_DATABASE_URL="$ADMIN_URL" \
  go test -count=1 -tags=integration $PACKAGES

printf 'isolated integration tests passed; cleanup runs automatically\n'
