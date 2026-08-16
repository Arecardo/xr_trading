"""Unit tests for application.backtest.tracking_error.compute_tracking_error (BT-003a)."""

from __future__ import annotations

from datetime import date, timedelta
from decimal import Decimal
from uuid import UUID, uuid4

from application.backtest.config import BacktestConfig
from application.backtest.models import BacktestResult, DailyPortfolioDetail
from application.backtest.tracking_error import compute_tracking_error
from domain.valuation.models import ValuationSnapshot

_PORTFOLIO_ID: UUID = uuid4()
_START = date(2026, 1, 1)
_ASSET = "equity:nasdaq:NVDA"


def _snapshot(day_offset: int, nav: str) -> ValuationSnapshot:
    nav_dec = Decimal(nav)
    return ValuationSnapshot(
        portfolio_id=_PORTFOLIO_ID,
        valuation_date=_START + timedelta(days=day_offset),
        positions_value=Decimal("0"),
        cash_value=nav_dec,
        net_asset_value=nav_dec,
        base_currency="USD",
        price_status="fresh",
    )


def _detail(day_offset: int, *, target_weight: str, position_value: str) -> DailyPortfolioDetail:
    return DailyPortfolioDetail(
        valuation_date=_START + timedelta(days=day_offset),
        target_weights={
            _ASSET: Decimal(target_weight),
            "cash:USD": Decimal("1") - Decimal(target_weight),
        },
        position_values={_ASSET: Decimal(position_value)} if position_value != "0" else {},
        fx_rates_applied={},
    )


def _result(
    equity_curve: tuple[ValuationSnapshot, ...], daily_detail: tuple[DailyPortfolioDetail, ...]
) -> BacktestResult:
    config = BacktestConfig(
        portfolio_id=_PORTFOLIO_ID,
        start_date=_START,
        end_date=_START + timedelta(days=len(equity_curve) - 1),
    )
    return BacktestResult(
        portfolio_id=_PORTFOLIO_ID,
        config=config,
        equity_curve=equity_curve,
        trades=(),
        replaced_risk_checks=(),
        final_positions={},
        final_cash=Decimal("0"),
        daily_detail=daily_detail,
    )


class TestComputeTrackingError:
    def test_empty_daily_detail_returns_empty_dict(self) -> None:
        result = _result((_snapshot(0, "1000"),), ())

        assert compute_tracking_error(result) == {}

    def test_perfect_tracking_has_zero_error(self) -> None:
        # target 0.5, position_value 500, nav 1000 -> actual weight exactly 0.5.
        result = _result(
            (_snapshot(0, "1000"),),
            (_detail(0, target_weight="0.5", position_value="500"),),
        )

        errors = compute_tracking_error(result)

        assert errors[_ASSET].mean_absolute_error == Decimal("0")
        assert errors[_ASSET].max_absolute_error == Decimal("0")
        assert errors[_ASSET].days_considered == 1

    def test_averages_absolute_error_across_days(self) -> None:
        # day0: target 0.20, actual 400/1000 = 0.40 -> error 0.20.
        # day1: target 0.20, actual 100/1000 = 0.10 -> error 0.10.
        # mean = (0.20 + 0.10) / 2 = 0.15; max = 0.20.
        result = _result(
            (_snapshot(0, "1000"), _snapshot(1, "1000")),
            (
                _detail(0, target_weight="0.20", position_value="400"),
                _detail(1, target_weight="0.20", position_value="100"),
            ),
        )

        errors = compute_tracking_error(result)

        assert errors[_ASSET].mean_absolute_error == Decimal("0.15")
        assert errors[_ASSET].max_absolute_error == Decimal("0.20")
        assert errors[_ASSET].days_considered == 2

    def test_skips_days_with_non_positive_nav(self) -> None:
        result = _result(
            (_snapshot(0, "0"), _snapshot(1, "1000")),
            (
                _detail(0, target_weight="0.5", position_value="0"),
                _detail(1, target_weight="0.5", position_value="500"),
            ),
        )

        errors = compute_tracking_error(result)

        assert errors[_ASSET].days_considered == 1
        assert errors[_ASSET].mean_absolute_error == Decimal("0")

    def test_cash_key_is_excluded_from_the_asset_set(self) -> None:
        result = _result(
            (_snapshot(0, "1000"),),
            (_detail(0, target_weight="0.5", position_value="500"),),
        )

        errors = compute_tracking_error(result)

        assert "cash:USD" not in errors
        assert set(errors) == {_ASSET}
