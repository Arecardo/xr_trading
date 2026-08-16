"""Markdown backtest report rendering (BT-007).

Pure, downstream-consumer rendering of ``application.backtest.models.
BacktestResult`` + ``application.backtest.metrics.BacktestMetrics`` +
``application.backtest.reconciliation.ReconciliationResult`` -- this module
does not compute metrics, does not run reconciliation, and does not simulate
anything. It only projects three already-computed objects into a Markdown
document that a human can read without opening the source data.

Report structure follows the *spirit* of `doc/technical/09_logging_reports.md`
§6/§7's plan/daily-report templates (a summary up top, tables for
allocations/signals/risk events) adapted to a single backtest *run* rather
than one calendar day, per §5's "回测报告 | 按需" row (Markdown or HTML; this
module produces Markdown only -- see the BT-007 task report for why an HTML
variant was judged not worth the added maintenance surface for a first
cut). Section headers and prose are written in English, matching the rest of
this codebase's runtime-generated content (``TradeRecord.reason`` strings,
``RiskPolicy.replaced_checks()`` identifiers, and every other module's
docstrings/log messages are English; only ``doc/`` design prose is Chinese)
-- keeping the report in the same language as the identifiers/values it
embeds avoids an awkward code-switched document.

Seven sections (the original six required by
`05_backtest_reporting_and_reconciliation.md` BT-007 row + task brief, plus
a 2026-08-16 addition, §7), each implemented by one ``_render_*`` helper
below:

1. ``_render_config_section`` -- full ``BacktestConfig`` + the matching-rule
   prose `08_backtest_engine.md` §6 requires be written into the report
   ("具体规则必须写入回测报告，避免回测与实盘执行假设不一致").
2. ``_render_performance_section`` -- every ``BacktestMetrics`` field,
   captioned with its governing assumption (risk-free rate, MAR, turnover
   definition, gross-of-commission win/loss, benchmark currency caveat) --
   see ``metrics.py``'s module docstring judgment calls §1-§6, faithfully
   restated here, not paraphrased away.
3. ``_render_trade_records_section`` -- a status-count overview, then
   *separate* executed/not-executed tables (see that function's docstring
   for why split, not combined).
4. ``_render_risk_substitutions_section`` -- ``replaced_risk_checks``,
   surfaced as its own prominent section, never folded into a footnote.
5. ``_render_reconciliation_section`` -- pass/fail + every ``Discrepancy``,
   plus an explicit statement of ``reconciliation.py``'s documented
   checked/not-checked scope (never implying full five-field -- 目标权重/
   持仓/现金/汇率/净值 -- coverage that was not actually performed).
6. ``_render_precision_section`` -- ``precision_mode`` used, plus (when
   ``restricted``) a visible count of ``skipped_min_quantity``/
   ``skipped_precision_unavailable`` trades, not buried only in the trade
   table.
7. ``_render_daily_detail_section`` (2026-08-16 addition) -- per-day
   ``target_weights``/``position_values``/``fx_rates_applied`` from
   ``BacktestResult.daily_detail``, making the M4 gate's 目标权重/汇率
   dimensions human-readable, not just internally cross-checked in §5.
   Renders a "not available" note instead of a table when ``daily_detail``
   is empty (a hand-built or pre-2026-08-16 result).

No wall-clock time is read anywhere in this module: ``generated_at`` is a
required, caller-supplied, timezone-aware ``datetime`` (mirrors
``backtest.market_data_client``'s ``_require_aware_utc`` fail-closed
pattern) -- this mirrors BT-003/BT-006's reproducibility discipline: the
same three inputs always render the same Markdown string.
"""

from __future__ import annotations

from datetime import UTC, date, datetime
from decimal import ROUND_HALF_UP, Decimal
from pathlib import Path
from uuid import UUID

from .metrics import BacktestMetrics
from .models import BacktestResult, TradeRecord, TradeStatus
from .reconciliation import Discrepancy, ReconciliationResult

_NA = "N/A"

_ALL_TRADE_STATUSES: tuple[TradeStatus, ...] = (
    "filled",
    "rejected",
    "skipped_min_quantity",
    "skipped_precision_unavailable",
    "skipped_no_next_day",
)

_TRADE_STATUS_LEGEND: dict[TradeStatus, str] = {
    "filled": "the order executed; quantity/fill_price are populated.",
    "rejected": (
        "the domain risk policy refused the order (RiskPolicy.check_order) -- never filled."
    ),
    "skipped_min_quantity": (
        "precision_mode='restricted' rounded the desired quantity to zero or below the "
        "instrument's min_quantity -- never filled."
    ),
    "skipped_precision_unavailable": (
        "precision_mode='restricted' but no usable instrument precision was available "
        "(fail-closed) -- never filled."
    ),
    "skipped_no_next_day": (
        "the decision was approved on the last day of the run, so there was no next day's "
        "open to execute at -- never filled."
    ),
}

_PRECISION_RELATED_SKIP_STATUSES: tuple[TradeStatus, ...] = (
    "skipped_min_quantity",
    "skipped_precision_unavailable",
)


# ---------------------------------------------------------------------------
# Formatting helpers -- Decimal/date -> fixed-point strings, never float
# ---------------------------------------------------------------------------


def _fmt_decimal(value: Decimal | None) -> str:
    """Fixed-point string, never scientific notation, never a Python float.

    ``None`` renders as an explicit ``"N/A"`` marker -- never a blank cell
    or a fabricated ``0`` (python-backend-standards.md §1/§6).
    """
    if value is None:
        return _NA
    return format(value, "f")


def _fmt_pct(value: Decimal | None, *, decimals: int = 2) -> str:
    """``value`` (a fraction, e.g. ``Decimal("0.15")`` == 15%) as a fixed-point percentage.

    Rounding is explicit (``ROUND_HALF_UP``) rather than relying on the
    ambient Decimal context's default rounding mode, so this function's
    output does not silently vary with caller-side context configuration.
    """
    if value is None:
        return _NA
    exponent = Decimal(1).scaleb(-decimals)
    quantized = (value * Decimal(100)).quantize(exponent, rounding=ROUND_HALF_UP)
    return f"{format(quantized, 'f')}%"


def _fmt_ratio(value: Decimal | None, *, decimals: int = 4) -> str:
    """Fixed-point ratio (Sharpe/Sortino/profit-loss-ratio), rounded for display only."""
    if value is None:
        return _NA
    exponent = Decimal(1).scaleb(-decimals)
    quantized = value.quantize(exponent, rounding=ROUND_HALF_UP)
    return format(quantized, "f")


def _fmt_date(value: date) -> str:
    return value.isoformat()


def _fmt_optional_date(value: date | None) -> str:
    return value.isoformat() if value is not None else _NA


def _fmt_reason(reason: tuple[str, ...]) -> str:
    return _escape_cell("; ".join(reason)) if reason else "\u2014"


def _escape_cell(text: str) -> str:
    """Escape Markdown table-breaking characters in free-text cell content."""
    return text.replace("|", "\\|").replace("\n", " ")


def _require_aware_utc(moment: datetime, *, param_name: str) -> datetime:
    if moment.tzinfo is None:
        raise ValueError(f"{param_name} must be timezone-aware (UTC); got a naive datetime")
    return moment.astimezone(UTC)


def _fmt_generated_at(moment: datetime) -> str:
    return moment.strftime("%Y-%m-%dT%H:%M:%SZ")


# ---------------------------------------------------------------------------
# Section renderers
# ---------------------------------------------------------------------------


def _render_config_section(result: BacktestResult) -> str:
    config = result.config
    lines = [
        "## 1. Configuration and Matching Assumptions",
        "",
        "| Field | Value |",
        "| --- | --- |",
        f"| portfolio_id | {result.portfolio_id} |",
        f"| start_date | {_fmt_date(config.start_date)} |",
        f"| end_date | {_fmt_date(config.end_date)} |",
        f"| initial_cash | {_fmt_decimal(config.initial_cash)} |",
        f"| base_currency | {config.base_currency} |",
        f"| commission_pct | {_fmt_pct(config.commission_pct, decimals=4)} |",
        f"| min_commission | {_fmt_decimal(config.min_commission)} |",
        f"| slippage_pct | {_fmt_pct(config.slippage_pct, decimals=4)} |",
        f"| precision_mode | {config.precision_mode} |",
        f"| rebalance_threshold_pct | {_fmt_pct(config.rebalance_threshold_pct)} |",
        "",
        "**Matching rule actually implemented by the engine** "
        "(`application.backtest.engine`, per `08_backtest_engine.md` §6): an order "
        "approved from day D's close-of-day signal is executed at day D+1's **open** "
        "price, adjusted by slippage (added for buys, subtracted for sells) -- never "
        "the same day's close, to avoid look-ahead bias. Commission is "
        "`max(notional * commission_pct, min_commission)`. A decision made on the "
        "run's last day has no next day to execute at and is recorded as "
        "`skipped_no_next_day` rather than silently dropped.",
        "",
    ]
    return "\n".join(lines)


def _render_performance_section(metrics: BacktestMetrics) -> str:
    perf = metrics.performance
    lines = [
        "## 2. Performance Summary",
        "",
        "| Metric | Value | Assumption / definition |",
        "| --- | --- | --- |",
        f"| Period | {_fmt_date(metrics.start_date)} to {_fmt_date(metrics.end_date)} | |",
        f"| Initial cash | {_fmt_decimal(metrics.initial_cash)} | |",
        f"| Final equity | {_fmt_decimal(metrics.final_equity)} | |",
        (
            f"| Total return | {_fmt_pct(perf.total_return_pct)} "
            "| Simple (not annualized) return, first-to-last equity_curve NAV. |"
        ),
        (
            f"| Max drawdown | {_fmt_pct(perf.max_drawdown_pct)} "
            "| Largest peak-to-trough decline over the period. |"
        ),
        (
            f"| Annualized volatility | {_fmt_pct(perf.annualized_volatility)} "
            "| stdev(daily NAV returns) * sqrt(365) (365-day/year, per DEC-002). |"
        ),
        (
            f"| Sharpe ratio | {_fmt_ratio(perf.sharpe_ratio)} "
            f"| Risk-free rate assumed {_fmt_pct(metrics.risk_free_rate_pct)}/year "
            "(0% default unless overridden -- this codebase has no real "
            "risk-free-rate data source; not an observed rate). "
            "sqrt(365)-annualized daily excess return / daily stdev. |"
        ),
        (
            f"| Sortino ratio | {_fmt_ratio(perf.sortino_ratio)} "
            f"| Minimum acceptable return (MAR) assumed {_fmt_pct(metrics.mar_pct)}/year "
            "(defaults to the risk-free rate when not separately configured). "
            "sqrt(365)-annualized daily excess-over-MAR / downside deviation. |"
        ),
        (
            f"| Win rate | {_fmt_pct(metrics.win_rate)} "
            "| Share of FIFO-matched closed round trips with positive realized P&L, "
            "**gross of commission** (price movement only). |"
        ),
        (
            f"| Profit/loss ratio | {_fmt_ratio(metrics.profit_loss_ratio)} "
            "| avg(win amount) / avg(loss amount), both gross of commission. N/A if there "
            "are no wins or no losses to divide by. |"
        ),
        (
            f"| Closed trade count | {metrics.closed_trade_count} "
            f"(wins {metrics.winning_trade_count} / losses {metrics.losing_trade_count}) "
            "| FIFO-matched buy/sell round trips among filled trades only. |"
        ),
        (
            f"| Turnover | {_fmt_pct(metrics.turnover)} "
            "| sum(filled trade notional) / mean(NAV) over the period -- a simple "
            "total-notional proxy, not one-way (buys-only) turnover. |"
        ),
        (
            f"| Trade count | {metrics.trade_count} "
            "| Filled trades only -- rejected/skipped decisions are excluded from this "
            "count (see §3) but never discarded from the record. |"
        ),
        (
            f"| Benchmark return | {_fmt_pct(perf.benchmark_return_pct)} "
            "| Buy-and-hold return of the configured benchmark's own close series. "
            "N/A when no benchmark is configured. **Not FX-converted**: this figure is "
            "only directly comparable to the portfolio's own base-currency return when "
            "the benchmark's quote currency equals the portfolio's base_currency -- "
            "otherwise it mixes two currencies' returns without adjustment. |"
        ),
        "",
    ]
    return "\n".join(lines)


def _render_status_overview(trades: tuple[TradeRecord, ...]) -> str:
    counts = dict.fromkeys(_ALL_TRADE_STATUSES, 0)
    for trade in trades:
        counts[trade.status] += 1

    lines = [
        "### 3.1 Status overview",
        "",
        "| Status | Count | Meaning |",
        "| --- | --- | --- |",
    ]
    for status in _ALL_TRADE_STATUSES:
        lines.append(f"| {status} | {counts[status]} | {_TRADE_STATUS_LEGEND[status]} |")
    lines.append("")
    return "\n".join(lines)


def _render_executed_trades_table(trades: tuple[TradeRecord, ...]) -> str:
    filled = [t for t in trades if t.status == "filled"]
    lines = [
        "### 3.2 Executed trades",
        "",
    ]
    if not filled:
        lines.append("No trades were filled during this run.")
        lines.append("")
        return "\n".join(lines)

    lines += [
        "| trade_id | asset_id | side | decision_date | trade_date | quantity | "
        "fill_price | commission | slippage | signal_score | reason |",
        "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |",
    ]
    for t in filled:
        lines.append(
            f"| {t.trade_id} | {t.asset_id} | {t.side} | {_fmt_date(t.decision_date)} | "
            f"{_fmt_optional_date(t.trade_date)} | {_fmt_decimal(t.quantity)} | "
            f"{_fmt_decimal(t.fill_price)} | {_fmt_decimal(t.commission)} | "
            f"{_fmt_decimal(t.slippage)} | {_fmt_decimal(t.signal_score)} | "
            f"{_fmt_reason(t.reason)} |"
        )
    lines.append("")
    return "\n".join(lines)


def _render_unfilled_trades_table(trades: tuple[TradeRecord, ...]) -> str:
    unfilled = [t for t in trades if t.status != "filled"]
    lines = [
        "### 3.3 Rejected / skipped decisions",
        "",
        "These decisions never executed. They are listed explicitly, with their reason, "
        "so a reader can see *why* -- not just that no fill happened "
        '(`09_logging_reports.md` §10: "风控拦截必须出现在复盘报告中").',
        "",
    ]
    if not unfilled:
        lines.append("No decisions were rejected or skipped during this run.")
        lines.append("")
        return "\n".join(lines)

    lines += [
        "| trade_id | asset_id | side | status | decision_date | planned_quantity | "
        "signal_score | reason |",
        "| --- | --- | --- | --- | --- | --- | --- | --- |",
    ]
    for t in unfilled:
        lines.append(
            f"| {t.trade_id} | {t.asset_id} | {t.side} | {t.status} | "
            f"{_fmt_date(t.decision_date)} | {_fmt_decimal(t.planned_quantity)} | "
            f"{_fmt_decimal(t.signal_score)} | {_fmt_reason(t.reason)} |"
        )
    lines.append("")
    return "\n".join(lines)


def _render_trade_records_section(result: BacktestResult) -> str:
    """Status overview, then split executed/not-executed tables.

    Split rather than combined: a filled row's meaningful columns
    (quantity/fill_price/commission/slippage/trade_date) are exactly the
    columns a rejected/skipped row has none of (zeroed or ``None`` by
    ``TradeRecord``'s own documented contract), and a rejected/skipped row's
    single most important column -- ``status`` plus its ``reason`` -- would
    be a redundant, always-"filled" column in an executed-only table. Two
    narrower, denser tables read better than one wide table half-full of
    "N/A"/"\u2014" cells.
    """
    lines = [
        "## 3. Trade Records",
        "",
        _render_status_overview(result.trades),
        _render_executed_trades_table(result.trades),
        _render_unfilled_trades_table(result.trades),
    ]
    return "\n".join(lines)


def _render_risk_substitutions_section(result: BacktestResult) -> str:
    lines = [
        "## 4. Risk-Check Substitutions",
        "",
    ]
    if not result.replaced_risk_checks:
        lines.append(
            "No checks were reported as substituted for this run. This does **not** by "
            "itself mean paper/live-equivalent enforcement -- it means "
            "`RiskPolicy.replaced_checks()` returned an empty tuple for the configured "
            "environment; consult the risk policy implementation for what it does and does "
            "not structurally enforce."
        )
    else:
        lines.append(
            "**The following risk checks were NOT run for real in this backtest and were "
            "instead passed through by an alternate/no-op implementation.** A reader must "
            "not assume this run's risk enforcement is identical to paper/live:"
        )
        lines.append("")
        for check in result.replaced_risk_checks:
            lines.append(f"- `{check}`")
    lines.append("")
    return "\n".join(lines)


def _render_reconciliation_section(
    result: BacktestResult, reconciliation: ReconciliationResult
) -> str:
    status = "PASSED" if reconciliation.passed else "FAILED"
    has_daily_detail = bool(result.daily_detail)
    lines = [
        "## 5. Reconciliation Summary",
        "",
        f"**Result: {status}** ({len(reconciliation.discrepancies)} discrepancies)",
        "",
        "Independently re-derived and checked against `BacktestResult`, day by day where "
        "applicable, using exact Decimal equality (`application.backtest.reconciliation`):",
        "",
        "- Cash, every day (replayed from filled trades vs. the recorded snapshot).",
        "- NAV internal consistency, every day (`positions_value + cash_value == "
        "net_asset_value`).",
        "- Final positions (once, independently accumulated per asset).",
        "- Final cash (once, independently replayed).",
    ]
    if has_daily_detail:
        lines += [
            "- Per-asset position value vs. the aggregate `positions_value` total, every day.",
            "- Zero-quantity/zero-value consistency between the independent trade replay and "
            "the recorded per-asset position values, every day.",
            "- Target-weight bounds (`[0, 1]`, sums to exactly 1 including the cash key), "
            "every day a target weight was recorded.",
            "- FX rate positivity, every day a rate was recorded.",
        ]
    else:
        lines.append(
            "- *(This result has no `daily_detail` -- see §7. The four checks above that "
            "depend on it were not run for this result.)*"
        )
    lines += [
        "",
        "**Not checked**, a genuine remaining gap (`application.backtest.reconciliation` "
        "module docstring): none of the above independently recomputes a per-asset dollar "
        "value or FX rate from a freshly-loaded, third-party price source -- doing so would "
        "require re-fetching the same market data `BacktestEngine` already used, which is not "
        "really an independent check. There is also no check that a `quote_currency`-bearing "
        "asset actually has an FX rate recorded (`BacktestResult` does not carry per-asset "
        "currency), and no check that a recorded `target_weights` entry actually caused the "
        "trade decided that day (would need each asset's per-day price, not just its dollar "
        "position value). This report does not claim independent numeric reconciliation "
        "beyond what is listed above.",
        "",
    ]
    if reconciliation.discrepancies:
        lines += [
            "| valuation_date | field | expected | actual | detail |",
            "| --- | --- | --- | --- | --- |",
        ]
        for d in reconciliation.discrepancies:
            lines.append(_render_discrepancy_row(d))
        lines.append("")
    return "\n".join(lines)


def _render_discrepancy_row(d: Discrepancy) -> str:
    return (
        f"| {_fmt_date(d.valuation_date)} | {d.field} | {_fmt_decimal(d.expected)} | "
        f"{_fmt_decimal(d.actual)} | {_escape_cell(d.detail)} |"
    )


def _render_precision_section(result: BacktestResult) -> str:
    config = result.config
    lines = [
        "## 6. Precision Mode",
        "",
        f"`precision_mode = {config.precision_mode}`",
        "",
    ]
    if config.precision_mode == "unrestricted":
        lines.append(
            "Fill quantities were recorded at unrounded `Decimal` precision, ignoring each "
            "instrument's real `lot_size`/`quantity_scale`/`min_quantity`. This does **not** "
            "reflect what a real broker order (Longbridge/Bybit) would accept -- Longbridge "
            "US equities in particular do not support fractional-share orders. Use "
            "`precision_mode='restricted'` to validate execution assumptions against real "
            "lot constraints before trusting this run's fills as paper/live-representative."
        )
    else:
        skip_counts = {
            status: sum(1 for t in result.trades if t.status == status)
            for status in _PRECISION_RELATED_SKIP_STATUSES
        }
        total_precision_skips = sum(skip_counts.values())
        lines.append(
            "Fill quantities were rounded to each instrument's real `lot_size`/"
            "`quantity_scale`, and orders below `min_quantity` after rounding were skipped, "
            "simulating the real broker order constraints (`08_backtest_engine.md` §3/§6)."
        )
        lines.append("")
        lines.append(
            f"**{total_precision_skips} trade decision(s) were skipped for precision "
            "reasons** (also itemized in §3.1/§3.3):"
        )
        lines.append("")
        for status in _PRECISION_RELATED_SKIP_STATUSES:
            lines.append(f"- `{status}`: {skip_counts[status]}")
    lines.append("")
    return "\n".join(lines)


def _render_daily_detail_section(result: BacktestResult) -> str:
    """Per-day target weights / per-asset position values / FX rates (see `daily_detail`).

    Makes the M4 gate's 目标权重/汇率 dimensions actually *readable* by a
    human, not just internally cross-checked by `application.backtest
    .reconciliation` -- an auditor should be able to see what the strategy
    proposed and what FX rate was applied on any given day without opening
    the raw `BacktestResult` in a debugger.
    """
    lines = ["## 7. Daily Detail (Target Weights / Position Values / FX Rates)", ""]
    if not result.daily_detail:
        lines.append(
            "Not available for this result -- `daily_detail` is empty (a hand-built or "
            "pre-2026-08-16 `BacktestResult`). `application.backtest.engine.BacktestEngine"
            ".run()` always populates this for a real backtest."
        )
        lines.append("")
        return "\n".join(lines)

    lines += [
        "| valuation_date | target_weights | position_values | fx_rates_applied |",
        "| --- | --- | --- | --- |",
    ]
    for day_detail in sorted(result.daily_detail, key=lambda d: d.valuation_date):
        lines.append(
            f"| {_fmt_date(day_detail.valuation_date)} | "
            f"{_fmt_weight_map(day_detail.target_weights)} | "
            f"{_fmt_decimal_map(day_detail.position_values)} | "
            f"{_fmt_decimal_map(day_detail.fx_rates_applied)} |"
        )
    lines.append("")
    return "\n".join(lines)


def _fmt_weight_map(values: dict[str, Decimal]) -> str:
    if not values:
        return "—"
    return _escape_cell(", ".join(f"{k}: {_fmt_pct(v)}" for k, v in sorted(values.items())))


def _fmt_decimal_map(values: dict[str, Decimal]) -> str:
    if not values:
        return "—"
    return _escape_cell(", ".join(f"{k}: {_fmt_decimal(v)}" for k, v in sorted(values.items())))


def _render_summary_section(
    result: BacktestResult,
    metrics: BacktestMetrics,
    reconciliation: ReconciliationResult,
    generated_at: datetime,
) -> str:
    reconciliation_status = "PASSED" if reconciliation.passed else "FAILED"
    lines = [
        f"# Backtest Report -- {result.portfolio_id}",
        "",
        f"- Generated at: {_fmt_generated_at(generated_at)}",
        f"- Portfolio ID: {result.portfolio_id}",
        f"- Period: {_fmt_date(metrics.start_date)} to {_fmt_date(metrics.end_date)}",
        f"- Initial cash -> final equity: {_fmt_decimal(metrics.initial_cash)} -> "
        f"{_fmt_decimal(metrics.final_equity)} "
        f"({_fmt_pct(metrics.performance.total_return_pct)})",
        f"- Max drawdown: {_fmt_pct(metrics.performance.max_drawdown_pct)}",
        f"- Sharpe / Sortino: {_fmt_ratio(metrics.performance.sharpe_ratio)} / "
        f"{_fmt_ratio(metrics.performance.sortino_ratio)}",
        f"- Precision mode: {result.config.precision_mode}",
        f"- Reconciliation: {reconciliation_status} "
        f"({len(reconciliation.discrepancies)} discrepancies)",
        f"- Risk checks substituted this run: {len(result.replaced_risk_checks)}",
        "",
    ]
    return "\n".join(lines)


# ---------------------------------------------------------------------------
# Public entry points
# ---------------------------------------------------------------------------


def render_backtest_report(
    result: BacktestResult,
    metrics: BacktestMetrics,
    reconciliation: ReconciliationResult,
    *,
    generated_at: datetime,
) -> str:
    """Render a full Markdown backtest report from three already-computed inputs.

    Pure function: no filesystem access, no wall-clock read (``generated_at``
    must be supplied by the caller, timezone-aware). The same three inputs
    always render the same string.

    Raises ``ValueError`` if ``generated_at`` is a naive ``datetime`` (no
    ``tzinfo``) -- fail-closed, mirrors ``backtest.market_data_client``'s
    own timestamp-handling convention, rather than silently assuming a
    timezone.
    """
    aware_generated_at = _require_aware_utc(generated_at, param_name="generated_at")

    sections = [
        _render_summary_section(result, metrics, reconciliation, aware_generated_at),
        _render_config_section(result),
        _render_performance_section(metrics),
        _render_trade_records_section(result),
        _render_risk_substitutions_section(result),
        _render_reconciliation_section(result, reconciliation),
        _render_precision_section(result),
        _render_daily_detail_section(result),
    ]
    return "\n".join(sections)


def backtest_report_filename(
    *,
    portfolio_id: UUID,
    start_date: date,
    end_date: date,
    generated_at: datetime,
) -> str:
    """Stable, informative filename: ``{portfolio_id}_{start}_{end}_{generated_at}.md``.

    ``generated_at`` (timezone-aware, formatted compactly as
    ``YYYYMMDDTHHMMSSZ``) is included so re-running the same backtest
    (identical portfolio/period, possibly different config or a bugfix)
    never silently overwrites a prior report -- every render gets its own
    file, and the portfolio_id/date-range prefix keeps files for the same
    run grouped and greppable in a directory listing.
    """
    aware_generated_at = _require_aware_utc(generated_at, param_name="generated_at")
    stamp = aware_generated_at.strftime("%Y%m%dT%H%M%SZ")
    return f"{portfolio_id}_{start_date.isoformat()}_{end_date.isoformat()}_{stamp}.md"


def write_backtest_report(
    markdown: str,
    *,
    portfolio_id: UUID,
    start_date: date,
    end_date: date,
    generated_at: datetime,
    reports_dir: Path = Path("reports/backtest"),
) -> Path:
    """Write an already-rendered report to ``reports_dir`` (`09_logging_reports.md` §9).

    Thin filesystem wrapper, deliberately separate from
    ``render_backtest_report`` so the rendering logic stays unit-testable
    without touching disk. Creates ``reports_dir`` (and parents) if it does
    not exist. Returns the path written.
    """
    reports_dir.mkdir(parents=True, exist_ok=True)
    filename = backtest_report_filename(
        portfolio_id=portfolio_id,
        start_date=start_date,
        end_date=end_date,
        generated_at=generated_at,
    )
    path = reports_dir / filename
    path.write_text(markdown, encoding="utf-8")
    return path


__all__ = [
    "backtest_report_filename",
    "render_backtest_report",
    "write_backtest_report",
]
