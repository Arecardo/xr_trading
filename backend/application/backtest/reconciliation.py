"""Daily reconciliation of a ``BacktestResult`` (BT-006).

Implements the M4 exit gate's "逐日对账...净值必须可逐日对账"
(`05_backtest_reporting_and_reconciliation.md` §3) requirement: an
*independent* re-derivation of cash and NAV internal consistency from
``BacktestResult.trades``, checked bit-for-bit (exact ``Decimal`` equality,
not an approximate/tolerance check) against ``BacktestResult.equity_curve``
for every UTC calendar day in the run.

Deliberately independent, not a restatement of ``engine.py``
--------------------------------------------------------------
This module never reads ``BacktestEngine``'s internal running ``cash``
variable. It starts from ``BacktestResult.config.initial_cash`` and replays
only the publicly recorded ``TradeRecord`` facts (``side``/``quantity``/
``fill_price``/``commission``, only ``status == "filled"`` records --
anything else never moved cash or positions) through the same well-known
accounting identity the engine itself is *supposed* to implement:

- buy:  ``cash -= quantity * fill_price + commission``
- sell: ``cash += quantity * fill_price - commission``

If ``engine.py`` ever has a bookkeeping bug (a sign error, a double-counted
commission, a fill applied on the wrong day), this independent replay
diverges from ``equity_curve``'s recorded ``cash_value`` and the mismatch
is reported as a ``Discrepancy`` -- that is the whole point of this module
existing as separate code, not a call into ``engine.py``.

Scope, and a genuine, documented gap
-------------------------------------
`05_backtest_reporting_and_reconciliation.md` §3's exit gate names five
things to reconcile daily: 目标权重 (target weights) / 持仓 (positions) /
现金 (cash) / 汇率 (FX) / 净值 (NAV). ``BacktestResult`` as BT-003 defined it
does **not** carry a per-day target-weight record, a per-day per-asset
quantity/price breakdown, or the FX rate applied on each day -- only
``TradeRecord`` (a discrete decision/fill log) and ``equity_curve``
(aggregate ``positions_value``/``cash_value``/``net_asset_value`` per day,
with no per-asset or per-currency detail). Given that, this module checks
everything that *is* independently re-derivable from those two inputs:

1. **Cash, every day**: reconstructed running cash vs.
   ``equity_curve[day].cash_value``.
2. **NAV internal consistency, every day**: ``equity_curve[day]
   .positions_value + equity_curve[day].cash_value ==
   equity_curve[day].net_asset_value`` -- catches a NAV computed from a
   different positions/cash pairing than the one actually recorded that
   day.
3. **Final positions, once**: independently accumulated per-asset
   quantity (buys add, sells subtract) vs. ``BacktestResult.final_positions``.
4. **Final cash, once**: the fully-replayed running cash vs.
   ``BacktestResult.final_cash`` (a redundant cross-check against item 1's
   last day, kept because ``final_cash`` and the last ``equity_curve`` entry
   are two separately-populated fields in ``engine.py`` and could in
   principle diverge from each other even if each individually matched this
   replay).

**Not checked here, and flagged as an open gap for BT-007/BT-003a**:
per-day *positions_value in dollar terms* cannot be independently
recomputed without re-loading the same historical price series
``BacktestEngine`` used (this module intentionally has zero market-data/
network dependency, matching the rest of ``application.backtest``'s
"non-deterministic capabilities are injected, not reached for" discipline)
-- doing so would not really be *independent* verification, since any price
data pulled fresh from the same provider a second time is exactly what the
engine already used, not a differently-sourced check. Per-day target
weights and per-day FX rates are not present anywhere in ``BacktestResult``
at all today; if the M4 gate's literal five-field wording needs to be
checked at that granularity, ``BacktestResult``/``TradeRecord`` need a
further additive field (a natural next BT-003a/BT-007 task) to carry them.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date
from decimal import Decimal

from .models import BacktestResult, TradeRecord

_ZERO = Decimal("0")


@dataclass(frozen=True)
class Discrepancy:
    """One mismatch between the independent replay and ``BacktestResult``'s recorded values."""

    valuation_date: date
    field: str
    expected: Decimal
    actual: Decimal
    detail: str = ""


@dataclass(frozen=True)
class ReconciliationResult:
    """The full outcome of one ``reconcile_backtest_result`` run.

    ``passed`` is ``True`` iff ``discrepancies`` is empty. Callers get the
    full list rather than a raise-on-first-mismatch exception, per the task
    brief's explicit instruction, so a single reconciliation run reports
    every divergence in one pass.
    """

    passed: bool
    discrepancies: tuple[Discrepancy, ...]


def reconcile_backtest_result(result: BacktestResult) -> ReconciliationResult:
    """Independently replay ``result.trades`` and reconcile against ``result.equity_curve``.

    See module docstring for exactly what is (cash/day, NAV-consistency/day,
    final positions, final cash) and is not (per-day positions_value in
    dollar terms, target weights, FX rates) checked. Exact ``Decimal``
    equality throughout -- this is bit-for-bit reconciliation, not an
    approximate check.
    """
    discrepancies: list[Discrepancy] = []

    trades_by_date: dict[date, list[TradeRecord]] = {}
    for trade in result.trades:
        if trade.status != "filled" or trade.trade_date is None:
            continue
        trades_by_date.setdefault(trade.trade_date, []).append(trade)

    running_cash = result.config.initial_cash
    running_quantities: dict[str, Decimal] = {}

    for snapshot in sorted(result.equity_curve, key=lambda s: s.valuation_date):
        for trade in trades_by_date.get(snapshot.valuation_date, ()):
            assert trade.fill_price is not None  # guaranteed by status == "filled"
            notional = trade.quantity * trade.fill_price
            if trade.side == "buy":
                running_cash -= notional + trade.commission
                running_quantities[trade.asset_id] = (
                    running_quantities.get(trade.asset_id, _ZERO) + trade.quantity
                )
            else:
                running_cash += notional - trade.commission
                running_quantities[trade.asset_id] = (
                    running_quantities.get(trade.asset_id, _ZERO) - trade.quantity
                )

        if running_cash != snapshot.cash_value:
            discrepancies.append(
                Discrepancy(
                    valuation_date=snapshot.valuation_date,
                    field="cash_value",
                    expected=running_cash,
                    actual=snapshot.cash_value,
                    detail=(
                        "independently replayed cash (initial_cash + filled trade cash deltas "
                        "up to and including this day) does not match the recorded snapshot"
                    ),
                )
            )

        recomputed_nav = snapshot.positions_value + snapshot.cash_value
        if recomputed_nav != snapshot.net_asset_value:
            discrepancies.append(
                Discrepancy(
                    valuation_date=snapshot.valuation_date,
                    field="net_asset_value",
                    expected=recomputed_nav,
                    actual=snapshot.net_asset_value,
                    detail="positions_value + cash_value does not equal net_asset_value",
                )
            )

    final_date = result.config.end_date
    all_asset_ids = set(running_quantities) | set(result.final_positions)
    for asset_id in sorted(all_asset_ids):
        expected_qty = running_quantities.get(asset_id, _ZERO)
        actual_qty = result.final_positions.get(asset_id, _ZERO)
        if expected_qty != actual_qty:
            discrepancies.append(
                Discrepancy(
                    valuation_date=final_date,
                    field=f"final_position:{asset_id}",
                    expected=expected_qty,
                    actual=actual_qty,
                    detail=(
                        "independently replayed quantity (sum of filled buy/sell fills) does "
                        "not match BacktestResult.final_positions"
                    ),
                )
            )

    if running_cash != result.final_cash:
        discrepancies.append(
            Discrepancy(
                valuation_date=final_date,
                field="final_cash",
                expected=running_cash,
                actual=result.final_cash,
                detail=(
                    "independently replayed final cash does not match BacktestResult.final_cash"
                ),
            )
        )

    return ReconciliationResult(passed=not discrepancies, discrepancies=tuple(discrepancies))


__all__ = ["Discrepancy", "ReconciliationResult", "reconcile_backtest_result"]
