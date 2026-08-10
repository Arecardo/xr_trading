"""TTL-cached, fail-closed wrapper around CONTRACT-005's batch precision endpoint.

`doc/ai_quant_trading_system_requirements.md` §5.1.1 and BE-004's task
definition both require: (a) precision data is never hardcoded in Python,
(b) it is fetched via the batch endpoint and cached locally with a TTL to
avoid a network round trip per order, and (c) whenever fresh data cannot be
obtained -- cache expired *and* the refetch fails, or the instrument is
explicitly reported as missing -- callers must get a hard failure, never a
stale value or a guessed default.
"""

from __future__ import annotations

import asyncio
from collections.abc import Callable, Sequence
from dataclasses import dataclass
from datetime import UTC, datetime, timedelta
from typing import Protocol
from uuid import UUID

from .errors import MarketDataError, PrecisionUnavailableError
from .models import InstrumentPrecision, PrecisionBatchResult

_DEFAULT_TTL: timedelta = timedelta(minutes=15)
_MAX_BATCH_SIZE = 100
"""Matches ``client._MAX_BATCH_PRECISION_IDS`` -- kept as a separate constant
so this module does not need to reach into `client.py`'s private name."""


class PrecisionBatchFetcher(Protocol):
    """The one method of ``MarketInfoClient`` this cache depends on.

    Structural typing (rather than importing the concrete
    ``client.MarketInfoClient`` class) keeps this module decoupled from the
    rest of the HTTP client and makes it trivial to drive with an in-memory
    fake in tests -- no ``httpx.MockTransport`` required for cache-logic
    tests, matching this repo's general preference for injecting
    non-deterministic capabilities behind small interfaces
    (python-backend-standards.md §1).
    """

    async def get_precision_batch(self, instrument_ids: Sequence[UUID]) -> PrecisionBatchResult: ...


def _default_clock() -> datetime:
    return datetime.now(UTC)


@dataclass
class _CacheEntry:
    """``value is None`` means "confirmed missing from the catalog as of `fetched_at`"
    (an instrument in a prior response's ``missing_instrument_ids``) -- cached with the
    same TTL as a hit, so a persistently-missing instrument doesn't cause a batch call
    on every lookup, but still re-checked once the TTL lapses in case the catalog was
    fixed."""

    value: InstrumentPrecision | None
    fetched_at: datetime


class PrecisionCache:
    """TTL-cached, fail-closed reader for instrument precision (CONTRACT-005).

    Not thread-safe across event loops, but safe for concurrent ``asyncio``
    tasks on one loop: concurrent lookups for the same expired/missing id
    are coalesced behind a single lock so they trigger at most one batch
    refetch, not one per caller.
    """

    def __init__(
        self,
        client: PrecisionBatchFetcher,
        *,
        ttl: timedelta = _DEFAULT_TTL,
        clock: Callable[[], datetime] = _default_clock,
    ) -> None:
        self._client = client
        self._ttl = ttl
        self._clock = clock
        self._entries: dict[UUID, _CacheEntry] = {}
        self._refresh_lock = asyncio.Lock()

    def _is_fresh(self, entry: _CacheEntry, now: datetime) -> bool:
        return now - entry.fetched_at < self._ttl

    def _fresh_cached_value(self, instrument_id: UUID, now: datetime) -> InstrumentPrecision | None:
        """Return the cached value if present and fresh. Raises
        ``PrecisionUnavailableError`` if present, fresh, and confirmed missing (no
        network call needed for that case). Returns ``None`` (meaning "needs a
        fetch") if absent or expired."""
        entry = self._entries.get(instrument_id)
        if entry is None or not self._is_fresh(entry, now):
            return None
        if entry.value is None:
            raise PrecisionUnavailableError(
                f"instrument {instrument_id} has no precision data in the "
                "market-info-service catalog (cached negative result, fail-closed)"
            )
        return entry.value

    async def get(self, instrument_id: UUID) -> InstrumentPrecision:
        """Return precision for a single instrument, fetching/refreshing as needed.

        Raises ``PrecisionUnavailableError`` (fail-closed) if the instrument
        has no precision data in the catalog, or if a required fetch fails
        because `market-info-service` errored or was unreachable.
        """
        result = await self.get_many([instrument_id])
        return result[instrument_id]

    async def get_many(self, instrument_ids: Sequence[UUID]) -> dict[UUID, InstrumentPrecision]:
        """Return precision for every id in ``instrument_ids``.

        Cache hits are served without a network call. Misses/expired entries
        for all requested ids are refetched in as few batch calls as
        possible (chunked at the server's 100-id limit). Raises
        ``PrecisionUnavailableError`` -- naming every affected id -- if any
        requested instrument cannot be resolved: either it comes back in the
        batch response's ``missing_instrument_ids``, or the batch call
        itself fails (``MarketDataResponseError``/``MarketDataUnavailableError``).
        No partial/stale/default fallback is ever returned for a failed id.
        """
        if not instrument_ids:
            return {}

        # De-duplicate while preserving first-seen order.
        unique_ids = list(dict.fromkeys(instrument_ids))

        now = self._clock()
        result: dict[UUID, InstrumentPrecision] = {}
        to_fetch: list[UUID] = []
        for instrument_id in unique_ids:
            cached = self._fresh_cached_value(instrument_id, now)
            if cached is not None:
                result[instrument_id] = cached
            else:
                to_fetch.append(instrument_id)

        if to_fetch:
            async with self._refresh_lock:
                # Re-check after acquiring the lock: a concurrent caller may have
                # already refreshed these ids while we were waiting.
                now = self._clock()
                still_to_fetch = [i for i in to_fetch if i not in result]
                still_to_fetch = [
                    i
                    for i in still_to_fetch
                    if self._entries.get(i) is None or not self._is_fresh(self._entries[i], now)
                ]

                for chunk_start in range(0, len(still_to_fetch), _MAX_BATCH_SIZE):
                    chunk = still_to_fetch[chunk_start : chunk_start + _MAX_BATCH_SIZE]
                    if not chunk:
                        continue
                    try:
                        batch = await self._client.get_precision_batch(chunk)
                    except MarketDataError as exc:
                        raise PrecisionUnavailableError(
                            f"market-info-service unavailable while fetching precision for "
                            f"{[str(i) for i in chunk]}; refusing to use stale or default "
                            "precision (fail-closed, per doc/ai_quant_trading_system_"
                            "requirements.md §5.1.1)"
                        ) from exc

                    fetched_at = self._clock()
                    for item in batch.items:
                        self._entries[item.instrument_id] = _CacheEntry(
                            value=item, fetched_at=fetched_at
                        )
                    for missing_id in batch.missing_instrument_ids:
                        self._entries[missing_id] = _CacheEntry(value=None, fetched_at=fetched_at)

            for instrument_id in to_fetch:
                entry = self._entries.get(instrument_id)
                if entry is None or entry.value is None:
                    raise PrecisionUnavailableError(
                        f"instrument {instrument_id} has no precision data in the "
                        "market-info-service catalog (fail-closed)"
                    )
                result[instrument_id] = entry.value

        return result

    def invalidate(self, instrument_id: UUID | None = None) -> None:
        """Drop a cached entry (or, with no argument, every cached entry).

        Forces the next lookup to hit the network regardless of TTL --
        useful after an out-of-band signal that the catalog changed.
        """
        if instrument_id is None:
            self._entries.clear()
        else:
            self._entries.pop(instrument_id, None)


__all__ = ["PrecisionCache"]
