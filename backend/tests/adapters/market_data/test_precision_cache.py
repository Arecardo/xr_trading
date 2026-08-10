"""Unit tests for adapters.market_data.precision_cache.PrecisionCache.

Covers: TTL cache hit/expiry, chunked refresh, and fail-closed behaviour
when the batch endpoint reports an instrument missing or the underlying
client errors/is unreachable. Uses a fake ``MarketInfoClient``-shaped stub
(not ``httpx.MockTransport``) since the unit under test only calls
``get_precision_batch`` -- exercising the real HTTP layer is already covered
by ``test_client.py``.
"""

from __future__ import annotations

from collections.abc import Sequence
from datetime import UTC, datetime, timedelta
from decimal import Decimal
from uuid import UUID, uuid4

import pytest

from adapters.market_data.errors import (
    MarketDataResponseError,
    MarketDataUnavailableError,
    PrecisionUnavailableError,
)
from adapters.market_data.models import InstrumentPrecision, PrecisionBatchResult
from adapters.market_data.precision_cache import PrecisionCache


def _precision(instrument_id: UUID, *, as_of: datetime) -> InstrumentPrecision:
    return InstrumentPrecision(
        instrument_id=instrument_id,
        instrument_code="instrument.bybit.spot.btc-usdt",
        price_scale=2,
        quantity_scale=6,
        lot_size=Decimal("0.000001"),
        min_quantity=Decimal("0.0001"),
        as_of=as_of,
    )


class _FakeClock:
    def __init__(self, start: datetime) -> None:
        self._now = start

    def __call__(self) -> datetime:
        return self._now

    def advance(self, delta: timedelta) -> None:
        self._now += delta


class _FakeMarketInfoClient:
    """Records every ``get_precision_batch`` call; responses are queued or computed."""

    def __init__(self) -> None:
        self.calls: list[list[UUID]] = []
        self.responses: list[PrecisionBatchResult | Exception] = []

    async def get_precision_batch(self, instrument_ids: Sequence[UUID]) -> PrecisionBatchResult:
        self.calls.append(list(instrument_ids))
        response = self.responses.pop(0)
        if isinstance(response, Exception):
            raise response
        return response


@pytest.mark.anyio
async def test_get_fetches_on_cache_miss_and_caches_result() -> None:
    instrument_id = uuid4()
    now = datetime(2026, 1, 1, tzinfo=UTC)
    clock = _FakeClock(now)
    fake_client = _FakeMarketInfoClient()
    fake_client.responses.append(
        PrecisionBatchResult(
            items=(_precision(instrument_id, as_of=now),), missing_instrument_ids=()
        )
    )
    cache = PrecisionCache(fake_client, clock=clock)

    result = await cache.get(instrument_id)

    assert result.lot_size == Decimal("0.000001")
    assert len(fake_client.calls) == 1

    # Second call within TTL must be served from cache -- no second network call.
    result2 = await cache.get(instrument_id)
    assert result2 == result
    assert len(fake_client.calls) == 1


@pytest.mark.anyio
async def test_get_refetches_after_ttl_expiry() -> None:
    instrument_id = uuid4()
    start = datetime(2026, 1, 1, tzinfo=UTC)
    clock = _FakeClock(start)
    fake_client = _FakeMarketInfoClient()
    fake_client.responses.append(
        PrecisionBatchResult(
            items=(_precision(instrument_id, as_of=start),), missing_instrument_ids=()
        )
    )
    fake_client.responses.append(
        PrecisionBatchResult(
            items=(_precision(instrument_id, as_of=start),), missing_instrument_ids=()
        )
    )
    cache = PrecisionCache(fake_client, ttl=timedelta(minutes=1), clock=clock)

    await cache.get(instrument_id)
    assert len(fake_client.calls) == 1

    clock.advance(timedelta(minutes=2))
    await cache.get(instrument_id)
    assert len(fake_client.calls) == 2


@pytest.mark.anyio
async def test_get_raises_fail_closed_when_instrument_reported_missing() -> None:
    instrument_id = uuid4()
    now = datetime(2026, 1, 1, tzinfo=UTC)
    fake_client = _FakeMarketInfoClient()
    fake_client.responses.append(
        PrecisionBatchResult(items=(), missing_instrument_ids=(instrument_id,))
    )
    cache = PrecisionCache(fake_client, clock=_FakeClock(now))

    with pytest.raises(PrecisionUnavailableError):
        await cache.get(instrument_id)


@pytest.mark.anyio
async def test_missing_result_is_cached_and_does_not_refetch_within_ttl() -> None:
    instrument_id = uuid4()
    now = datetime(2026, 1, 1, tzinfo=UTC)
    fake_client = _FakeMarketInfoClient()
    fake_client.responses.append(
        PrecisionBatchResult(items=(), missing_instrument_ids=(instrument_id,))
    )
    cache = PrecisionCache(fake_client, clock=_FakeClock(now))

    with pytest.raises(PrecisionUnavailableError):
        await cache.get(instrument_id)
    with pytest.raises(PrecisionUnavailableError):
        await cache.get(instrument_id)

    assert len(fake_client.calls) == 1


@pytest.mark.anyio
async def test_get_fails_closed_on_service_error_without_using_stale_value() -> None:
    instrument_id = uuid4()
    start = datetime(2026, 1, 1, tzinfo=UTC)
    clock = _FakeClock(start)
    fake_client = _FakeMarketInfoClient()
    fake_client.responses.append(
        PrecisionBatchResult(
            items=(_precision(instrument_id, as_of=start),), missing_instrument_ids=()
        )
    )
    fake_client.responses.append(MarketDataResponseError("market-info-service returned 500"))
    cache = PrecisionCache(fake_client, ttl=timedelta(minutes=1), clock=clock)

    fresh = await cache.get(instrument_id)
    assert fresh.lot_size == Decimal("0.000001")

    clock.advance(timedelta(minutes=2))
    with pytest.raises(PrecisionUnavailableError):
        await cache.get(instrument_id)


@pytest.mark.anyio
async def test_get_fails_closed_when_service_unreachable() -> None:
    instrument_id = uuid4()
    now = datetime(2026, 1, 1, tzinfo=UTC)
    fake_client = _FakeMarketInfoClient()
    fake_client.responses.append(MarketDataUnavailableError("connection refused"))
    cache = PrecisionCache(fake_client, clock=_FakeClock(now))

    with pytest.raises(PrecisionUnavailableError):
        await cache.get(instrument_id)


@pytest.mark.anyio
async def test_get_many_fetches_only_missing_and_expired_entries() -> None:
    fresh_id = uuid4()
    expired_id = uuid4()
    new_id = uuid4()
    start = datetime(2026, 1, 1, tzinfo=UTC)
    clock = _FakeClock(start)
    fake_client = _FakeMarketInfoClient()
    cache = PrecisionCache(fake_client, ttl=timedelta(seconds=60), clock=clock)

    # t=0: fetch `expired_id` only.
    fake_client.responses.append(
        PrecisionBatchResult(
            items=(_precision(expired_id, as_of=start),), missing_instrument_ids=()
        )
    )
    await cache.get_many([expired_id])
    assert len(fake_client.calls) == 1

    # t=70: `expired_id` (fetched at t=0, ttl=60s) is now stale. Fetch `fresh_id` now.
    clock.advance(timedelta(seconds=70))
    fake_client.responses.append(
        PrecisionBatchResult(
            items=(_precision(fresh_id, as_of=clock()),), missing_instrument_ids=()
        )
    )
    await cache.get_many([fresh_id])
    assert len(fake_client.calls) == 2

    # t=100: `fresh_id` (fetched at t=70) is still within its ttl (30s < 60s) and must
    # be served from cache; `expired_id` (fetched at t=0) and `new_id` (never fetched)
    # both need a refetch.
    clock.advance(timedelta(seconds=30))
    fake_client.responses.append(
        PrecisionBatchResult(
            items=(_precision(expired_id, as_of=clock()), _precision(new_id, as_of=clock())),
            missing_instrument_ids=(),
        )
    )
    result = await cache.get_many([fresh_id, expired_id, new_id])

    assert len(fake_client.calls) == 3
    third_call_ids = set(fake_client.calls[2])
    assert fresh_id not in third_call_ids
    assert expired_id in third_call_ids
    assert new_id in third_call_ids
    assert set(result) == {fresh_id, expired_id, new_id}


@pytest.mark.anyio
async def test_get_many_chunks_requests_over_the_batch_limit() -> None:
    ids = [uuid4() for _ in range(150)]
    start = datetime(2026, 1, 1, tzinfo=UTC)
    fake_client = _FakeMarketInfoClient()
    fake_client.responses.append(
        PrecisionBatchResult(
            items=tuple(_precision(i, as_of=start) for i in ids[:100]), missing_instrument_ids=()
        )
    )
    fake_client.responses.append(
        PrecisionBatchResult(
            items=tuple(_precision(i, as_of=start) for i in ids[100:]), missing_instrument_ids=()
        )
    )
    cache = PrecisionCache(fake_client, clock=_FakeClock(start))

    result = await cache.get_many(ids)

    assert len(fake_client.calls) == 2
    assert len(fake_client.calls[0]) == 100
    assert len(fake_client.calls[1]) == 50
    assert len(result) == 150


@pytest.mark.anyio
async def test_invalidate_forces_refetch() -> None:
    instrument_id = uuid4()
    now = datetime(2026, 1, 1, tzinfo=UTC)
    fake_client = _FakeMarketInfoClient()
    fake_client.responses.append(
        PrecisionBatchResult(
            items=(_precision(instrument_id, as_of=now),), missing_instrument_ids=()
        )
    )
    fake_client.responses.append(
        PrecisionBatchResult(
            items=(_precision(instrument_id, as_of=now),), missing_instrument_ids=()
        )
    )
    cache = PrecisionCache(fake_client, clock=_FakeClock(now))

    await cache.get(instrument_id)
    cache.invalidate(instrument_id)
    await cache.get(instrument_id)

    assert len(fake_client.calls) == 2
