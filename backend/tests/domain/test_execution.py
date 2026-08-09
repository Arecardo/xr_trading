"""Unit tests for domain.execution: instantiation of Order and Fill."""

from __future__ import annotations

from datetime import UTC, datetime
from decimal import Decimal
from typing import Literal
from uuid import uuid4

import pytest

from domain.execution import Fill, Order


@pytest.mark.parametrize(
    "status",
    [
        "pending_risk",
        "rejected",
        "submitted",
        "partially_filled",
        "filled",
        "cancelled",
        "unknown",
    ],
)
def test_order_accepts_every_status_in_the_state_machine(
    status: Literal[
        "pending_risk",
        "rejected",
        "submitted",
        "partially_filled",
        "filled",
        "cancelled",
        "unknown",
    ],
) -> None:
    order = Order(
        order_id=uuid4(),
        portfolio_id=uuid4(),
        asset_id="equity:nasdaq:NVDA",
        side="buy",
        quantity=Decimal("10"),
        order_type="market",
        limit_price=None,
        status=status,
    )
    assert order.status == status


def test_order_limit_price_is_optional_for_market_orders() -> None:
    order = Order(
        order_id=uuid4(),
        portfolio_id=uuid4(),
        asset_id="equity:nasdaq:NVDA",
        side="sell",
        quantity=Decimal("5"),
        order_type="limit",
        limit_price=Decimal("135.00"),
        status="pending_risk",
    )
    assert order.limit_price == Decimal("135.00")


def test_fill_instantiates_with_expected_field_types() -> None:
    fill = Fill(
        fill_id=uuid4(),
        order_id=uuid4(),
        quantity=Decimal("5"),
        price=Decimal("134.75"),
        commission=Decimal("0.50"),
        filled_at=datetime(2026, 8, 8, 12, 0, tzinfo=UTC),
    )
    assert isinstance(fill.quantity, Decimal)
    assert isinstance(fill.price, Decimal)
    assert isinstance(fill.commission, Decimal)
    assert fill.filled_at.tzinfo is not None
