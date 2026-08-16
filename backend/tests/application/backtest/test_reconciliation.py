"""Unit tests for application.backtest.reconciliation.reconcile_backtest_result (BT-006)."""

from __future__ import annotations

from datetime import date, timedelta
from decimal import Decimal
from typing import Literal
from uuid import UUID, uuid4

from application.backtest.config import BacktestConfig
from application.backtest.models import (
    BacktestResult,
    DailyPortfolioDetail,
    TradeRecord,
    TradeStatus,
)
from application.backtest.reconciliation import reconcile_backtest_result
from domain.valuation.models import ValuationSnapshot

_PORTFOLIO_ID: UUID = uuid4()
_START = date(2026, 1, 1)
_ASSET = "equity:nasdaq:NVDA"
_INITIAL_CASH = Decimal("1000")


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
    side: Literal["buy", "sell"],
    quantity: str,
    fill_price: str,
    commission: str,
    day_offset: int,
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
        fill_price=Decimal(fill_price) if is_filled else None,
        commission=Decimal(commission) if is_filled else Decimal("0"),
        slippage=Decimal("0"),
        signal_score=None,
        reason=(),
        decision_date=day,
        trade_date=day if is_filled else None,
    )


def _detail(
    day_offset: int,
    *,
    target_weights: dict[str, str],
    position_values: dict[str, str],
    fx_rates: dict[str, str] | None = None,
) -> DailyPortfolioDetail:
    return DailyPortfolioDetail(
        valuation_date=_START + timedelta(days=day_offset),
        target_weights={k: Decimal(v) for k, v in target_weights.items()},
        position_values={k: Decimal(v) for k, v in position_values.items()},
        fx_rates_applied={k: Decimal(v) for k, v in (fx_rates or {}).items()},
    )


def _correct_result() -> BacktestResult:
    """A hand-built, internally-consistent 3-day BacktestResult.

    day0: no trades. cash=1000, positions=0, nav=1000.
    day1: buy 5@100, commission=2 -> cash = 1000 - 500 - 2 = 498.
          Revalued at close 100 -> positions_value = 500. nav = 998.
    day2: no new trades. Revalued at close 110 -> positions_value = 550.
          cash unchanged at 498. nav = 1048.

    ``daily_detail`` mirrors the same scenario: no FX (single USD asset), a
    fixed 50/50 target-weight split from day1 onward (arbitrary but
    internally consistent -- these tests exercise ``reconcile_backtest_result``,
    not ``SimpleRuleStrategy``).
    """
    trades = (
        _trade(
            trade_id="t1",
            side="buy",
            quantity="5",
            fill_price="100",
            commission="2",
            day_offset=1,
        ),
    )
    equity_curve = (
        _snapshot(0, cash="1000", positions="0"),
        _snapshot(1, cash="498", positions="500"),
        _snapshot(2, cash="498", positions="550"),
    )
    daily_detail = (
        _detail(0, target_weights={"cash:USD": "1"}, position_values={}),
        _detail(
            1,
            target_weights={_ASSET: "0.5", "cash:USD": "0.5"},
            position_values={_ASSET: "500"},
        ),
        _detail(
            2,
            target_weights={_ASSET: "0.5", "cash:USD": "0.5"},
            position_values={_ASSET: "550"},
        ),
    )
    config = BacktestConfig(
        portfolio_id=_PORTFOLIO_ID,
        start_date=_START,
        end_date=_START + timedelta(days=2),
        initial_cash=_INITIAL_CASH,
    )
    return BacktestResult(
        portfolio_id=_PORTFOLIO_ID,
        config=config,
        equity_curve=equity_curve,
        trades=trades,
        replaced_risk_checks=(),
        final_positions={_ASSET: Decimal("5")},
        final_cash=Decimal("498"),
        daily_detail=daily_detail,
    )


class TestReconcileBacktestResult:
    def test_passes_on_a_correct_result(self) -> None:
        result = reconcile_backtest_result(_correct_result())

        assert result.passed is True
        assert result.discrepancies == ()

    def test_flags_a_corrupted_cash_value_on_the_exact_day(self) -> None:
        base = _correct_result()
        corrupted_curve = (
            base.equity_curve[0],
            base.equity_curve[1],
            # cash_value wrong (500 instead of the replay-correct 498), but
            # net_asset_value kept self-consistent (550 + 500 = 1050) so
            # only the cash_value check -- not the NAV-consistency check --
            # fires here.
            ValuationSnapshot(
                portfolio_id=_PORTFOLIO_ID,
                valuation_date=_START + timedelta(days=2),
                positions_value=Decimal("550"),
                cash_value=Decimal("500"),
                net_asset_value=Decimal("1050"),
                base_currency="USD",
                price_status="fresh",
            ),
        )
        corrupted = BacktestResult(
            portfolio_id=base.portfolio_id,
            config=base.config,
            equity_curve=corrupted_curve,
            trades=base.trades,
            replaced_risk_checks=base.replaced_risk_checks,
            final_positions=base.final_positions,
            final_cash=base.final_cash,
            daily_detail=base.daily_detail,
        )

        result = reconcile_backtest_result(corrupted)

        assert result.passed is False
        matches = [
            d
            for d in result.discrepancies
            if d.field == "cash_value" and d.valuation_date == _START + timedelta(days=2)
        ]
        assert len(matches) == 1
        assert matches[0].expected == Decimal("498")
        assert matches[0].actual == Decimal("500")

    def test_flags_a_nav_internal_consistency_break(self) -> None:
        base = _correct_result()
        corrupted_curve = (
            base.equity_curve[0],
            # net_asset_value doesn't equal positions_value + cash_value
            # (998 recorded, but 500 + 498 = 998 -- make it visibly wrong: 950).
            ValuationSnapshot(
                portfolio_id=_PORTFOLIO_ID,
                valuation_date=_START + timedelta(days=1),
                positions_value=Decimal("500"),
                cash_value=Decimal("498"),
                net_asset_value=Decimal("950"),
                base_currency="USD",
                price_status="fresh",
            ),
            base.equity_curve[2],
        )
        corrupted = BacktestResult(
            portfolio_id=base.portfolio_id,
            config=base.config,
            equity_curve=corrupted_curve,
            trades=base.trades,
            replaced_risk_checks=base.replaced_risk_checks,
            final_positions=base.final_positions,
            final_cash=base.final_cash,
            daily_detail=base.daily_detail,
        )

        result = reconcile_backtest_result(corrupted)

        assert result.passed is False
        matches = [
            d
            for d in result.discrepancies
            if d.field == "net_asset_value" and d.valuation_date == _START + timedelta(days=1)
        ]
        assert len(matches) == 1
        assert matches[0].expected == Decimal("998")
        assert matches[0].actual == Decimal("950")

    def test_flags_a_final_position_mismatch(self) -> None:
        base = _correct_result()
        corrupted = BacktestResult(
            portfolio_id=base.portfolio_id,
            config=base.config,
            equity_curve=base.equity_curve,
            trades=base.trades,
            replaced_risk_checks=base.replaced_risk_checks,
            final_positions={_ASSET: Decimal("999")},  # should be 5, per the one buy fill
            final_cash=base.final_cash,
            daily_detail=base.daily_detail,
        )

        result = reconcile_backtest_result(corrupted)

        assert result.passed is False
        matches = [d for d in result.discrepancies if d.field == f"final_position:{_ASSET}"]
        assert len(matches) == 1
        assert matches[0].expected == Decimal("5")
        assert matches[0].actual == Decimal("999")

    def test_flags_a_final_cash_mismatch(self) -> None:
        base = _correct_result()
        corrupted = BacktestResult(
            portfolio_id=base.portfolio_id,
            config=base.config,
            equity_curve=base.equity_curve,
            trades=base.trades,
            replaced_risk_checks=base.replaced_risk_checks,
            final_positions=base.final_positions,
            final_cash=Decimal("0"),  # should be 498
            daily_detail=base.daily_detail,
        )

        result = reconcile_backtest_result(corrupted)

        assert result.passed is False
        matches = [d for d in result.discrepancies if d.field == "final_cash"]
        assert len(matches) == 1
        assert matches[0].expected == Decimal("498")
        assert matches[0].actual == Decimal("0")

    def test_rejected_and_skipped_trades_do_not_affect_the_replay(self) -> None:
        base = _correct_result()
        noise_trades = base.trades + (
            _trade(
                trade_id="noise-1",
                side="buy",
                quantity="1000",
                fill_price="1",
                commission="0",
                day_offset=1,
                status="rejected",
            ),
            _trade(
                trade_id="noise-2",
                side="sell",
                quantity="1000",
                fill_price="1",
                commission="0",
                day_offset=1,
                status="skipped_min_quantity",
            ),
        )
        with_noise = BacktestResult(
            portfolio_id=base.portfolio_id,
            config=base.config,
            equity_curve=base.equity_curve,
            trades=noise_trades,
            replaced_risk_checks=base.replaced_risk_checks,
            final_positions=base.final_positions,
            final_cash=base.final_cash,
            daily_detail=base.daily_detail,
        )

        result = reconcile_backtest_result(with_noise)

        assert result.passed is True
        assert result.discrepancies == ()

    def test_missing_daily_detail_skips_the_daily_detail_checks_not_fails_them(self) -> None:
        """Backward compatibility: a hand-built result predating ``daily_detail`` still passes."""
        base = _correct_result()
        legacy = BacktestResult(
            portfolio_id=base.portfolio_id,
            config=base.config,
            equity_curve=base.equity_curve,
            trades=base.trades,
            replaced_risk_checks=base.replaced_risk_checks,
            final_positions=base.final_positions,
            final_cash=base.final_cash,
            # daily_detail intentionally omitted -- defaults to ().
        )

        result = reconcile_backtest_result(legacy)

        assert result.passed is True
        assert result.discrepancies == ()

    def test_flags_a_positions_value_breakdown_mismatch(self) -> None:
        base = _correct_result()
        corrupted_detail = (
            base.daily_detail[0],
            # position_values sums to 400, but equity_curve day1's
            # positions_value is still 500.
            _detail(
                1,
                target_weights={_ASSET: "0.5", "cash:USD": "0.5"},
                position_values={_ASSET: "400"},
            ),
            base.daily_detail[2],
        )
        corrupted = BacktestResult(
            portfolio_id=base.portfolio_id,
            config=base.config,
            equity_curve=base.equity_curve,
            trades=base.trades,
            replaced_risk_checks=base.replaced_risk_checks,
            final_positions=base.final_positions,
            final_cash=base.final_cash,
            daily_detail=corrupted_detail,
        )

        result = reconcile_backtest_result(corrupted)

        assert result.passed is False
        matches = [
            d
            for d in result.discrepancies
            if d.field == "positions_value_breakdown"
            and d.valuation_date == _START + timedelta(days=1)
        ]
        assert len(matches) == 1
        assert matches[0].expected == Decimal("500")
        assert matches[0].actual == Decimal("400")

    def test_flags_a_zero_quantity_zero_value_consistency_break(self) -> None:
        base = _correct_result()
        corrupted_detail = (
            base.daily_detail[0],
            base.daily_detail[1],
            # day2: replayed quantity is 5 (nonzero, one buy on day1, no
            # sells) but position_values records nothing for the asset.
            _detail(2, target_weights={_ASSET: "0.5", "cash:USD": "0.5"}, position_values={}),
        )
        corrupted = BacktestResult(
            portfolio_id=base.portfolio_id,
            config=base.config,
            # positions_value must still equal the (now empty) breakdown sum
            # to isolate this from the positions_value_breakdown check.
            equity_curve=(
                base.equity_curve[0],
                base.equity_curve[1],
                ValuationSnapshot(
                    portfolio_id=_PORTFOLIO_ID,
                    valuation_date=_START + timedelta(days=2),
                    positions_value=Decimal("0"),
                    cash_value=Decimal("498"),
                    net_asset_value=Decimal("498"),
                    base_currency="USD",
                    price_status="fresh",
                ),
            ),
            trades=base.trades,
            replaced_risk_checks=base.replaced_risk_checks,
            final_positions=base.final_positions,
            final_cash=base.final_cash,
            daily_detail=corrupted_detail,
        )

        result = reconcile_backtest_result(corrupted)

        assert result.passed is False
        matches = [
            d
            for d in result.discrepancies
            if d.field == f"position_value_zero_consistency:{_ASSET}"
            and d.valuation_date == _START + timedelta(days=2)
        ]
        assert len(matches) == 1
        assert matches[0].expected == Decimal("5")
        assert matches[0].actual == Decimal("0")

    def test_flags_a_target_weight_out_of_bounds(self) -> None:
        base = _correct_result()
        corrupted_detail = (
            base.daily_detail[0],
            _detail(
                1,
                target_weights={_ASSET: "1.5", "cash:USD": "-0.5"},
                position_values={_ASSET: "500"},
            ),
            base.daily_detail[2],
        )
        corrupted = BacktestResult(
            portfolio_id=base.portfolio_id,
            config=base.config,
            equity_curve=base.equity_curve,
            trades=base.trades,
            replaced_risk_checks=base.replaced_risk_checks,
            final_positions=base.final_positions,
            final_cash=base.final_cash,
            daily_detail=corrupted_detail,
        )

        result = reconcile_backtest_result(corrupted)

        assert result.passed is False
        matches = [
            d
            for d in result.discrepancies
            if d.field == f"target_weight_bounds:{_ASSET}"
            and d.valuation_date == _START + timedelta(days=1)
        ]
        assert len(matches) == 1
        assert matches[0].actual == Decimal("1.5")
        # -0.5 is also out of bounds (below 0).
        cash_matches = [
            d for d in result.discrepancies if d.field == "target_weight_bounds:cash:USD"
        ]
        assert len(cash_matches) == 1
        assert cash_matches[0].actual == Decimal("-0.5")

    def test_flags_a_target_weights_sum_violation(self) -> None:
        base = _correct_result()
        corrupted_detail = (
            base.daily_detail[0],
            # Sums to 0.9, not 1.
            _detail(
                1,
                target_weights={_ASSET: "0.5", "cash:USD": "0.4"},
                position_values={_ASSET: "500"},
            ),
            base.daily_detail[2],
        )
        corrupted = BacktestResult(
            portfolio_id=base.portfolio_id,
            config=base.config,
            equity_curve=base.equity_curve,
            trades=base.trades,
            replaced_risk_checks=base.replaced_risk_checks,
            final_positions=base.final_positions,
            final_cash=base.final_cash,
            daily_detail=corrupted_detail,
        )

        result = reconcile_backtest_result(corrupted)

        assert result.passed is False
        matches = [
            d
            for d in result.discrepancies
            if d.field == "target_weights_sum" and d.valuation_date == _START + timedelta(days=1)
        ]
        assert len(matches) == 1
        assert matches[0].expected == Decimal("1")
        assert matches[0].actual == Decimal("0.9")

    def test_flags_a_non_positive_fx_rate(self) -> None:
        base = _correct_result()
        corrupted_detail = (
            base.daily_detail[0],
            _detail(
                1,
                target_weights={_ASSET: "0.5", "cash:USD": "0.5"},
                position_values={_ASSET: "500"},
                fx_rates={"HKD": "0"},
            ),
            base.daily_detail[2],
        )
        corrupted = BacktestResult(
            portfolio_id=base.portfolio_id,
            config=base.config,
            equity_curve=base.equity_curve,
            trades=base.trades,
            replaced_risk_checks=base.replaced_risk_checks,
            final_positions=base.final_positions,
            final_cash=base.final_cash,
            daily_detail=corrupted_detail,
        )

        result = reconcile_backtest_result(corrupted)

        assert result.passed is False
        matches = [
            d
            for d in result.discrepancies
            if d.field == "fx_rate_positivity:HKD"
            and d.valuation_date == _START + timedelta(days=1)
        ]
        assert len(matches) == 1
        assert matches[0].actual == Decimal("0")
