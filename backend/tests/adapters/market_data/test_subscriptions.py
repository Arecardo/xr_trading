"""Unit tests for adapters.market_data.subscriptions.

Covers: computing desired collection scope from portfolio membership
(fake ``PortfolioRepository``, no real DB) and diffing/reconciling it
against `market-info-service`'s collection-subscriptions admin API
(``httpx.MockTransport``, no real network).
"""

from __future__ import annotations

from decimal import Decimal
from uuid import UUID, uuid4

import httpx
import pytest

from adapters.market_data.errors import SubscriptionSyncError
from adapters.market_data.models import InstrumentRef
from adapters.market_data.subscriptions import (
    CollectionSubscriptionClient,
    compute_desired_instruments,
    sync_collection_subscriptions,
)
from domain.portfolios.models import Portfolio, PortfolioMember


class _FakePortfolioRepository:
    def __init__(
        self, portfolios: dict[UUID, Portfolio], members: dict[UUID, list[PortfolioMember]]
    ) -> None:
        self._portfolios = portfolios
        self._members = members

    def get(self, portfolio_id: UUID) -> Portfolio:
        return self._portfolios[portfolio_id]

    def list_members(self, portfolio_id: UUID, status: str | None = None) -> list[PortfolioMember]:
        members = self._members.get(portfolio_id, [])
        if status is None:
            return list(members)
        return [m for m in members if m.member_status == status]


class _StaticResolver:
    """Maps a fixed set of asset_ids to InstrumentRefs; unknown asset_ids resolve to nothing."""

    def __init__(self, mapping: dict[str, list[InstrumentRef]]) -> None:
        self._mapping = mapping

    def resolve(self, asset_id: str) -> list[InstrumentRef]:
        return self._mapping.get(asset_id, [])


def _portfolio(portfolio_id: UUID, benchmark: str | None) -> Portfolio:
    return Portfolio(
        portfolio_id=portfolio_id,
        name="Test",
        base_currency="USD",
        benchmark_asset_id=benchmark,
        risk_level="moderate",
        execution_mode="paper",
        status="active",
    )


def _member(portfolio_id: UUID, asset_id: str, status: str) -> PortfolioMember:
    return PortfolioMember(
        portfolio_id=portfolio_id,
        asset_id=asset_id,
        member_status=status,  # type: ignore[arg-type]
        target_weight_min=Decimal("0"),
        target_weight_max=Decimal("1"),
    )


# --- compute_desired_instruments ------------------------------------------


def test_compute_desired_instruments_includes_all_member_statuses_and_benchmark() -> None:
    portfolio_id = uuid4()
    btc_ref = InstrumentRef(
        provider="bybit", instrument_code="instrument.bybit.spot.btc-usdt", interval="1d"
    )
    nvda_ref = InstrumentRef(
        provider="longbridge", instrument_code="instrument.longbridge.us.nvda", interval="1d"
    )
    qqq_ref = InstrumentRef(
        provider="longbridge", instrument_code="instrument.longbridge.us.qqq", interval="1d"
    )

    repo = _FakePortfolioRepository(
        portfolios={portfolio_id: _portfolio(portfolio_id, benchmark="equity:nasdaq:QQQ")},
        members={
            portfolio_id: [
                _member(portfolio_id, "crypto:bybit:BTC-USDT", "candidate"),
                _member(portfolio_id, "equity:nasdaq:NVDA", "restricted"),
            ]
        },
    )
    resolver = _StaticResolver(
        {
            "crypto:bybit:BTC-USDT": [btc_ref],
            "equity:nasdaq:NVDA": [nvda_ref],
            "equity:nasdaq:QQQ": [qqq_ref],
        }
    )

    desired = compute_desired_instruments(repo, resolver, portfolio_ids=[portfolio_id])

    assert desired == {btc_ref, nvda_ref, qqq_ref}


def test_compute_desired_instruments_unions_across_portfolios() -> None:
    p1, p2 = uuid4(), uuid4()
    ref_a = InstrumentRef(
        provider="bybit", instrument_code="instrument.bybit.spot.btc-usdt", interval="1d"
    )
    ref_b = InstrumentRef(
        provider="longbridge", instrument_code="instrument.longbridge.us.nvda", interval="1d"
    )

    repo = _FakePortfolioRepository(
        portfolios={p1: _portfolio(p1, benchmark=None), p2: _portfolio(p2, benchmark=None)},
        members={
            p1: [_member(p1, "crypto:bybit:BTC-USDT", "held")],
            p2: [_member(p2, "equity:nasdaq:NVDA", "approved")],
        },
    )
    resolver = _StaticResolver({"crypto:bybit:BTC-USDT": [ref_a], "equity:nasdaq:NVDA": [ref_b]})

    desired = compute_desired_instruments(repo, resolver, portfolio_ids=[p1, p2])

    assert desired == {ref_a, ref_b}


def test_compute_desired_instruments_no_benchmark_is_fine() -> None:
    portfolio_id = uuid4()
    repo = _FakePortfolioRepository(
        portfolios={portfolio_id: _portfolio(portfolio_id, benchmark=None)},
        members={portfolio_id: []},
    )
    resolver = _StaticResolver({})

    desired = compute_desired_instruments(repo, resolver, portfolio_ids=[portfolio_id])

    assert desired == set()


# --- sync_collection_subscriptions ----------------------------------------


def _subscription_json(
    *, subscription_id: str, provider: str, instrument_code: str, interval: str, enabled: bool
) -> dict[str, object]:
    return {
        "subscription_id": subscription_id,
        "provider": provider,
        "instrument_code": instrument_code,
        "interval": interval,
        "enabled": enabled,
        "priority": 100,
        "close_delay_seconds": 120,
        "revision_delay_seconds": None,
    }


@pytest.mark.anyio
async def test_sync_creates_missing_enables_disabled_and_disables_undesired() -> None:
    want_new = InstrumentRef(
        provider="bybit", instrument_code="instrument.bybit.spot.btc-usdt", interval="1d"
    )
    want_reenable = InstrumentRef(
        provider="longbridge", instrument_code="instrument.longbridge.us.nvda", interval="1d"
    )
    want_unchanged = InstrumentRef(
        provider="longbridge", instrument_code="instrument.longbridge.us.qqq", interval="1d"
    )
    no_longer_wanted_id = str(uuid4())

    calls: list[tuple[str, str, dict[str, object] | None]] = []

    def handler(request: httpx.Request) -> httpx.Response:
        method = request.method
        path = request.url.path
        body = None
        if request.content:
            import json

            body = json.loads(request.content)
        calls.append((method, path, body))

        if method == "GET" and path == "/api/market-info/v1/collection-subscriptions":
            return httpx.Response(
                200,
                json={
                    "items": [
                        _subscription_json(
                            subscription_id=str(uuid4()),
                            provider="longbridge",
                            instrument_code="instrument.longbridge.us.nvda",
                            interval="1d",
                            enabled=False,
                        ),
                        _subscription_json(
                            subscription_id=str(uuid4()),
                            provider="longbridge",
                            instrument_code="instrument.longbridge.us.qqq",
                            interval="1d",
                            enabled=True,
                        ),
                        _subscription_json(
                            subscription_id=no_longer_wanted_id,
                            provider="bybit",
                            instrument_code="instrument.bybit.spot.eth-usdt",
                            interval="1d",
                            enabled=True,
                        ),
                    ],
                    "next_cursor": None,
                },
            )
        if method == "POST" and path == "/api/market-info/v1/collection-subscriptions":
            assert body is not None
            assert body["reason"] == "portfolio membership sync"
            return httpx.Response(
                200,
                json=_subscription_json(
                    subscription_id=str(uuid4()),
                    provider=str(body["provider"]),
                    instrument_code=str(body["instrument_code"]),
                    interval=str(body["interval"]),
                    enabled=True,
                ),
            )
        if method == "PATCH":
            subscription_id = path.rsplit("/", 1)[-1]
            assert body is not None
            enabled = bool(body["enabled"])
            # Reflect back plausible identity fields for whichever row this is.
            if subscription_id == no_longer_wanted_id:
                provider, instrument_code = "bybit", "instrument.bybit.spot.eth-usdt"
            else:
                provider, instrument_code = "longbridge", "instrument.longbridge.us.nvda"
            return httpx.Response(
                200,
                json=_subscription_json(
                    subscription_id=subscription_id,
                    provider=provider,
                    instrument_code=instrument_code,
                    interval="1d",
                    enabled=enabled,
                ),
            )
        raise AssertionError(f"unexpected request: {method} {path}")

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(transport=transport, base_url="http://market-info.test") as http:
        client = CollectionSubscriptionClient(http)
        result = await sync_collection_subscriptions(
            client,
            [want_new, want_reenable, want_unchanged],
            reason="portfolio membership sync",
        )

    assert result.created == (want_new,)
    assert result.enabled == (want_reenable,)
    assert result.unchanged_count == 1
    assert len(result.disabled) == 1
    assert result.disabled[0].instrument_code == "instrument.bybit.spot.eth-usdt"

    post_calls = [c for c in calls if c[0] == "POST"]
    assert len(post_calls) == 1
    patch_calls = [c for c in calls if c[0] == "PATCH"]
    assert len(patch_calls) == 2  # one re-enable, one disable


@pytest.mark.anyio
async def test_sync_is_a_noop_when_current_matches_desired() -> None:
    ref = InstrumentRef(
        provider="bybit", instrument_code="instrument.bybit.spot.btc-usdt", interval="1d"
    )

    def handler(request: httpx.Request) -> httpx.Response:
        if request.method == "GET":
            return httpx.Response(
                200,
                json={
                    "items": [
                        _subscription_json(
                            subscription_id=str(uuid4()),
                            provider="bybit",
                            instrument_code="instrument.bybit.spot.btc-usdt",
                            interval="1d",
                            enabled=True,
                        )
                    ],
                    "next_cursor": None,
                },
            )
        raise AssertionError(f"unexpected write call: {request.method} {request.url.path}")

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(transport=transport, base_url="http://market-info.test") as http:
        client = CollectionSubscriptionClient(http)
        result = await sync_collection_subscriptions(client, [ref], reason="noop check")

    assert result.created == ()
    assert result.enabled == ()
    assert result.disabled == ()
    assert result.unchanged_count == 1


@pytest.mark.anyio
async def test_sync_raises_subscription_sync_error_on_service_failure() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(500, json={"error": {"code": "INTERNAL_ERROR", "message": "boom"}})

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(transport=transport, base_url="http://market-info.test") as http:
        client = CollectionSubscriptionClient(http)
        with pytest.raises(SubscriptionSyncError):
            await sync_collection_subscriptions(
                client,
                [InstrumentRef(provider="bybit", instrument_code="x", interval="1d")],
                reason="failure check",
            )


@pytest.mark.anyio
async def test_sync_raises_on_conflict_creating_subscription() -> None:
    def handler(request: httpx.Request) -> httpx.Response:
        if request.method == "GET":
            return httpx.Response(200, json={"items": [], "next_cursor": None})
        return httpx.Response(
            409,
            json={
                "error": {
                    "code": "SUBSCRIPTION_ALREADY_EXISTS",
                    "message": "conflict",
                    "retryable": False,
                    "request_id": "req_test",
                }
            },
        )

    transport = httpx.MockTransport(handler)
    async with httpx.AsyncClient(transport=transport, base_url="http://market-info.test") as http:
        client = CollectionSubscriptionClient(http)
        with pytest.raises(SubscriptionSyncError, match="SUBSCRIPTION_ALREADY_EXISTS"):
            await sync_collection_subscriptions(
                client,
                [
                    InstrumentRef(
                        provider="bybit",
                        instrument_code="instrument.bybit.spot.btc-usdt",
                        interval="1d",
                    )
                ],
                reason="conflict check",
            )
