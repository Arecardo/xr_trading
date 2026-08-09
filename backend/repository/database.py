"""Database configuration and connection pooling (BE-002, CONTRACT-004).

Fail-closed posture (security-standards.md §4, python-backend-standards.md
§7): every DSN is read from an environment variable with no built-in
default; a missing/blank variable raises ``DatabaseConfigError`` at startup
rather than silently falling back to a predictable local connection string.

Sync vs. async, and why both exist here
----------------------------------------
CONTRACT-001's repository Protocols (``AssetRepository``, ``PortfolioRepository``,
``AccountBindingRepository`` -- doc/technical/implementation_plan/
01_contracts_and_foundation.md §0.1) declare *synchronous* methods, e.g.::

    class AssetRepository(Protocol):
        def get(self, asset_id: str) -> Asset: ...

Those signatures are frozen; this task does not have license to change them.
So the three concrete repository implementations in this package
(``assets.py``, ``portfolios.py``, ``accounts.py``) run on
``build_sync_engine()`` (the ``psycopg`` v3 driver, plain SQLAlchemy
``Engine``/``QueuePool``).

Separately, BE-002 was also asked to add an async Postgres driver and stand
up an async connection pool ahead of BE-003/BE-004, which will likely want
native async DB access from FastAPI request handlers. ``build_async_engine()``
(the ``asyncpg`` driver, SQLAlchemy ``AsyncEngine``) provides that pool. It
is not wired into the three CONTRACT-001 repositories in this task -- see
the BE-002 report for this as an explicit open question (should CONTRACT-001
be amended to async in a follow-up contract change?).

Role switching
--------------
Both engines connect as a shared login user (``BACKEND_POSTGRES_USER``, a
local superuser/admin in dev) and then switch to a narrower NOLOGIN role for
the duration of the session, exactly like market-info-service does via pgx
(see market-info-service/compose.yaml): the DSN carries
``?options=-c%20role%3D<role>``, which both ``psycopg`` and ``asyncpg``
honor -- but through different mechanisms. ``psycopg`` (via the libpq C
library) parses ``options`` directly out of the DSN. SQLAlchemy's asyncpg
dialect does not forward a DSN ``options`` query parameter to
``asyncpg.connect()`` (confirmed empirically: it passes it through as an
unrecognized ``connect()`` keyword argument, which asyncpg rejects with
``TypeError: connect() got an unexpected keyword argument 'options'``).
``_split_options`` below extracts ``options`` from the DSN and re-injects it
via ``connect_args={"server_settings": {...}}``, which asyncpg does accept.
"""

from __future__ import annotations

import os
from dataclasses import dataclass
from urllib.parse import parse_qsl, urlencode, urlsplit, urlunsplit

from sqlalchemy import create_engine
from sqlalchemy.engine import Engine
from sqlalchemy.ext.asyncio import AsyncEngine, create_async_engine

RUNTIME_DSN_ENV_VAR = "BACKEND_DATABASE_URL"
ASYNC_RUNTIME_DSN_ENV_VAR = "BACKEND_ASYNC_DATABASE_URL"
MIGRATION_DSN_ENV_VAR = "BACKEND_MIGRATION_DATABASE_URL"
MIN_CONNS_ENV_VAR = "BACKEND_DB_MIN_CONNS"
MAX_CONNS_ENV_VAR = "BACKEND_DB_MAX_CONNS"


class DatabaseConfigError(RuntimeError):
    """Raised when required database configuration is missing or invalid.

    Fail-closed: callers must not catch this and substitute a default
    connection -- an absent credential means the process should not start.
    """


def require_env(name: str) -> str:
    """Return ``os.environ[name]``, raising ``DatabaseConfigError`` if unset/blank."""
    value = os.environ.get(name)
    if not value:
        raise DatabaseConfigError(
            f"{name} is not set; refusing to start with an implicit/default database "
            "connection (security-standards.md §4: fail closed on missing credentials)."
        )
    return value


@dataclass(frozen=True)
class PoolSettings:
    """Connection pool sizing, read from environment variables."""

    min_size: int
    max_size: int


def load_pool_settings() -> PoolSettings:
    """Read pool bounds from ``BACKEND_DB_MIN_CONNS``/``BACKEND_DB_MAX_CONNS``.

    Defaults (1/8) mirror market-info-service's
    ``MARKET_INFO_DB_MIN_CONNS``/``MARKET_INFO_DB_MAX_CONNS``. Raises
    ``DatabaseConfigError`` for a non-numeric or inverted range rather than
    silently clamping -- a misconfigured pool should fail at startup, not
    degrade at request time.
    """
    raw_min = os.environ.get(MIN_CONNS_ENV_VAR, "1")
    raw_max = os.environ.get(MAX_CONNS_ENV_VAR, "8")
    try:
        min_size = int(raw_min)
        max_size = int(raw_max)
    except ValueError as exc:
        raise DatabaseConfigError(
            f"{MIN_CONNS_ENV_VAR}/{MAX_CONNS_ENV_VAR} must be integers, got {raw_min!r}/{raw_max!r}"
        ) from exc
    if min_size < 0 or max_size < 1 or min_size > max_size:
        raise DatabaseConfigError(
            f"invalid pool bounds: {MIN_CONNS_ENV_VAR}={min_size}, "
            f"{MAX_CONNS_ENV_VAR}={max_size} (require 0 <= min <= max, max >= 1)"
        )
    return PoolSettings(min_size=min_size, max_size=max_size)


def _split_options(dsn: str) -> tuple[str, str | None]:
    """Split a ``?options=...`` query parameter out of a DSN.

    Returns ``(dsn_without_options, options_value_or_None)``. See the module
    docstring for why the async engine needs this and the sync engine does
    not.
    """
    parts = urlsplit(dsn)
    query_pairs = parse_qsl(parts.query, keep_blank_values=True)
    remaining = [(k, v) for k, v in query_pairs if k != "options"]
    options_values = [v for k, v in query_pairs if k == "options"]
    options = options_values[-1] if options_values else None
    stripped = urlunsplit(
        (parts.scheme, parts.netloc, parts.path, urlencode(remaining), parts.fragment)
    )
    return stripped, options


def build_sync_engine(dsn: str, *, pool: PoolSettings | None = None) -> Engine:
    """Build a synchronous SQLAlchemy engine/pool (``psycopg`` v3 driver).

    Used by the CONTRACT-001 repository implementations -- see the module
    docstring for why this is sync rather than async.
    """
    pool = pool or load_pool_settings()
    return create_engine(
        dsn,
        pool_size=max(pool.min_size, 1),
        max_overflow=max(pool.max_size - pool.min_size, 0),
        pool_pre_ping=True,
        future=True,
    )


def build_async_engine(dsn: str, *, pool: PoolSettings | None = None) -> AsyncEngine:
    """Build an async SQLAlchemy engine/pool (``asyncpg`` driver).

    General-purpose infrastructure for future async consumers (BE-003/
    BE-004, FastAPI request handlers); not used by the three CONTRACT-001
    repositories in this task. See the module docstring for the role-switch
    DSN handling this requires.
    """
    pool = pool or load_pool_settings()
    stripped_dsn, options = _split_options(dsn)
    connect_args: dict[str, object] = {}
    if options:
        connect_args["server_settings"] = {"options": options}
    return create_async_engine(
        stripped_dsn,
        pool_size=max(pool.min_size, 1),
        max_overflow=max(pool.max_size - pool.min_size, 0),
        pool_pre_ping=True,
        connect_args=connect_args,
        future=True,
    )


def runtime_engine_from_env() -> Engine:
    """Build the sync runtime engine from ``BACKEND_DATABASE_URL`` (fail-closed)."""
    return build_sync_engine(require_env(RUNTIME_DSN_ENV_VAR))


def async_runtime_engine_from_env() -> AsyncEngine:
    """Build the async runtime engine from ``BACKEND_ASYNC_DATABASE_URL`` (fail-closed)."""
    return build_async_engine(require_env(ASYNC_RUNTIME_DSN_ENV_VAR))


def migration_engine_from_env() -> Engine:
    """Build the sync migration/owner engine from ``BACKEND_MIGRATION_DATABASE_URL``."""
    return build_sync_engine(require_env(MIGRATION_DSN_ENV_VAR))


__all__ = [
    "RUNTIME_DSN_ENV_VAR",
    "ASYNC_RUNTIME_DSN_ENV_VAR",
    "MIGRATION_DSN_ENV_VAR",
    "MIN_CONNS_ENV_VAR",
    "MAX_CONNS_ENV_VAR",
    "DatabaseConfigError",
    "PoolSettings",
    "require_env",
    "load_pool_settings",
    "build_sync_engine",
    "build_async_engine",
    "runtime_engine_from_env",
    "async_runtime_engine_from_env",
    "migration_engine_from_env",
]
