"""Extended backtest performance metrics (BT-006).

Computes the full `doc/technical/08_backtest_engine.md` §7 metric set (total
return, max drawdown, annualized volatility, Sharpe, Sortino, win rate,
profit/loss ratio, turnover, benchmark comparison) from one
``application.backtest.models.BacktestResult`` -- BT-003's output. This
module is the second, documented consumer of that not-frozen contract (see
``application.backtest.models``'s module docstring).

Reuse, not reimplementation
----------------------------
``total_return_pct``/``max_drawdown_pct``/``annualized_volatility`` are not
recomputed here -- ``domain.valuation.performance.compute_performance_snapshot``
(BE-003) already computes exactly these three from a
``ValuationSnapshot`` series, and this module calls it directly on
``BacktestResult.equity_curve``. That function always returns
``sharpe_ratio=None``/``sortino_ratio=None``/``benchmark_return_pct=None``
by its own explicit design (its docstring: "deliberately left
unimplemented, not half-implemented with an invented assumption" -- BT-006's
job). This module fills in those three fields via ``dataclasses.replace``
rather than constructing a second, parallel ``PerformanceSnapshot``.

Judgment calls made here, documented per the task brief's instruction not to
leave them implicit
------------------------------------------------------------------------

**1. Risk-free rate default: 0%, fully overridable.** ``compute_backtest_metrics``
takes a ``risk_free_rate_pct: Decimal`` parameter (annualized, as a fraction
-- e.g. ``Decimal("0.03")`` means 3%/year) defaulting to ``Decimal("0")``.
This is a deliberate, standard baseline-modeling *assumption*, not a claim
about any real observed rate: many retail-portfolio Sharpe/Sortino
calculations use 0% when no specific risk-free benchmark (e.g. a specific
T-bill series) is wired into the system, because it isolates "return per
unit of volatility" without importing an unverified external rate. This
codebase has no risk-free-rate data source anywhere yet (no fixture, no
adapter, no config field) -- guessing a "real" number here would be exactly
the kind of fabricated financial precision value the project's
decision-workflow guidance forbids. Callers who have a real risk-free
series (e.g. from a future rates adapter) pass it explicitly; this default
is a documented fallback, never silently treated as ground truth.

**2. Sortino MAR default: same as the risk-free rate.** ``compute_backtest_metrics``
takes an optional ``mar_pct: Decimal | None`` (minimum acceptable return,
annualized fraction); when ``None`` (the default), it uses
``risk_free_rate_pct`` as the MAR. This mirrors the most common textbook
convention when no portfolio-specific target return is configured (Sortino
was originally framed as "downside deviation below the risk-free rate" as
a special case of the more general "downside deviation below any MAR").
Nothing in ``doc/`` specifies a MAR, so this is a judgment call, not a
transcribed requirement -- documented here exactly like BT-004/BT-005's own
flagged defaults.

**3. Sharpe/Sortino annualization method.** Both compute *daily* returns
from ``equity_curve``'s NAV series, then annualize by scaling the daily
mean and the daily volatility measure separately by ``sqrt(365)``
(``annualized_sharpe = sqrt(365) * mean(daily_excess_return) /
stdev(daily_return)``) rather than compounding a CAGR. This matches
``domain.valuation.performance``'s own ``annualized_volatility`` convention
(daily stdev * ``sqrt(365)``, chosen there to match DEC-002's UTC-calendar-day,
365-day/year cadence) -- using the same annualization convention for the
denominator of Sharpe/Sortino as for the standalone volatility figure they
sit next to in the same report keeps the two numbers mutually consistent
(dividing ``total_return_pct`` by ``annualized_volatility`` would *not*
reproduce ``sharpe_ratio`` under a mixed-convention approach). Internally
uses ``float`` for the mean/stdev arithmetic (mirroring
``domain.valuation.performance._annualized_volatility``'s own documented
exception to the "Decimal for money" rule -- this is a statistical estimate,
not a ledger amount), re-quantized to ``Decimal`` on return via
``Decimal(str(...))``.

Both need at least 2 daily-return observations (3 snapshots) to be
defined, same threshold as ``annualized_volatility``; below that, or when
the return series' volatility (Sharpe) / downside deviation (Sortino) is
exactly zero (division would be undefined, not "infinitely good"), the
result is ``None`` rather than a fabricated value.

**4. FIFO lot-matching realized P&L is gross of commission.** ``win_rate``/
``profit_loss_ratio`` come from FIFO-matching each ``sell`` fill against the
oldest still-open ``buy`` fill(s) for the same ``asset_id`` (only
``status == "filled"`` trades participate -- ``rejected``/``skipped_*``
never reached the market). Each closed lot's realized P&L is
``matched_quantity * (sell.fill_price - buy.fill_price)`` -- i.e. gross of
commission. Commission is deliberately excluded from the win/loss
classification: a single ``sell`` can close multiple ``buy`` lots opened at
different times (and a single ``buy`` lot can itself be split across
multiple later sells), and CONTRACT-001/§8's trade-record shape gives one
commission figure per *fill*, not per matched-lot-pair -- allocating a
fill's commission proportionally across an arbitrary number of partial lot
matches is an extra layer of approximation nowhere specified in ``doc/``.
Commission's effect on the portfolio is already fully captured, exactly
(not approximated), in ``equity_curve``'s NAV series and therefore in
``total_return_pct``/``sharpe_ratio``/etc. -- this module's win/loss
classification answers a narrower, price-movement-only question ("did this
round trip's execution price move favorably"), documented explicitly here
so a reader of ``win_rate``/``profit_loss_ratio`` does not assume it nets
trading costs. Ties (`realized == 0`, an exact breakeven close) count
toward ``closed_trade_count`` but are neither a win nor a loss.

``win_rate = wins / closed_trade_count`` is ``None`` when there are no
closes at all (``closed_trade_count == 0``). ``profit_loss_ratio =
avg(win amounts) / avg(loss amounts)`` (with the loss average taken as a
positive magnitude) is ``None`` whenever there are no losses (nothing to
divide by) *or* no wins (the ratio's numerator is undefined, not
usefully "0") -- never a divide-by-zero, never a fabricated ratio.

**5. Turnover definition: total filled notional / average NAV over the
period.** No value or formula is specified anywhere in ``doc/`` for
turnover -- this is a genuine BT-006 judgment call, flagged exactly like
BT-003's ``DEFAULT_REBALANCE_THRESHOLD_PCT``. Definition used:
``turnover = sum(quantity * fill_price for every filled trade) /
mean(net_asset_value for every equity_curve snapshot)``. This is a standard,
simple portfolio-turnover proxy (total traded dollar volume relative to
typical portfolio size over the same period) -- deliberately *not*
"one-way turnover" (buys only, or the smaller of buys/sells, both common
refinements) because ``BacktestResult`` does not distinguish rebalancing
trades from position-closing trades, and the simpler total-notional
definition is unambiguous given what is available. ``None`` when
``equity_curve`` is empty (mean NAV undefined) or the mean NAV is exactly
zero.

**6. ``trade_count`` counts filled trades only.** `08_backtest_engine.md`
§7's example JSON has a bare ``"trade_count": 42`` with no further
definition. This module counts only ``status == "filled"`` records --
consistent with `08_backtest_engine.md` §8's "每笔模拟交易" (each *simulated*
trade) referring to actual executions, not rejected/skipped decision
attempts (which ``TradeRecord`` also uses this same list to record, per
BT-003's own explicit "never silently dropped" design). Rejected/skipped
counts remain fully available via ``BacktestResult.trades`` for a caller
that wants them (e.g. BT-007's report), just not folded into this one
headline number.
"""

from __future__ import annotations

import math
from dataclasses import dataclass, replace
from datetime import date
from decimal import Decimal
from statistics import StatisticsError, fmean, stdev
from uuid import UUID

from domain.valuation.models import PerformanceSnapshot, ValuationSnapshot
from domain.valuation.performance import compute_performance_snapshot

from .models import BacktestResult, TradeRecord

_ZERO = Decimal("0")
_ONE = Decimal("1")
_CALENDAR_DAYS_PER_YEAR = Decimal("365")
_MIN_RETURNS_FOR_RATIO = 2

DEFAULT_RISK_FREE_RATE_PCT = Decimal("0")
"""Annualized fraction (e.g. ``Decimal("0.03")`` == 3%/year). See module
docstring judgment call §1: a documented baseline-modeling assumption, not
an observed rate."""


@dataclass(frozen=True)
class BacktestMetrics:
    """The full BT-006 extended performance metric set for one ``BacktestResult``.

    ``performance`` is a complete ``domain.valuation.models.PerformanceSnapshot``
    -- ``total_return_pct``/``max_drawdown_pct``/``annualized_volatility``
    computed by BE-003's ``compute_performance_snapshot``; ``sharpe_ratio``/
    ``sortino_ratio``/``benchmark_return_pct`` filled in by this module.
    """

    portfolio_id: UUID
    start_date: date
    end_date: date
    initial_cash: Decimal
    final_equity: Decimal
    performance: PerformanceSnapshot
    win_rate: Decimal | None
    profit_loss_ratio: Decimal | None
    closed_trade_count: int
    winning_trade_count: int
    losing_trade_count: int
    turnover: Decimal | None
    trade_count: int
    risk_free_rate_pct: Decimal
    mar_pct: Decimal
    replaced_risk_checks: tuple[str, ...]


def compute_backtest_metrics(
    result: BacktestResult,
    *,
    as_of: date | None = None,
    risk_free_rate_pct: Decimal = DEFAULT_RISK_FREE_RATE_PCT,
    mar_pct: Decimal | None = None,
) -> BacktestMetrics:
    """Compute the full BT-006 metric set from ``result``.

    ``as_of`` defaults to ``result.config.end_date`` (the natural "as of"
    date for a completed backtest run). ``risk_free_rate_pct``/``mar_pct``
    are annualized fractions -- see module docstring judgment calls §1/§2
    for their defaults and rationale. Raises ``ValueError`` if
    ``result.equity_curve`` is empty (mirrors
    ``compute_performance_snapshot``'s own failure path -- there is nothing
    to compute a performance summary from).
    """
    if not result.equity_curve:
        raise ValueError("result.equity_curve must not be empty")

    resolved_as_of = as_of if as_of is not None else result.config.end_date
    resolved_mar_pct = mar_pct if mar_pct is not None else risk_free_rate_pct

    base_snapshot = compute_performance_snapshot(
        result.portfolio_id, resolved_as_of, result.equity_curve
    )
    navs = [snap.net_asset_value for snap in result.equity_curve]

    performance = replace(
        base_snapshot,
        sharpe_ratio=_sharpe_ratio(navs, risk_free_rate_pct),
        sortino_ratio=_sortino_ratio(navs, resolved_mar_pct),
        benchmark_return_pct=_benchmark_return_pct(result.benchmark_price_series),
    )

    filled_trades = [t for t in result.trades if t.status == "filled"]
    win_rate, profit_loss_ratio, closed_count, win_count, loss_count = _fifo_realized_pnl_stats(
        filled_trades
    )
    turnover = _turnover(filled_trades, result.equity_curve)

    return BacktestMetrics(
        portfolio_id=result.portfolio_id,
        start_date=result.config.start_date,
        end_date=result.config.end_date,
        initial_cash=result.config.initial_cash,
        final_equity=result.equity_curve[-1].net_asset_value,
        performance=performance,
        win_rate=win_rate,
        profit_loss_ratio=profit_loss_ratio,
        closed_trade_count=closed_count,
        winning_trade_count=win_count,
        losing_trade_count=loss_count,
        turnover=turnover,
        trade_count=len(filled_trades),
        risk_free_rate_pct=risk_free_rate_pct,
        mar_pct=resolved_mar_pct,
        replaced_risk_checks=result.replaced_risk_checks,
    )


# ---------------------------------------------------------------------------
# Sharpe / Sortino
# ---------------------------------------------------------------------------


def _daily_returns(navs: list[Decimal]) -> list[Decimal] | None:
    """Day-over-day simple returns; ``None`` if any base NAV is zero (undefined return)."""
    returns: list[Decimal] = []
    for earlier, later in zip(navs, navs[1:], strict=False):
        if earlier == _ZERO:
            return None
        returns.append((later / earlier) - _ONE)
    return returns


def _sharpe_ratio(navs: list[Decimal], risk_free_rate_pct: Decimal) -> Decimal | None:
    """Annualized Sharpe ratio: ``sqrt(365) * mean(daily_excess) / stdev(daily_return)``.

    See module docstring judgment call §3 for the annualization convention.
    ``None`` when fewer than ``_MIN_RETURNS_FOR_RATIO`` return observations
    exist, any daily return is undefined (zero base NAV), or the return
    series has zero volatility (division would be undefined).
    """
    returns = _daily_returns(navs)
    if returns is None or len(returns) < _MIN_RETURNS_FOR_RATIO:
        return None

    daily_rf = risk_free_rate_pct / _CALENDAR_DAYS_PER_YEAR
    float_returns = [float(r) for r in returns]
    float_excess = [float(r - daily_rf) for r in returns]

    try:
        daily_stdev = stdev(float_returns)
    except StatisticsError:
        return None
    if daily_stdev == 0.0:
        return None

    sharpe = (fmean(float_excess) / daily_stdev) * math.sqrt(365.0)
    return Decimal(str(sharpe))


def _sortino_ratio(navs: list[Decimal], mar_pct: Decimal) -> Decimal | None:
    """Annualized Sortino ratio: ``sqrt(365) * mean(daily_excess_over_mar) / downside_deviation``.

    Downside deviation is the root-mean-square of ``min(daily_excess_over_mar, 0)``
    across *all* return observations (standard semi-deviation definition --
    days at/above the MAR contribute ``0``, not excluded from the count).
    ``None`` under the same conditions as ``_sharpe_ratio``, plus when the
    downside deviation is exactly zero (no observation fell below the MAR).
    """
    returns = _daily_returns(navs)
    if returns is None or len(returns) < _MIN_RETURNS_FOR_RATIO:
        return None

    daily_mar = mar_pct / _CALENDAR_DAYS_PER_YEAR
    float_excess = [float(r - daily_mar) for r in returns]
    downside_sq = [min(x, 0.0) ** 2 for x in float_excess]
    downside_deviation = math.sqrt(fmean(downside_sq))
    if downside_deviation == 0.0:
        return None

    sortino = (fmean(float_excess) / downside_deviation) * math.sqrt(365.0)
    return Decimal(str(sortino))


# ---------------------------------------------------------------------------
# Benchmark comparison
# ---------------------------------------------------------------------------


def _benchmark_return_pct(
    series: tuple[tuple[date, Decimal], ...] | None,
) -> Decimal | None:
    """Buy-and-hold total return of the benchmark series: ``close[-1]/close[0] - 1``.

    ``None`` when no benchmark is configured (``series is None``), the
    series has fewer than two points, or the starting close is zero
    (undefined return).
    """
    if series is None or len(series) < 2:
        return None
    start_close = series[0][1]
    end_close = series[-1][1]
    if start_close == _ZERO:
        return None
    return (end_close / start_close) - _ONE


# ---------------------------------------------------------------------------
# FIFO realized P&L -> win_rate / profit_loss_ratio
# ---------------------------------------------------------------------------


def _trade_sort_key(trade: TradeRecord) -> date:
    assert trade.trade_date is not None
    return trade.trade_date


def _fifo_realized_pnl_stats(
    filled_trades: list[TradeRecord],
) -> tuple[Decimal | None, Decimal | None, int, int, int]:
    """FIFO-match sells against the oldest open buys per ``asset_id``.

    Returns ``(win_rate, profit_loss_ratio, closed_trade_count,
    winning_trade_count, losing_trade_count)``. See module docstring
    judgment call §4 for the gross-of-commission realized P&L definition
    and the exact ``None`` conditions.
    """
    # Stable-sort by trade_date; ties keep the original (engine trade_seq)
    # order, which is already chronological within a day.
    dated_trades = [t for t in filled_trades if t.trade_date is not None]
    ordered = sorted(dated_trades, key=_trade_sort_key)

    open_lots: dict[str, list[list[Decimal]]] = {}
    wins: list[Decimal] = []
    losses: list[Decimal] = []
    ties = 0

    for trade in ordered:
        assert trade.fill_price is not None  # guaranteed by status == "filled"
        lots = open_lots.setdefault(trade.asset_id, [])

        if trade.side == "buy":
            lots.append([trade.quantity, trade.fill_price])
            continue

        # side == "sell": close against the oldest open buy lot(s) for this asset.
        remaining = trade.quantity
        while remaining > _ZERO and lots:
            lot_qty, lot_price = lots[0]
            matched = min(lot_qty, remaining)
            realized = matched * (trade.fill_price - lot_price)
            if realized > _ZERO:
                wins.append(realized)
            elif realized < _ZERO:
                losses.append(realized)
            else:
                ties += 1

            remaining -= matched
            lot_qty -= matched
            if lot_qty == _ZERO:
                lots.pop(0)
            else:
                lots[0][0] = lot_qty
        # A sell with no matching open buy lot (more sold than ever bought)
        # is not expected for a long-only, no-short engine (CONTRACT-004),
        # but if it occurs, the unmatched remainder simply produces no
        # closed-lot record here -- never a fabricated realized P&L.

    closed_count = len(wins) + len(losses) + ties
    if closed_count == 0:
        return None, None, 0, 0, 0

    win_rate = Decimal(len(wins)) / Decimal(closed_count)

    profit_loss_ratio: Decimal | None
    if not wins or not losses:
        profit_loss_ratio = None
    else:
        avg_win = sum(wins, _ZERO) / Decimal(len(wins))
        avg_loss = sum(losses, _ZERO) / Decimal(len(losses))
        profit_loss_ratio = avg_win / abs(avg_loss)

    return win_rate, profit_loss_ratio, closed_count, len(wins), len(losses)


# ---------------------------------------------------------------------------
# Turnover
# ---------------------------------------------------------------------------


def _turnover(
    filled_trades: list[TradeRecord],
    equity_curve: tuple[ValuationSnapshot, ...],
) -> Decimal | None:
    """``sum(filled notional) / mean(NAV)`` over the period. See judgment call §5."""
    if not equity_curve:
        return None

    total_notional = _ZERO
    for trade in filled_trades:
        assert trade.fill_price is not None
        total_notional += trade.quantity * trade.fill_price

    navs = [snap.net_asset_value for snap in equity_curve]
    mean_nav = sum(navs, _ZERO) / Decimal(len(navs))
    if mean_nav == _ZERO:
        return None
    return total_notional / mean_nav


__all__ = [
    "DEFAULT_RISK_FREE_RATE_PCT",
    "BacktestMetrics",
    "compute_backtest_metrics",
]
