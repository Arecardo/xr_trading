"""Unit tests for backtest.loader.HistoricalDataLoader (fetch + alignment glue)."""

from __future__ import annotations

from datetime import date

import httpx
import pytest

from backtest.loader import HistoricalDataLoader
from backtest.market_data_client import MarketInfoBarsClient


def _bar_payload(open_time: str, close: str) -> dict[str, object]:
    return {
        "open_time": open_time,
        "close_time": open_time,
        "open": close,
        "high": close,
        "low": close,
        "close": close,
        "volume": "10",
        "quality_status": "valid",
    }


@pytest.mark.anyio
async def test_load_requests_half_open_range_covering_inclusive_end_date() -> None:
    # end_date=2024-01-08 (inclusive) must translate to end_time=2024-01-09T00:00:00Z
    # (exclusive) so the day itself is actually included in the upstream query.
    captured_params: dict[str, str] = {}

    def handler(request: httpx.Request) -> httpx.Response:
        captured_params.update(dict(request.url.params))
        return httpx.Response(
            200,
            json={
                "bars": [
                    _bar_payload("2024-01-05T00:00:00Z", "100.00"),
                    _bar_payload("2024-01-08T00:00:00Z", "103.00"),
                ],
                "next_cursor": None,
            },
        )

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(
        transport=transport, base_url="http://market-info.test"
    ) as http_client:
        loader = HistoricalDataLoader(MarketInfoBarsClient(http_client))
        series = await loader.load(
            instrument_code="instrument.longbridge.us.nvda",
            provider="longbridge",
            asset_type="STOCK",
            start_date=date(2024, 1, 5),
            end_date=date(2024, 1, 8),
        )

    assert captured_params["start_time"] == "2024-01-05T00:00:00Z"
    assert captured_params["end_time"] == "2024-01-09T00:00:00Z"  # exclusive, day after end_date

    assert series.start_date == date(2024, 1, 5)
    assert series.end_date == date(2024, 1, 8)
    assert len(series.bars) == 4  # Fri, Sat, Sun, Mon -- gapless
    by_day = {bar.trading_day: bar for bar in series.bars}
    assert by_day[date(2024, 1, 6)].price_status == "stale"  # Saturday carried forward
    assert by_day[date(2024, 1, 8)].price_status == "fresh"


@pytest.mark.anyio
async def test_load_rejects_start_after_end() -> None:
    async with httpx.AsyncClient(
        transport=httpx.MockTransport(lambda r: httpx.Response(200, json={"bars": []})),
        base_url="http://market-info.test",
    ) as http_client:
        loader = HistoricalDataLoader(MarketInfoBarsClient(http_client))
        with pytest.raises(ValueError, match="must not be after"):
            await loader.load(
                instrument_code="instrument.longbridge.us.nvda",
                provider="longbridge",
                asset_type="STOCK",
                start_date=date(2024, 1, 8),
                end_date=date(2024, 1, 5),
            )
