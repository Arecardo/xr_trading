"""UTC-calendar-day alignment (BT-001 core logic).

Implements `doc/technical/08_backtest_engine.md` §5: `time_step` is a UTC
calendar day, stepped for the full requested range regardless of US market
holidays. For each day: a US equity/ETF carries forward the prior trading
day's close and is marked ``price_status="stale"`` on a non-trading day;
crypto is expected to have a fresh bar every day (24/7).

Pure function, no I/O -- takes already-fetched ``RawBar``s (see
``market_data_client.MarketInfoBarsClient.fetch_daily_bars``) and produces an
``AlignedSeries``.
"""

from __future__ import annotations

from collections.abc import Sequence
from datetime import date, timedelta
from decimal import Decimal

from .calendar import is_us_equity_trading_day
from .errors import MissingTradingDayBarError
from .models import AlignedBar, AlignedSeries, AssetClass, RawBar


def align_daily_series(
    *,
    instrument_code: str,
    provider: str,
    asset_type: AssetClass,
    start_date: date,
    end_date: date,
    raw_bars: Sequence[RawBar],
) -> AlignedSeries:
    """Align ``raw_bars`` into a gapless series covering every UTC day in
    `[start_date, end_date]` (inclusive on both ends).

    Behaviour per day:
    - A day with a matching raw bar (bucketed by ``open_time.date()``) is
      ``price_status="fresh"``.
    - CRYPTO: every day is expected to be fresh. A day with no matching bar
      raises ``MissingTradingDayBarError`` -- crypto trades 24/7, so a gap
      is a real data problem, never an ordinary non-trading day.
    - STOCK/ETF: a day that is a scheduled US equity trading day (per
      ``backtest.calendar.is_us_equity_trading_day``) with no matching bar
      also raises ``MissingTradingDayBarError`` (a real gap, e.g. a
      provider outage, must not be silently mistaken for a holiday). A
      non-trading day carries the prior day's close forward as
      ``price_status="stale"``.
    - If the very first day(s) of the range are non-trading days with no
      prior fresh bar yet observed (e.g. the range starts on a weekend and
      no bar was returned before it), there is nothing to carry forward and
      ``MissingTradingDayBarError`` is raised -- callers should start
      ranges on/after the instrument's actual first trading day.

    Defensively drops (rather than uses) any raw bar whose day falls outside
    `[start_date, end_date]` -- this guards against no future/out-of-range
    data leaking into the aligned series even if the upstream API, a proxy,
    or (in tests) a misbehaving mock server returns more than was asked for.
    When multiple raw bars land on the same UTC day (should not happen for a
    single Instrument/Provider/interval per the `/bars` contract, but is not
    assumed), the one with the latest ``open_time`` wins.
    """
    if start_date > end_date:
        raise ValueError(f"start_date {start_date} must not be after end_date {end_date}")

    bars_by_day: dict[date, RawBar] = {}
    for bar in raw_bars:
        day = bar.open_time.date()
        if day < start_date or day > end_date:
            continue
        existing = bars_by_day.get(day)
        if existing is None or bar.open_time > existing.open_time:
            bars_by_day[day] = bar

    aligned: list[AlignedBar] = []
    last_close: Decimal | None = None
    total_days = (end_date - start_date).days + 1

    for offset in range(total_days):
        day = start_date + timedelta(days=offset)
        raw = bars_by_day.get(day)

        if raw is not None:
            aligned.append(
                AlignedBar(
                    trading_day=day,
                    open=raw.open,
                    high=raw.high,
                    low=raw.low,
                    close=raw.close,
                    volume=raw.volume,
                    price_status="fresh",
                    source_open_time=raw.open_time,
                )
            )
            last_close = raw.close
            continue

        is_expected_fresh = asset_type == "CRYPTO" or is_us_equity_trading_day(day)
        if is_expected_fresh:
            reason = (
                "crypto (24/7)" if asset_type == "CRYPTO" else "scheduled US equity trading day"
            )
            raise MissingTradingDayBarError(
                f"{instrument_code}/{provider}: expected a fresh bar for "
                f"{day.isoformat()} ({reason}) but market-info-service returned none"
            )

        if last_close is None:
            raise MissingTradingDayBarError(
                f"{instrument_code}/{provider}: no prior close available to carry "
                f"forward onto non-trading day {day.isoformat()} -- the requested "
                "range must start on or after the instrument's first available "
                "trading day"
            )

        aligned.append(
            AlignedBar(
                trading_day=day,
                open=last_close,
                high=last_close,
                low=last_close,
                close=last_close,
                volume=Decimal(0),
                price_status="stale",
                source_open_time=None,
            )
        )

    return AlignedSeries(
        instrument_code=instrument_code,
        provider=provider,
        asset_type=asset_type,
        start_date=start_date,
        end_date=end_date,
        bars=tuple(aligned),
    )
