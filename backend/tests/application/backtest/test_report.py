"""Unit tests for application.backtest.report (BT-007)."""

from __future__ import annotations

from dataclasses import replace as dc_replace
from datetime import UTC, date, datetime, timedelta
from decimal import Decimal
from pathlib import Path
from typing import Literal
from uuid import UUID, uuid4

import pytest

from application.backtest.config import BacktestConfig
from application.backtest.metrics import compute_backtest_metrics
from application.backtest.models import BacktestResult, TradeRecord, TradeStatus
from application.backtest.reconciliation import (
    Discrepancy,
    ReconciliationResult,
    reconcile_backtest_result,
)
from application.backtest.report import (
    backtest_report_filename,
    render_backtest_report,
    write_backtest_report,
)
from domain.valuation.models import ValuationSnapshot

_PORTFOLIO_ID: UUID = uuid4()
_START = date(2026, 1, 1)
_END = date(2026, 1, 5)
_ASSET = "equity:nasdaq:NVDA"
_ASSET2 = "crypto:bybit:BTC-USDT"
_GENERATED_AT = datetime(2026, 1, 6, 0, 0, 0, tzinfo=UTC)


def _snapshot(day_offset: int, *, cash: str, positions: str) -> ValuationSnapshot:
    cash_dec = Decimal(cash)
    positions_dec = Decimal(positions)
    return ValuationSnapshot(
        portfolio_id=_PORTFOLIO_ID,
        valuation_date=_START + timedelta(days=day_offset),
        positions_value=positions_dec,
        cash_value=cash_dec,
        net_asset_value=cash_dec + positions_dec,
        base_currency="USD",
        price_status="fresh",
    )


def _trade(
    *,
    trade_id: str,
    asset_id: str,
    side: Literal["buy", "sell"],
    status: TradeStatus,
    decision_day_offset: int,
    planned_quantity: str = "0",
    quantity: str = "0",
    fill_price: str | None = None,
    commission: str = "0",
    trade_day_offset: int | None = None,
    reason: tuple[str, ...] = (),
) -> TradeRecord:
    decision_date = _START + timedelta(days=decision_day_offset)
    trade_date = _START + timedelta(days=trade_day_offset) if trade_day_offset is not None else None
    return TradeRecord(
        trade_id=trade_id,
        portfolio_id=_PORTFOLIO_ID,
        asset_id=asset_id,
        symbol=asset_id.rsplit(":", maxsplit=1)[-1],
        side=side,
        status=status,
        planned_quantity=Decimal(planned_quantity),
        quantity=Decimal(quantity),
        fill_price=Decimal(fill_price) if fill_price is not None else None,
        commission=Decimal(commission),
        slippage=Decimal("0"),
        signal_score=None,
        reason=reason,
        decision_date=decision_date,
        trade_date=trade_date,
    )


def _full_result() -> BacktestResult:
    """A reconciliation-clean, 5-day result exercising every trade status.

    day0: no trades. cash=1000, positions=0, nav=1000.
    day1: buy 5 NVDA @ 100, commission=2 -> cash=498, positions_value=500, nav=998.
    day2: sell 5 NVDA @ 110, commission=1 -> cash=1047, positions_value=0, nav=1047.
          Also: a risk-rejected BTC buy, and a min-quantity-skipped BTC buy (neither
          moves cash/positions).
    day3: no fills. A precision-unavailable-skipped BTC buy (no cash/position effect).
    day4 (end_date): a no-next-day-skipped NVDA buy (no cash/position effect).
    """
    trades = (
        _trade(
            trade_id="buy1",
            asset_id=_ASSET,
            side="buy",
            status="filled",
            decision_day_offset=0,
            planned_quantity="5",
            quantity="5",
            fill_price="100",
            commission="2",
            trade_day_offset=1,
            reason=("technical trend positive",),
        ),
        _trade(
            trade_id="sell1",
            asset_id=_ASSET,
            side="sell",
            status="filled",
            decision_day_offset=1,
            planned_quantity="5",
            quantity="5",
            fill_price="110",
            commission="1",
            trade_day_offset=2,
            reason=("target weight reduced",),
        ),
        _trade(
            trade_id="rej1",
            asset_id=_ASSET2,
            side="buy",
            status="rejected",
            decision_day_offset=2,
            planned_quantity="0.5",
            reason=("order exceeds max_single_crypto_weight",),
        ),
        _trade(
            trade_id="skipmin1",
            asset_id=_ASSET2,
            side="buy",
            status="skipped_min_quantity",
            decision_day_offset=2,
            planned_quantity="0.0004",
            reason=("rounded quantity 0 is below min_quantity 0.001 | see precision.py",),
        ),
        _trade(
            trade_id="skipprec1",
            asset_id=_ASSET2,
            side="buy",
            status="skipped_precision_unavailable",
            decision_day_offset=3,
            planned_quantity="1",
            reason=("no instrument precision available",),
        ),
        _trade(
            trade_id="skipnext1",
            asset_id=_ASSET,
            side="buy",
            status="skipped_no_next_day",
            decision_day_offset=4,
            planned_quantity="1",
            reason=("no next day's open to execute at",),
        ),
    )
    equity_curve = (
        _snapshot(0, cash="1000", positions="0"),
        _snapshot(1, cash="498", positions="500"),
        _snapshot(2, cash="1047", positions="0"),
        _snapshot(3, cash="1047", positions="0"),
        _snapshot(4, cash="1047", positions="0"),
    )
    benchmark_price_series = (
        (_START, Decimal("100")),
        (_START + timedelta(days=1), Decimal("101")),
        (_START + timedelta(days=2), Decimal("102")),
        (_START + timedelta(days=3), Decimal("103")),
        (_END, Decimal("105")),
    )
    config = BacktestConfig(
        portfolio_id=_PORTFOLIO_ID,
        start_date=_START,
        end_date=_END,
        initial_cash=Decimal("1000"),
        precision_mode="restricted",
    )
    return BacktestResult(
        portfolio_id=_PORTFOLIO_ID,
        config=config,
        equity_curve=equity_curve,
        trades=trades,
        replaced_risk_checks=(
            "trading_hours",
            "manual_confirmation",
            "duplicate_open_order_conflict",
            "emergency_stop_switch",
        ),
        final_positions={},
        final_cash=Decimal("1047"),
        benchmark_price_series=benchmark_price_series,
    )


def _flat_result() -> BacktestResult:
    """A minimal 2-day, no-trade, no-benchmark result -- forces every optional metric to None."""
    start = date(2026, 2, 1)
    end = date(2026, 2, 2)
    equity_curve = (
        ValuationSnapshot(
            portfolio_id=_PORTFOLIO_ID,
            valuation_date=start,
            positions_value=Decimal("0"),
            cash_value=Decimal("500"),
            net_asset_value=Decimal("500"),
            base_currency="USD",
            price_status="fresh",
        ),
        ValuationSnapshot(
            portfolio_id=_PORTFOLIO_ID,
            valuation_date=end,
            positions_value=Decimal("0"),
            cash_value=Decimal("500"),
            net_asset_value=Decimal("500"),
            base_currency="USD",
            price_status="fresh",
        ),
    )
    config = BacktestConfig(
        portfolio_id=_PORTFOLIO_ID,
        start_date=start,
        end_date=end,
        initial_cash=Decimal("500"),
        precision_mode="unrestricted",
    )
    return BacktestResult(
        portfolio_id=_PORTFOLIO_ID,
        config=config,
        equity_curve=equity_curve,
        trades=(),
        replaced_risk_checks=(),
        final_positions={},
        final_cash=Decimal("500"),
        benchmark_price_series=None,
    )


class TestRenderBacktestReportFullFixture:
    def setup_method(self) -> None:
        self.result = _full_result()
        self.metrics = compute_backtest_metrics(self.result)
        self.reconciliation = reconcile_backtest_result(self.result)
        self.markdown = render_backtest_report(
            self.result, self.metrics, self.reconciliation, generated_at=_GENERATED_AT
        )

    def test_reconciliation_actually_passes_on_this_fixture(self) -> None:
        # Sanity check on the fixture itself, not the renderer -- if this
        # fails, the render assertions below about "PASSED"/"0 discrepancies"
        # would be testing a broken fixture, not the renderer.
        assert self.reconciliation.passed is True

    def test_summary_header_present(self) -> None:
        assert f"# Backtest Report -- {_PORTFOLIO_ID}" in self.markdown
        assert "Generated at: 2026-01-06T00:00:00Z" in self.markdown

    def test_config_section_has_every_field(self) -> None:
        md = self.markdown
        assert "## 1. Configuration and Matching Assumptions" in md
        assert f"| portfolio_id | {_PORTFOLIO_ID} |" in md
        assert "| start_date | 2026-01-01 |" in md
        assert "| end_date | 2026-01-05 |" in md
        assert "| initial_cash | 1000 |" in md
        assert "| base_currency | USD |" in md
        assert "| precision_mode | restricted |" in md
        assert "next day's **open**" in md or "next day's open" in md.replace("**", "")

    def test_performance_section_values(self) -> None:
        md = self.markdown
        assert "## 2. Performance Summary" in md
        assert "| Win rate | 100.00% |" in md
        assert "Profit/loss ratio | N/A |" in md  # no losses to divide by
        assert "Benchmark return | 5.00% |" in md
        assert "Not FX-converted" in md

    def test_status_overview_counts(self) -> None:
        md = self.markdown
        assert "| filled | 2 |" in md
        assert "| rejected | 1 |" in md
        assert "| skipped_min_quantity | 1 |" in md
        assert "| skipped_precision_unavailable | 1 |" in md
        assert "| skipped_no_next_day | 1 |" in md

    def test_executed_trades_table_has_fill_details(self) -> None:
        md = self.markdown
        assert "### 3.2 Executed trades" in md
        assert "buy1" in md
        assert "sell1" in md
        assert "technical trend positive" in md

    def test_unfilled_trades_table_shows_reasons(self) -> None:
        md = self.markdown
        assert "### 3.3 Rejected / skipped decisions" in md
        assert "rej1" in md
        assert "order exceeds max_single_crypto_weight" in md
        assert "skipprec1" in md
        assert "no instrument precision available" in md

    def test_pipe_character_in_reason_is_escaped(self) -> None:
        # raw reason text contains " | " -- must not break the Markdown table.
        assert "below min_quantity 0.001 \\| see precision.py" in self.markdown

    def test_risk_substitutions_prominent(self) -> None:
        md = self.markdown
        assert "## 4. Risk-Check Substitutions" in md
        assert "`trading_hours`" in md
        assert "`manual_confirmation`" in md
        assert "`duplicate_open_order_conflict`" in md
        assert "`emergency_stop_switch`" in md
        assert "must not assume" in md

    def test_reconciliation_section_passed(self) -> None:
        md = self.markdown
        assert "## 5. Reconciliation Summary" in md
        assert "**Result: PASSED**" in md
        assert "(0 discrepancies)" in md
        assert "Not checked" in md
        assert "目标权重" in md and "汇率" in md  # explicit gap naming, per reconciliation.py

    def test_precision_section_shows_skip_counts(self) -> None:
        md = self.markdown
        assert "## 6. Precision Mode" in md
        assert "precision_mode = restricted" in md
        assert "2 trade decision(s) were skipped for precision reasons" in md
        assert "`skipped_min_quantity`: 1" in md
        assert "`skipped_precision_unavailable`: 1" in md


class TestRenderBacktestReportNoneMetrics:
    def setup_method(self) -> None:
        self.result = _flat_result()
        self.metrics = compute_backtest_metrics(self.result)
        self.reconciliation = reconcile_backtest_result(self.result)
        self.markdown = render_backtest_report(
            self.result, self.metrics, self.reconciliation, generated_at=_GENERATED_AT
        )

    def test_reconciliation_passes_on_the_flat_fixture(self) -> None:
        assert self.reconciliation.passed is True

    def test_undefined_metrics_render_as_explicit_na_not_blank_or_zero(self) -> None:
        md = self.markdown
        assert "| Sharpe ratio | N/A" in md
        assert "| Sortino ratio | N/A" in md
        assert "| Annualized volatility | N/A" in md
        assert "| Win rate | N/A |" in md
        assert "Profit/loss ratio | N/A |" in md
        assert "| Benchmark return | N/A |" in md

    def test_turnover_is_a_real_zero_not_conflated_with_undefined(self) -> None:
        # Zero trades genuinely means zero turnover -- a real computed value,
        # distinct from the N/A markers above for metrics that are undefined.
        assert "| Turnover | 0.00% |" in self.markdown

    def test_no_trades_messages(self) -> None:
        md = self.markdown
        assert "No trades were filled during this run." in md
        assert "No decisions were rejected or skipped during this run." in md

    def test_no_risk_substitutions_message(self) -> None:
        assert "No checks were reported as substituted for this run." in self.markdown

    def test_unrestricted_precision_text(self) -> None:
        md = self.markdown
        assert "precision_mode = unrestricted" in md
        assert "fractional-share orders" in md
        # No skip-count line should appear when nothing was skipped for precision.
        assert "trade decision(s) were skipped for precision reasons" not in md


class TestRenderBacktestReportDiscrepancies:
    def test_discrepancies_are_rendered(self) -> None:
        result = _full_result()
        metrics = compute_backtest_metrics(result)
        bad_reconciliation = ReconciliationResult(
            passed=False,
            discrepancies=(
                Discrepancy(
                    valuation_date=date(2026, 1, 2),
                    field="cash_value",
                    expected=Decimal("498"),
                    actual=Decimal("500"),
                    detail="independently replayed cash does not match the recorded snapshot",
                ),
            ),
        )
        md = render_backtest_report(result, metrics, bad_reconciliation, generated_at=_GENERATED_AT)

        assert "**Result: FAILED**" in md
        assert "(1 discrepancies)" in md
        assert "| 2026-01-02 | cash_value | 498 | 500 |" in md
        assert "does not match the recorded snapshot" in md


class TestRenderBacktestReportEmptyReason:
    def test_empty_reason_renders_as_dash_not_blank(self) -> None:
        start = date(2026, 3, 1)
        end = date(2026, 3, 1)
        snapshot = ValuationSnapshot(
            portfolio_id=_PORTFOLIO_ID,
            valuation_date=start,
            positions_value=Decimal("0"),
            cash_value=Decimal("100"),
            net_asset_value=Decimal("100"),
            base_currency="USD",
            price_status="fresh",
        )
        trade = _trade(
            trade_id="norason1",
            asset_id=_ASSET,
            side="buy",
            status="rejected",
            decision_day_offset=0,
            planned_quantity="1",
            reason=(),
        )
        # decision_day_offset is relative to module-level _START; rebuild trade
        # manually with the right decision_date for this local start date.
        trade = dc_replace(trade, decision_date=start, trade_date=None)
        config = BacktestConfig(
            portfolio_id=_PORTFOLIO_ID, start_date=start, end_date=end, initial_cash=Decimal("100")
        )
        result = BacktestResult(
            portfolio_id=_PORTFOLIO_ID,
            config=config,
            equity_curve=(snapshot,),
            trades=(trade,),
            replaced_risk_checks=(),
            final_positions={},
            final_cash=Decimal("100"),
        )
        metrics = compute_backtest_metrics(result)
        reconciliation = reconcile_backtest_result(result)

        md = render_backtest_report(result, metrics, reconciliation, generated_at=_GENERATED_AT)
        assert "norason1" in md
        assert "—" in md


class TestRenderBacktestReportFailurePaths:
    def test_naive_generated_at_raises(self) -> None:
        result = _flat_result()
        metrics = compute_backtest_metrics(result)
        reconciliation = reconcile_backtest_result(result)
        with pytest.raises(ValueError, match="timezone-aware"):
            render_backtest_report(
                result, metrics, reconciliation, generated_at=datetime(2026, 1, 6)
            )


class TestBacktestReportFilename:
    def test_filename_format(self) -> None:
        name = backtest_report_filename(
            portfolio_id=_PORTFOLIO_ID,
            start_date=_START,
            end_date=_END,
            generated_at=datetime(2026, 1, 6, 12, 30, 45, tzinfo=UTC),
        )
        assert name == f"{_PORTFOLIO_ID}_2026-01-01_2026-01-05_20260106T123045Z.md"

    def test_filename_naive_generated_at_raises(self) -> None:
        with pytest.raises(ValueError, match="timezone-aware"):
            backtest_report_filename(
                portfolio_id=_PORTFOLIO_ID,
                start_date=_START,
                end_date=_END,
                generated_at=datetime(2026, 1, 6),
            )


class TestWriteBacktestReport:
    def test_writes_file_with_expected_name_and_content(self, tmp_path: Path) -> None:
        markdown = "# Backtest Report -- example\n"
        reports_dir = tmp_path / "backtest"
        path = write_backtest_report(
            markdown,
            portfolio_id=_PORTFOLIO_ID,
            start_date=_START,
            end_date=_END,
            generated_at=_GENERATED_AT,
            reports_dir=reports_dir,
        )

        expected_name = backtest_report_filename(
            portfolio_id=_PORTFOLIO_ID,
            start_date=_START,
            end_date=_END,
            generated_at=_GENERATED_AT,
        )
        assert path == reports_dir / expected_name
        assert path.exists()
        assert path.read_text(encoding="utf-8") == markdown

    def test_creates_missing_parent_directories(self, tmp_path: Path) -> None:
        reports_dir = tmp_path / "nested" / "backtest"
        assert not reports_dir.exists()

        write_backtest_report(
            "content",
            portfolio_id=_PORTFOLIO_ID,
            start_date=_START,
            end_date=_END,
            generated_at=_GENERATED_AT,
            reports_dir=reports_dir,
        )

        assert reports_dir.exists()
