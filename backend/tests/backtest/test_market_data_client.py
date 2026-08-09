"""Unit tests for backtest.market_data_client.MarketInfoBarsClient.

Per python-backend-standards.md §9, no real network calls: every test drives
the client via ``httpx.MockTransport``.
"""

from __future__ import annotations

from datetime import UTC, datetime
from decimal import Decimal

import httpx
import pytest

from backtest.errors import MarketDataResponseError
from backtest.market_data_client import MarketInfoBarsClient


def _bar_payload(open_time: str, close: str) -> dict[str, object]:
    return {
        "open_time": open_time,
        "close_time": open_time,
        "open": close,
        "high": close,
        "low": close,
        "close": close,
        "volume": "123.456",
        "quote_volume": "999",
        "trade_count": 10,
        "revision": 1,
        "is_closed": True,
        "quality_status": "valid",
        "provider_updated_at": open_time,
        "collected_at": open_time,
    }


@pytest.mark.anyio
async def test_fetch_daily_bars_parses_single_page_response() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.path == "/api/market-info/v1/bars"
        params = dict(request.url.params)
        assert params["instrument_code"] == "instrument.bybit.spot.btc-usdt"
        assert params["provider"] == "bybit"
        assert params["interval"] == "1d"
        assert params["order"] == "asc"
        assert params["start_time"] == "2024-01-01T00:00:00Z"
        assert params["end_time"] == "2024-01-03T00:00:00Z"
        return httpx.Response(
            200,
            json={
                "instrument": {"instrument_code": "instrument.bybit.spot.btc-usdt"},
                "provider": {"provider_code": "bybit"},
                "interval": "1d",
                "order": "asc",
                "bars": [
                    _bar_payload("2024-01-01T00:00:00Z", "42000.00"),
                    _bar_payload("2024-01-02T00:00:00Z", "42500.50"),
                ],
                "next_cursor": None,
            },
        )

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(
        transport=transport, base_url="http://market-info.test"
    ) as http_client:
        client = MarketInfoBarsClient(http_client)
        bars = await client.fetch_daily_bars(
            instrument_code="instrument.bybit.spot.btc-usdt",
            provider="bybit",
            start_time=datetime(2024, 1, 1, tzinfo=UTC),
            end_time=datetime(2024, 1, 3, tzinfo=UTC),
        )

    assert len(bars) == 2
    assert bars[0].close == Decimal("42000.00")
    assert bars[1].close == Decimal("42500.50")
    assert bars[0].open_time == datetime(2024, 1, 1, tzinfo=UTC)
    assert bars[0].volume == Decimal("123.456")


@pytest.mark.anyio
async def test_fetch_daily_bars_follows_pagination_cursor() -> None:
    calls: list[dict[str, str]] = []

    def handler(request: httpx.Request) -> httpx.Response:
        params = dict(request.url.params)
        calls.append(params)
        if "cursor" not in params:
            return httpx.Response(
                200,
                json={
                    "bars": [_bar_payload("2024-01-01T00:00:00Z", "100.00")],
                    "next_cursor": "opaque-cursor-1",
                },
            )
        assert params["cursor"] == "opaque-cursor-1"
        # Second page must resend identical query params (§2.2 contract).
        assert params["instrument_code"] == "instrument.longbridge.us.nvda"
        assert params["start_time"] == calls[0]["start_time"]
        return httpx.Response(
            200,
            json={
                "bars": [_bar_payload("2024-01-02T00:00:00Z", "101.00")],
                "next_cursor": None,
            },
        )

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(
        transport=transport, base_url="http://market-info.test"
    ) as http_client:
        client = MarketInfoBarsClient(http_client)
        bars = await client.fetch_daily_bars(
            instrument_code="instrument.longbridge.us.nvda",
            provider="longbridge",
            start_time=datetime(2024, 1, 1, tzinfo=UTC),
            end_time=datetime(2024, 1, 3, tzinfo=UTC),
        )

    assert len(calls) == 2
    assert [bar.close for bar in bars] == [Decimal("100.00"), Decimal("101.00")]


@pytest.mark.anyio
async def test_fetch_daily_bars_raises_on_error_envelope() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            404,
            json={
                "error": {
                    "code": "INSTRUMENT_NOT_FOUND",
                    "message": "unknown instrument",
                    "retryable": False,
                    "request_id": "req_test",
                }
            },
        )

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(
        transport=transport, base_url="http://market-info.test"
    ) as http_client:
        client = MarketInfoBarsClient(http_client)
        with pytest.raises(MarketDataResponseError, match="INSTRUMENT_NOT_FOUND"):
            await client.fetch_daily_bars(
                instrument_code="instrument.unknown",
                provider="longbridge",
                start_time=datetime(2024, 1, 1, tzinfo=UTC),
                end_time=datetime(2024, 1, 3, tzinfo=UTC),
            )


@pytest.mark.anyio
async def test_fetch_daily_bars_raises_on_malformed_decimal_field() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            200,
            json={
                "bars": [_bar_payload("2024-01-01T00:00:00Z", "not-a-number")],
                "next_cursor": None,
            },
        )

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(
        transport=transport, base_url="http://market-info.test"
    ) as http_client:
        client = MarketInfoBarsClient(http_client)
        with pytest.raises(MarketDataResponseError):
            await client.fetch_daily_bars(
                instrument_code="instrument.longbridge.us.nvda",
                provider="longbridge",
                start_time=datetime(2024, 1, 1, tzinfo=UTC),
                end_time=datetime(2024, 1, 3, tzinfo=UTC),
            )


@pytest.mark.anyio
async def test_fetch_daily_bars_rejects_naive_datetimes() -> None:
    transport = httpx.MockTransport(lambda request: httpx.Response(200, json={"bars": []}))
    async with httpx.AsyncClient(
        transport=transport, base_url="http://market-info.test"
    ) as http_client:
        client = MarketInfoBarsClient(http_client)
        with pytest.raises(ValueError, match="timezone-aware"):
            await client.fetch_daily_bars(
                instrument_code="instrument.longbridge.us.nvda",
                provider="longbridge",
                start_time=datetime(2024, 1, 1),  # naive
                end_time=datetime(2024, 1, 3, tzinfo=UTC),
            )


@pytest.mark.anyio
async def test_list_instruments_parses_items_and_follows_cursor() -> None:
    calls: list[dict[str, str]] = []

    def handler(request: httpx.Request) -> httpx.Response:
        params = dict(request.url.params)
        calls.append(params)
        if "cursor" not in params:
            return httpx.Response(
                200,
                json={
                    "items": [{"instrument_code": "instrument.bybit.spot.btc-usdt"}],
                    "next_cursor": "cursor-1",
                },
            )
        return httpx.Response(
            200,
            json={
                "items": [{"instrument_code": "instrument.bybit.spot.eth-usdt"}],
                "next_cursor": None,
            },
        )

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(
        transport=transport, base_url="http://market-info.test"
    ) as http_client:
        client = MarketInfoBarsClient(http_client)
        items = await client.list_instruments(asset_code="asset.crypto.btc")

    assert len(calls) == 2
    assert [item["instrument_code"] for item in items] == [
        "instrument.bybit.spot.btc-usdt",
        "instrument.bybit.spot.eth-usdt",
    ]
