"""Unit tests for backtest.calendar: NASDAQ/NYSE trading-day determination."""

from __future__ import annotations

from datetime import date

import pytest

from backtest.calendar import is_us_equity_trading_day


@pytest.mark.parametrize(
    ("day", "label"),
    [
        (date(2024, 1, 6), "Saturday"),
        (date(2024, 1, 7), "Sunday"),
    ],
)
def test_weekends_are_not_trading_days(day: date, label: str) -> None:
    assert is_us_equity_trading_day(day) is False, label


@pytest.mark.parametrize(
    ("day", "holiday_name"),
    [
        (date(2024, 1, 1), "New Year's Day"),
        (date(2024, 1, 15), "MLK Day (3rd Monday of Jan)"),
        (date(2024, 2, 19), "Presidents Day (3rd Monday of Feb)"),
        (date(2024, 3, 29), "Good Friday"),
        (date(2024, 5, 27), "Memorial Day (last Monday of May)"),
        (date(2024, 6, 19), "Juneteenth"),
        (date(2024, 7, 4), "Independence Day"),
        (date(2024, 9, 2), "Labor Day (1st Monday of Sep)"),
        (date(2024, 11, 28), "Thanksgiving (4th Thursday of Nov)"),
        (date(2024, 12, 25), "Christmas"),
    ],
)
def test_2024_holidays_are_not_trading_days(day: date, holiday_name: str) -> None:
    assert is_us_equity_trading_day(day) is False, holiday_name


@pytest.mark.parametrize(
    "day",
    [
        date(2024, 1, 2),  # ordinary Tuesday
        date(2024, 1, 3),  # ordinary Wednesday
        date(2024, 6, 20),  # day after Juneteenth
        date(2024, 12, 26),  # day after Christmas
    ],
)
def test_ordinary_weekdays_are_trading_days(day: date) -> None:
    assert is_us_equity_trading_day(day) is True


def test_juneteenth_not_observed_before_2022() -> None:
    # Juneteenth became a NYSE/NASDAQ holiday starting 2022; in prior years
    # June 19th is an ordinary trading day (when it isn't a weekend).
    # 2020-06-19 was a Friday.
    assert date(2020, 6, 19).weekday() not in (5, 6)
    assert is_us_equity_trading_day(date(2020, 6, 19)) is True


def test_new_years_day_on_saturday_does_not_shift_to_preceding_friday() -> None:
    # 2022-01-01 was a Saturday. Unlike every other holiday, New Year's Day
    # does NOT shift to the preceding Friday in this case (see
    # backtest.calendar module docstring) -- 2021-12-31 remains a trading day.
    assert date(2022, 1, 1).weekday() == 5
    assert is_us_equity_trading_day(date(2021, 12, 31)) is True


def test_holiday_on_saturday_shifts_to_preceding_friday() -> None:
    # 2021-07-04 (Independence Day) was a Sunday -> observed Monday 2021-07-05.
    assert date(2021, 7, 4).weekday() == 6
    assert is_us_equity_trading_day(date(2021, 7, 4)) is False
    assert is_us_equity_trading_day(date(2021, 7, 5)) is False


def test_holiday_on_sunday_shifts_to_following_monday() -> None:
    # 2027-12-25 (Christmas) is a Saturday -> observed on Friday 2027-12-24.
    assert date(2027, 12, 25).weekday() == 5
    assert is_us_equity_trading_day(date(2027, 12, 24)) is False


def test_thanksgiving_never_shifts_for_weekend() -> None:
    # Thanksgiving is always the 4th Thursday of November, so it can never
    # land on a weekend; this test documents that no shifting logic applies.
    for year in (2023, 2024, 2025, 2026):
        thanksgiving = _fourth_thursday_of_november(year)
        assert thanksgiving.weekday() == 3
        assert is_us_equity_trading_day(thanksgiving) is False


def _fourth_thursday_of_november(year: int) -> date:
    from datetime import timedelta

    first = date(year, 11, 1)
    offset = (3 - first.weekday()) % 7
    return first + timedelta(days=offset + 21)
