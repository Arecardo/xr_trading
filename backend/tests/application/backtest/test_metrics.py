"""Unit tests for application.backtest.metrics.compute_backtest_metrics (BT-006)."""

from __future__ import annotations

import math
from datetime import date, timedelta
from decimal import Decimal
from statistics import fmean, stdev
from typing import Literal
from uuid import UUID, uuid4

import pytest

from application.backtest.config import BacktestConfig
from application.backtest.metrics import compute_backtest_metrics
from application.backtest.models import BacktestResult, TradeRecord, TradeStatus
from domain.valuation.models import ValuationSnapshot

_PORTFOLIO_ID: UUID = uuid4()
_START = date(2026, 1, 1)
_ASSET = "equity:nasdaq:NVDA"


def _snapshot(
    day_offset: int, nav: str, *, cash: str | None = None, positions: str | None = None
) -> ValuationSnapshot:
    day = _START + timedelta(days=day_offset)
    nav_dec = Decimal(nav)
    cash_dec = Decimal(cash) if cash is not None else nav_dec
    positions_dec = Decimal(positions) if positions is not None else Decimal("0")
    return ValuationSnapshot(
        portfolio_id=_PORTFOLIO_ID,
        valuation_date=day,
        positions_value=positions_dec,
        cash_value=cash_dec,
        net_asset_value=nav_dec,
        base_currency="USD",
        price_status="fresh",
    )


def _config(end_offset: int) -> BacktestConfig:
    return BacktestConfig(
        portfolio_id=_PORTFOLIO_ID,
        start_date=_START,
        end_date=_START + timedelta(days=end_offset),
    )


def _trade(
    *,
    trade_id: str,
    side: Literal["buy", "sell"],
    quantity: str,
    fill_price: str | None,
    day_offset: int,
    commission: str = "0",
    status: TradeStatus = "filled",
    asset_id: str = _ASSET,
) -> TradeRecord:
    day = _START + timedelta(days=day_offset)
    is_filled = status == "filled"
    return TradeRecord(
        trade_id=trade_id,
        portfolio_id=_PORTFOLIO_ID,
        asset_id=asset_id,
        symbol="NVDA",
        side=side,
        status=status,
        planned_quantity=Decimal(quantity),
        quantity=Decimal(quantity) if is_filled else Decimal("0"),
        fill_price=Decimal(fill_price) if (is_filled and fill_price is not None) else None,
        commission=Decimal(commission) if is_filled else Decimal("0"),
        slippage=Decimal("0"),
        signal_score=None,
        reason=(),
        decision_date=day,
        trade_date=day if is_filled else None,
    )


def _result(
    *,
    equity_curve: tuple[ValuationSnapshot, ...],
    trades: tuple[TradeRecord, ...] = (),
    final_positions: dict[str, Decimal] | None = None,
    final_cash: Decimal = Decimal("0"),
    benchmark_price_series: tuple[tuple[date, Decimal], ...] | None = None,
) -> BacktestResult:
    return BacktestResult(
        portfolio_id=_PORTFOLIO_ID,
        config=_config(max(len(equity_curve) - 1, 0)),
        equity_curve=equity_curve,
        trades=trades,
        replaced_risk_checks=("manual_confirmation",),
        final_positions=final_positions or {},
        final_cash=final_cash,
        benchmark_price_series=benchmark_price_series,
    )


class TestReusesBEPerformanceSnapshot:
    def test_total_return_and_drawdown_come_from_compute_performance_snapshot(self) -> None:
        curve = (
            _snapshot(0, "1000"),
            _snapshot(1, "1100"),
            _snapshot(2, "990"),
        )
        result = _result(equity_curve=curve)

        metrics = compute_backtest_metrics(result)

        assert metrics.performance.total_return_pct == Decimal("990") / Decimal("1000") - 1
        assert metrics.performance.max_drawdown_pct == Decimal("990") / Decimal("1100") - 1

    def test_raises_on_empty_equity_curve(self) -> None:
        result = _result(equity_curve=())

        with pytest.raises(ValueError, match="equity_curve"):
            compute_backtest_metrics(result)

    def test_replaced_risk_checks_are_surfaced(self) -> None:
        result = _result(equity_curve=(_snapshot(0, "1000"),))

        metrics = compute_backtest_metrics(result)

        assert metrics.replaced_risk_checks == ("manual_confirmation",)


class TestSharpeAndSortinoRatios:
    def _curve(self) -> tuple[ValuationSnapshot, ...]:
        # Two returns: +10%, -10% -- enough variance for a defined stdev.
        return (_snapshot(0, "1000"), _snapshot(1, "1100"), _snapshot(2, "990"))

    def test_sharpe_is_none_with_fewer_than_two_returns(self) -> None:
        result = _result(equity_curve=(_snapshot(0, "1000"), _snapshot(1, "1100")))

        metrics = compute_backtest_metrics(result)

        assert metrics.performance.sharpe_ratio is None
        assert metrics.performance.sortino_ratio is None

    def test_zero_risk_free_rate_matches_hand_computed_sharpe(self) -> None:
        result = _result(equity_curve=self._curve())

        metrics = compute_backtest_metrics(result, risk_free_rate_pct=Decimal("0"))

        returns = [0.1, -0.1]
        expected = (fmean(returns) / stdev(returns)) * math.sqrt(365.0)
        assert metrics.performance.sharpe_ratio is not None
        assert float(metrics.performance.sharpe_ratio) == pytest.approx(expected, rel=1e-9)

    def test_nonzero_risk_free_rate_produces_a_different_sharpe(self) -> None:
        result = _result(equity_curve=self._curve())

        sharpe_zero_rf = compute_backtest_metrics(
            result, risk_free_rate_pct=Decimal("0")
        ).performance.sharpe_ratio
        sharpe_with_rf = compute_backtest_metrics(
            result, risk_free_rate_pct=Decimal("0.1")
        ).performance.sharpe_ratio

        assert sharpe_zero_rf is not None
        assert sharpe_with_rf is not None
        assert sharpe_zero_rf != sharpe_with_rf

        daily_rf = 0.1 / 365.0
        returns = [0.1, -0.1]
        excess = [r - daily_rf for r in returns]
        expected = (fmean(excess) / stdev(returns)) * math.sqrt(365.0)
        assert float(sharpe_with_rf) == pytest.approx(expected, rel=1e-9)

    def test_sharpe_is_none_when_return_series_has_zero_volatility(self) -> None:
        result = _result(
            equity_curve=(_snapshot(0, "1000"), _snapshot(1, "1010"), _snapshot(2, "1020.1"))
        )

        metrics = compute_backtest_metrics(result)

        assert metrics.performance.sharpe_ratio is None

    def test_sortino_defaults_mar_to_risk_free_rate(self) -> None:
        result = _result(equity_curve=self._curve())

        metrics = compute_backtest_metrics(result, risk_free_rate_pct=Decimal("0.05"))

        daily_mar = 0.05 / 365.0
        returns = [0.1, -0.1]
        excess = [r - daily_mar for r in returns]
        downside = math.sqrt(fmean([min(x, 0.0) ** 2 for x in excess]))
        expected = (fmean(excess) / downside) * math.sqrt(365.0)
        assert metrics.mar_pct == Decimal("0.05")
        assert metrics.performance.sortino_ratio is not None
        assert float(metrics.performance.sortino_ratio) == pytest.approx(expected, rel=1e-9)

    def test_sortino_uses_explicit_mar_when_given(self) -> None:
        result = _result(equity_curve=self._curve())

        default_mar_metrics = compute_backtest_metrics(result, risk_free_rate_pct=Decimal("0.05"))
        explicit_mar_metrics = compute_backtest_metrics(
            result, risk_free_rate_pct=Decimal("0.05"), mar_pct=Decimal("0.2")
        )

        assert explicit_mar_metrics.mar_pct == Decimal("0.2")
        assert (
            explicit_mar_metrics.performance.sortino_ratio
            != default_mar_metrics.performance.sortino_ratio
        )

    def test_sortino_is_none_when_no_return_falls_below_mar(self) -> None:
        # Both returns are +10%/+5%, MAR is far below both -- zero downside deviation.
        curve = (_snapshot(0, "1000"), _snapshot(1, "1100"), _snapshot(2, "1155"))
        result = _result(equity_curve=curve)

        metrics = compute_backtest_metrics(result, mar_pct=Decimal("-1"))

        assert metrics.performance.sortino_ratio is None


class TestBenchmarkReturnPct:
    def test_none_when_no_benchmark_configured(self) -> None:
        result = _result(equity_curve=(_snapshot(0, "1000"),), benchmark_price_series=None)

        metrics = compute_backtest_metrics(result)

        assert metrics.performance.benchmark_return_pct is None

    def test_buy_and_hold_return_of_benchmark_series(self) -> None:
        series = ((_START, Decimal("100")), (_START + timedelta(days=5), Decimal("110")))
        result = _result(equity_curve=(_snapshot(0, "1000"),), benchmark_price_series=series)

        metrics = compute_backtest_metrics(result)

        assert metrics.performance.benchmark_return_pct == Decimal("0.1")

    def test_none_when_benchmark_starting_close_is_zero(self) -> None:
        series = ((_START, Decimal("0")), (_START + timedelta(days=5), Decimal("110")))
        result = _result(equity_curve=(_snapshot(0, "1000"),), benchmark_price_series=series)

        metrics = compute_backtest_metrics(result)

        assert metrics.performance.benchmark_return_pct is None


class TestWinRateAndProfitLossRatio:
    def test_no_closes_at_all_is_none(self) -> None:
        trades = (_trade(trade_id="t1", side="buy", quantity="10", fill_price="100", day_offset=0),)
        result = _result(equity_curve=(_snapshot(0, "1000"),), trades=trades)

        metrics = compute_backtest_metrics(result)

        assert metrics.win_rate is None
        assert metrics.profit_loss_ratio is None
        assert metrics.closed_trade_count == 0

    def test_simple_full_close_win_has_no_defined_profit_loss_ratio(self) -> None:
        trades = (
            _trade(trade_id="t1", side="buy", quantity="10", fill_price="100", day_offset=0),
            _trade(trade_id="t2", side="sell", quantity="10", fill_price="120", day_offset=1),
        )
        result = _result(equity_curve=(_snapshot(0, "1000"), _snapshot(1, "1000")), trades=trades)

        metrics = compute_backtest_metrics(result)

        assert metrics.closed_trade_count == 1
        assert metrics.winning_trade_count == 1
        assert metrics.losing_trade_count == 0
        assert metrics.win_rate == Decimal("1")
        assert metrics.profit_loss_ratio is None  # no losses to divide by

    def test_all_losses_has_no_defined_profit_loss_ratio(self) -> None:
        trades = (
            _trade(trade_id="t1", side="buy", quantity="10", fill_price="100", day_offset=0),
            _trade(trade_id="t2", side="sell", quantity="10", fill_price="90", day_offset=1),
        )
        result = _result(equity_curve=(_snapshot(0, "1000"), _snapshot(1, "1000")), trades=trades)

        metrics = compute_backtest_metrics(result)

        assert metrics.win_rate == Decimal("0")
        assert metrics.profit_loss_ratio is None  # no wins to divide by

    def test_partial_fill_lot_splitting_across_two_buy_lots_and_two_sells(self) -> None:
        """FIFO across a sell that spans two buy lots, plus a later sell closing the remainder.

        Lots: buy 10@100 (day0), buy 5@110 (day1).
        sell 12@130 (day2) matches 10 from lot1 (realized 10*30=300, win) then
          2 from lot2 (realized 2*20=40, win); lot2 has 3 left open.
        sell 3@90 (day3) matches the remaining 3@110 (realized 3*(90-110)=-60, loss).
        """
        trades = (
            _trade(trade_id="t1", side="buy", quantity="10", fill_price="100", day_offset=0),
            _trade(trade_id="t2", side="buy", quantity="5", fill_price="110", day_offset=1),
            _trade(trade_id="t3", side="sell", quantity="12", fill_price="130", day_offset=2),
            _trade(trade_id="t4", side="sell", quantity="3", fill_price="90", day_offset=3),
        )
        result = _result(equity_curve=tuple(_snapshot(i, "1000") for i in range(4)), trades=trades)

        metrics = compute_backtest_metrics(result)

        assert metrics.closed_trade_count == 3
        assert metrics.winning_trade_count == 2
        assert metrics.losing_trade_count == 1
        assert metrics.win_rate == Decimal("2") / Decimal("3")
        avg_win = (Decimal("300") + Decimal("40")) / Decimal("2")
        avg_loss = Decimal("60")
        assert metrics.profit_loss_ratio == avg_win / avg_loss

    def test_rejected_and_skipped_trades_are_excluded_from_fifo_matching(self) -> None:
        trades = (
            _trade(trade_id="t1", side="buy", quantity="10", fill_price="100", day_offset=0),
            _trade(
                trade_id="t2",
                side="sell",
                quantity="10",
                fill_price=None,
                day_offset=1,
                status="rejected",
            ),
            _trade(
                trade_id="t3",
                side="sell",
                quantity="10",
                fill_price=None,
                day_offset=1,
                status="skipped_min_quantity",
            ),
            _trade(trade_id="t4", side="sell", quantity="10", fill_price="120", day_offset=2),
        )
        result = _result(equity_curve=tuple(_snapshot(i, "1000") for i in range(3)), trades=trades)

        metrics = compute_backtest_metrics(result)

        assert metrics.closed_trade_count == 1
        assert metrics.trade_count == 2  # only the two "filled" records (t1, t4)


class TestTurnover:
    def test_matches_hand_computed_notional_over_mean_nav(self) -> None:
        trades = (
            _trade(trade_id="t1", side="buy", quantity="10", fill_price="100", day_offset=0),
            _trade(trade_id="t2", side="sell", quantity="4", fill_price="120", day_offset=1),
        )
        curve = (_snapshot(0, "1000"), _snapshot(1, "1100"), _snapshot(2, "1050"))
        result = _result(equity_curve=curve, trades=trades)

        metrics = compute_backtest_metrics(result)

        total_notional = Decimal("10") * Decimal("100") + Decimal("4") * Decimal("120")
        mean_nav = (Decimal("1000") + Decimal("1100") + Decimal("1050")) / Decimal("3")
        assert metrics.turnover == total_notional / mean_nav

    def test_none_when_mean_nav_is_zero(self) -> None:
        result = _result(equity_curve=(_snapshot(0, "0"),))

        metrics = compute_backtest_metrics(result)

        assert metrics.turnover is None

    def test_excludes_non_filled_trades(self) -> None:
        trades = (
            _trade(trade_id="t1", side="buy", quantity="10", fill_price="100", day_offset=0),
            _trade(
                trade_id="t2",
                side="buy",
                quantity="999",
                fill_price=None,
                day_offset=0,
                status="rejected",
            ),
        )
        curve = (_snapshot(0, "1000"),)
        result = _result(equity_curve=curve, trades=trades)

        metrics = compute_backtest_metrics(result)

        assert metrics.turnover == Decimal("1000") / Decimal("1000")
