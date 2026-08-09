"""Fixtures for repository integration tests (BE-002).

These tests need a real PostgreSQL instance with the BE-002 migration
already applied -- they never touch a fake/in-memory substitute, because the
thing under test (role separation, CHECK constraints, FK behavior, numeric
precision round-tripping) only exists in the real database engine
(python-backend-standards.md §9 explicitly allows/expects this for
integration tests, as distinct from the *unit* tests in test_database.py/
test_schema.py, which must not touch a real DB).

Configuration is read from the same environment variables documented in
backend/.env.example:

  * BACKEND_TEST_ADMIN_DATABASE_URL -- unrestricted connection used only to
    prepare/reset fixture data between tests.
  * BACKEND_MIGRATION_DATABASE_URL  -- xr_core_owner role, used to insert
    fixture rows that satisfy the "owner created it, runtime can only DML
    it" story realistically instead of always inserting as an unrestricted
    superuser.
  * BACKEND_DATABASE_URL            -- xr_core_runtime role, the DSN the
    three repository implementations under test actually use.

If BACKEND_TEST_ADMIN_DATABASE_URL is unset or the database is unreachable,
every test in this package is skipped with a clear reason rather than
failing -- this environment may not have Docker/Postgres available (see the
BE-002 report for whether that was the case here).
"""

from __future__ import annotations

from collections.abc import Iterator

import pytest
from sqlalchemy import text
from sqlalchemy.engine import Engine

from repository import database
from repository.schema import metadata

ADMIN_DSN_ENV_VAR = "BACKEND_TEST_ADMIN_DATABASE_URL"


def _admin_dsn() -> str | None:
    import os

    return os.environ.get(ADMIN_DSN_ENV_VAR) or None


def _postgres_reachable(dsn: str) -> bool:
    try:
        engine = database.build_sync_engine(dsn, pool=database.PoolSettings(min_size=1, max_size=1))
        with engine.connect() as conn:
            conn.execute(text("SELECT 1"))
        engine.dispose()
        return True
    except Exception:
        return False


def _skip_reason() -> str | None:
    dsn = _admin_dsn()
    if dsn is None:
        return (
            f"{ADMIN_DSN_ENV_VAR} is not set -- repository integration tests need a real "
            "PostgreSQL instance with the BE-002 migration applied (see backend/compose.yaml "
            "and backend/.env.example). Skipping rather than faking the database, per "
            "python-backend-standards.md §9."
        )
    if not _postgres_reachable(dsn):
        return f"{ADMIN_DSN_ENV_VAR} is set but the database at that DSN is not reachable."
    return None


@pytest.fixture(scope="session")
def _require_postgres() -> None:
    reason = _skip_reason()
    if reason is not None:
        pytest.skip(reason)


@pytest.fixture(scope="session")
def owner_engine(_require_postgres: None) -> Iterator[Engine]:
    """Engine connected as ``xr_core_owner`` (DDL rights, used only by fixtures here)."""
    engine = database.migration_engine_from_env()
    yield engine
    engine.dispose()


@pytest.fixture(scope="session")
def runtime_engine(_require_postgres: None) -> Iterator[Engine]:
    """Engine connected as ``xr_core_runtime`` -- what the repositories under test use."""
    engine = database.runtime_engine_from_env()
    yield engine
    engine.dispose()


@pytest.fixture(autouse=True)
def _clean_tables(_require_postgres: None, owner_engine: Engine) -> Iterator[None]:
    """Truncate all core.* tables before each test so tests don't leak state."""
    yield
    with owner_engine.begin() as conn:
        table_names = ", ".join(
            f"{metadata.schema}.{t.name}" for t in reversed(metadata.sorted_tables)
        )
        conn.execute(text(f"TRUNCATE TABLE {table_names} CASCADE"))
