"""Integration tests for SqlPortfolioRepository against a real PostgreSQL instance.

Requires BACKEND_TEST_ADMIN_DATABASE_URL to be set and reachable -- see
conftest.py.
"""

from __future__ import annotations

from decimal import Decimal
from uuid import UUID, uuid4

import pytest
from sqlalchemy import insert
from sqlalchemy.engine import Engine

from repository.errors import NotFoundError
from repository.portfolios import SqlPortfolioRepository
from repository.schema import assets as assets_table
from repository.schema import portfolio_members as portfolio_members_table
from repository.schema import portfolios as portfolios_table


def _insert_asset(engine: Engine, asset_id: str) -> None:
    with engine.begin() as conn:
        conn.execute(
            insert(assets_table).values(
                asset_id=asset_id,
                asset_type="STOCK",
                symbol=asset_id.rsplit(":", 1)[-1],
                venue="nasdaq",
                quote_currency="USD",
                provider_symbols={},
                trading_status="tradable",
            )
        )


def _insert_portfolio(engine: Engine, portfolio_id: UUID, **overrides: object) -> None:
    row = {
        "portfolio_id": portfolio_id,
        "name": "Test Portfolio",
        "base_currency": "USD",
        "benchmark_asset_id": None,
        "risk_level": "moderate",
        "execution_mode": "research",
        "status": "active",
    }
    row.update(overrides)
    with engine.begin() as conn:
        conn.execute(insert(portfolios_table).values(**row))


def _insert_member(
    engine: Engine, portfolio_id: UUID, asset_id: str, member_status: str = "held"
) -> None:
    with engine.begin() as conn:
        conn.execute(
            insert(portfolio_members_table).values(
                portfolio_id=portfolio_id,
                asset_id=asset_id,
                member_status=member_status,
                target_weight_min=Decimal("0.050000"),
                target_weight_max=Decimal("0.150000"),
            )
        )


class TestGet:
    def test_returns_matching_portfolio(self, owner_engine: Engine, runtime_engine: Engine) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(
            owner_engine, portfolio_id, name="Growth", execution_mode="paper", status="active"
        )
        repo = SqlPortfolioRepository(runtime_engine)

        portfolio = repo.get(portfolio_id)

        assert portfolio.portfolio_id == portfolio_id
        assert portfolio.name == "Growth"
        assert portfolio.execution_mode == "paper"
        assert portfolio.status == "active"
        assert portfolio.benchmark_asset_id is None

    def test_raises_not_found_for_missing_portfolio(self, runtime_engine: Engine) -> None:
        repo = SqlPortfolioRepository(runtime_engine)

        with pytest.raises(NotFoundError):
            repo.get(uuid4())

    def test_benchmark_asset_id_round_trips(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        _insert_asset(owner_engine, "equity:nasdaq:QQQ")
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id, benchmark_asset_id="equity:nasdaq:QQQ")
        repo = SqlPortfolioRepository(runtime_engine)

        portfolio = repo.get(portfolio_id)

        assert portfolio.benchmark_asset_id == "equity:nasdaq:QQQ"


class TestListMembers:
    def test_returns_all_members_when_status_is_none(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        _insert_asset(owner_engine, "equity:nasdaq:NVDA")
        _insert_asset(owner_engine, "equity:nasdaq:QQQ")
        _insert_member(owner_engine, portfolio_id, "equity:nasdaq:NVDA", member_status="held")
        _insert_member(owner_engine, portfolio_id, "equity:nasdaq:QQQ", member_status="candidate")
        repo = SqlPortfolioRepository(runtime_engine)

        members = repo.list_members(portfolio_id)

        assert {m.asset_id for m in members} == {"equity:nasdaq:NVDA", "equity:nasdaq:QQQ"}

    def test_filters_by_status(self, owner_engine: Engine, runtime_engine: Engine) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        _insert_asset(owner_engine, "equity:nasdaq:NVDA")
        _insert_asset(owner_engine, "equity:nasdaq:QQQ")
        _insert_member(owner_engine, portfolio_id, "equity:nasdaq:NVDA", member_status="held")
        _insert_member(owner_engine, portfolio_id, "equity:nasdaq:QQQ", member_status="candidate")
        repo = SqlPortfolioRepository(runtime_engine)

        members = repo.list_members(portfolio_id, status="held")

        assert [m.asset_id for m in members] == ["equity:nasdaq:NVDA"]

    def test_target_weight_round_trips_as_decimal_not_float(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        _insert_asset(owner_engine, "equity:nasdaq:NVDA")
        _insert_member(owner_engine, portfolio_id, "equity:nasdaq:NVDA")
        repo = SqlPortfolioRepository(runtime_engine)

        [member] = repo.list_members(portfolio_id)

        assert member.target_weight_min == Decimal("0.050000")
        assert isinstance(member.target_weight_min, Decimal)
        assert member.target_weight_max == Decimal("0.150000")

    def test_no_members_returns_empty_list(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        repo = SqlPortfolioRepository(runtime_engine)

        assert repo.list_members(portfolio_id) == []
