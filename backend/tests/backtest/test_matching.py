"""Unit tests for backtest.matching (apply_slippage / compute_commission)."""

from __future__ import annotations

from decimal import Decimal

import pytest

from backtest.matching import Side, apply_slippage, compute_commission


@pytest.mark.parametrize(
    ("side", "expected"),
    [
        pytest.param("buy", Decimal("101.00"), id="buy_adds_slippage"),
        pytest.param("sell", Decimal("99.00"), id="sell_subtracts_slippage"),
    ],
)
def test_apply_slippage_moves_price_against_the_trader(side: Side, expected: Decimal) -> None:
    result = apply_slippage(Decimal("100.00"), side=side, slippage_pct=Decimal("0.01"))
    assert result == expected


def test_apply_slippage_zero_pct_is_a_no_op() -> None:
    assert apply_slippage(Decimal("100"), side="buy", slippage_pct=Decimal("0")) == Decimal("100")
    assert apply_slippage(Decimal("100"), side="sell", slippage_pct=Decimal("0")) == Decimal("100")


def test_apply_slippage_rejects_non_positive_base_price() -> None:
    with pytest.raises(ValueError, match="base_price must be positive"):
        apply_slippage(Decimal("0"), side="buy", slippage_pct=Decimal("0.01"))


def test_apply_slippage_rejects_negative_slippage_pct() -> None:
    with pytest.raises(ValueError, match="slippage_pct must be non-negative"):
        apply_slippage(Decimal("100"), side="buy", slippage_pct=Decimal("-0.01"))


@pytest.mark.parametrize(
    ("notional", "commission_pct", "min_commission", "expected"),
    [
        pytest.param(
            Decimal("1000"), Decimal("0.001"), Decimal("0"), Decimal("1.000"), id="pct_dominates"
        ),
        pytest.param(
            Decimal("10"), Decimal("0.001"), Decimal("1"), Decimal("1"), id="floor_dominates"
        ),
        pytest.param(
            Decimal("0"), Decimal("0.001"), Decimal("0"), Decimal("0"), id="zero_notional"
        ),
    ],
)
def test_compute_commission_takes_the_max_of_pct_and_floor(
    notional: Decimal, commission_pct: Decimal, min_commission: Decimal, expected: Decimal
) -> None:
    assert (
        compute_commission(
            notional=notional, commission_pct=commission_pct, min_commission=min_commission
        )
        == expected
    )


@pytest.mark.parametrize(
    ("notional", "commission_pct", "min_commission"),
    [
        pytest.param(Decimal("-1"), Decimal("0.001"), Decimal("0"), id="negative_notional"),
        pytest.param(Decimal("1"), Decimal("-0.001"), Decimal("0"), id="negative_commission_pct"),
        pytest.param(Decimal("1"), Decimal("0.001"), Decimal("-1"), id="negative_min_commission"),
    ],
)
def test_compute_commission_rejects_negative_inputs(
    notional: Decimal, commission_pct: Decimal, min_commission: Decimal
) -> None:
    with pytest.raises(ValueError):
        compute_commission(
            notional=notional, commission_pct=commission_pct, min_commission=min_commission
        )
