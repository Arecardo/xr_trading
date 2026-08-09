"""Data shapes for BT-001 historical data loading.

``AlignedSeries``/``AlignedBar`` are the internal, non-frozen-contract
output shape that BT-002 (indicator calculation) is expected to consume --
see the BT-001 task report for the rationale. ``RawBar`` is the
intermediate, unaligned shape parsed directly from the `market-info-service`
`/bars` response.

Money/price/volume fields use ``Decimal`` throughout
(python-backend-standards.md §1, §6) -- never ``float``.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date, datetime
from decimal import Decimal
from typing import Literal

AssetClass = Literal["STOCK", "ETF", "CRYPTO"]
"""Determines calendar behaviour: STOCK/ETF follow the US equity trading
calendar (``backtest.calendar``); CRYPTO is treated as 24/7 with fresh data
expected every UTC calendar day."""

PriceStatus = Literal["fresh", "stale"]
"""``fresh``: a real bar was returned by the provider for this UTC day.
``stale``: no new trading session occurred (US equity non-trading day); the
prior trading day's close was carried forward, per
`doc/technical/08_backtest_engine.md` §5."""


@dataclass(frozen=True)
class RawBar:
    """One provider-sourced daily bar, parsed from a `/bars` response entry.

    ``open_time`` is the bar's UTC-aware open timestamp; alignment buckets a
    bar into a UTC calendar day using ``open_time.date()``.
    """

    open_time: datetime
    close_time: datetime
    open: Decimal
    high: Decimal
    low: Decimal
    close: Decimal
    volume: Decimal
    quality_status: str


@dataclass(frozen=True)
class AlignedBar:
    """One UTC-calendar-day aligned data point.

    An ``AlignedSeries.bars`` sequence has exactly one ``AlignedBar`` per
    UTC calendar day in ``[start_date, end_date]`` -- never a gap, per the
    BT-001 completion condition ("全年 365 天无跳空").

    On a ``stale`` day, ``open``/``high``/``low``/``close`` are all set to
    the prior trading day's close (a flat carry-forward, since no session
    occurred) and ``volume`` is ``Decimal("0")``; ``source_open_time`` is
    ``None`` because there is no real bar backing the day.
    """

    trading_day: date
    open: Decimal
    high: Decimal
    low: Decimal
    close: Decimal
    volume: Decimal
    price_status: PriceStatus
    source_open_time: datetime | None


@dataclass(frozen=True)
class AlignedSeries:
    """A gapless, UTC-calendar-day aligned historical series for one instrument.

    Not a frozen cross-task contract (BT-001 is the only producer so far) --
    documented here for BT-002 to consume. ``bars`` is ordered ascending by
    ``trading_day`` and covers every day in ``[start_date, end_date]``
    inclusive.
    """

    instrument_code: str
    provider: str
    asset_type: AssetClass
    start_date: date
    end_date: date
    bars: tuple[AlignedBar, ...]
