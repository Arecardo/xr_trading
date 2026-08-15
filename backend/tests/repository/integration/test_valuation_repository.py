"""Integration tests for the BE-003 valuation repositories against a real PostgreSQL instance.

Requires BACKEND_TEST_ADMIN_DATABASE_URL to be set and reachable -- see
conftest.py. Mirrors test_portfolios_repository.py's fixture-insertion style.
"""

from __future__ import annotations

from datetime import date
from decimal import Decimal
from uuid import UUID, uuid4

import pytest
from sqlalchemy import func, insert, select
from sqlalchemy.engine import Engine

from domain.valuation.models import CashBalance, PerformanceSnapshot, Position, ValuationSnapshot
from repository.errors import NotFoundError
from repository.schema import assets as assets_table
from repository.schema import portfolios as portfolios_table
from repository.schema import valuation_snapshots as valuation_snapshots_table
from repository.valuation import (
    SqlCashBalanceRepository,
    SqlPerformanceSnapshotRepository,
    SqlPositionRepository,
    SqlValuationSnapshotRepository,
)


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


class TestSqlPositionRepository:
    def test_upsert_then_list_round_trips_decimal_fields(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        _insert_asset(owner_engine, "equity:nasdaq:NVDA")
        repo = SqlPositionRepository(runtime_engine)

        repo.upsert(
            Position(
                portfolio_id=portfolio_id,
                asset_id="equity:nasdaq:NVDA",
                quantity=Decimal("10.123456"),
                average_cost=Decimal("120.50"),
            )
        )

        [position] = repo.list_by_portfolio(portfolio_id)
        assert position.quantity == Decimal("10.123456")
        assert isinstance(position.quantity, Decimal)
        assert position.average_cost == Decimal("120.50")

    def test_upsert_overwrites_existing_position(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        _insert_asset(owner_engine, "equity:nasdaq:NVDA")
        repo = SqlPositionRepository(runtime_engine)

        repo.upsert(
            Position(
                portfolio_id=portfolio_id,
                asset_id="equity:nasdaq:NVDA",
                quantity=Decimal("10"),
                average_cost=Decimal("100"),
            )
        )
        repo.upsert(
            Position(
                portfolio_id=portfolio_id,
                asset_id="equity:nasdaq:NVDA",
                quantity=Decimal("15"),
                average_cost=Decimal("110"),
            )
        )

        [position] = repo.list_by_portfolio(portfolio_id)
        assert position.quantity == Decimal("15")
        assert position.average_cost == Decimal("110")

    def test_list_by_portfolio_returns_empty_list_when_no_positions(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        repo = SqlPositionRepository(runtime_engine)

        assert repo.list_by_portfolio(portfolio_id) == []


class TestSqlCashBalanceRepository:
    def test_upsert_then_list_round_trips_decimal_fields(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        repo = SqlCashBalanceRepository(runtime_engine)

        repo.upsert(
            CashBalance(portfolio_id=portfolio_id, currency="USD", amount=Decimal("543.21"))
        )

        [cash] = repo.list_by_portfolio(portfolio_id)
        assert cash.amount == Decimal("543.21")
        assert isinstance(cash.amount, Decimal)

    def test_upsert_overwrites_existing_balance(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        repo = SqlCashBalanceRepository(runtime_engine)

        repo.upsert(CashBalance(portfolio_id=portfolio_id, currency="USD", amount=Decimal("100")))
        repo.upsert(CashBalance(portfolio_id=portfolio_id, currency="USD", amount=Decimal("250")))

        [cash] = repo.list_by_portfolio(portfolio_id)
        assert cash.amount == Decimal("250")

    def test_multiple_currencies_coexist(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        repo = SqlCashBalanceRepository(runtime_engine)

        repo.upsert(CashBalance(portfolio_id=portfolio_id, currency="USD", amount=Decimal("100")))
        repo.upsert(CashBalance(portfolio_id=portfolio_id, currency="USDT", amount=Decimal("50")))

        balances = {c.currency: c.amount for c in repo.list_by_portfolio(portfolio_id)}
        assert balances == {"USD": Decimal("100"), "USDT": Decimal("50")}


class TestSqlValuationSnapshotRepository:
    def test_get_latest_raises_not_found_when_no_snapshot_exists(
        self, runtime_engine: Engine
    ) -> None:
        repo = SqlValuationSnapshotRepository(runtime_engine)

        with pytest.raises(NotFoundError):
            repo.get_latest(uuid4())

    def test_get_by_date_returns_none_when_no_snapshot_exists(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        repo = SqlValuationSnapshotRepository(runtime_engine)

        assert repo.get_by_date(portfolio_id, date(2026, 8, 8)) is None

    def test_upsert_then_get_latest_round_trips(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        repo = SqlValuationSnapshotRepository(runtime_engine)

        repo.upsert(
            ValuationSnapshot(
                portfolio_id=portfolio_id,
                valuation_date=date(2026, 8, 8),
                positions_value=Decimal("1000"),
                cash_value=Decimal("500"),
                net_asset_value=Decimal("1500"),
                base_currency="USD",
                price_status="fresh",
            )
        )

        snapshot = repo.get_latest(portfolio_id)
        assert snapshot.valuation_date == date(2026, 8, 8)
        assert snapshot.net_asset_value == Decimal("1500")
        assert isinstance(snapshot.net_asset_value, Decimal)
        assert snapshot.price_status == "fresh"

    def test_get_latest_returns_most_recent_by_valuation_date(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        repo = SqlValuationSnapshotRepository(runtime_engine)

        unordered_days = [
            (date(2026, 8, 6), "1000"),
            (date(2026, 8, 8), "1200"),
            (date(2026, 8, 7), "1100"),
        ]
        for day, nav in unordered_days:
            repo.upsert(
                ValuationSnapshot(
                    portfolio_id=portfolio_id,
                    valuation_date=day,
                    positions_value=Decimal(nav),
                    cash_value=Decimal("0"),
                    net_asset_value=Decimal(nav),
                    base_currency="USD",
                    price_status="fresh",
                )
            )

        snapshot = repo.get_latest(portfolio_id)
        assert snapshot.valuation_date == date(2026, 8, 8)
        assert snapshot.net_asset_value == Decimal("1200")

    def test_upsert_for_existing_date_overwrites_in_place(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        repo = SqlValuationSnapshotRepository(runtime_engine)

        repo.upsert(
            ValuationSnapshot(
                portfolio_id=portfolio_id,
                valuation_date=date(2026, 8, 8),
                positions_value=Decimal("1000"),
                cash_value=Decimal("500"),
                net_asset_value=Decimal("1500"),
                base_currency="USD",
                price_status="stale",
            )
        )
        repo.upsert(
            ValuationSnapshot(
                portfolio_id=portfolio_id,
                valuation_date=date(2026, 8, 8),
                positions_value=Decimal("1100"),
                cash_value=Decimal("500"),
                net_asset_value=Decimal("1600"),
                base_currency="USD",
                price_status="fresh",
            )
        )

        snapshot = repo.get_by_date(portfolio_id, date(2026, 8, 8))
        assert snapshot is not None
        assert snapshot.net_asset_value == Decimal("1600")
        assert snapshot.price_status == "fresh"

        # Overwriting a date must not create a second row for that date.
        with runtime_engine.connect() as conn:
            count = conn.execute(
                select(func.count())
                .select_from(valuation_snapshots_table)
                .where(valuation_snapshots_table.c.portfolio_id == portfolio_id)
            ).scalar_one()
        assert count == 1

    def test_id_factory_is_injectable_for_determinism(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        fixed_id = uuid4()
        repo = SqlValuationSnapshotRepository(runtime_engine, id_factory=lambda: fixed_id)

        repo.upsert(
            ValuationSnapshot(
                portfolio_id=portfolio_id,
                valuation_date=date(2026, 8, 8),
                positions_value=Decimal("1000"),
                cash_value=Decimal("0"),
                net_asset_value=Decimal("1000"),
                base_currency="USD",
                price_status="fresh",
            )
        )

        with runtime_engine.connect() as conn:
            row = (
                conn.execute(
                    select(valuation_snapshots_table.c.valuation_snapshot_id).where(
                        valuation_snapshots_table.c.portfolio_id == portfolio_id
                    )
                )
                .mappings()
                .first()
            )
        assert row is not None
        assert row["valuation_snapshot_id"] == fixed_id


class TestSqlPerformanceSnapshotRepository:
    def test_upsert_then_list_round_trips_and_allows_null_metrics(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        repo = SqlPerformanceSnapshotRepository(runtime_engine)

        repo.upsert(
            PerformanceSnapshot(
                portfolio_id=portfolio_id,
                as_of=date(2026, 8, 8),
                total_return_pct=Decimal("0.05"),
                max_drawdown_pct=Decimal("-0.12"),
                annualized_volatility=None,
                sharpe_ratio=None,
                sortino_ratio=None,
                benchmark_return_pct=None,
            )
        )

        [snapshot] = repo.list_by_portfolio(portfolio_id)
        assert snapshot.total_return_pct == Decimal("0.05")
        assert snapshot.max_drawdown_pct == Decimal("-0.12")
        assert snapshot.annualized_volatility is None
        assert snapshot.sharpe_ratio is None

    def test_upsert_for_existing_as_of_overwrites_in_place(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        repo = SqlPerformanceSnapshotRepository(runtime_engine)

        repo.upsert(
            PerformanceSnapshot(
                portfolio_id=portfolio_id,
                as_of=date(2026, 8, 8),
                total_return_pct=Decimal("0.05"),
                max_drawdown_pct=None,
                annualized_volatility=None,
                sharpe_ratio=None,
                sortino_ratio=None,
                benchmark_return_pct=None,
            )
        )
        repo.upsert(
            PerformanceSnapshot(
                portfolio_id=portfolio_id,
                as_of=date(2026, 8, 8),
                total_return_pct=Decimal("0.08"),
                max_drawdown_pct=None,
                annualized_volatility=None,
                sharpe_ratio=None,
                sortino_ratio=None,
                benchmark_return_pct=None,
            )
        )

        snapshots = repo.list_by_portfolio(portfolio_id)
        assert len(snapshots) == 1
        assert snapshots[0].total_return_pct == Decimal("0.08")

    def test_list_by_portfolio_orders_ascending_by_as_of(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        repo = SqlPerformanceSnapshotRepository(runtime_engine)

        for as_of in [date(2026, 8, 8), date(2026, 8, 6), date(2026, 8, 7)]:
            repo.upsert(
                PerformanceSnapshot(
                    portfolio_id=portfolio_id,
                    as_of=as_of,
                    total_return_pct=None,
                    max_drawdown_pct=None,
                    annualized_volatility=None,
                    sharpe_ratio=None,
                    sortino_ratio=None,
                    benchmark_return_pct=None,
                )
            )

        snapshots = repo.list_by_portfolio(portfolio_id)
        assert [s.as_of for s in snapshots] == [
            date(2026, 8, 6),
            date(2026, 8, 7),
            date(2026, 8, 8),
        ]
