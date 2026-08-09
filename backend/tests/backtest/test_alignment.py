"""Unit tests for backtest.alignment: UTC-calendar-day alignment (BT-001 core logic).

Covers the BT-001 completion conditions
(`doc/technical/implementation_plan/03_backtest_data_and_indicators.md`):
US equity non-trading days are marked `stale` (not misreported as missing),
crypto is valued 24/7, no future data leaks in, and a full calendar-day
range never has a gap.
"""

from __future__ import annotations

from datetime import UTC, date, datetime
from decimal import Decimal

import pytest

from backtest.alignment import align_daily_series
from backtest.errors import MissingTradingDayBarError
from backtest.models import RawBar


def _bar(day: date, close: str, *, open_: str | None = None) -> RawBar:
    open_time = datetime(day.year, day.month, day.day, tzinfo=UTC)
    return RawBar(
        open_time=open_time,
        close_time=open_time,
        open=Decimal(open_ if open_ is not None else close),
        high=Decimal(close),
        low=Decimal(close),
        close=Decimal(close),
        volume=Decimal("1000"),
        quality_status="valid",
    )


def test_us_equity_weekend_gap_is_carried_forward_and_marked_stale() -> None:
    # Friday 2024-01-05 .. Monday 2024-01-08, NVDA-like equity.
    raw_bars = [
        _bar(date(2024, 1, 5), "100.00"),
        _bar(date(2024, 1, 8), "102.50"),
    ]

    series = align_daily_series(
        instrument_code="instrument.longbridge.us.nvda",
        provider="longbridge",
        asset_type="STOCK",
        start_date=date(2024, 1, 5),
        end_date=date(2024, 1, 8),
        raw_bars=raw_bars,
    )

    by_day = {bar.trading_day: bar for bar in series.bars}
    assert len(series.bars) == 4  # Fri, Sat, Sun, Mon -- no gap

    assert by_day[date(2024, 1, 5)].price_status == "fresh"
    assert by_day[date(2024, 1, 5)].close == Decimal("100.00")

    for weekend_day in (date(2024, 1, 6), date(2024, 1, 7)):
        weekend_bar = by_day[weekend_day]
        assert weekend_bar.price_status == "stale"
        assert weekend_bar.close == Decimal("100.00")  # carried forward from Friday
        assert weekend_bar.open == Decimal("100.00")
        assert weekend_bar.high == Decimal("100.00")
        assert weekend_bar.low == Decimal("100.00")
        assert weekend_bar.volume == Decimal(0)
        assert weekend_bar.source_open_time is None

    assert by_day[date(2024, 1, 8)].price_status == "fresh"
    assert by_day[date(2024, 1, 8)].close == Decimal("102.50")


def test_us_equity_holiday_gap_is_carried_forward_and_marked_stale() -> None:
    # 2024-07-04 (Independence Day, a Thursday) is a NASDAQ holiday.
    raw_bars = [
        _bar(date(2024, 7, 3), "50.00"),
        _bar(date(2024, 7, 5), "51.00"),
    ]

    series = align_daily_series(
        instrument_code="instrument.longbridge.us.qqq",
        provider="longbridge",
        asset_type="ETF",
        start_date=date(2024, 7, 3),
        end_date=date(2024, 7, 5),
        raw_bars=raw_bars,
    )

    by_day = {bar.trading_day: bar for bar in series.bars}
    assert len(series.bars) == 3  # Wed, Thu (holiday), Fri -- no gap

    holiday_bar = by_day[date(2024, 7, 4)]
    assert holiday_bar.price_status == "stale"
    assert holiday_bar.close == Decimal("50.00")  # carried forward, not "missing"

    assert by_day[date(2024, 7, 5)].price_status == "fresh"


def test_crypto_continuous_daily_data_has_no_stale_days() -> None:
    raw_bars = [
        _bar(date(2024, 1, 5), "42000"),
        _bar(date(2024, 1, 6), "42500"),  # Saturday -- crypto trades anyway
        _bar(date(2024, 1, 7), "43000"),  # Sunday -- crypto trades anyway
        _bar(date(2024, 1, 8), "43200"),
    ]

    series = align_daily_series(
        instrument_code="instrument.bybit.spot.btc-usdt",
        provider="bybit",
        asset_type="CRYPTO",
        start_date=date(2024, 1, 5),
        end_date=date(2024, 1, 8),
        raw_bars=raw_bars,
    )

    assert len(series.bars) == 4
    assert all(bar.price_status == "fresh" for bar in series.bars)


def test_crypto_missing_day_raises_instead_of_silently_carrying_forward() -> None:
    # Crypto is 24/7; a missing day is a real data problem, not an expected
    # non-trading day, so it must never be silently marked stale.
    raw_bars = [
        _bar(date(2024, 1, 5), "42000"),
        # 2024-01-06 missing
        _bar(date(2024, 1, 7), "43000"),
    ]

    with pytest.raises(MissingTradingDayBarError):
        align_daily_series(
            instrument_code="instrument.bybit.spot.btc-usdt",
            provider="bybit",
            asset_type="CRYPTO",
            start_date=date(2024, 1, 5),
            end_date=date(2024, 1, 7),
            raw_bars=raw_bars,
        )


def test_us_equity_missing_scheduled_trading_day_raises() -> None:
    # 2024-01-03 is an ordinary Wednesday (a scheduled trading day). A gap
    # here is a real provider problem, not a holiday, so it must raise
    # rather than being silently mistaken for one.
    raw_bars = [
        _bar(date(2024, 1, 2), "100.00"),
        # 2024-01-03 missing (ordinary trading day, not a holiday)
        _bar(date(2024, 1, 4), "101.00"),
    ]

    with pytest.raises(MissingTradingDayBarError):
        align_daily_series(
            instrument_code="instrument.longbridge.us.nvda",
            provider="longbridge",
            asset_type="STOCK",
            start_date=date(2024, 1, 2),
            end_date=date(2024, 1, 4),
            raw_bars=raw_bars,
        )


def test_full_range_covers_every_utc_calendar_day_with_no_gaps() -> None:
    # A full year, BTC-USDT (crypto -- always fresh so no calendar
    # dependency muddies the day-count assertion).
    start = date(2024, 1, 1)
    end = date(2024, 12, 31)
    raw_bars = [
        _bar(date.fromordinal(start.toordinal() + offset), "100.00")
        for offset in range((end - start).days + 1)
    ]

    series = align_daily_series(
        instrument_code="instrument.bybit.spot.btc-usdt",
        provider="bybit",
        asset_type="CRYPTO",
        start_date=start,
        end_date=end,
        raw_bars=raw_bars,
    )

    assert len(series.bars) == 366  # 2024 is a leap year
    days = [bar.trading_day for bar in series.bars]
    assert days == sorted(days)
    assert days[0] == start
    assert days[-1] == end
    # Gapless: consecutive days differ by exactly one day. The two sequences
    # are intentionally offset by one element (days[1:] is one shorter), so
    # strict=False is correct here, not an oversight.
    for previous, current in zip(days, days[1:], strict=False):
        assert (current - previous).days == 1


def test_no_future_data_leaks_in_even_if_upstream_returns_out_of_range_bars() -> None:
    # Defensive filter: a bar dated after the requested end_date must never
    # appear in the aligned series, even if a misbehaving upstream/mock
    # response includes one.
    raw_bars = [
        _bar(date(2024, 1, 5), "100.00"),
        _bar(date(2024, 1, 8), "102.50"),
        _bar(date(2024, 1, 9), "999.00"),  # out of range: after end_date
        _bar(date(2024, 1, 1), "1.00"),  # out of range: before start_date
    ]

    series = align_daily_series(
        instrument_code="instrument.longbridge.us.nvda",
        provider="longbridge",
        asset_type="STOCK",
        start_date=date(2024, 1, 5),
        end_date=date(2024, 1, 8),
        raw_bars=raw_bars,
    )

    assert series.end_date == date(2024, 1, 8)
    assert all(bar.trading_day <= date(2024, 1, 8) for bar in series.bars)
    assert all(bar.trading_day >= date(2024, 1, 5) for bar in series.bars)
    closes = {bar.trading_day: bar.close for bar in series.bars}
    assert closes[date(2024, 1, 8)] == Decimal("102.50")  # not overwritten by the future bar


def test_non_trading_start_with_no_prior_close_raises() -> None:
    # Range starts on a Saturday with no fresh bar before it to carry forward.
    raw_bars = [_bar(date(2024, 1, 8), "100.00")]

    with pytest.raises(MissingTradingDayBarError):
        align_daily_series(
            instrument_code="instrument.longbridge.us.nvda",
            provider="longbridge",
            asset_type="STOCK",
            start_date=date(2024, 1, 6),  # Saturday
            end_date=date(2024, 1, 8),
            raw_bars=raw_bars,
        )


def test_start_date_after_end_date_raises_value_error() -> None:
    with pytest.raises(ValueError, match="must not be after"):
        align_daily_series(
            instrument_code="instrument.longbridge.us.nvda",
            provider="longbridge",
            asset_type="STOCK",
            start_date=date(2024, 1, 8),
            end_date=date(2024, 1, 5),
            raw_bars=[],
        )
