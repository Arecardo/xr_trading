"""Unit tests for adapters.market_data.client.MarketInfoClient.

Per python-backend-standards.md §9, no real network calls: every test drives
the client via ``httpx.MockTransport``.
"""

from __future__ import annotations

import json
from datetime import UTC, datetime
from decimal import Decimal
from uuid import UUID, uuid4

import httpx
import pytest

from adapters.market_data.client import MarketInfoClient
from adapters.market_data.errors import MarketDataResponseError, MarketDataUnavailableError


def _quote_payload(instrument_id: str, price: str) -> dict[str, object]:
    return {
        "instrument_id": instrument_id,
        "instrument_code": "instrument.bybit.spot.btc-usdt",
        "provider": "bybit",
        "provider_instrument_id": instrument_id,
        "provider_instrument_code": "provider.bybit.spot.btcusdt",
        "provider_symbol": "BTCUSDT",
        "price": price,
        "bid_price": "62349.8",
        "bid_size": "0.42",
        "ask_price": "62350.2",
        "ask_size": "0.35",
        "open_24h": "61000",
        "high_24h": "63000",
        "low_24h": "60500",
        "base_volume_24h": "15234.8",
        "quote_volume_24h": "941234567.8",
        "quote_currency": "USDT",
        "market_time": "2026-07-02T08:00:00Z",
        "received_at": "2026-07-02T08:00:01Z",
        "quality_status": "valid",
    }


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


def _precision_payload(instrument_id: str) -> dict[str, object]:
    return {
        "instrument_id": instrument_id,
        "instrument_code": "instrument.bybit.spot.btc-usdt",
        "price_scale": 2,
        "quantity_scale": 6,
        "lot_size": "0.000001",
        "min_quantity": "0.0001",
        "as_of": "2026-08-05T00:00:00Z",
    }


# --- get_latest_quotes ------------------------------------------------


@pytest.mark.anyio
async def test_get_latest_quotes_parses_asset_and_quotes() -> None:
    instrument_id = str(uuid4())
    asset_id = str(uuid4())

    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.path == "/api/market-info/v1/quotes/latest"
        assert dict(request.url.params) == {"asset_code": "asset.crypto.btc"}
        return httpx.Response(
            200,
            json={
                "asset": {
                    "asset_id": asset_id,
                    "asset_code": "asset.crypto.btc",
                    "asset_type": "crypto",
                },
                "quotes": [_quote_payload(instrument_id, "62350.12")],
            },
        )

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(transport=transport, base_url="http://market-info.test") as http:
        client = MarketInfoClient(http)
        result = await client.get_latest_quotes(asset_code="asset.crypto.btc")

    assert result.asset_id == UUID(asset_id)
    assert result.asset_code == "asset.crypto.btc"
    assert len(result.quotes) == 1
    assert result.quotes[0].price == Decimal("62350.12")
    assert result.quotes[0].instrument_id == UUID(instrument_id)


@pytest.mark.anyio
async def test_get_latest_quotes_empty_quotes_is_not_an_error() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            200,
            json={
                "asset": {
                    "asset_id": str(uuid4()),
                    "asset_code": "asset.crypto.btc",
                    "asset_type": "crypto",
                },
                "quotes": [],
            },
        )

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(transport=transport, base_url="http://market-info.test") as http:
        client = MarketInfoClient(http)
        result = await client.get_latest_quotes(asset_code="asset.crypto.btc")

    assert result.quotes == ()


@pytest.mark.anyio
async def test_get_latest_quotes_raises_on_unknown_asset() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            404,
            json={
                "error": {
                    "code": "ASSET_NOT_FOUND",
                    "message": "unknown asset",
                    "retryable": False,
                    "request_id": "req_test",
                }
            },
        )

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(transport=transport, base_url="http://market-info.test") as http:
        client = MarketInfoClient(http)
        with pytest.raises(MarketDataResponseError, match="ASSET_NOT_FOUND"):
            await client.get_latest_quotes(asset_code="asset.crypto.unknown")


@pytest.mark.anyio
async def test_get_latest_quotes_requires_asset_or_instrument_code() -> None:
    transport = httpx.MockTransport(lambda request: httpx.Response(200, json={"quotes": []}))
    async with httpx.AsyncClient(transport=transport, base_url="http://x.test") as http:
        client = MarketInfoClient(http)
        with pytest.raises(ValueError, match="asset_code or instrument_code"):
            await client.get_latest_quotes()


@pytest.mark.anyio
async def test_get_latest_quotes_raises_on_network_failure() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ConnectError("connection refused", request=request)

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(transport=transport, base_url="http://market-info.test") as http:
        client = MarketInfoClient(http)
        with pytest.raises(MarketDataUnavailableError):
            await client.get_latest_quotes(asset_code="asset.crypto.btc")


@pytest.mark.anyio
async def test_get_latest_quotes_raises_on_timeout() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        raise httpx.ReadTimeout("timed out", request=request)

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(transport=transport, base_url="http://market-info.test") as http:
        client = MarketInfoClient(http)
        with pytest.raises(MarketDataUnavailableError):
            await client.get_latest_quotes(asset_code="asset.crypto.btc")


# --- get_bars -----------------------------------------------------------


@pytest.mark.anyio
async def test_get_bars_follows_pagination_cursor() -> None:
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
        return httpx.Response(
            200,
            json={
                "bars": [_bar_payload("2024-01-02T00:00:00Z", "101.00")],
                "next_cursor": None,
            },
        )

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(transport=transport, base_url="http://market-info.test") as http:
        client = MarketInfoClient(http)
        bars = await client.get_bars(
            instrument_code="instrument.bybit.spot.btc-usdt",
            provider="bybit",
            interval="1h",
            start_time=datetime(2024, 1, 1, tzinfo=UTC),
            end_time=datetime(2024, 1, 3, tzinfo=UTC),
        )

    assert len(calls) == 2
    assert [bar.close for bar in bars] == [Decimal("100.00"), Decimal("101.00")]


@pytest.mark.anyio
async def test_get_bars_rejects_naive_datetime() -> None:
    transport = httpx.MockTransport(lambda request: httpx.Response(200, json={"bars": []}))
    async with httpx.AsyncClient(transport=transport, base_url="http://market-info.test") as http:
        client = MarketInfoClient(http)
        with pytest.raises(ValueError, match="timezone-aware"):
            await client.get_bars(
                instrument_code="instrument.longbridge.us.nvda",
                provider="longbridge",
                interval="1d",
                start_time=datetime(2024, 1, 1),
            )


@pytest.mark.anyio
async def test_get_bars_raises_on_unsupported_interval() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(
            400,
            json={
                "error": {
                    "code": "UNSUPPORTED_INTERVAL",
                    "message": "interval not supported",
                    "retryable": False,
                    "request_id": "req_test",
                }
            },
        )

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(transport=transport, base_url="http://market-info.test") as http:
        client = MarketInfoClient(http)
        with pytest.raises(MarketDataResponseError, match="UNSUPPORTED_INTERVAL"):
            await client.get_bars(
                instrument_code="instrument.bybit.spot.btc-usdt",
                provider="bybit",
                interval="1w",
            )


# --- get_instrument_options ----------------------------------------------


@pytest.mark.anyio
async def test_get_instrument_options_parses_items_and_paginates() -> None:
    calls: list[dict[str, str]] = []

    def handler(request: httpx.Request) -> httpx.Response:
        params = dict(request.url.params)
        calls.append(params)
        assert params["enabled"] == "true"
        if "cursor" not in params:
            return httpx.Response(
                200,
                json={
                    "items": [
                        {
                            "instrument_id": str(uuid4()),
                            "instrument_code": "instrument.bybit.spot.btc-usdt",
                            "display_name": "BTC/USDT",
                            "providers": [
                                {
                                    "provider_code": "bybit",
                                    "display_name": "Bybit",
                                    "is_default": True,
                                    "priority": 10,
                                    "supported_intervals": ["1h", "1d"],
                                }
                            ],
                        }
                    ],
                    "next_cursor": "cursor-1",
                },
            )
        return httpx.Response(
            200,
            json={
                "items": [
                    {
                        "instrument_id": str(uuid4()),
                        "instrument_code": "instrument.bybit.spot.eth-usdt",
                        "display_name": "ETH/USDT",
                        "providers": [],
                    }
                ],
                "next_cursor": None,
            },
        )

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(transport=transport, base_url="http://market-info.test") as http:
        client = MarketInfoClient(http)
        items = await client.get_instrument_options(asset_code="asset.crypto.btc")

    assert len(calls) == 2
    assert [item.instrument_code for item in items] == [
        "instrument.bybit.spot.btc-usdt",
        "instrument.bybit.spot.eth-usdt",
    ]
    assert items[0].providers[0].provider_code == "bybit"
    assert items[0].providers[0].supported_intervals == ("1h", "1d")


# --- get_precision_batch --------------------------------------------------


@pytest.mark.anyio
async def test_get_precision_batch_parses_items_and_missing_ids() -> None:
    present_id = uuid4()
    missing_id = uuid4()

    def handler(request: httpx.Request) -> httpx.Response:
        assert request.url.path == "/api/market-info/v1/instruments/precision:batch"
        payload = json.loads(request.read())
        assert payload["instrument_ids"] == [str(present_id), str(missing_id)]
        return httpx.Response(
            200,
            json={
                "items": [_precision_payload(str(present_id))],
                "missing_instrument_ids": [str(missing_id)],
            },
        )

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(transport=transport, base_url="http://market-info.test") as http:
        client = MarketInfoClient(http)
        result = await client.get_precision_batch([present_id, missing_id])

    assert len(result.items) == 1
    assert result.items[0].instrument_id == present_id
    assert result.items[0].lot_size == Decimal("0.000001")
    assert result.missing_instrument_ids == (missing_id,)


@pytest.mark.anyio
async def test_get_precision_batch_rejects_empty_list() -> None:
    transport = httpx.MockTransport(lambda request: httpx.Response(200, json={}))
    async with httpx.AsyncClient(transport=transport, base_url="http://market-info.test") as http:
        client = MarketInfoClient(http)
        with pytest.raises(ValueError, match="empty"):
            await client.get_precision_batch([])


@pytest.mark.anyio
async def test_get_precision_batch_rejects_over_limit() -> None:
    transport = httpx.MockTransport(lambda request: httpx.Response(200, json={}))
    async with httpx.AsyncClient(transport=transport, base_url="http://market-info.test") as http:
        client = MarketInfoClient(http)
        with pytest.raises(ValueError, match="100"):
            await client.get_precision_batch([uuid4() for _ in range(101)])


@pytest.mark.anyio
async def test_get_precision_batch_raises_on_service_error() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(500, json={"error": {"code": "INTERNAL_ERROR", "message": "boom"}})

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(transport=transport, base_url="http://market-info.test") as http:
        client = MarketInfoClient(http)
        with pytest.raises(MarketDataResponseError, match="INTERNAL_ERROR"):
            await client.get_precision_batch([uuid4()])
