"""US equity (NASDAQ/NYSE) trading-day calendar.

Design decision (BT-001, documented per the task's instruction to justify
this choice rather than silently shipping a weekends-only calendar): this
module computes NASDAQ/NYSE holiday observance **formulaically** (fixed
dates, nth-weekday-of-month rules, and the Anonymous Gregorian / Meeus
algorithm for Easter/Good Friday) instead of depending on
``pandas-market-calendars``/``exchange_calendars``.

Rationale:
- Those libraries are correct and well-maintained, but pull in ``pandas``
  and ``numpy`` -- a heavy numeric stack -- into a backend that is otherwise
  deliberately stdlib/``httpx``/``fastapi``/``Decimal``-only
  (python-backend-standards.md §1, §3). BT-001 only needs a yes/no "was
  there a trading session on this UTC day" answer for a small, fixed first
  batch of US-listed instruments (NVDA, QQQ); it does not need intraday
  session times, early-close half-days (irrelevant to daily-bar alignment),
  or non-US calendars.
- The eleven holidays implemented here (New Year's Day, MLK Day,
  Presidents Day, Good Friday, Memorial Day, Juneteenth, Independence Day,
  Labor Day, Thanksgiving, Christmas) plus weekend/observed-holiday shifting
  are exactly the set NASDAQ/NYSE publish years in advance and have not
  changed in the modern era (Juneteenth was added starting 2022; guarded
  below).

**Known limitation (must be weighed by the integration lead before this
feeds anything higher-stakes than `research`/`backtest`):** this calendar
cannot know about *unscheduled, ad hoc* full-day closures that are not
derivable from a fixed rule -- e.g. the NYSE/NASDAQ closure on 2025-01-09
for a national day of mourning. Such days will be (incorrectly) treated as
ordinary trading days: if the market-info-service response happens to have
no bar for that day, ``align_daily_series`` will raise
``MissingTradingDayBarError`` (fail-closed -- surfaced loudly, not silently
misreported as an expected holiday) rather than silently marking it
``stale``. If `market-info-service` happens to serve a (possibly stale
upstream, e.g. provider-side data quality issue) bar for such a day anyway,
it will be accepted as ``fresh`` without complaint. A future upgrade path if
this proves insufficient: swap this module's ``is_us_equity_trading_day``
for a call into ``pandas-market-calendars``/``exchange_calendars``, or against
market-info-service's own NYSE calendar (SCH-002, currently internal/not
exposed via a public API) once/if it is exposed -- the call sites in
``alignment.py`` would not need to change.

This module is pure (no I/O, no network) and intentionally has zero
third-party dependencies.
"""

from __future__ import annotations

from datetime import date, timedelta
from functools import cache

_MONDAY = 0
_THURSDAY = 3
_SATURDAY = 5
_SUNDAY = 6

_JUNETEENTH_FIRST_OBSERVED_YEAR = 2022


def _nth_weekday_of_month(year: int, month: int, weekday: int, n: int) -> date:
    """The date of the ``n``-th occurrence of ``weekday`` (Mon=0..Sun=6) in ``month``."""
    first_of_month = date(year, month, 1)
    offset = (weekday - first_of_month.weekday()) % 7
    return first_of_month + timedelta(days=offset + 7 * (n - 1))


def _last_weekday_of_month(year: int, month: int, weekday: int) -> date:
    """The date of the last occurrence of ``weekday`` (Mon=0..Sun=6) in ``month``."""
    if month == 12:
        last_of_month = date(year + 1, 1, 1) - timedelta(days=1)
    else:
        last_of_month = date(year, month + 1, 1) - timedelta(days=1)
    offset = (last_of_month.weekday() - weekday) % 7
    return last_of_month - timedelta(days=offset)


def _easter_sunday(year: int) -> date:
    """Gregorian Easter Sunday via the Anonymous Gregorian (Meeus/Jones/Butcher) algorithm."""
    a = year % 19
    b = year // 100
    c = year % 100
    d = b // 4
    e = b % 4
    f = (b + 8) // 25
    g = (b - f + 1) // 3
    h = (19 * a + b - d - g + 15) % 30
    i = c // 4
    k = c % 4
    l_ = (32 + 2 * e + 2 * i - h - k) % 7
    m = (a + 11 * h + 22 * l_) // 451
    month = (h + l_ - 7 * m + 114) // 31
    day = ((h + l_ - 7 * m + 114) % 31) + 1
    return date(year, month, day)


def _shift_weekend_observance(holiday: date) -> date:
    """Standard NYSE/NASDAQ rule: Saturday -> preceding Friday, Sunday -> following Monday.

    Does NOT apply to New Year's Day -- see ``_new_years_day_observed``.
    """
    if holiday.weekday() == _SATURDAY:
        return holiday - timedelta(days=1)
    if holiday.weekday() == _SUNDAY:
        return holiday + timedelta(days=1)
    return holiday


def _new_years_day_observed(year: int) -> date | None:
    """New Year's Day has a documented exception to the usual Saturday shift.

    When January 1st falls on a Saturday, NYSE/NASDAQ do NOT close on the
    preceding Friday (December 31st of the prior year remains a normal
    trading day) -- unlike every other holiday. When it falls on a Sunday,
    the usual shift to the following Monday applies. Returns ``None`` when
    there is no observed closure for New Year's Day in ``year`` (Saturday
    case).
    """
    day = date(year, 1, 1)
    if day.weekday() == _SATURDAY:
        return None
    if day.weekday() == _SUNDAY:
        return day + timedelta(days=1)
    return day


@cache
def _holidays_for_year(year: int) -> frozenset[date]:
    """The set of NASDAQ/NYSE observed full-day holiday closures in ``year``."""
    holidays: set[date] = set()

    new_years = _new_years_day_observed(year)
    if new_years is not None:
        holidays.add(new_years)

    holidays.add(_shift_weekend_observance(_nth_weekday_of_month(year, 1, _MONDAY, 3)))  # MLK
    holidays.add(
        _shift_weekend_observance(_nth_weekday_of_month(year, 2, _MONDAY, 3))
    )  # Presidents Day
    holidays.add(_easter_sunday(year) - timedelta(days=2))  # Good Friday (never shifted)
    holidays.add(_shift_weekend_observance(_last_weekday_of_month(year, 5, _MONDAY)))  # Memorial
    if year >= _JUNETEENTH_FIRST_OBSERVED_YEAR:
        holidays.add(_shift_weekend_observance(date(year, 6, 19)))
    holidays.add(_shift_weekend_observance(date(year, 7, 4)))  # Independence Day
    holidays.add(_shift_weekend_observance(_nth_weekday_of_month(year, 9, _MONDAY, 1)))  # Labor
    holidays.add(_nth_weekday_of_month(year, 11, _THURSDAY, 4))  # Thanksgiving (never shifted)
    holidays.add(_shift_weekend_observance(date(year, 12, 25)))  # Christmas

    return frozenset(holidays)


def is_us_equity_trading_day(day: date) -> bool:
    """Whether NASDAQ/NYSE hold a regular trading session on ``day`` (UTC calendar date).

    ``False`` for Saturdays, Sundays, and the observed NASDAQ/NYSE holidays
    computed by ``_holidays_for_year``. See the module docstring for the
    known limitation around unscheduled ad hoc closures.
    """
    if day.weekday() in (_SATURDAY, _SUNDAY):
        return False
    return day not in _holidays_for_year(day.year)
