#!/usr/bin/env python3
"""XR-Trading portfolio research platform API server."""

from __future__ import annotations

import hashlib
import hmac
import json
import os
import re
import secrets
import sqlite3
from datetime import datetime, timezone
from decimal import Decimal, InvalidOperation
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import parse_qs, unquote, urlencode, urlparse
from urllib.request import Request, urlopen


ROOT = Path(__file__).resolve().parents[1]
FRONTEND_DIR = ROOT / "frontend"
DATA_DIR = ROOT / "data" / "app"
DB_PATH = Path(os.environ.get("XR_TRADING_DB", DATA_DIR / "xr_trading.sqlite3"))
PBKDF2_ITERATIONS = 210_000

ASSET_TYPES = {"STOCK", "ETF", "CRYPTO", "CASH"}
PORTFOLIO_STATUSES = {"draft", "active", "paused", "archived"}
EXECUTION_MODES = {"research", "backtest", "paper", "live"}
RISK_LEVELS = {"conservative", "moderate", "growth", "aggressive"}
MEMBER_STATUSES = {"candidate", "approved", "restricted"}
PERMISSION_OPERATIONS_READ = "operations.read"
PERMISSION_SUBSCRIPTIONS_MANAGE = "subscriptions.manage"
PERMISSION_INGESTION_MANAGE = "ingestion.manage"
COLLECTION_SUBSCRIPTIONS_PATH = "/api/market-info/v1/collection-subscriptions"
COLLECTION_SUBSCRIPTION_ITEM_PATTERN = re.compile(
    r"^/api/market-info/v1/collection-subscriptions/[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
)
INGESTION_RUNS_PATH = "/api/market-info/v1/ingestion-runs"
INGESTION_TASKS_PATH = "/api/market-info/v1/ingestion-tasks"
INGESTION_BACKFILL_PATH = f"{INGESTION_RUNS_PATH}/backfill"
INGESTION_RUN_ITEM_PATTERN = re.compile(
    r"^/api/market-info/v1/ingestion-runs/[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
)
INGESTION_TASK_ITEM_PATTERN = re.compile(
    r"^/api/market-info/v1/ingestion-tasks/[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$"
)
INGESTION_TASK_COMMAND_PATTERN = re.compile(
    r"^/api/market-info/v1/ingestion-tasks/[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}/(?:retry|cancel)$"
)
MARKET_INFO_PUBLIC_QUERY_PATHS = {
    "/api/market-info/v1/instruments",
    "/api/market-info/v1/quotes/latest",
    "/api/market-info/v1/bars",
}


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def json_response(
    handler: BaseHTTPRequestHandler,
    status: int,
    payload: dict[str, Any],
    headers: dict[str, str] | None = None,
) -> None:
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json; charset=utf-8")
    handler.send_header("Content-Length", str(len(body)))
    for name, value in (headers or {}).items():
        if value:
            handler.send_header(name, value)
    handler.end_headers()
    handler.wfile.write(body)


class AppError(Exception):
    def __init__(self, status: int, message: str):
        self.status = status
        self.message = message
        super().__init__(message)


class MarketInfoClient:
    """Server-side credential boundary for the market information service."""

    def __init__(self, base_url: str, read_bearer_token: str, manage_bearer_token: str = "", timeout: float = 3.0):
        self.base_url = base_url.rstrip("/")
        self.read_bearer_token = read_bearer_token
        self.manage_bearer_token = manage_bearer_token
        self.timeout = timeout

    def provider_status(self) -> tuple[int, dict[str, Any], str]:
        return self.request("GET", "/api/market-info/v1/providers/status")

    def request(
        self,
        method: str,
        path: str,
        query: str = "",
        payload: dict[str, Any] | None = None,
        manage: bool = False,
    ) -> tuple[int, dict[str, Any], str]:
        bearer_token = self.manage_bearer_token if manage else self.read_bearer_token
        if not self.base_url or not bearer_token:
            return self._unavailable("市场资讯服务代理尚未配置")
        target = f"{self.base_url}{path}"
        if query:
            target += f"?{query}"
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8") if payload is not None else None
        headers = {"Accept": "application/json", "Authorization": f"Bearer {bearer_token}"}
        if body is not None:
            headers["Content-Type"] = "application/json"
        request = Request(
            target,
            data=body,
            method=method,
            headers=headers,
        )
        try:
            with urlopen(request, timeout=self.timeout) as response:  # noqa: S310 - trusted operator configuration
                return int(response.status), self._decode(response.read()), response.headers.get("X-Request-ID", "")
        except HTTPError as exc:
            return int(exc.code), self._decode(exc.read()), exc.headers.get("X-Request-ID", "")
        except (URLError, TimeoutError, OSError):
            return self._unavailable("市场资讯服务暂时不可用")

    @staticmethod
    def _decode(body: bytes) -> dict[str, Any]:
        try:
            payload = json.loads(body.decode("utf-8"))
        except (UnicodeDecodeError, json.JSONDecodeError) as exc:
            raise AppError(HTTPStatus.BAD_GATEWAY, "市场资讯服务返回了无效响应") from exc
        if not isinstance(payload, dict):
            raise AppError(HTTPStatus.BAD_GATEWAY, "市场资讯服务返回了无效响应")
        return payload

    @staticmethod
    def _unavailable(message: str) -> tuple[int, dict[str, Any], str]:
        return HTTPStatus.SERVICE_UNAVAILABLE, {
            "error": {
                "code": "MARKET_INFO_UNAVAILABLE",
                "message": message,
                "retryable": True,
                "details": {},
                "request_id": "",
            }
        }, ""


def user_matches_allowlist(user: dict[str, Any], raw_allowlist: str) -> bool:
    managers = {
        value.strip().lower()
        for value in raw_allowlist.split(",")
        if value.strip()
    }
    identities = {str(user.get("id", "")).lower(), str(user.get("username", "")).lower(), str(user.get("email", "")).lower()}
    return bool(managers.intersection(identities))


def permissions_for_user(user: dict[str, Any]) -> list[str]:
    permissions = [PERMISSION_OPERATIONS_READ]
    subscription_managers = os.environ.get("XR_TRADING_SUBSCRIPTION_MANAGERS", "")
    if user_matches_allowlist(user, subscription_managers):
        permissions.append(PERMISSION_SUBSCRIPTIONS_MANAGE)
    ingestion_managers = os.environ.get("XR_TRADING_INGESTION_MANAGERS", subscription_managers)
    if user_matches_allowlist(user, ingestion_managers):
        permissions.append(PERMISSION_INGESTION_MANAGE)
    return permissions


def user_with_permissions(user: dict[str, Any]) -> dict[str, Any]:
    return {**user, "permissions": permissions_for_user(user)}


def market_info_permission_denied(message: str = "当前研究账户没有订阅管理权限") -> tuple[int, dict[str, Any], str]:
    return HTTPStatus.FORBIDDEN, {
        "error": {
            "code": "PERMISSION_DENIED",
            "message": message,
            "retryable": False,
            "details": {},
            "request_id": "",
        }
    }, ""


def is_ingestion_query_path(method: str, path: str) -> bool:
    return method == "GET" and (
        path in {INGESTION_RUNS_PATH, INGESTION_TASKS_PATH}
        or INGESTION_RUN_ITEM_PATTERN.fullmatch(path) is not None
        or INGESTION_TASK_ITEM_PATTERN.fullmatch(path) is not None
    )


def is_ingestion_command_path(method: str, path: str) -> bool:
    return method == "POST" and (
        path == INGESTION_BACKFILL_PATH
        or INGESTION_TASK_COMMAND_PATTERN.fullmatch(path) is not None
    )


def is_market_info_public_query_path(method: str, path: str) -> bool:
    return method == "GET" and path in MARKET_INFO_PUBLIC_QUERY_PATHS


class Database:
    def __init__(self, path: Path):
        self.path = path
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.init_schema()

    def connect(self) -> sqlite3.Connection:
        conn = sqlite3.connect(self.path)
        conn.row_factory = sqlite3.Row
        conn.execute("PRAGMA foreign_keys = ON")
        return conn

    def init_schema(self) -> None:
        with self.connect() as conn:
            conn.executescript(
                """
                CREATE TABLE IF NOT EXISTS users (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    username TEXT NOT NULL UNIQUE,
                    email TEXT NOT NULL UNIQUE,
                    password_hash TEXT NOT NULL,
                    status TEXT NOT NULL DEFAULT 'active',
                    created_at TEXT NOT NULL,
                    updated_at TEXT NOT NULL
                );

                CREATE TABLE IF NOT EXISTS sessions (
                    token TEXT PRIMARY KEY,
                    user_id INTEGER NOT NULL,
                    created_at TEXT NOT NULL,
                    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
                );

                CREATE TABLE IF NOT EXISTS stock_pools (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    user_id INTEGER NOT NULL,
                    name TEXT NOT NULL,
                    theme TEXT NOT NULL DEFAULT '',
                    description TEXT NOT NULL DEFAULT '',
                    status TEXT NOT NULL DEFAULT 'active',
                    created_at TEXT NOT NULL,
                    updated_at TEXT NOT NULL,
                    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
                    UNIQUE (user_id, name)
                );

                CREATE TABLE IF NOT EXISTS assets (
                    id TEXT PRIMARY KEY,
                    asset_type TEXT NOT NULL,
                    symbol TEXT NOT NULL,
                    name TEXT NOT NULL,
                    venue TEXT NOT NULL DEFAULT '',
                    quote_currency TEXT NOT NULL,
                    trading_status TEXT NOT NULL DEFAULT 'watch',
                    metadata_json TEXT NOT NULL DEFAULT '{}',
                    created_at TEXT NOT NULL,
                    updated_at TEXT NOT NULL,
                    UNIQUE (asset_type, venue, symbol)
                );

                CREATE TABLE IF NOT EXISTS portfolios (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    user_id INTEGER NOT NULL,
                    legacy_stock_pool_id INTEGER UNIQUE,
                    name TEXT NOT NULL,
                    description TEXT NOT NULL DEFAULT '',
                    base_currency TEXT NOT NULL DEFAULT 'USD',
                    benchmark TEXT NOT NULL DEFAULT 'QQQ',
                    risk_level TEXT NOT NULL DEFAULT 'moderate',
                    execution_mode TEXT NOT NULL DEFAULT 'research',
                    allowed_asset_types_json TEXT NOT NULL DEFAULT '["STOCK","ETF","CRYPTO","CASH"]',
                    status TEXT NOT NULL DEFAULT 'draft',
                    created_at TEXT NOT NULL,
                    updated_at TEXT NOT NULL,
                    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
                    FOREIGN KEY (legacy_stock_pool_id) REFERENCES stock_pools(id) ON DELETE SET NULL,
                    UNIQUE (user_id, name)
                );

                CREATE TABLE IF NOT EXISTS portfolio_members (
                    portfolio_id INTEGER NOT NULL,
                    asset_id TEXT NOT NULL,
                    member_status TEXT NOT NULL DEFAULT 'candidate',
                    target_weight_min TEXT NOT NULL DEFAULT '0',
                    target_weight_max TEXT NOT NULL DEFAULT '0',
                    priority TEXT NOT NULL DEFAULT 'medium',
                    added_reason TEXT NOT NULL DEFAULT '',
                    note TEXT NOT NULL DEFAULT '',
                    created_at TEXT NOT NULL,
                    updated_at TEXT NOT NULL,
                    PRIMARY KEY (portfolio_id, asset_id),
                    FOREIGN KEY (portfolio_id) REFERENCES portfolios(id) ON DELETE CASCADE,
                    FOREIGN KEY (asset_id) REFERENCES assets(id) ON DELETE RESTRICT
                );

                CREATE TABLE IF NOT EXISTS strategy_assignments (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    portfolio_id INTEGER NOT NULL,
                    strategy_key TEXT NOT NULL,
                    strategy_version TEXT NOT NULL,
                    allocation_pct TEXT NOT NULL DEFAULT '1',
                    status TEXT NOT NULL DEFAULT 'active',
                    created_at TEXT NOT NULL,
                    updated_at TEXT NOT NULL,
                    FOREIGN KEY (portfolio_id) REFERENCES portfolios(id) ON DELETE CASCADE,
                    UNIQUE (portfolio_id, strategy_key)
                );

                CREATE TABLE IF NOT EXISTS account_bindings (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    portfolio_id INTEGER NOT NULL,
                    provider TEXT NOT NULL,
                    external_account_id TEXT NOT NULL,
                    environment TEXT NOT NULL,
                    status TEXT NOT NULL DEFAULT 'active',
                    created_at TEXT NOT NULL,
                    updated_at TEXT NOT NULL,
                    FOREIGN KEY (portfolio_id) REFERENCES portfolios(id) ON DELETE CASCADE,
                    UNIQUE (portfolio_id, provider, external_account_id, environment)
                );

                CREATE TABLE IF NOT EXISTS positions (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    portfolio_id INTEGER NOT NULL,
                    account_binding_id INTEGER,
                    asset_id TEXT NOT NULL,
                    quantity TEXT NOT NULL,
                    average_cost TEXT NOT NULL,
                    cost_currency TEXT NOT NULL,
                    as_of TEXT NOT NULL,
                    created_at TEXT NOT NULL,
                    updated_at TEXT NOT NULL,
                    FOREIGN KEY (portfolio_id) REFERENCES portfolios(id) ON DELETE CASCADE,
                    FOREIGN KEY (account_binding_id) REFERENCES account_bindings(id) ON DELETE SET NULL,
                    FOREIGN KEY (asset_id) REFERENCES assets(id) ON DELETE RESTRICT,
                    UNIQUE (portfolio_id, account_binding_id, asset_id)
                );

                CREATE TABLE IF NOT EXISTS portfolio_snapshots (
                    id INTEGER PRIMARY KEY AUTOINCREMENT,
                    portfolio_id INTEGER NOT NULL,
                    as_of TEXT NOT NULL,
                    base_currency TEXT NOT NULL,
                    total_equity TEXT NOT NULL,
                    cash_value TEXT NOT NULL,
                    daily_return TEXT,
                    total_return TEXT,
                    max_drawdown TEXT,
                    created_at TEXT NOT NULL,
                    FOREIGN KEY (portfolio_id) REFERENCES portfolios(id) ON DELETE CASCADE,
                    UNIQUE (portfolio_id, as_of)
                );

                CREATE INDEX IF NOT EXISTS idx_portfolios_user_status ON portfolios(user_id, status);
                CREATE INDEX IF NOT EXISTS idx_members_asset ON portfolio_members(asset_id);
                CREATE INDEX IF NOT EXISTS idx_snapshots_portfolio_as_of ON portfolio_snapshots(portfolio_id, as_of);
                """
            )
            self._seed_assets(conn)
            self._migrate_stock_pools(conn)

    def _seed_assets(self, conn: sqlite3.Connection) -> None:
        timestamp = now_iso()
        assets = [
            ("equity:nasdaq:NVDA", "STOCK", "NVDA", "NVIDIA", "NASDAQ", "USD", "tradable"),
            ("equity:nasdaq:MSFT", "STOCK", "MSFT", "Microsoft", "NASDAQ", "USD", "tradable"),
            ("etf:nasdaq:QQQ", "ETF", "QQQ", "Invesco QQQ Trust", "NASDAQ", "USD", "tradable"),
            ("etf:nyse:SPY", "ETF", "SPY", "SPDR S&P 500 ETF Trust", "NYSE", "USD", "tradable"),
            ("crypto:coinbase:BTC-USD", "CRYPTO", "BTC", "Bitcoin", "COINBASE", "USD", "watch"),
            ("crypto:coinbase:ETH-USD", "CRYPTO", "ETH", "Ethereum", "COINBASE", "USD", "watch"),
            ("cash:USD", "CASH", "USD", "US Dollar", "", "USD", "tradable"),
        ]
        conn.executemany(
            """
            INSERT OR IGNORE INTO assets
                (id, asset_type, symbol, name, venue, quote_currency, trading_status, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
            """,
            [(*asset, timestamp, timestamp) for asset in assets],
        )

    def _migrate_stock_pools(self, conn: sqlite3.Connection) -> None:
        conn.execute(
            """
            INSERT OR IGNORE INTO portfolios
                (user_id, legacy_stock_pool_id, name, description, base_currency, benchmark,
                 risk_level, execution_mode, allowed_asset_types_json, status, created_at, updated_at)
            SELECT user_id, id, name,
                   CASE WHEN theme = '' THEN description
                        WHEN description = '' THEN '历史主题：' || theme
                        ELSE description || '\n历史主题：' || theme END,
                   'USD', 'QQQ', 'moderate', 'research',
                   '["STOCK","ETF"]', 'draft', created_at, updated_at
            FROM stock_pools
            """
        )


class UserService:
    def __init__(self, db: Database):
        self.db = db

    def hash_password(self, password: str) -> str:
        salt = secrets.token_hex(16)
        digest = hashlib.pbkdf2_hmac("sha256", password.encode(), salt.encode(), PBKDF2_ITERATIONS)
        return f"pbkdf2_sha256${PBKDF2_ITERATIONS}${salt}${digest.hex()}"

    def verify_password(self, password: str, encoded: str) -> bool:
        try:
            algorithm, iterations, salt, expected = encoded.split("$", 3)
            if algorithm != "pbkdf2_sha256":
                return False
            digest = hashlib.pbkdf2_hmac("sha256", password.encode(), salt.encode(), int(iterations))
            return hmac.compare_digest(digest.hex(), expected)
        except ValueError:
            return False

    def register(self, username: str, email: str, password: str) -> dict[str, Any]:
        username, email = username.strip(), email.strip().lower()
        if len(username) < 3:
            raise AppError(HTTPStatus.BAD_REQUEST, "用户名至少 3 位")
        if "@" not in email or "." not in email:
            raise AppError(HTTPStatus.BAD_REQUEST, "邮箱格式不正确")
        if len(password) < 8:
            raise AppError(HTTPStatus.BAD_REQUEST, "密码至少 8 位")
        timestamp = now_iso()
        try:
            with self.db.connect() as conn:
                cur = conn.execute(
                    "INSERT INTO users (username, email, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
                    (username, email, self.hash_password(password), timestamp, timestamp),
                )
        except sqlite3.IntegrityError as exc:
            raise AppError(HTTPStatus.CONFLICT, "用户名或邮箱已存在") from exc
        return self.issue_session(int(cur.lastrowid))

    def login(self, identity: str, password: str) -> dict[str, Any]:
        identity = identity.strip().lower()
        with self.db.connect() as conn:
            row = conn.execute(
                "SELECT * FROM users WHERE status = 'active' AND (LOWER(username) = ? OR LOWER(email) = ?)",
                (identity, identity),
            ).fetchone()
        if row is None or not self.verify_password(password, row["password_hash"]):
            raise AppError(HTTPStatus.UNAUTHORIZED, "用户名、邮箱或密码错误")
        return self.issue_session(int(row["id"]))

    def issue_session(self, user_id: int) -> dict[str, Any]:
        token = secrets.token_urlsafe(32)
        with self.db.connect() as conn:
            conn.execute("INSERT INTO sessions (token, user_id, created_at) VALUES (?, ?, ?)", (token, user_id, now_iso()))
        return {"token": token, "user": self.get_user(user_id)}

    def get_user_by_token(self, token: str) -> dict[str, Any]:
        with self.db.connect() as conn:
            row = conn.execute(
                """SELECT users.* FROM sessions JOIN users ON users.id = sessions.user_id
                   WHERE sessions.token = ? AND users.status = 'active'""",
                (token,),
            ).fetchone()
        if row is None:
            raise AppError(HTTPStatus.UNAUTHORIZED, "请先登录")
        return self._public_user(row)

    def get_user(self, user_id: int) -> dict[str, Any]:
        with self.db.connect() as conn:
            row = conn.execute("SELECT * FROM users WHERE id = ?", (user_id,)).fetchone()
        if row is None:
            raise AppError(HTTPStatus.NOT_FOUND, "用户不存在")
        return self._public_user(row)

    def change_password(self, user_id: int, old_password: str, new_password: str) -> None:
        if len(new_password) < 8:
            raise AppError(HTTPStatus.BAD_REQUEST, "新密码至少 8 位")
        with self.db.connect() as conn:
            row = conn.execute("SELECT * FROM users WHERE id = ? AND status = 'active'", (user_id,)).fetchone()
            if row is None or not self.verify_password(old_password, row["password_hash"]):
                raise AppError(HTTPStatus.UNAUTHORIZED, "原密码错误")
            conn.execute("UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?", (self.hash_password(new_password), now_iso(), user_id))
            conn.execute("DELETE FROM sessions WHERE user_id = ?", (user_id,))

    def logout(self, token: str) -> None:
        with self.db.connect() as conn:
            conn.execute("DELETE FROM sessions WHERE token = ?", (token,))

    def deactivate(self, user_id: int) -> None:
        with self.db.connect() as conn:
            conn.execute("UPDATE users SET status = 'deleted', updated_at = ? WHERE id = ?", (now_iso(), user_id))
            conn.execute("DELETE FROM sessions WHERE user_id = ?", (user_id,))

    @staticmethod
    def _public_user(row: sqlite3.Row) -> dict[str, Any]:
        return {key: row[key] for key in ("id", "username", "email", "status", "created_at", "updated_at")}


class AssetService:
    def __init__(self, db: Database):
        self.db = db

    def list_assets(self, query: str = "", asset_type: str = "") -> list[dict[str, Any]]:
        clauses, params = [], []
        if query.strip():
            clauses.append("(LOWER(symbol) LIKE ? OR LOWER(name) LIKE ?)")
            needle = f"%{query.strip().lower()}%"
            params.extend([needle, needle])
        if asset_type.strip():
            normalized = asset_type.strip().upper()
            if normalized not in ASSET_TYPES:
                raise AppError(HTTPStatus.BAD_REQUEST, "不支持的资产类型")
            clauses.append("asset_type = ?")
            params.append(normalized)
        sql = "SELECT * FROM assets"
        if clauses:
            sql += " WHERE " + " AND ".join(clauses)
        sql += " ORDER BY CASE asset_type WHEN 'STOCK' THEN 1 WHEN 'ETF' THEN 2 WHEN 'CRYPTO' THEN 3 ELSE 4 END, symbol"
        with self.db.connect() as conn:
            rows = conn.execute(sql, params).fetchall()
        return [self._to_asset(row) for row in rows]

    def get_asset(self, asset_id: str) -> dict[str, Any]:
        with self.db.connect() as conn:
            row = conn.execute("SELECT * FROM assets WHERE id = ?", (asset_id,)).fetchone()
        if row is None:
            raise AppError(HTTPStatus.NOT_FOUND, "资产不存在")
        return self._to_asset(row)

    @staticmethod
    def _to_asset(row: sqlite3.Row) -> dict[str, Any]:
        return {
            "id": row["id"], "asset_type": row["asset_type"], "symbol": row["symbol"],
            "name": row["name"], "venue": row["venue"], "quote_currency": row["quote_currency"],
            "trading_status": row["trading_status"], "metadata": json.loads(row["metadata_json"] or "{}"),
        }


class PortfolioService:
    def __init__(self, db: Database, assets: AssetService):
        self.db = db
        self.assets = assets

    def list_portfolios(self, user_id: int, include_archived: bool = False) -> list[dict[str, Any]]:
        condition = "" if include_archived else "AND p.status != 'archived'"
        with self.db.connect() as conn:
            rows = conn.execute(
                f"""SELECT p.*, COUNT(pm.asset_id) AS member_count
                    FROM portfolios p LEFT JOIN portfolio_members pm ON pm.portfolio_id = p.id
                    WHERE p.user_id = ? {condition}
                    GROUP BY p.id ORDER BY p.updated_at DESC, p.id DESC""",
                (user_id,),
            ).fetchall()
        return [self._to_portfolio(row) for row in rows]

    def create_portfolio(self, user_id: int, payload: dict[str, Any]) -> dict[str, Any]:
        values = self._validate_payload(payload, creating=True)
        timestamp = now_iso()
        try:
            with self.db.connect() as conn:
                cur = conn.execute(
                    """INSERT INTO portfolios
                       (user_id, name, description, base_currency, benchmark, risk_level, execution_mode,
                        allowed_asset_types_json, status, created_at, updated_at)
                       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
                    (user_id, values["name"], values["description"], values["base_currency"], values["benchmark"],
                     values["risk_level"], values["execution_mode"], json.dumps(values["allowed_asset_types"]),
                     values["status"], timestamp, timestamp),
                )
        except sqlite3.IntegrityError as exc:
            raise AppError(HTTPStatus.CONFLICT, "该投资组合名称已存在") from exc
        return self.get_portfolio(user_id, int(cur.lastrowid))

    def get_portfolio(self, user_id: int, portfolio_id: int) -> dict[str, Any]:
        with self.db.connect() as conn:
            row = conn.execute(
                """SELECT p.*, COUNT(pm.asset_id) AS member_count
                   FROM portfolios p LEFT JOIN portfolio_members pm ON pm.portfolio_id = p.id
                   WHERE p.id = ? AND p.user_id = ? GROUP BY p.id""",
                (portfolio_id, user_id),
            ).fetchone()
        if row is None:
            raise AppError(HTTPStatus.NOT_FOUND, "投资组合不存在")
        return self._to_portfolio(row)

    def update_portfolio(self, user_id: int, portfolio_id: int, payload: dict[str, Any]) -> dict[str, Any]:
        current = self.get_portfolio(user_id, portfolio_id)
        merged = {**current, **payload}
        values = self._validate_payload(merged, creating=False)
        try:
            with self.db.connect() as conn:
                conn.execute(
                    """UPDATE portfolios SET name = ?, description = ?, base_currency = ?, benchmark = ?,
                       risk_level = ?, execution_mode = ?, allowed_asset_types_json = ?, status = ?, updated_at = ?
                       WHERE id = ? AND user_id = ?""",
                    (values["name"], values["description"], values["base_currency"], values["benchmark"],
                     values["risk_level"], values["execution_mode"], json.dumps(values["allowed_asset_types"]),
                     values["status"], now_iso(), portfolio_id, user_id),
                )
        except sqlite3.IntegrityError as exc:
            raise AppError(HTTPStatus.CONFLICT, "该投资组合名称已存在") from exc
        return self.get_portfolio(user_id, portfolio_id)

    def archive_portfolio(self, user_id: int, portfolio_id: int) -> None:
        self.get_portfolio(user_id, portfolio_id)
        with self.db.connect() as conn:
            conn.execute("UPDATE portfolios SET status = 'archived', updated_at = ? WHERE id = ? AND user_id = ?", (now_iso(), portfolio_id, user_id))

    def list_members(self, user_id: int, portfolio_id: int) -> list[dict[str, Any]]:
        self.get_portfolio(user_id, portfolio_id)
        with self.db.connect() as conn:
            rows = conn.execute(
                """SELECT pm.*, a.asset_type, a.symbol, a.name, a.venue, a.quote_currency, a.trading_status
                   FROM portfolio_members pm JOIN assets a ON a.id = pm.asset_id
                   WHERE pm.portfolio_id = ? ORDER BY a.asset_type, a.symbol""",
                (portfolio_id,),
            ).fetchall()
        return [self._to_member(row) for row in rows]

    def add_member(self, user_id: int, portfolio_id: int, payload: dict[str, Any]) -> dict[str, Any]:
        portfolio = self.get_portfolio(user_id, portfolio_id)
        asset_id = str(payload.get("asset_id", "")).strip()
        asset = self.assets.get_asset(asset_id)
        if asset["asset_type"] not in portfolio["allowed_asset_types"]:
            raise AppError(HTTPStatus.BAD_REQUEST, "该资产类型不在组合允许范围内")
        values = self._validate_member(payload)
        timestamp = now_iso()
        try:
            with self.db.connect() as conn:
                conn.execute(
                    """INSERT INTO portfolio_members
                       (portfolio_id, asset_id, member_status, target_weight_min, target_weight_max,
                        priority, added_reason, note, created_at, updated_at)
                       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)""",
                    (portfolio_id, asset_id, values["member_status"], values["target_weight_min"],
                     values["target_weight_max"], values["priority"], values["added_reason"], values["note"],
                     timestamp, timestamp),
                )
        except sqlite3.IntegrityError as exc:
            raise AppError(HTTPStatus.CONFLICT, "该资产已在投资组合中") from exc
        return self.get_member(user_id, portfolio_id, asset_id)

    def get_member(self, user_id: int, portfolio_id: int, asset_id: str) -> dict[str, Any]:
        self.get_portfolio(user_id, portfolio_id)
        with self.db.connect() as conn:
            row = conn.execute(
                """SELECT pm.*, a.asset_type, a.symbol, a.name, a.venue, a.quote_currency, a.trading_status
                   FROM portfolio_members pm JOIN assets a ON a.id = pm.asset_id
                   WHERE pm.portfolio_id = ? AND pm.asset_id = ?""",
                (portfolio_id, asset_id),
            ).fetchone()
        if row is None:
            raise AppError(HTTPStatus.NOT_FOUND, "组合资产不存在")
        return self._to_member(row)

    def update_member(self, user_id: int, portfolio_id: int, asset_id: str, payload: dict[str, Any]) -> dict[str, Any]:
        current = self.get_member(user_id, portfolio_id, asset_id)
        values = self._validate_member({**current, **payload})
        with self.db.connect() as conn:
            conn.execute(
                """UPDATE portfolio_members SET member_status = ?, target_weight_min = ?, target_weight_max = ?,
                   priority = ?, added_reason = ?, note = ?, updated_at = ?
                   WHERE portfolio_id = ? AND asset_id = ?""",
                (values["member_status"], values["target_weight_min"], values["target_weight_max"],
                 values["priority"], values["added_reason"], values["note"], now_iso(), portfolio_id, asset_id),
            )
        return self.get_member(user_id, portfolio_id, asset_id)

    def remove_member(self, user_id: int, portfolio_id: int, asset_id: str) -> None:
        self.get_member(user_id, portfolio_id, asset_id)
        with self.db.connect() as conn:
            held = conn.execute(
                "SELECT 1 FROM positions WHERE portfolio_id = ? AND asset_id = ? AND quantity != '0' LIMIT 1",
                (portfolio_id, asset_id),
            ).fetchone()
            if held:
                raise AppError(HTTPStatus.CONFLICT, "持仓资产不能直接移除，请先完成减仓或退出")
            conn.execute("DELETE FROM portfolio_members WHERE portfolio_id = ? AND asset_id = ?", (portfolio_id, asset_id))

    def _validate_payload(self, payload: dict[str, Any], creating: bool) -> dict[str, Any]:
        name = str(payload.get("name", "")).strip()
        if not name:
            raise AppError(HTTPStatus.BAD_REQUEST, "投资组合名称不能为空")
        if len(name) > 60:
            raise AppError(HTTPStatus.BAD_REQUEST, "投资组合名称不能超过 60 个字符")
        risk_level = str(payload.get("risk_level", "moderate"))
        execution_mode = str(payload.get("execution_mode", "research"))
        status = str(payload.get("status", "draft" if creating else "active"))
        if risk_level not in RISK_LEVELS:
            raise AppError(HTTPStatus.BAD_REQUEST, "风险等级无效")
        if execution_mode not in EXECUTION_MODES:
            raise AppError(HTTPStatus.BAD_REQUEST, "执行模式无效")
        if status not in PORTFOLIO_STATUSES:
            raise AppError(HTTPStatus.BAD_REQUEST, "组合状态无效")
        if execution_mode == "live":
            raise AppError(HTTPStatus.FORBIDDEN, "当前版本未开放实盘组合")
        raw_types = payload.get("allowed_asset_types", list(ASSET_TYPES))
        if isinstance(raw_types, str):
            raw_types = [value.strip() for value in raw_types.split(",") if value.strip()]
        allowed_types = list(dict.fromkeys(str(value).upper() for value in raw_types))
        if not allowed_types or any(value not in ASSET_TYPES for value in allowed_types):
            raise AppError(HTTPStatus.BAD_REQUEST, "允许的资产类型无效")
        return {
            "name": name,
            "description": str(payload.get("description", "")).strip(),
            "base_currency": str(payload.get("base_currency", "USD")).strip().upper() or "USD",
            "benchmark": str(payload.get("benchmark", "QQQ")).strip().upper() or "QQQ",
            "risk_level": risk_level,
            "execution_mode": execution_mode,
            "allowed_asset_types": allowed_types,
            "status": status,
        }

    @staticmethod
    def _validate_member(payload: dict[str, Any]) -> dict[str, str]:
        status = str(payload.get("member_status", "candidate"))
        if status not in MEMBER_STATUSES:
            raise AppError(HTTPStatus.BAD_REQUEST, "组合成员状态无效")
        try:
            weight_min = Decimal(str(payload.get("target_weight_min", "0")))
            weight_max = Decimal(str(payload.get("target_weight_max", "0")))
        except InvalidOperation as exc:
            raise AppError(HTTPStatus.BAD_REQUEST, "目标权重格式错误") from exc
        if weight_min < 0 or weight_max < 0 or weight_min > 1 or weight_max > 1 or weight_min > weight_max:
            raise AppError(HTTPStatus.BAD_REQUEST, "目标权重必须在 0 到 1 之间，且最小值不能大于最大值")
        priority = str(payload.get("priority", "medium"))
        if priority not in {"low", "medium", "high"}:
            raise AppError(HTTPStatus.BAD_REQUEST, "优先级无效")
        return {
            "member_status": status,
            "target_weight_min": str(weight_min),
            "target_weight_max": str(weight_max),
            "priority": priority,
            "added_reason": str(payload.get("added_reason", "")).strip(),
            "note": str(payload.get("note", "")).strip(),
        }

    @staticmethod
    def _to_portfolio(row: sqlite3.Row) -> dict[str, Any]:
        return {
            "id": row["id"], "user_id": row["user_id"], "name": row["name"],
            "description": row["description"], "base_currency": row["base_currency"],
            "benchmark": row["benchmark"], "risk_level": row["risk_level"],
            "execution_mode": row["execution_mode"],
            "allowed_asset_types": json.loads(row["allowed_asset_types_json"]),
            "status": row["status"], "member_count": row["member_count"] if "member_count" in row.keys() else 0,
            "legacy_stock_pool_id": row["legacy_stock_pool_id"],
            "created_at": row["created_at"], "updated_at": row["updated_at"],
        }

    @staticmethod
    def _to_member(row: sqlite3.Row) -> dict[str, Any]:
        return {
            "portfolio_id": row["portfolio_id"], "asset_id": row["asset_id"],
            "asset_type": row["asset_type"], "symbol": row["symbol"], "name": row["name"],
            "venue": row["venue"], "quote_currency": row["quote_currency"],
            "trading_status": row["trading_status"], "member_status": row["member_status"],
            "target_weight_min": row["target_weight_min"], "target_weight_max": row["target_weight_max"],
            "priority": row["priority"], "added_reason": row["added_reason"], "note": row["note"],
        }


db = Database(DB_PATH)
user_service = UserService(db)
asset_service = AssetService(db)
portfolio_service = PortfolioService(db, asset_service)
market_info_client = MarketInfoClient(
    os.environ.get("MARKET_INFO_SERVICE_URL", "http://127.0.0.1:8090"),
    os.environ.get("MARKET_INFO_READ_BEARER_TOKEN", ""),
    os.environ.get("MARKET_INFO_MANAGE_BEARER_TOKEN", os.environ.get("MARKET_INFO_READ_BEARER_TOKEN", "")),
)


class ApiHandler(BaseHTTPRequestHandler):
    server_version = "XRTrading/0.2"

    def log_message(self, fmt: str, *args: Any) -> None:
        print(f"[{now_iso()}] {self.address_string()} {fmt % args}")

    def do_GET(self) -> None:
        self.route()

    def do_POST(self) -> None:
        self.route()

    def do_PATCH(self) -> None:
        self.route()

    def do_DELETE(self) -> None:
        self.route()

    def route(self) -> None:
        parsed = urlparse(self.path)
        try:
            if parsed.path.startswith("/api/"):
                self.handle_api(parsed.path, parse_qs(parsed.query))
            else:
                self.serve_static(parsed.path)
        except AppError as exc:
            json_response(self, int(exc.status), {"error": exc.message})
        except (TypeError, ValueError):
            json_response(self, HTTPStatus.BAD_REQUEST, {"error": "请求参数格式错误"})
        except Exception as exc:  # noqa: BLE001
            print(f"Unhandled error: {exc}")
            json_response(self, HTTPStatus.INTERNAL_SERVER_ERROR, {"error": "服务暂时不可用"})

    def handle_api(self, path: str, query: dict[str, list[str]]) -> None:
        if path == "/api/health" and self.command == "GET":
            json_response(self, HTTPStatus.OK, {"status": "ok", "product": "XR-Trading 量化投资研究平台", "schema_version": "0.2"})
            return
        if path == "/api/auth/register" and self.command == "POST":
            payload = self.read_json()
            result = user_service.register(payload.get("username", ""), payload.get("email", ""), payload.get("password", ""))
            result["user"] = user_with_permissions(result["user"])
            json_response(self, HTTPStatus.CREATED, result)
            return
        if path == "/api/auth/login" and self.command == "POST":
            payload = self.read_json()
            result = user_service.login(payload.get("identity", ""), payload.get("password", ""))
            result["user"] = user_with_permissions(result["user"])
            json_response(self, HTTPStatus.OK, result)
            return
        if path == "/api/auth/logout" and self.command == "POST":
            user_service.logout(self.require_token())
            json_response(self, HTTPStatus.OK, {"ok": True})
            return

        user = user_with_permissions(self.require_user())
        user_id = int(user["id"])
        if is_market_info_public_query_path(self.command, path):
            status, payload, request_id = market_info_client.request(
                "GET", path, query=urlencode(query, doseq=True)
            )
            response_headers = {"X-Request-ID": request_id} if request_id else None
            json_response(self, status, payload, response_headers)
            return
        if path == "/api/market-info/v1/providers/status" and self.command == "GET":
            status, payload, request_id = market_info_client.provider_status()
            response_headers = {"X-Request-ID": request_id} if request_id else None
            json_response(self, status, payload, response_headers)
            return
        if path == COLLECTION_SUBSCRIPTIONS_PATH and self.command in {"GET", "POST"}:
            manage = self.command == "POST"
            if manage and PERMISSION_SUBSCRIPTIONS_MANAGE not in user["permissions"]:
                status, payload, request_id = market_info_permission_denied()
            else:
                request_payload = self.read_json() if manage else None
                status, payload, request_id = market_info_client.request(
                    self.command,
                    path,
                    query=urlencode(query, doseq=True),
                    payload=request_payload,
                    manage=manage,
                )
            response_headers = {"X-Request-ID": request_id} if request_id else None
            json_response(self, status, payload, response_headers)
            return
        if COLLECTION_SUBSCRIPTION_ITEM_PATTERN.fullmatch(path) and self.command == "PATCH":
            if PERMISSION_SUBSCRIPTIONS_MANAGE not in user["permissions"]:
                status, payload, request_id = market_info_permission_denied()
            else:
                status, payload, request_id = market_info_client.request(
                    "PATCH", path, payload=self.read_json(), manage=True
                )
            response_headers = {"X-Request-ID": request_id} if request_id else None
            json_response(self, status, payload, response_headers)
            return
        if is_ingestion_query_path(self.command, path):
            status, payload, request_id = market_info_client.request(
                "GET", path, query=urlencode(query, doseq=True)
            )
            response_headers = {"X-Request-ID": request_id} if request_id else None
            json_response(self, status, payload, response_headers)
            return
        if is_ingestion_command_path(self.command, path):
            if PERMISSION_INGESTION_MANAGE not in user["permissions"]:
                status, payload, request_id = market_info_permission_denied("当前研究账户没有采集操作权限")
            else:
                status, payload, request_id = market_info_client.request(
                    "POST",
                    path,
                    query=urlencode(query, doseq=True),
                    payload=self.read_json(),
                    manage=True,
                )
            response_headers = {"X-Request-ID": request_id} if request_id else None
            json_response(self, status, payload, response_headers)
            return
        if path == "/api/users/me" and self.command == "GET":
            json_response(self, HTTPStatus.OK, {"user": user})
            return
        if path == "/api/users/password" and self.command == "POST":
            payload = self.read_json()
            user_service.change_password(user_id, payload.get("old_password", ""), payload.get("new_password", ""))
            json_response(self, HTTPStatus.OK, {"ok": True, "message": "密码已修改，请重新登录"})
            return
        if path == "/api/users/me" and self.command == "DELETE":
            user_service.deactivate(user_id)
            json_response(self, HTTPStatus.OK, {"ok": True})
            return
        if path == "/api/assets" and self.command == "GET":
            assets = asset_service.list_assets(query.get("q", [""])[0], query.get("type", [""])[0])
            json_response(self, HTTPStatus.OK, {"assets": assets})
            return
        if path == "/api/portfolios" and self.command == "GET":
            include_archived = query.get("include_archived", ["false"])[0].lower() == "true"
            json_response(self, HTTPStatus.OK, {"portfolios": portfolio_service.list_portfolios(user_id, include_archived)})
            return
        if path == "/api/portfolios" and self.command == "POST":
            json_response(self, HTTPStatus.CREATED, {"portfolio": portfolio_service.create_portfolio(user_id, self.read_json())})
            return

        parts = [unquote(part) for part in path.strip("/").split("/")]
        if len(parts) >= 3 and parts[:2] == ["api", "portfolios"]:
            portfolio_id = int(parts[2])
            if len(parts) == 3:
                if self.command == "GET":
                    json_response(self, HTTPStatus.OK, {"portfolio": portfolio_service.get_portfolio(user_id, portfolio_id)})
                    return
                if self.command == "PATCH":
                    json_response(self, HTTPStatus.OK, {"portfolio": portfolio_service.update_portfolio(user_id, portfolio_id, self.read_json())})
                    return
                if self.command == "DELETE":
                    portfolio_service.archive_portfolio(user_id, portfolio_id)
                    json_response(self, HTTPStatus.OK, {"ok": True, "message": "投资组合已归档"})
                    return
            if len(parts) == 4 and parts[3] == "members":
                if self.command == "GET":
                    json_response(self, HTTPStatus.OK, {"members": portfolio_service.list_members(user_id, portfolio_id)})
                    return
                if self.command == "POST":
                    json_response(self, HTTPStatus.CREATED, {"member": portfolio_service.add_member(user_id, portfolio_id, self.read_json())})
                    return
            if len(parts) == 5 and parts[3] == "members":
                asset_id = parts[4]
                if self.command == "PATCH":
                    json_response(self, HTTPStatus.OK, {"member": portfolio_service.update_member(user_id, portfolio_id, asset_id, self.read_json())})
                    return
                if self.command == "DELETE":
                    portfolio_service.remove_member(user_id, portfolio_id, asset_id)
                    json_response(self, HTTPStatus.OK, {"ok": True})
                    return

        # Deprecated compatibility layer: old clients map stock pools to portfolios.
        if path == "/api/stock-pools" and self.command == "GET":
            portfolios = portfolio_service.list_portfolios(user_id)
            pools = [{"id": item["id"], "user_id": item["user_id"], "name": item["name"],
                      "theme": ", ".join(item["allowed_asset_types"]), "description": item["description"],
                      "status": item["status"], "created_at": item["created_at"], "updated_at": item["updated_at"]}
                     for item in portfolios]
            json_response(self, HTTPStatus.OK, {"pools": pools, "deprecated": True, "replacement": "/api/portfolios"})
            return
        if path == "/api/stock-pools" and self.command == "POST":
            payload = self.read_json()
            portfolio = portfolio_service.create_portfolio(user_id, {
                "name": payload.get("name", ""), "description": payload.get("description", ""),
                "allowed_asset_types": ["STOCK", "ETF"], "status": "draft", "execution_mode": "research",
            })
            json_response(self, HTTPStatus.CREATED, {"pool": portfolio, "deprecated": True, "replacement": "/api/portfolios"})
            return
        if path.startswith("/api/stock-pools/") and self.command == "DELETE":
            portfolio_service.archive_portfolio(user_id, int(path.rsplit("/", 1)[-1]))
            json_response(self, HTTPStatus.OK, {"ok": True, "deprecated": True, "replacement": "/api/portfolios"})
            return
        raise AppError(HTTPStatus.NOT_FOUND, "接口不存在")

    def read_json(self) -> dict[str, Any]:
        length = int(self.headers.get("Content-Length", "0"))
        if length == 0:
            return {}
        try:
            payload = json.loads(self.rfile.read(length).decode("utf-8"))
        except json.JSONDecodeError as exc:
            raise AppError(HTTPStatus.BAD_REQUEST, "请求 JSON 格式错误") from exc
        if not isinstance(payload, dict):
            raise AppError(HTTPStatus.BAD_REQUEST, "请求 JSON 必须是对象")
        return payload

    def require_token(self) -> str:
        auth = self.headers.get("Authorization", "")
        if auth.startswith("Bearer "):
            return auth.removeprefix("Bearer ").strip()
        raise AppError(HTTPStatus.UNAUTHORIZED, "请先登录")

    def require_user(self) -> dict[str, Any]:
        return user_service.get_user_by_token(self.require_token())

    def serve_static(self, path: str) -> None:
        clean_path = "/index.html" if path in ("", "/") else path
        target = (FRONTEND_DIR / clean_path.lstrip("/")).resolve()
        if not str(target).startswith(str(FRONTEND_DIR.resolve())) or not target.exists() or target.is_dir():
            target = FRONTEND_DIR / "index.html"
        content = target.read_bytes()
        content_type = self.content_type_for(target)
        self.send_response(HTTPStatus.OK)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(content)))
        self.end_headers()
        self.wfile.write(content)

    @staticmethod
    def content_type_for(path: Path) -> str:
        return {".html": "text/html; charset=utf-8", ".css": "text/css; charset=utf-8", ".js": "application/javascript; charset=utf-8"}.get(path.suffix.lower(), "application/octet-stream")


def main() -> None:
    port = int(os.environ.get("PORT", "8080"))
    server = ThreadingHTTPServer(("127.0.0.1", port), ApiHandler)
    print(f"XR-Trading 量化投资研究平台运行于 http://127.0.0.1:{port}")
    print(f"SQLite database: {DB_PATH}")
    server.serve_forever()


if __name__ == "__main__":
    main()
