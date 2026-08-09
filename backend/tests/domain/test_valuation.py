"""Unit tests for domain.valuation: instantiation and PortfolioState composition."""

from __future__ import annotations

from datetime import date
from decimal import Decimal
from typing import Literal
from uuid import UUID, uuid4

import pytest

from domain.portfolios import Portfolio, PortfolioMember
from domain.valuation import CashBalance, PortfolioState, Position, ValuationSnapshot


def _portfolio(portfolio_id: UUID | None = None) -> Portfolio:
    return Portfolio(
        portfolio_id=portfolio_id if portfolio_id is not None else uuid4(),
        name="Core Growth",
        base_currency="USD",
        benchmark_asset_id="equity:nasdaq:QQQ",
        risk_level="moderate",
        execution_mode="paper",
        status="active",
    )


def test_position_and_cash_balance_use_decimal_for_money_fields() -> None:
    portfolio_id = uuid4()
    position = Position(
        portfolio_id=portfolio_id,
        asset_id="equity:nasdaq:NVDA",
        quantity=Decimal("10.5"),
        average_cost=Decimal("120.25"),
    )
    cash = CashBalance(portfolio_id=portfolio_id, currency="USD", amount=Decimal("500.00"))

    assert isinstance(position.quantity, Decimal)
    assert isinstance(position.average_cost, Decimal)
    assert isinstance(cash.amount, Decimal)


@pytest.mark.parametrize("price_status", ["fresh", "stale"])
def test_valuation_snapshot_accepts_every_price_status(
    price_status: Literal["fresh", "stale"],
) -> None:
    snapshot = ValuationSnapshot(
        portfolio_id=uuid4(),
        valuation_date=date(2026, 8, 8),
        positions_value=Decimal("1000"),
        cash_value=Decimal("500"),
        net_asset_value=Decimal("1500"),
        base_currency="USD",
        price_status=price_status,
    )
    assert snapshot.price_status == price_status
    assert snapshot.net_asset_value == Decimal("1500")


def test_portfolio_state_composes_portfolio_members_positions_cash_and_snapshot() -> None:
    portfolio_id = uuid4()
    portfolio = _portfolio(portfolio_id)
    member = PortfolioMember(
        portfolio_id=portfolio_id,
        asset_id="equity:nasdaq:NVDA",
        member_status="held",
        target_weight_min=Decimal("0.05"),
        target_weight_max=Decimal("0.20"),
    )
    position = Position(
        portfolio_id=portfolio_id,
        asset_id="equity:nasdaq:NVDA",
        quantity=Decimal("10"),
        average_cost=Decimal("100"),
    )
    cash = CashBalance(portfolio_id=portfolio_id, currency="USD", amount=Decimal("500"))
    snapshot = ValuationSnapshot(
        portfolio_id=portfolio_id,
        valuation_date=date(2026, 8, 8),
        positions_value=Decimal("1000"),
        cash_value=Decimal("500"),
        net_asset_value=Decimal("1500"),
        base_currency="USD",
        price_status="fresh",
    )

    state = PortfolioState(
        portfolio=portfolio,
        members=(member,),
        positions=(position,),
        cash=(cash,),
        latest_snapshot=snapshot,
    )

    assert state.portfolio is portfolio
    assert state.members == (member,)
    assert state.positions == (position,)
    assert state.cash == (cash,)
    assert state.latest_snapshot is snapshot
