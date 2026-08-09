"""Unit tests for domain.risk: instantiation of RiskCheckResult and OrderIntent."""

from __future__ import annotations

from decimal import Decimal
from typing import Literal
from uuid import uuid4

import pytest

from domain.risk import OrderIntent, RiskCheckResult


def test_risk_check_result_approved_case_has_no_rejection_reasons_required() -> None:
    result = RiskCheckResult(
        approved=True,
        rejection_reasons=(),
        checked_rules=("max_position_weight", "max_order_notional"),
        context={"max_position_weight": "0.25"},
    )
    assert result.approved is True
    assert result.rejection_reasons == ()
    assert "max_position_weight" in result.checked_rules


def test_risk_check_result_rejected_case_carries_reasons_and_context() -> None:
    result = RiskCheckResult(
        approved=False,
        rejection_reasons=("exceeds_max_position_weight",),
        checked_rules=("max_position_weight",),
        context={"attempted_weight": "0.35", "limit": "0.25"},
    )
    assert result.approved is False
    assert result.rejection_reasons == ("exceeds_max_position_weight",)
    assert result.context["limit"] == "0.25"


@pytest.mark.parametrize("side", ["buy", "sell"])
def test_order_intent_accepts_both_sides(side: Literal["buy", "sell"]) -> None:
    intent = OrderIntent(
        portfolio_id=uuid4(),
        asset_id="equity:nasdaq:NVDA",
        side=side,
        quantity=Decimal("10"),
        estimated_price=Decimal("120.50"),
    )
    assert intent.side == side
    assert isinstance(intent.quantity, Decimal)
    assert isinstance(intent.estimated_price, Decimal)
