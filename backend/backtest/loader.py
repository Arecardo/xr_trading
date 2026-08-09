"""Orchestrates fetch + alignment into a single BT-001 entry point."""

from __future__ import annotations

from datetime import UTC, date, datetime, time, timedelta

from .alignment import align_daily_series
from .market_data_client import MarketInfoBarsClient
from .models import AlignedSeries, AssetClass


class HistoricalDataLoader:
    """Loads a gapless, UTC-calendar-day aligned historical series for one instrument.

    Combines ``MarketInfoBarsClient.fetch_daily_bars`` (I/O) with
    ``align_daily_series`` (pure alignment) behind a single call, which is
    what a later BT-002 (indicator calculation) caller is expected to use.
    """

    def __init__(self, bars_client: MarketInfoBarsClient) -> None:
        self._bars_client = bars_client

    async def load(
        self,
        *,
        instrument_code: str,
        provider: str,
        asset_type: AssetClass,
        start_date: date,
        end_date: date,
    ) -> AlignedSeries:
        """Load and align bars covering every UTC day in `[start_date, end_date]` inclusive.

        Translates the inclusive calendar-day range into the `/bars`
        contract's `[start_time, end_time)` half-open RFC3339 range (§2.2):
        ``start_time`` is midnight UTC on ``start_date``, ``end_time`` is
        midnight UTC the day *after* ``end_date`` -- so no bar dated after
        ``end_date`` can ever be requested from, or returned by, the
        upstream API in the first place (defence against future-data leakage
        happens at the request boundary; ``align_daily_series`` adds a
        second, independent defensive filter on the response).
        """
        if start_date > end_date:
            raise ValueError(f"start_date {start_date} must not be after end_date {end_date}")

        start_time = datetime.combine(start_date, time.min, tzinfo=UTC)
        end_time = datetime.combine(end_date, time.min, tzinfo=UTC) + timedelta(days=1)

        raw_bars = await self._bars_client.fetch_daily_bars(
            instrument_code=instrument_code,
            provider=provider,
            start_time=start_time,
            end_time=end_time,
        )

        return align_daily_series(
            instrument_code=instrument_code,
            provider=provider,
            asset_type=asset_type,
            start_date=start_date,
            end_date=end_date,
            raw_bars=raw_bars,
        )
