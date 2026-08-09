"""Integration tests for SqlAssetRepository against a real PostgreSQL instance.

Requires BACKEND_TEST_ADMIN_DATABASE_URL to be set and reachable -- see
conftest.py. Exercises the repository through ``runtime_engine`` (the
xr_core_runtime role, the same one the application uses), while fixture
rows are written through ``owner_engine`` to mirror how the schema is
actually populated (migrations/admin tooling own writes to xr_core_owner-
level operations; here we just need *some* writer for setup).
"""

from __future__ import annotations

import pytest
from sqlalchemy import insert
from sqlalchemy.engine import Engine

from repository.assets import SqlAssetRepository
from repository.errors import NotFoundError
from repository.schema import assets as assets_table


def _insert_asset(engine: Engine, **overrides: object) -> dict[str, object]:
    row: dict[str, object] = {
        "asset_id": "equity:nasdaq:NVDA",
        "asset_type": "STOCK",
        "symbol": "NVDA",
        "venue": "nasdaq",
        "quote_currency": "USD",
        "provider_symbols": {"longbridge": "NVDA.US"},
        "trading_status": "tradable",
    }
    row.update(overrides)
    with engine.begin() as conn:
        conn.execute(insert(assets_table).values(**row))
    return row


class TestGet:
    def test_returns_matching_asset(self, owner_engine: Engine, runtime_engine: Engine) -> None:
        _insert_asset(owner_engine)
        repo = SqlAssetRepository(runtime_engine)

        asset = repo.get("equity:nasdaq:NVDA")

        assert asset.asset_id == "equity:nasdaq:NVDA"
        assert asset.asset_type == "STOCK"
        assert asset.symbol == "NVDA"
        assert asset.venue == "nasdaq"
        assert asset.quote_currency == "USD"
        assert asset.provider_symbols == {"longbridge": "NVDA.US"}
        assert asset.trading_status == "tradable"

    def test_two_segment_cash_asset_id_is_accepted(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        # CONTRACT-001 §0.2 uses "cash:USD" (two segments) as an asset_id
        # example, while CONTRACT-004's literal wording says "type:venue:
        # symbol" (three segments) -- see repository/schema.py's
        # ck_assets_asset_id_format comment. This test locks in the
        # permissive interpretation this implementation chose.
        _insert_asset(
            owner_engine,
            asset_id="cash:USD",
            asset_type="CASH",
            symbol="USD",
            venue="cash",
            quote_currency="USD",
            provider_symbols={},
            trading_status="tradable",
        )
        repo = SqlAssetRepository(runtime_engine)

        asset = repo.get("cash:USD")

        assert asset.asset_id == "cash:USD"

    def test_raises_not_found_for_missing_asset(self, runtime_engine: Engine) -> None:
        repo = SqlAssetRepository(runtime_engine)

        with pytest.raises(NotFoundError):
            repo.get("equity:nasdaq:DOES-NOT-EXIST")


class TestListByIds:
    def test_returns_matching_subset_and_omits_missing_ids(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        _insert_asset(owner_engine, asset_id="equity:nasdaq:NVDA", symbol="NVDA")
        _insert_asset(owner_engine, asset_id="equity:nasdaq:QQQ", asset_type="ETF", symbol="QQQ")
        repo = SqlAssetRepository(runtime_engine)

        results = repo.list_by_ids(
            ["equity:nasdaq:NVDA", "equity:nasdaq:QQQ", "equity:nasdaq:DOES-NOT-EXIST"]
        )

        assert {a.asset_id for a in results} == {"equity:nasdaq:NVDA", "equity:nasdaq:QQQ"}

    def test_empty_input_returns_empty_list(self, runtime_engine: Engine) -> None:
        repo = SqlAssetRepository(runtime_engine)

        assert repo.list_by_ids([]) == []
