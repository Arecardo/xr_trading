"""Unit tests for domain.portfolios: instantiation and expected field types."""

from __future__ import annotations

from decimal import Decimal
from typing import Literal
from uuid import UUID, uuid4

import pytest

from domain.portfolios import Portfolio, PortfolioMember


def test_portfolio_instantiates_with_expected_field_types() -> None:
    portfolio_id = uuid4()
    portfolio = Portfolio(
        portfolio_id=portfolio_id,
        name="Core Growth",
        base_currency="USD",
        benchmark_asset_id="equity:nasdaq:QQQ",
        risk_level="moderate",
        execution_mode="paper",
        status="active",
    )

    assert isinstance(portfolio.portfolio_id, UUID)
    assert portfolio.name == "Core Growth"
    assert portfolio.base_currency == "USD"
    assert portfolio.benchmark_asset_id == "equity:nasdaq:QQQ"
    assert portfolio.execution_mode == "paper"
    assert portfolio.status == "active"


def test_portfolio_allows_no_benchmark() -> None:
    portfolio = Portfolio(
        portfolio_id=uuid4(),
        name="No Benchmark",
        base_currency="USD",
        benchmark_asset_id=None,
        risk_level="low",
        execution_mode="research",
        status="draft",
    )
    assert portfolio.benchmark_asset_id is None


@pytest.mark.parametrize("member_status", ["candidate", "approved", "held", "restricted"])
def test_portfolio_member_accepts_every_status(
    member_status: Literal["candidate", "approved", "held", "restricted"],
) -> None:
    member = PortfolioMember(
        portfolio_id=uuid4(),
        asset_id="equity:nasdaq:NVDA",
        member_status=member_status,
        target_weight_min=Decimal("0.05"),
        target_weight_max=Decimal("0.20"),
    )
    assert member.member_status == member_status
    assert isinstance(member.target_weight_min, Decimal)
    assert isinstance(member.target_weight_max, Decimal)


def test_portfolio_member_allows_no_target_weight_band() -> None:
    member = PortfolioMember(
        portfolio_id=uuid4(),
        asset_id="crypto:bybit:BTC-USDT",
        member_status="candidate",
        target_weight_min=None,
        target_weight_max=None,
    )
    assert member.target_weight_min is None
    assert member.target_weight_max is None
