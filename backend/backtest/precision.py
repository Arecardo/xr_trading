"""Precision-mode rounding (BT-003), pure and infra-free.

Implements `doc/technical/08_backtest_engine.md` §6's `precision_mode` rule:
``unrestricted`` records fill quantity at full ``Decimal`` precision (this
module is simply never called in that mode -- see
``application.backtest.engine``); ``restricted`` rounds a desired fill
quantity down to the instrument's real ``lot_size`` step and refuses to fill
(rather than filling a rounded-to-zero or below-``min_quantity`` amount) when
the rounded quantity is below ``min_quantity`` -- doc's explicit "取整后数量低于
min_quantity 时该笔交易不成交并记录原因" rule.

Rounding direction: **always floor (round toward zero)**, for both buys and
sells -- a deliberate, documented choice:

- Buy: flooring never requests more shares/units than the desired notional
  affords, mirroring how a real broker would reject (not silently round up)
  an order that would overspend the intended budget.
- Sell: flooring never attempts to sell more than was intended, avoiding an
  over-sell past a held quantity from a rounding-up error. This can leave a
  small residual position below one lot after a "full exit" sell diff (e.g.
  selling 2.97 lots of a 1.0-lot-size instrument sells 2 lots, leaving 0.97
  units held) -- a known, accepted MVP limitation for `restricted` mode,
  flagged in the BT-003 task report as a candidate for BT-003a/BT-006 to
  revisit (e.g. an explicit "close remaining odd lot" follow-up order) if it
  proves material to the fractional-share tracking-error comparison BT-003a
  exists to produce.
"""

from __future__ import annotations

from dataclasses import dataclass
from decimal import ROUND_FLOOR, Decimal


@dataclass(frozen=True)
class RoundedQuantity:
    """The result of rounding a desired quantity to an instrument's real lot size.

    ``quantity`` is always a non-negative multiple of the instrument's
    ``lot_size`` (or the original ``Decimal("0")`` when the desired quantity
    was already zero/negative). ``fillable`` is ``False`` -- and ``reason``
    explains why -- whenever the rounded quantity cannot actually be traded
    (below ``min_quantity``, or rounds to zero).
    """

    quantity: Decimal
    fillable: bool
    reason: str | None


def round_quantity_to_lot(
    desired_quantity: Decimal, *, lot_size: Decimal, min_quantity: Decimal
) -> RoundedQuantity:
    """Round ``desired_quantity`` down to the nearest ``lot_size`` multiple.

    Raises ``ValueError`` if ``lot_size`` is not positive or ``min_quantity``
    is negative -- these are configuration-data problems (a malformed
    ``InstrumentPrecision``), not an ordinary "cannot fill" business outcome,
    so they are not folded into ``RoundedQuantity.fillable=False``.
    """
    if lot_size <= Decimal("0"):
        raise ValueError(f"lot_size must be positive, got {lot_size}")
    if min_quantity < Decimal("0"):
        raise ValueError(f"min_quantity must be non-negative, got {min_quantity}")

    if desired_quantity <= Decimal("0"):
        return RoundedQuantity(
            quantity=Decimal("0"),
            fillable=False,
            reason=f"desired quantity {desired_quantity} is not positive, nothing to round",
        )

    steps = (desired_quantity / lot_size).to_integral_value(rounding=ROUND_FLOOR)
    rounded = steps * lot_size

    if rounded <= Decimal("0"):
        return RoundedQuantity(
            quantity=Decimal("0"),
            fillable=False,
            reason=(f"desired quantity {desired_quantity} rounds down to 0 at lot_size {lot_size}"),
        )
    if rounded < min_quantity:
        return RoundedQuantity(
            quantity=rounded,
            fillable=False,
            reason=(
                f"rounded quantity {rounded} (from desired {desired_quantity}, "
                f"lot_size {lot_size}) is below min_quantity {min_quantity}"
            ),
        )
    return RoundedQuantity(quantity=rounded, fillable=True, reason=None)


__all__ = ["RoundedQuantity", "round_quantity_to_lot"]
