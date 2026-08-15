"""Target-weight -> order diffing (BT-003), pure and infra-free.

Turns a ``Strategy.generate_targets`` output (``target_weights``, in base
currency terms) plus the portfolio's current holdings into a set of
``PlannedOrder``s a caller can risk-check and simulate a fill for.

Two judgment calls made here, both documented per the task brief's
instruction not to leave them implicit:

1. **Rebalance threshold.** A target weight that differs from the current
   weight by only a tiny amount is not worth trading -- the commission floor
   alone could exceed the economic benefit of chasing a sub-basis-point
   drift, and constantly trading noise-level differences would understate
   how a real, cost-aware rebalance policy behaves. ``rebalance_threshold_pct``
   (a fraction of NAV) is the caller-supplied cutoff below which a diff is
   skipped entirely (no order at all, not even a zero-quantity one).
2. **Stale-equity no-new-signal rule (`08_backtest_engine.md` §5): "stale
   标价日不生成新的交易信号（可评估现有持仓但不产生新的美股调仓建议）".**
   ``Strategy.generate_targets`` (CONTRACT-002) has no parameter to suppress
   this itself -- its frozen signature is exactly
   ``(analysis, portfolio_state) -> StrategyOutput``, so there is no slot to
   pass "and also, don't rebalance this specific asset today." The only
   compliant place left to enforce the rule without changing a frozen
   interface is here, at the point ``target_weights`` is turned into actual
   orders: any asset whose ``is_stale`` flag is set and whose
   ``asset_class`` is ``"STOCK"``/``"ETF"`` is skipped entirely for the day
   (no buy, no sell) -- crypto is exempted (trades 24/7, per §5, so is never
   marked stale by ``backtest.calendar``/``align_daily_series`` in the first
   place, but the check is written generically rather than assuming that).
   The asset's existing position is still carried forward and still
   contributes to that day's valuation -- this function simply produces no
   order for it, which is exactly "hold as-is."
"""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass
from decimal import Decimal
from typing import Literal

from .models import AssetClass

Side = Literal["buy", "sell"]

_ZERO = Decimal("0")


@dataclass(frozen=True)
class PlannedOrder:
    """A proposed order, before risk approval -- the BT-003 analogue of
    ``domain.risk.models.OrderIntent`` before it is wrapped as one.

    ``estimated_price`` and the implied notional are in the portfolio's base
    currency (see ``application.backtest.engine`` for why: it converts every
    non-base-currency instrument's price to base currency before this
    function ever sees it, so weight/notional arithmetic is self-consistent
    with ``PortfolioState.latest_snapshot.net_asset_value``, which is itself
    always base-currency).
    """

    asset_id: str
    side: Side
    quantity: Decimal
    estimated_price: Decimal


def diff_target_weights_to_orders(
    *,
    target_weights: Mapping[str, Decimal],
    current_quantities: Mapping[str, Decimal],
    prices: Mapping[str, Decimal],
    nav: Decimal,
    tradable_asset_ids: Mapping[str, AssetClass],
    is_stale: Mapping[str, bool],
    rebalance_threshold_pct: Decimal,
) -> tuple[PlannedOrder, ...]:
    """Diff ``target_weights`` against current holdings into ``PlannedOrder``s.

    ``tradable_asset_ids`` maps every tradable (non-cash) asset_id this
    portfolio can hold to its ``AssetClass`` -- this function only ever
    considers assets present here (cash entries in ``target_weights``, e.g.
    ``"cash:USD"``, are implicitly skipped since they never appear in this
    mapping). ``prices``/``current_quantities``/``is_stale`` must have an
    entry for every key of ``tradable_asset_ids``.

    Returns an empty tuple (not an error) if ``nav`` is not positive -- there
    is no meaningful weight-to-notional conversion without a positive NAV,
    and the caller (``application.backtest.engine``) already treats that as
    a data problem upstream of this pure function.
    """
    if nav <= _ZERO:
        return ()
    if rebalance_threshold_pct < _ZERO:
        raise ValueError(
            f"rebalance_threshold_pct must be non-negative, got {rebalance_threshold_pct}"
        )

    orders: list[PlannedOrder] = []
    for asset_id, asset_class in tradable_asset_ids.items():
        if asset_class in ("STOCK", "ETF") and is_stale.get(asset_id, False):
            continue

        price = prices[asset_id]
        if price <= _ZERO:
            # No usable price for this asset today -- nothing safe to diff
            # against; the caller's own upstream data-availability handling
            # is responsible for surfacing why (this function fails closed
            # by simply not proposing a trade, never by guessing a price).
            continue

        current_quantity = current_quantities.get(asset_id, _ZERO)
        current_value = current_quantity * price
        target_weight = target_weights.get(asset_id, _ZERO)
        target_value = target_weight * nav

        diff_value = target_value - current_value
        if abs(diff_value) < rebalance_threshold_pct * nav:
            continue

        side: Side = "buy" if diff_value > _ZERO else "sell"
        quantity = abs(diff_value) / price
        if quantity <= _ZERO:
            continue

        orders.append(
            PlannedOrder(
                asset_id=asset_id,
                side=side,
                quantity=quantity,
                estimated_price=price,
            )
        )

    return tuple(orders)


__all__ = ["PlannedOrder", "Side", "diff_target_weights_to_orders"]
