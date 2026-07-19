#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MARKET_DIR="$ROOT_DIR/market-info-service"
STATE_DIR="${XR_DEV_STATE_DIR:-$ROOT_DIR/.run/dev-services}"
MARKET_PID_FILE="$STATE_DIR/market-info.pid"
WEB_PID_FILE="$STATE_DIR/web.pid"
MARKET_LOG_FILE="$STATE_DIR/market-info.log"
WEB_LOG_FILE="$STATE_DIR/web.log"
MARKET_BINARY="$MARKET_DIR/bin/market-info"
COMPOSE_ENV_FILE="$MARKET_DIR/.env.example"

log() {
  printf '[xr-dev] %s\n' "$*"
}

fail() {
  printf '[xr-dev] ERROR: %s\n' "$*" >&2
  exit 1
}

load_env_file() {
  local file="$1" line key value
  [[ -f "$file" ]] || return 0
  while IFS= read -r line || [[ -n "$line" ]]; do
    line="${line#"${line%%[![:space:]]*}"}"
    [[ -z "$line" || "${line:0:1}" == "#" || "$line" != *"="* ]] && continue
    key="${line%%=*}"
    value="${line#*=}"
    [[ "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue
    if [[ -z "${!key+x}" ]]; then
      if [[ "$value" == \"*\" && "$value" == *\" ]]; then
        value="${value:1:${#value}-2}"
      elif [[ "$value" == \'*\' && "$value" == *\' ]]; then
        value="${value:1:${#value}-2}"
      fi
      printf -v "$key" '%s' "$value"
      export "$key"
    fi
  done < "$file"
}

# Precedence: current shell > repository local > service local > checked-in example.
load_env_file "$ROOT_DIR/.env.local"
load_env_file "$MARKET_DIR/.env.local"
load_env_file "$COMPOSE_ENV_FILE"

export MARKET_INFO_SERVICE_URL="${MARKET_INFO_SERVICE_URL:-http://127.0.0.1:8090}"
export MARKET_INFO_READ_BEARER_TOKEN="${MARKET_INFO_READ_BEARER_TOKEN:-${MARKET_INFO_ADMIN_BEARER_TOKEN:-}}"
export MARKET_INFO_MANAGE_BEARER_TOKEN="${MARKET_INFO_MANAGE_BEARER_TOKEN:-${MARKET_INFO_ADMIN_BEARER_TOKEN:-}}"
export PORT="${XR_WEB_PORT:-${PORT:-8080}}"

MARKET_PORT="${XR_MARKET_INFO_PORT:-8090}"
MARKET_HEALTH_URL="${XR_MARKET_INFO_HEALTH_URL:-http://127.0.0.1:${MARKET_PORT}/healthz}"
WEB_HEALTH_URL="${XR_WEB_HEALTH_URL:-http://127.0.0.1:${PORT}/api/health}"

compose() {
  (cd "$MARKET_DIR" && docker compose --env-file "$COMPOSE_ENV_FILE" "$@")
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "缺少命令：$1"
}

pid_matches() {
  local pid="$1" expected="$2" command
  [[ "$pid" =~ ^[0-9]+$ ]] || return 1
  kill -0 "$pid" 2>/dev/null || return 1
  command="$(ps -p "$pid" -o command= 2>/dev/null || true)"
  [[ "$command" == *"$expected"* ]]
}

managed_pid() {
  local pid_file="$1" expected="$2" pid
  [[ -f "$pid_file" ]] || return 1
  pid="$(<"$pid_file")"
  if pid_matches "$pid" "$expected"; then
    printf '%s\n' "$pid"
    return 0
  fi
  rm -f "$pid_file"
  return 1
}

port_pid() {
  lsof -nP -tiTCP:"$1" -sTCP:LISTEN 2>/dev/null | head -n 1
}

ensure_port_available() {
  local port="$1" label="$2" pid
  pid="$(port_pid "$port" || true)"
  [[ -z "$pid" ]] || fail "$label 无法启动：端口 $port 已被未纳管进程 PID $pid 占用"
}

wait_http() {
  local label="$1" url="$2" attempts="${3:-40}" i
  for ((i = 1; i <= attempts; i += 1)); do
    if curl --fail --silent --show-error --max-time 1 "$url" >/dev/null 2>&1; then
      log "$label 已就绪：$url"
      return 0
    fi
    sleep 0.5
  done
  return 1
}

ensure_docker() {
  require_command docker
  if docker info >/dev/null 2>&1; then
    return 0
  fi
  require_command colima
  log "Docker 尚未就绪，正在启动 Colima"
  colima start --cpu "${COLIMA_CPU:-2}" --memory "${COLIMA_MEMORY:-4}"
  docker info >/dev/null 2>&1 || fail "Colima 已启动，但 Docker daemon 仍不可用"
}

wait_postgres() {
  local i
  for ((i = 1; i <= 40; i += 1)); do
    if compose exec -T postgres pg_isready -U "${POSTGRES_USER:-postgres}" -d "${POSTGRES_DB:-xr_trading}" >/dev/null 2>&1; then
      log "PostgreSQL 已就绪"
      return 0
    fi
    sleep 0.5
  done
  fail "PostgreSQL 未在预期时间内就绪，请检查 docker compose logs postgres"
}

start_postgres() {
  ensure_docker
  log "启动 PostgreSQL 容器"
  compose up -d postgres
  wait_postgres
}

run_migrations() {
  require_command go
  log "执行 PostgreSQL migration"
  (cd "$MARKET_DIR" && go run ./cmd/market-info-migrate)
}

start_market_info() {
  local pid
  if pid="$(managed_pid "$MARKET_PID_FILE" "$MARKET_BINARY")"; then
    log "市场资讯服务已运行（PID $pid）"
    return 0
  fi
  ensure_port_available "$MARKET_PORT" "市场资讯服务"
  require_command go
  mkdir -p "$STATE_DIR" "$MARKET_DIR/bin"
  log "构建并启动市场资讯服务"
  (cd "$MARKET_DIR" && go build -o "$MARKET_BINARY" ./cmd/market-info)
  (
    cd "$MARKET_DIR"
    nohup "$MARKET_BINARY" >>"$MARKET_LOG_FILE" 2>&1 &
    printf '%s\n' "$!" >"$MARKET_PID_FILE"
  )
  if ! wait_http "市场资讯服务" "$MARKET_HEALTH_URL"; then
    stop_managed "市场资讯服务" "$MARKET_PID_FILE" "$MARKET_BINARY"
    fail "市场资讯服务启动失败，请查看 $MARKET_LOG_FILE"
  fi
}

start_web() {
  local pid
  if pid="$(managed_pid "$WEB_PID_FILE" "backend/app.py")"; then
    log "XR-Trading Web 已运行（PID $pid）"
    return 0
  fi
  ensure_port_available "$PORT" "XR-Trading Web"
  require_command python3
  mkdir -p "$STATE_DIR"
  log "启动 XR-Trading Web"
  (
    cd "$ROOT_DIR"
    nohup python3 backend/app.py >>"$WEB_LOG_FILE" 2>&1 &
    printf '%s\n' "$!" >"$WEB_PID_FILE"
  )
  if ! wait_http "XR-Trading Web" "$WEB_HEALTH_URL"; then
    stop_managed "XR-Trading Web" "$WEB_PID_FILE" "backend/app.py"
    fail "XR-Trading Web 启动失败，请查看 $WEB_LOG_FILE"
  fi
}

stop_managed() {
  local label="$1" pid_file="$2" expected="$3" pid i
  if ! pid="$(managed_pid "$pid_file" "$expected")"; then
    log "$label 未由本脚本运行"
    return 0
  fi
  log "停止 ${label}（PID ${pid}）"
  kill -TERM "$pid"
  for ((i = 1; i <= 50; i += 1)); do
    if ! kill -0 "$pid" 2>/dev/null; then
      rm -f "$pid_file"
      return 0
    fi
    sleep 0.2
  done
  log "$label 未及时退出，发送 KILL"
  kill -KILL "$pid" 2>/dev/null || true
  rm -f "$pid_file"
}

start_all() {
  start_postgres
  run_migrations
  start_market_info
  start_web
  log "全部服务已启动：http://127.0.0.1:${PORT}/"
}

stop_all() {
  local stop_colima="${1:-false}"
  stop_managed "XR-Trading Web" "$WEB_PID_FILE" "backend/app.py"
  stop_managed "市场资讯服务" "$MARKET_PID_FILE" "$MARKET_BINARY"
  if docker info >/dev/null 2>&1; then
    log "停止 PostgreSQL 容器"
    compose stop postgres
  else
    log "Docker daemon 未运行，跳过 PostgreSQL"
  fi
  if [[ "$stop_colima" == "true" ]]; then
    if command -v colima >/dev/null 2>&1 && colima status >/dev/null 2>&1; then
      log "停止 Colima"
      colima stop
    else
      log "Colima 未运行"
    fi
  fi
  log "项目服务已停止"
}

service_status() {
  local label="$1" pid_file="$2" expected="$3" port="$4" pid
  if pid="$(managed_pid "$pid_file" "$expected")"; then
    printf '%-22s running  PID %s（脚本纳管）\n' "$label" "$pid"
    return 0
  fi
  pid="$(port_pid "$port" || true)"
  if [[ -n "$pid" ]]; then
    printf '%-22s running  PID %s（未纳管，端口 %s）\n' "$label" "$pid" "$port"
  else
    printf '%-22s stopped\n' "$label"
  fi
}

show_status() {
  local postgres_status="stopped" colima_status="not installed"
  service_status "XR-Trading Web" "$WEB_PID_FILE" "backend/app.py" "$PORT"
  service_status "Market Information" "$MARKET_PID_FILE" "$MARKET_BINARY" "$MARKET_PORT"
  if docker info >/dev/null 2>&1; then
    if compose ps --status running --services 2>/dev/null | grep -qx postgres; then
      postgres_status="running（Docker Compose）"
    fi
  else
    postgres_status="Docker daemon stopped"
  fi
  if command -v colima >/dev/null 2>&1; then
    if colima status >/dev/null 2>&1; then
      colima_status="running"
    else
      colima_status="stopped"
    fi
  fi
  printf '%-22s %s\n' "PostgreSQL" "$postgres_status"
  printf '%-22s %s\n' "Colima" "$colima_status"
  printf '%-22s %s\n' "日志目录" "$STATE_DIR"
}

show_logs() {
  mkdir -p "$STATE_DIR"
  touch "$MARKET_LOG_FILE" "$WEB_LOG_FILE"
  tail -n 80 "$MARKET_LOG_FILE" "$WEB_LOG_FILE"
}

usage() {
  cat <<'EOF'
用法：scripts/dev-services.sh <command> [option]

命令：
  start                 启动 Colima（如有必要）、PostgreSQL、migration、Go 服务和 Web
  stop                  停止 Web、Go 服务和 PostgreSQL；保留 Colima
  stop --with-colima    停止以上服务，并停止 Colima 虚拟机
  restart               重启项目服务；保留 Colima
  restart --with-colima 重启前同时重启 Colima
  status                查看脚本纳管进程、端口、PostgreSQL 和 Colima 状态
  logs                  显示 Web 和 Go 服务最近 80 行日志

配置优先级：当前 shell > .env.local > market-info-service/.env.local > .env.example
本地覆盖文件已被 Git 忽略。可在 .env.local 配置 XR_TRADING_SUBSCRIPTION_MANAGERS 和 XR_TRADING_INGESTION_MANAGERS。
EOF
}

command="${1:-status}"
option="${2:-}"
case "$command" in
  start)
    [[ -z "$option" ]] || fail "start 不接受参数；Colima 不可用时会自动启动"
    start_all
    ;;
  stop)
    [[ -z "$option" || "$option" == "--with-colima" ]] || fail "未知参数：$option"
    stop_all "$([[ "$option" == "--with-colima" ]] && printf true || printf false)"
    ;;
  restart)
    [[ -z "$option" || "$option" == "--with-colima" ]] || fail "未知参数：$option"
    stop_all "$([[ "$option" == "--with-colima" ]] && printf true || printf false)"
    start_all
    ;;
  status)
    [[ -z "$option" ]] || fail "status 不接受参数"
    show_status
    ;;
  logs)
    [[ -z "$option" ]] || fail "logs 不接受参数"
    show_logs
    ;;
  help|-h|--help)
    usage
    ;;
  *)
    usage >&2
    exit 2
    ;;
esac
