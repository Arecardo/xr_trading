"""Unit tests for repository.database (BE-002).

No real network/DB access -- per python-backend-standards.md §9. Covers
fail-closed behavior on missing env vars and the DSN options-splitting
helper the async engine builder depends on.
"""

from __future__ import annotations

import pytest

from repository import database


class TestRequireEnv:
    def test_returns_value_when_set(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("SOME_VAR", "value")
        assert database.require_env("SOME_VAR") == "value"

    def test_raises_when_unset(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.delenv("SOME_VAR", raising=False)
        with pytest.raises(database.DatabaseConfigError, match="SOME_VAR"):
            database.require_env("SOME_VAR")

    def test_raises_when_blank(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv("SOME_VAR", "")
        with pytest.raises(database.DatabaseConfigError, match="SOME_VAR"):
            database.require_env("SOME_VAR")


class TestRuntimeAndMigrationDsnFromEnv:
    def test_runtime_engine_fails_closed_when_unset(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.delenv(database.RUNTIME_DSN_ENV_VAR, raising=False)
        with pytest.raises(database.DatabaseConfigError, match=database.RUNTIME_DSN_ENV_VAR):
            database.runtime_engine_from_env()

    def test_migration_engine_fails_closed_when_unset(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        monkeypatch.delenv(database.MIGRATION_DSN_ENV_VAR, raising=False)
        with pytest.raises(database.DatabaseConfigError, match=database.MIGRATION_DSN_ENV_VAR):
            database.migration_engine_from_env()

    def test_async_runtime_engine_fails_closed_when_unset(
        self, monkeypatch: pytest.MonkeyPatch
    ) -> None:
        monkeypatch.delenv(database.ASYNC_RUNTIME_DSN_ENV_VAR, raising=False)
        with pytest.raises(database.DatabaseConfigError, match=database.ASYNC_RUNTIME_DSN_ENV_VAR):
            database.async_runtime_engine_from_env()


class TestLoadPoolSettings:
    def test_defaults(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.delenv(database.MIN_CONNS_ENV_VAR, raising=False)
        monkeypatch.delenv(database.MAX_CONNS_ENV_VAR, raising=False)
        settings = database.load_pool_settings()
        assert settings.min_size == 1
        assert settings.max_size == 8

    def test_reads_env_vars(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv(database.MIN_CONNS_ENV_VAR, "2")
        monkeypatch.setenv(database.MAX_CONNS_ENV_VAR, "16")
        settings = database.load_pool_settings()
        assert settings.min_size == 2
        assert settings.max_size == 16

    @pytest.mark.parametrize(
        ("min_conns", "max_conns"),
        [
            ("-1", "8"),  # negative min
            ("8", "0"),  # max below 1
            ("8", "4"),  # min > max
        ],
    )
    def test_rejects_invalid_bounds(
        self, monkeypatch: pytest.MonkeyPatch, min_conns: str, max_conns: str
    ) -> None:
        monkeypatch.setenv(database.MIN_CONNS_ENV_VAR, min_conns)
        monkeypatch.setenv(database.MAX_CONNS_ENV_VAR, max_conns)
        with pytest.raises(database.DatabaseConfigError):
            database.load_pool_settings()

    def test_rejects_non_numeric(self, monkeypatch: pytest.MonkeyPatch) -> None:
        monkeypatch.setenv(database.MIN_CONNS_ENV_VAR, "not-a-number")
        with pytest.raises(database.DatabaseConfigError):
            database.load_pool_settings()


class TestSplitOptions:
    def test_extracts_options_and_strips_from_dsn(self) -> None:
        dsn = "postgresql://user:pass@host:5432/db?options=-c%20role%3Dxr_core_runtime&sslmode=disable"
        stripped, options = database._split_options(dsn)
        assert options == "-c role=xr_core_runtime"
        assert "options" not in stripped
        assert "sslmode=disable" in stripped

    def test_no_options_present(self) -> None:
        dsn = "postgresql://user:pass@host:5432/db"
        stripped, options = database._split_options(dsn)
        assert options is None
        assert stripped == dsn

    def test_only_options_present(self) -> None:
        dsn = "postgresql://user:pass@host:5432/db?options=-c%20role%3Dxr_core_owner"
        stripped, options = database._split_options(dsn)
        assert options == "-c role=xr_core_owner"
        assert stripped == "postgresql://user:pass@host:5432/db"


class TestBuildEngines:
    def test_build_sync_engine_does_not_connect(self) -> None:
        # Engine construction must not eagerly connect (lazy pool) -- this
        # runs without a live Postgres instance.
        engine = database.build_sync_engine(
            "postgresql+psycopg://user:pass@localhost:5999/db",
            pool=database.PoolSettings(min_size=1, max_size=2),
        )
        assert engine.pool is not None
        engine.dispose()

    def test_build_async_engine_strips_options_into_connect_args(self) -> None:
        engine = database.build_async_engine(
            "postgresql+asyncpg://user:pass@localhost:5999/db?options=-c%20role%3Dxr_core_runtime",
            pool=database.PoolSettings(min_size=1, max_size=2),
        )
        assert "options" not in str(engine.url)
