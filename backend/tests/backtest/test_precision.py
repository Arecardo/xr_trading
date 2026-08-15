"""Unit tests for backtest.precision.round_quantity_to_lot."""

from __future__ import annotations

from decimal import Decimal

import pytest

from backtest.precision import round_quantity_to_lot


@pytest.mark.parametrize(
    ("desired", "lot_size", "min_quantity", "expected_quantity"),
    [
        pytest.param(Decimal("2.97"), Decimal("1"), Decimal("1"), Decimal("2"), id="floors_down"),
        pytest.param(
            Decimal("10"), Decimal("1"), Decimal("1"), Decimal("10"), id="exact_multiple_unchanged"
        ),
        pytest.param(
            Decimal("0.000123"),
            Decimal("0.000001"),
            Decimal("0.0001"),
            Decimal("0.000123"),
            id="fine_grained_lot_size",
        ),
    ],
)
def test_round_quantity_to_lot_floors_and_is_fillable(
    desired: Decimal, lot_size: Decimal, min_quantity: Decimal, expected_quantity: Decimal
) -> None:
    result = round_quantity_to_lot(desired, lot_size=lot_size, min_quantity=min_quantity)
    assert result.quantity == expected_quantity
    assert result.fillable is True
    assert result.reason is None


def test_round_quantity_to_lot_below_min_quantity_is_not_fillable_but_reports_rounded_qty() -> None:
    # lot_size=0.1 -> 0.5 rounds to a nonzero 0.5 (5 lots), but that is still
    # below min_quantity=1 -- distinct from rounding all the way down to 0.
    result = round_quantity_to_lot(
        Decimal("0.54"), lot_size=Decimal("0.1"), min_quantity=Decimal("1")
    )
    assert result.quantity == Decimal("0.5")
    assert result.fillable is False
    assert result.reason is not None
    assert "below min_quantity" in result.reason


def test_round_quantity_to_lot_rounds_to_zero_when_below_one_lot() -> None:
    result = round_quantity_to_lot(Decimal("0.4"), lot_size=Decimal("1"), min_quantity=Decimal("0"))
    assert result.quantity == Decimal("0")
    assert result.fillable is False
    assert result.reason is not None
    assert "rounds down to 0" in result.reason


@pytest.mark.parametrize("desired", [Decimal("0"), Decimal("-1")])
def test_round_quantity_to_lot_rejects_non_positive_desired_quantity(desired: Decimal) -> None:
    result = round_quantity_to_lot(desired, lot_size=Decimal("1"), min_quantity=Decimal("0"))
    assert result.quantity == Decimal("0")
    assert result.fillable is False
    assert result.reason is not None
    assert "not positive" in result.reason


def test_round_quantity_to_lot_rejects_non_positive_lot_size() -> None:
    with pytest.raises(ValueError, match="lot_size must be positive"):
        round_quantity_to_lot(Decimal("1"), lot_size=Decimal("0"), min_quantity=Decimal("0"))


def test_round_quantity_to_lot_rejects_negative_min_quantity() -> None:
    with pytest.raises(ValueError, match="min_quantity must be non-negative"):
        round_quantity_to_lot(Decimal("1"), lot_size=Decimal("1"), min_quantity=Decimal("-1"))


def test_round_quantity_to_lot_is_pure_and_deterministic() -> None:
    args = {"lot_size": Decimal("0.5"), "min_quantity": Decimal("0.5")}
    first = round_quantity_to_lot(Decimal("3.2"), **args)
    second = round_quantity_to_lot(Decimal("3.2"), **args)
    assert first == second
