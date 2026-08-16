"""Daily reconciliation of a ``BacktestResult`` (BT-006, extended 2026-08-16).

Implements the M4 exit gate's "逐日对账...(目标权重/持仓/现金/汇率/净值)"
(`05_backtest_reporting_and_reconciliation.md` §3) requirement: an
*independent* re-derivation of cash/positions from ``BacktestResult.trades``,
plus internal-consistency checks against ``BacktestResult.equity_curve`` and
``BacktestResult.daily_detail``, checked bit-for-bit (exact ``Decimal``
equality, not an approximate/tolerance check) for every UTC calendar day in
the run.

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

Scope
-----
`05_backtest_reporting_and_reconciliation.md` §3's exit gate names five
things to reconcile daily: 目标权重 (target weights) / 持仓 (positions) /
现金 (cash) / 汇率 (FX) / 净值 (NAV). This module checks all five, at two
different levels of rigor depending on what is genuinely, independently
re-derivable:

1. **Cash, every day**: reconstructed running cash (replayed from
   ``BacktestResult.trades``, starting at ``config.initial_cash``) vs.
   ``equity_curve[day].cash_value``. Fully independent of ``engine.py``'s
   own running total.
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
5. **Per-asset positions vs. the aggregate total, every day** (needs
   ``BacktestResult.daily_detail``, 2026-08-16 addition): ``sum(daily_detail
   [day].position_values.values()) == equity_curve[day].positions_value``.
6. **Zero-quantity/zero-value consistency, every day** (needs
   ``daily_detail``): for every asset that ever appears, the independently
   *replayed* quantity for that day (item 1's same trade replay, evaluated
   per day rather than only at the end) is zero if and only if
   ``daily_detail[day].position_values`` has no nonzero entry for it -- a
   genuine cross-check between two independently-sourced signals (the trade
   log replay vs. the recorded per-asset valuation), not merely restating
   one of them.
7. **Target-weight bounds, every day with a nonempty ``target_weights``**
   (needs ``daily_detail``): every weight in ``[0, 1]`` and the full mapping
   (including the cash key) sums to exactly ``1``, per CONTRACT-002's own
   ``StrategyOutput.target_weights`` contract. Guards against
   ``daily_detail`` being corrupted or mis-threaded between the strategy
   call and ``BacktestResult`` construction -- ``domain.strategies
   .simple_rule_strategy`` already enforces this at the point the weights
   are *computed*, so this check's real job is catching a bug in this
   codebase's own plumbing, not in the strategy itself.
8. **FX rate positivity, every day** (needs ``daily_detail``): every
   recorded ``fx_rates_applied`` value is strictly positive. Weaker than a
   presence check (see the documented gap below) but catches a zero/negative
   rate slipping through, which no valid market FX rate is.

Items 5-8 are skipped for a given day/run when ``BacktestResult
.daily_detail`` is empty (its default, for backward compatibility with
hand-built fixtures that predate this field) -- ``BacktestEngine.run()``
always populates it for a real backtest, so a real run gets full coverage.

Genuine, documented remaining gaps
-----------------------------------
- **No independent numeric recomputation of per-asset dollar values or FX
  rates against a third data source.** Item 5 checks *internal* consistency
  (aggregate vs. per-asset breakdown, both produced by the same engine run);
  it does not re-derive either figure from freshly-loaded market data. Doing
  that would require re-loading the same historical price series
  ``BacktestEngine`` already used (this module intentionally has zero
  market-data/network dependency, matching the rest of
  ``application.backtest``'s "non-deterministic capabilities are injected,
  not reached for" discipline) -- and a second read of the *same* provider
  response is not really an independent check to begin with.
- **No FX-rate-presence-for-every-foreign-asset check.** ``BacktestResult``
  does not carry each asset's ``quote_currency``, so this module cannot tell
  *which* assets ought to have an FX rate recorded for a given day -- only
  that whatever rates *are* recorded are positive (item 8).
- **No target-weight-to-trade-direction causal check.** Verifying that a
  recorded ``buy``/``sell`` decision on a given day actually follows from
  that day's recorded ``target_weights`` (via the same threshold rule
  ``backtest.rebalance.diff_target_weights_to_orders`` applies) would need
  each asset's per-day *price*, not just its dollar position value -- not
  currently carried by ``daily_detail`` either. Deliberately deferred rather
  than re-implemented on a narrower, potentially-misleading proxy; a natural
  next task if this granularity is ever required.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date
from decimal import Decimal

from domain.valuation.models import ValuationSnapshot

from .models import BacktestResult, DailyPortfolioDetail, TradeRecord

_ZERO = Decimal("0")
_ONE = Decimal("1")


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


def _check_daily_detail(
    snapshot: ValuationSnapshot,
    detail: DailyPortfolioDetail,
    running_quantities: dict[str, Decimal],
) -> list[Discrepancy]:
    """Items 5-8 of the module docstring, for one day. See there for rationale."""
    found: list[Discrepancy] = []
    day = snapshot.valuation_date

    # 5. Per-asset breakdown vs. the aggregate total.
    breakdown_total = sum(detail.position_values.values(), start=_ZERO)
    if breakdown_total != snapshot.positions_value:
        found.append(
            Discrepancy(
                valuation_date=day,
                field="positions_value_breakdown",
                expected=snapshot.positions_value,
                actual=breakdown_total,
                detail=(
                    "sum(daily_detail.position_values) does not match the aggregate "
                    "equity_curve.positions_value recorded for this day"
                ),
            )
        )

    # 6. Zero-quantity/zero-value consistency between the independent trade
    # replay and the recorded per-asset valuation.
    for asset_id in set(running_quantities) | set(detail.position_values):
        replayed_qty = running_quantities.get(asset_id, _ZERO)
        recorded_value = detail.position_values.get(asset_id, _ZERO)
        if (replayed_qty == _ZERO) != (recorded_value == _ZERO):
            found.append(
                Discrepancy(
                    valuation_date=day,
                    field=f"position_value_zero_consistency:{asset_id}",
                    expected=replayed_qty,
                    actual=recorded_value,
                    detail=(
                        "independently replayed quantity and daily_detail.position_values "
                        "disagree on whether this asset was held (zero vs. nonzero) on this day"
                    ),
                )
            )

    # 7. Target-weight bounds (only when the day recorded any).
    if detail.target_weights:
        for asset_id, weight in detail.target_weights.items():
            if weight < _ZERO or weight > _ONE:
                found.append(
                    Discrepancy(
                        valuation_date=day,
                        field=f"target_weight_bounds:{asset_id}",
                        expected=_ONE,
                        actual=weight,
                        detail="target weight is outside the valid [0, 1] range",
                    )
                )
        weight_total = sum(detail.target_weights.values(), start=_ZERO)
        if weight_total != _ONE:
            found.append(
                Discrepancy(
                    valuation_date=day,
                    field="target_weights_sum",
                    expected=_ONE,
                    actual=weight_total,
                    detail="daily_detail.target_weights (including the cash key) does not sum to 1",
                )
            )

    # 8. FX rate positivity.
    for currency, rate in detail.fx_rates_applied.items():
        if rate <= _ZERO:
            found.append(
                Discrepancy(
                    valuation_date=day,
                    field=f"fx_rate_positivity:{currency}",
                    expected=_ZERO,
                    actual=rate,
                    detail="recorded FX rate must be strictly positive",
                )
            )

    return found


def reconcile_backtest_result(result: BacktestResult) -> ReconciliationResult:
    """Independently replay ``result.trades`` and reconcile against ``result.equity_curve``.

    See module docstring for the full list of what is checked (items 1-8)
    and the genuine remaining gaps. Exact ``Decimal`` equality throughout --
    this is bit-for-bit reconciliation, not an approximate check.
    """
    discrepancies: list[Discrepancy] = []

    trades_by_date: dict[date, list[TradeRecord]] = {}
    for trade in result.trades:
        if trade.status != "filled" or trade.trade_date is None:
            continue
        trades_by_date.setdefault(trade.trade_date, []).append(trade)

    detail_by_date: dict[date, DailyPortfolioDetail] = {
        d.valuation_date: d for d in result.daily_detail
    }

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

        detail = detail_by_date.get(snapshot.valuation_date)
        if detail is not None:
            discrepancies.extend(_check_daily_detail(snapshot, detail, running_quantities))

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
