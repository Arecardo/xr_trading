"""Unit tests for backtest.indicators: the BT-002 raw technical indicators.

Every indicator is exercised with a fixed, hand-checkable sample (not just
"does it run") and with an explicit insufficient-history case per the
roadmap's "K 线不足 50 日时降级处理，不报错、不产出伪造值" requirement
(`doc/technical/roadmap/03_backtest_engine.md` BT-002). At least one case
per stale-day-sensitive indicator exercises a `price_status="stale"` day in
the rolling window, per the module's documented decision to treat a stale
day as a real zero-movement observation (see `backtest/indicators.py`
module docstring, decision 1).
"""

from __future__ import annotations

from datetime import date, timedelta
from decimal import Decimal

import pytest

from backtest.indicators import (
    IndicatorBar,
    compute_atr,
    compute_daily_returns,
    compute_indicators,
    compute_ma_trend,
    compute_moving_average,
    compute_price_position,
    compute_relative_strength,
    compute_rsi,
    compute_volume_change,
)
from backtest.models import AlignedBar, AlignedSeries, AssetClass

_START = date(2024, 1, 1)


def _fresh(
    offset: int,
    close: str,
    *,
    high: str | None = None,
    low: str | None = None,
    open_: str | None = None,
    volume: str = "1000",
) -> AlignedBar:
    day = _START + timedelta(days=offset)
    close_d = Decimal(close)
    return AlignedBar(
        trading_day=day,
        open=Decimal(open_) if open_ is not None else close_d,
        high=Decimal(high) if high is not None else close_d,
        low=Decimal(low) if low is not None else close_d,
        close=close_d,
        volume=Decimal(volume),
        price_status="fresh",
        source_open_time=None,
    )


def _stale(offset: int, close: str, *, volume: str = "0") -> AlignedBar:
    day = _START + timedelta(days=offset)
    close_d = Decimal(close)
    return AlignedBar(
        trading_day=day,
        open=close_d,
        high=close_d,
        low=close_d,
        close=close_d,
        volume=Decimal(volume),
        price_status="stale",
        source_open_time=None,
    )


def _series(
    bars: list[AlignedBar],
    *,
    instrument_code: str = "instrument.test.us.xyz",
    provider: str = "test",
    asset_type: AssetClass = "STOCK",
) -> AlignedSeries:
    return AlignedSeries(
        instrument_code=instrument_code,
        provider=provider,
        asset_type=asset_type,
        start_date=bars[0].trading_day,
        end_date=bars[-1].trading_day,
        bars=tuple(bars),
    )


# --- daily_return -----------------------------------------------------------


def test_daily_return_computes_close_to_close_pct_change() -> None:
    series = _series([_fresh(0, "100"), _fresh(1, "110"), _fresh(2, "99")])
    result = compute_daily_returns(series)
    assert result[0] is None  # no prior day
    assert result[1] == Decimal("0.10")
    assert result[2] == Decimal("-0.10")


def test_daily_return_none_when_prior_close_is_zero() -> None:
    series = _series([_fresh(0, "0"), _fresh(1, "5")])
    result = compute_daily_returns(series)
    assert result[1] is None


# --- moving_average -----------------------------------------------------------


@pytest.mark.parametrize(
    ("window", "expected"),
    [
        (3, [None, None, Decimal("20"), Decimal("30"), Decimal("40")]),
        (1, [Decimal("10"), Decimal("20"), Decimal("30"), Decimal("40"), Decimal("50")]),
    ],
)
def test_moving_average_fixed_sample(window: int, expected: list[Decimal | None]) -> None:
    series = _series([_fresh(i, str(c)) for i, c in enumerate([10, 20, 30, 40, 50])])
    result = compute_moving_average(series, window)
    assert list(result) == expected


def test_moving_average_insufficient_history_returns_none_not_fabricated() -> None:
    # Only 10 bars; MA50 needs 50 -- every entry must degrade to None, never
    # a computed-but-meaningless partial average.
    series = _series([_fresh(i, "100") for i in range(10)])
    result = compute_moving_average(series, 50)
    assert result == (None,) * 10


def test_moving_average_rejects_non_positive_window() -> None:
    series = _series([_fresh(0, "100")])
    with pytest.raises(ValueError, match="window must be >= 1"):
        compute_moving_average(series, 0)


# --- ma_trend -----------------------------------------------------------


def test_ma_trend_compares_ma5_to_ma20() -> None:
    ma5 = (None, Decimal("10"), Decimal("20"), Decimal("15"))
    ma20 = (None, None, Decimal("15"), Decimal("15"))
    result = compute_ma_trend(ma5, ma20)
    assert result == (None, None, "up", "flat")


def test_ma_trend_down() -> None:
    ma5 = (Decimal("10"),)
    ma20 = (Decimal("20"),)
    assert compute_ma_trend(ma5, ma20) == ("down",)


def test_ma_trend_rejects_mismatched_lengths() -> None:
    with pytest.raises(ValueError, match="length"):
        compute_ma_trend((Decimal("1"), Decimal("2")), (Decimal("1"),))


# --- rsi -----------------------------------------------------------


def test_rsi_fixed_sample_period_2() -> None:
    # changes: +3 (100->103), -1 (103->102); avg_gain=1.5, avg_loss=0.5,
    # RS=3, RSI = 100 - 100/4 = 75.
    series = _series([_fresh(0, "100"), _fresh(1, "103"), _fresh(2, "102")])
    result = compute_rsi(series, period=2)
    assert result[0] is None
    assert result[1] is None  # only 1 change available, period needs 2
    assert result[2] == Decimal("75")


def test_rsi_all_zero_movement_window_is_50_including_a_stale_day() -> None:
    # A stale day carries the price flat -- zero gain, zero loss -- which
    # must read as RSI 50 (flat), not divide-by-zero or a fabricated value.
    series = _series([_fresh(0, "100"), _stale(1, "100")])
    result = compute_rsi(series, period=1)
    assert result[1] == Decimal("50")


def test_rsi_all_gains_no_losses_is_100() -> None:
    series = _series([_fresh(0, "100"), _fresh(1, "105")])
    result = compute_rsi(series, period=1)
    assert result[1] == Decimal("100")


def test_rsi_insufficient_history_default_period_returns_none() -> None:
    # Default period=14 needs 15 bars; only 10 available.
    series = _series([_fresh(i, "100") for i in range(10)])
    result = compute_rsi(series)
    assert result == (None,) * 10


# --- atr -----------------------------------------------------------


def test_atr_fixed_sample_period_2() -> None:
    # bar0 close=100 (prev-close anchor only)
    # bar1 high=105 low=95 close=102 -> TR = max(10, |105-100|=5, |95-100|=5) = 10
    # bar2 high=110 low=104 close=108 -> TR = max(6, |110-102|=8, |104-102|=2) = 8
    # ATR(period=2) at index2 = (10 + 8) / 2 = 9
    series = _series(
        [
            _fresh(0, "100"),
            _fresh(1, "102", high="105", low="95"),
            _fresh(2, "108", high="110", low="104"),
        ]
    )
    result = compute_atr(series, period=2)
    assert result[0] is None
    assert result[1] is None
    assert result[2] == Decimal("9")


def test_atr_stale_day_contributes_zero_true_range() -> None:
    series = _series([_fresh(0, "50", high="52", low="48"), _stale(1, "50")])
    result = compute_atr(series, period=1)
    assert result[1] == Decimal("0")


def test_atr_insufficient_history_default_period_returns_none() -> None:
    series = _series([_fresh(i, "100") for i in range(10)])
    result = compute_atr(series)
    assert result == (None,) * 10


# --- volume_change -----------------------------------------------------------


def test_volume_change_fixed_sample_with_stale_days() -> None:
    series = _series(
        [
            _fresh(0, "100", volume="1000"),
            _fresh(1, "101", volume="1500"),
            _stale(2, "101"),  # volume 0
            _stale(3, "101"),  # volume 0
            _fresh(4, "102", volume="800"),
        ]
    )
    result = compute_volume_change(series)
    assert result[0] is None  # no prior day
    assert result[1] == Decimal("0.5")
    assert result[2] == Decimal("-1")  # 0 vs 1500 -- real -100% (market closed)
    assert result[3] == Decimal("0")  # 0 vs 0 -- defined as no change
    assert result[4] is None  # 800 vs 0 -- undefined ratio, not fabricated


# --- price_position_20d (tested at window=3 for hand-checkability) --------


def test_price_position_fixed_sample_window_3() -> None:
    series = _series([_fresh(i, str(c)) for i, c in enumerate([10, 12, 11, 15, 9])])
    result = compute_price_position(series, window=3)
    assert result[0] is None
    assert result[1] is None
    assert result[2] == Decimal("0.5")  # window [10,12,11], close=11, (11-10)/(12-10)
    assert result[3] == Decimal("1")  # window [12,11,15], close=15 == high
    assert result[4] == Decimal("0")  # window [11,15,9], close=9 == low


def test_price_position_flat_window_is_midpoint() -> None:
    series = _series([_fresh(i, "5") for i in range(3)])
    result = compute_price_position(series, window=3)
    assert result[2] == Decimal("0.5")


def test_price_position_insufficient_history_default_window_returns_none() -> None:
    series = _series([_fresh(i, "100") for i in range(10)])
    result = compute_price_position(series)
    assert result == (None,) * 10


# --- relative_strength_20d (tested at window=2 for hand-checkability) -----


def _pair_series(
    asset_closes: list[str], benchmark_closes: list[str]
) -> tuple[AlignedSeries, AlignedSeries]:
    asset = _series([_fresh(i, c) for i, c in enumerate(asset_closes)])
    benchmark = _series(
        [_fresh(i, c) for i, c in enumerate(benchmark_closes)],
        instrument_code="instrument.test.us.qqq",
    )
    return asset, benchmark


def test_relative_strength_fixed_sample_window_2() -> None:
    # asset: 100 -> 110 -> 121 (21% over 2 days)
    # benchmark: 50 -> 55 -> 60 (20% over 2 days)
    # relative strength = 0.21 - 0.20 = 0.01
    asset, benchmark = _pair_series(["100", "110", "121"], ["50", "55", "60"])
    result = compute_relative_strength(asset, benchmark, window=2)
    assert result[0] is None
    assert result[1] is None
    assert result[2] == Decimal("0.01")


def test_relative_strength_requires_matching_date_range() -> None:
    asset = _series([_fresh(i, "100") for i in range(3)])
    benchmark = _series([_fresh(i, "50") for i in range(2)])
    with pytest.raises(ValueError, match="same date range"):
        compute_relative_strength(asset, benchmark, window=1)


def test_relative_strength_none_when_anchor_close_is_zero() -> None:
    asset, benchmark = _pair_series(["0", "5"], ["50", "55"])
    result = compute_relative_strength(asset, benchmark, window=1)
    assert result[1] is None


def test_relative_strength_insufficient_history_default_window_returns_none() -> None:
    asset, benchmark = _pair_series(["100"] * 10, ["50"] * 10)
    result = compute_relative_strength(asset, benchmark)
    assert result == (None,) * 10


# --- compute_indicators (integration) -----------------------------------------


def test_compute_indicators_shape_matches_input_and_degrades_gracefully() -> None:
    # 10 days: too short for any indicator needing >10 bars (ma20/50, rsi14,
    # atr14, price_position_20d, relative_strength_20d, ma_trend) -- all
    # must be None throughout, never a fabricated value. daily_return/ma5
    # (needing <=5 bars) must be populated once available.
    asset, benchmark = _pair_series([str(100 + i) for i in range(10)], ["50"] * 10)
    result = compute_indicators(asset, benchmark)

    assert len(result) == 10
    assert [bar.trading_day for bar in result] == [b.trading_day for b in asset.bars]

    assert all(bar.ma20 is None for bar in result)
    assert all(bar.ma50 is None for bar in result)
    assert all(bar.ma_trend is None for bar in result)
    assert all(bar.rsi14 is None for bar in result)
    assert all(bar.atr14 is None for bar in result)
    assert all(bar.price_position_20d is None for bar in result)
    assert all(bar.relative_strength_20d is None for bar in result)

    assert result[0].daily_return is None
    assert result[1].daily_return == Decimal("0.01")  # (101-100)/100
    assert result[4].ma5 == Decimal("102")  # mean(100..104)


def test_compute_indicators_populates_all_fields_with_enough_history() -> None:
    # 60 fresh days is enough for every indicator (MA50 needs 50, RSI/ATR
    # need 15, the 20-day windows need 20/21).
    closes = [str(100 + i) for i in range(60)]
    asset, benchmark = _pair_series(closes, [str(50 + i) for i in range(60)])
    result = compute_indicators(asset, benchmark)

    last = result[-1]
    assert isinstance(last, IndicatorBar)
    assert last.daily_return is not None
    assert last.ma5 is not None
    assert last.ma20 is not None
    assert last.ma50 is not None
    assert last.ma_trend is not None
    assert last.rsi14 is not None
    assert last.atr14 is not None
    assert last.volume_change is not None
    assert last.price_position_20d is not None
    assert last.relative_strength_20d is not None

    # Cross-check against the standalone functions rather than re-deriving
    # by hand -- compute_indicators must not silently diverge from them.
    assert last.ma20 == compute_moving_average(asset, 20)[-1]
    assert last.rsi14 == compute_rsi(asset)[-1]
