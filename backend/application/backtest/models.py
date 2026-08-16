"""Output data shapes for BT-003's event-driven backtest engine.

``TradeRecord`` is a superset of `doc/technical/08_backtest_engine.md` §8's
trade-record JSON shape (``trade_id``/``asset_id``/``side``/``quantity``/
``fill_price``/``commission``/``slippage``/``signal_score``/``reason``/
``trade_date``, all present here) plus fields this task's own audit
requirements need that §8 does not name: ``status`` (so a rejected/skipped
decision is representable at all, not just a successful fill -- the task
brief's explicit "never silently dropped" instruction) and
``decision_date``/``planned_quantity`` (so a reader can tell which day's
signal produced this outcome and what was originally intended, distinct from
``trade_date``, the day a *fill* actually executed -- see
``application.backtest.engine``'s module docstring for the day-D-decision /
day-D+1-execution timing model this distinction exists to record). Like
``backend/backtest/models.py``'s ``AlignedSeries``, this is not a frozen
cross-task contract -- BT-003 is the first and only producer today; BT-006/
BT-007 (report generation) are free to project it down to the exact §8 shape
when they consume it.

``BacktestResult.equity_curve`` reuses ``domain.valuation.models.
ValuationSnapshot`` verbatim (the task brief's explicit instruction), not a
new dataclass -- one entry per UTC calendar day in ``[start_date, end_date]``,
gapless, per DEC-002.

``BacktestResult.benchmark_price_series`` (BT-006 addition, 2026-08-16):
narrow, documented additive field, not a breaking change to this
not-frozen contract (see this module's own docstring above). Populated by
``application.backtest.engine.BacktestEngine`` from the benchmark asset's
``AlignedSeries`` it already loads internally (``_load_asset_series``) to
feed ``compute_indicators``' relative-strength input -- exposing it here
costs no extra network call. One ``(trading_day, close)`` pair per UTC
calendar day in ``[config.start_date, config.end_date]``, gapless, same
alignment as ``equity_curve``. ``None`` iff ``Portfolio.benchmark_asset_id``
is unset. ``close`` is in the benchmark asset's own *native quote
currency*, not FX-converted to ``config.base_currency`` -- the engine does
not load an FX series for the benchmark's currency unless a portfolio
member happens to already need that same currency pair, and BT-006 was
directed not to add an extra network call to make this addition. Consumers
computing a benchmark total return from this series should be aware the
result is only directly comparable to the portfolio's own base-currency
return when the benchmark's ``quote_currency == config.base_currency``
(the common case, e.g. a USD portfolio benchmarked against QQQ in USD).

``BacktestResult.daily_detail`` (2026-08-16 addition, closes part of the
BT-006/BT-007-documented M4 daily-reconciliation gap): another narrow,
documented additive field. ``ValuationSnapshot`` (``equity_curve``) only
carries the *aggregate* ``positions_value``/``cash_value``/
``net_asset_value`` per day -- it has no per-day target-weight record, no
per-asset dollar breakdown, and no per-day FX rate, so
``application.backtest.reconciliation`` previously could not check three of
the M4 exit gate's five named daily-reconciliation dimensions (目标权重/
汇率, plus 持仓 at anything finer than the aggregate total). ``daily_detail``
carries exactly those three, one ``DailyPortfolioDetail`` per UTC calendar
day in ``[config.start_date, config.end_date]``, same gapless alignment as
``equity_curve`` -- see ``DailyPortfolioDetail``'s own docstring for the
per-field detail. Defaults to ``()`` for backward compatibility with
existing hand-built ``BacktestResult`` fixtures that predate this field and
do not exercise the reconciliation checks that depend on it;
``application.backtest.engine.BacktestEngine.run()`` always populates it in
full for a real backtest.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date
from decimal import Decimal
from typing import Literal
from uuid import UUID

from domain.valuation.models import ValuationSnapshot

from .config import BacktestConfig

Side = Literal["buy", "sell"]

TradeStatus = Literal[
    "filled",
    "rejected",
    "skipped_min_quantity",
    "skipped_precision_unavailable",
    "skipped_no_next_day",
]
"""
- ``filled``: the order executed; ``quantity``/``fill_price`` are populated.
- ``rejected``: ``domain.risk.RiskPolicy.check_order`` (via
  ``ExecutionService.submit``) refused the order -- never fills, per
  CONTRACT-001's non-bypassable constraint.
- ``skipped_min_quantity``: ``precision_mode="restricted"`` rounded the
  desired quantity to zero or below the instrument's ``min_quantity``
  (``backtest.precision.round_quantity_to_lot``) -- never fills.
- ``skipped_precision_unavailable``: ``precision_mode="restricted"`` but no
  usable ``InstrumentPrecision`` exists for this asset (fail-closed, DEC-003
  §3/§5.1.1: "缺失或过期时该标的当期 fail-closed，不生成撮合") -- never fills.
- ``skipped_no_next_day``: the decision was approved but made on the last
  UTC calendar day in ``[start_date, end_date]``, so there is no next day's
  open to execute at (see ``engine`` module docstring) -- never fills.
"""


@dataclass(frozen=True)
class TradeRecord:
    """One decision outcome: a fill, a rejection, or one of the documented skip reasons.

    ``fill_price``/``trade_date`` are ``None`` whenever ``status !=
    "filled"``; ``quantity`` is ``Decimal("0")`` and ``commission``/
    ``slippage`` are ``Decimal("0")`` in that case too (never a fabricated
    non-zero value for an order that never executed).
    """

    trade_id: str
    portfolio_id: UUID
    asset_id: str
    symbol: str
    side: Side
    status: TradeStatus
    planned_quantity: Decimal
    quantity: Decimal
    fill_price: Decimal | None
    commission: Decimal
    slippage: Decimal
    signal_score: Decimal | None
    reason: tuple[str, ...]
    decision_date: date
    trade_date: date | None


@dataclass(frozen=True)
class DailyPortfolioDetail:
    """One trading day's audit detail beyond the aggregate ``ValuationSnapshot``.

    Populated for every day ``BacktestEngine.run()`` produces (one entry per
    ``ValuationSnapshot`` in ``BacktestResult.equity_curve``, same
    ``valuation_date``) so ``application.backtest.reconciliation`` can check
    three of the M4 exit gate's five named daily-reconciliation dimensions
    that ``ValuationSnapshot`` alone does not carry: 目标权重
    (``target_weights``), 汇率 (``fx_rates_applied``), and per-asset 持仓 in
    dollar terms (``position_values``, vs. ``equity_curve``'s aggregate-only
    ``positions_value``).

    - ``target_weights``: the strategy's raw ``StrategyOutput.target_weights``
      for this day, *before* ``rebalance_threshold_pct`` filtering and the
      stale-day no-new-signal rule (``backtest.rebalance``) are applied --
      i.e. exactly what the strategy proposed that day, whether or not the
      engine actually acted on it. Includes the cash key (e.g.
      ``"cash:USD"``); per CONTRACT-002, values must sum to exactly 1.
    - ``position_values``: per-asset market value in ``config.base_currency``
      at this day's close (``quantity * price_base``), for every portfolio
      member with a nonzero position that day (an asset fully exited that
      day is simply absent, never a fabricated zero entry).
      ``sum(position_values.values()) == equity_curve[i].positions_value``
      for the same day, by construction -- ``reconcile_backtest_result``
      independently checks this identity still holds, catching a future
      regression where the two stop being computed together.
    - ``fx_rates_applied``: the close FX rate (foreign-currency-per-unit ->
      ``config.base_currency``) actually used to value each non-base quote
      currency's holdings this day, keyed by currency. Empty when every
      portfolio member already quotes in ``config.base_currency``.

    Like ``AlignedSeries``/``BacktestResult`` itself, this is not a frozen
    cross-task contract.
    """

    valuation_date: date
    target_weights: dict[str, Decimal]
    position_values: dict[str, Decimal]
    fx_rates_applied: dict[str, Decimal]


@dataclass(frozen=True)
class BacktestResult:
    """The full output of one ``application.backtest.engine.BacktestEngine.run()`` call.

    Deliberately does not compute ``sharpe_ratio``/``sortino_ratio``/
    ``win_rate``/``profit_loss_ratio``/a full report -- out of scope for
    BT-003 (already BT-006/BT-007's job per ``domain.valuation.performance``);
    this bundles the raw daily equity series and full trade/fill log those
    later tasks need, plus ``replaced_risk_checks`` (CONTRACT-003's
    ``RiskPolicy.replaced_checks()``, §9's "必须在报告中标注" requirement --
    must not be silently discarded).
    """

    portfolio_id: UUID
    config: BacktestConfig
    equity_curve: tuple[ValuationSnapshot, ...]
    trades: tuple[TradeRecord, ...]
    replaced_risk_checks: tuple[str, ...]
    final_positions: dict[str, Decimal]
    final_cash: Decimal
    daily_detail: tuple[DailyPortfolioDetail, ...] = ()
    benchmark_price_series: tuple[tuple[date, Decimal], ...] | None = None


__all__ = ["BacktestResult", "DailyPortfolioDetail", "Side", "TradeRecord", "TradeStatus"]
