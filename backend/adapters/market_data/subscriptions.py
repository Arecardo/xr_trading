"""Collection scope follows portfolio membership (BE-004 task description).

Two separate concerns, kept apart deliberately:

1. **Computing desired scope** (:func:`compute_desired_instruments`) --
   pure, synchronous, no I/O beyond the already-implemented
   ``PortfolioRepository`` (BE-002). Reads every member of the given
   portfolios (any ``member_status`` -- candidate/approved/held/restricted
   all still need live prices to be evaluated or valued, so none are
   filtered out) plus each portfolio's ``benchmark_asset_id``, and resolves
   each ``asset_id`` to the `market-info-service` ``(provider,
   instrument_code, interval)`` triples that should be collected for it.

2. **Applying the diff** (:class:`CollectionSubscriptionClient` +
   :func:`sync_collection_subscriptions`) -- async, talks to the admin
   collection-subscription API (§4) to create new subscriptions, re-enable
   disabled-but-now-desired ones, and disable enabled-but-no-longer-desired
   ones. The API has no `DELETE` (§4.3: "首期不提供 DELETE，保留订阅配置"), so
   "no longer desired" means "set enabled=false", never removing the row.

Open question for the integration lead (see BE-004 task report): resolving
a ``domain.assets.Asset``'s internal ``asset_id`` (e.g.
``"crypto:bybit:BTC-USDT"``) to a `market-info-service`
``instrument_code``/``provider`` pair is not implemented anywhere in the
repository yet -- `backend/backtest/market_data_client.py` (BT-001) also
takes an already-resolved ``instrument_code``/``provider`` from its caller
rather than deriving one. ``AssetInstrumentResolver`` below is therefore a
``Protocol`` the caller must supply a concrete implementation for (e.g.
backed by ``Asset.provider_symbols`` plus a lookup against `market-info-
service`'s own `/instruments?asset_code=...`, or a small static mapping) --
this module does not guess at that mapping.
"""

from __future__ import annotations

from collections.abc import Iterable, Sequence
from dataclasses import dataclass
from typing import Any, Final, Protocol
from uuid import UUID

import httpx

from domain.portfolios.models import PortfolioRepository

from ._http import send_json_request
from ._parsing import parse_bool, parse_int, parse_optional_int, parse_str, parse_uuid
from .errors import MarketDataError, MarketDataResponseError, SubscriptionSyncError
from .models import CollectionSubscription, InstrumentRef

_SUBSCRIPTIONS_PATH: Final = "/api/market-info/v1/collection-subscriptions"

_DEFAULT_PRIORITY: Final = 100
_DEFAULT_CLOSE_DELAY_SECONDS: Final = 120


class AssetInstrumentResolver(Protocol):
    """Resolves a `backend` domain ``asset_id`` to the `market-info-service`
    instrument(s)/interval(s) that should be collected for it.

    A single asset may map to zero (not yet cataloged upstream -- resolver's
    choice whether that's an error or an empty result), one, or several
    ``InstrumentRef``s (e.g. collecting both ``1h`` and ``1d`` for the same
    instrument).
    """

    def resolve(self, asset_id: str) -> Sequence[InstrumentRef]: ...


def compute_desired_instruments(
    portfolio_repository: PortfolioRepository,
    resolver: AssetInstrumentResolver,
    *,
    portfolio_ids: Sequence[UUID],
) -> set[InstrumentRef]:
    """Compute the union of instruments that should be collected for ``portfolio_ids``.

    ``portfolio_ids`` is caller-supplied rather than discovered here:
    ``PortfolioRepository`` (frozen, CONTRACT-001 §0.1) only exposes
    ``get``/``list_members``, not "list all active portfolios" -- that
    selection is an application-layer concern (e.g. an
    ``application.portfolios`` use case querying ``status == "active"``)
    that this adapter-layer function does not have visibility into.

    Includes members of every ``member_status`` (candidate/approved/held/
    restricted) and each portfolio's ``benchmark_asset_id`` when set.
    """
    asset_ids: set[str] = set()
    for portfolio_id in portfolio_ids:
        portfolio = portfolio_repository.get(portfolio_id)
        if portfolio.benchmark_asset_id is not None:
            asset_ids.add(portfolio.benchmark_asset_id)
        for member in portfolio_repository.list_members(portfolio_id):
            asset_ids.add(member.asset_id)

    desired: set[InstrumentRef] = set()
    for asset_id in asset_ids:
        desired.update(resolver.resolve(asset_id))
    return desired


def _parse_subscription(entry: dict[str, Any]) -> CollectionSubscription:
    try:
        return CollectionSubscription(
            subscription_id=parse_uuid(entry.get("subscription_id"), field="subscription_id"),
            provider=parse_str(entry.get("provider"), field="provider"),
            instrument_code=parse_str(entry.get("instrument_code"), field="instrument_code"),
            interval=parse_str(entry.get("interval"), field="interval"),
            enabled=parse_bool(entry.get("enabled"), field="enabled"),
            priority=parse_int(entry.get("priority"), field="priority"),
            close_delay_seconds=parse_int(
                entry.get("close_delay_seconds"), field="close_delay_seconds"
            ),
            revision_delay_seconds=parse_optional_int(
                entry.get("revision_delay_seconds"), field="revision_delay_seconds"
            ),
        )
    except MarketDataResponseError as exc:
        raise SubscriptionSyncError(str(exc)) from exc
    except (KeyError, TypeError) as exc:
        raise SubscriptionSyncError(f"malformed subscription entry: {entry!r}") from exc


class CollectionSubscriptionClient:
    """Client for the admin collection-subscription API (§4).

    ``client`` must already be configured with the market-info-service base
    URL and an ``Authorization: Bearer <MARKET_INFO_MANAGE_BEARER_TOKEN>``
    header (see ``config.load_subscription_sync_config``); this class owns
    no connection lifecycle of its own and does not close ``client``.

    Wraps every failure (network, timeout, non-2xx, malformed response) in
    ``SubscriptionSyncError`` -- a distinct error type from the read-path's
    ``MarketDataResponseError``/``MarketDataUnavailableError`` so a caller
    orchestrating a sync can catch subscription-sync failures specifically
    without conflating them with precision-lookup failures.
    """

    def __init__(self, client: httpx.AsyncClient) -> None:
        self._client = client

    async def _request(self, method: str, path: str, **kwargs: Any) -> dict[str, Any]:
        try:
            return await send_json_request(self._client, method, path, **kwargs)
        except MarketDataError as exc:
            raise SubscriptionSyncError(str(exc)) from exc

    async def list_subscriptions(self) -> list[CollectionSubscription]:
        """Fetch every collection subscription via `GET /collection-subscriptions` (§4.1),
        fully paginated."""
        subscriptions: list[CollectionSubscription] = []
        cursor: str | None = None
        while True:
            params: dict[str, str] = {}
            if cursor is not None:
                params["cursor"] = cursor

            payload = await self._request("GET", _SUBSCRIPTIONS_PATH, params=params)

            raw_items = payload.get("items")
            if not isinstance(raw_items, list):
                raise SubscriptionSyncError(
                    f"expected 'items' to be a list, got {type(raw_items).__name__}"
                )
            subscriptions.extend(_parse_subscription(entry) for entry in raw_items)

            cursor = payload.get("next_cursor")
            if cursor is None:
                break

        return subscriptions

    async def create_subscription(
        self,
        ref: InstrumentRef,
        *,
        reason: str,
        priority: int = _DEFAULT_PRIORITY,
        close_delay_seconds: int = _DEFAULT_CLOSE_DELAY_SECONDS,
        revision_delay_seconds: int | None = None,
    ) -> CollectionSubscription:
        """Create a new, enabled subscription via `POST /collection-subscriptions` (§4.2)."""
        body = {
            "provider": ref.provider,
            "instrument_code": ref.instrument_code,
            "interval": ref.interval,
            "enabled": True,
            "priority": priority,
            "close_delay_seconds": close_delay_seconds,
            "revision_delay_seconds": revision_delay_seconds,
            "reason": reason,
        }
        payload = await self._request("POST", _SUBSCRIPTIONS_PATH, json=body)
        return _parse_subscription(payload)

    async def set_enabled(
        self, subscription_id: UUID, *, enabled: bool, reason: str
    ) -> CollectionSubscription:
        """Enable/disable an existing subscription via
        `PATCH /collection-subscriptions/{id}` (§4.3)."""
        body = {"enabled": enabled, "reason": reason}
        payload = await self._request(
            "PATCH", f"{_SUBSCRIPTIONS_PATH}/{subscription_id}", json=body
        )
        return _parse_subscription(payload)


@dataclass(frozen=True)
class SubscriptionSyncResult:
    """Summary of what :func:`sync_collection_subscriptions` changed."""

    created: tuple[InstrumentRef, ...]
    enabled: tuple[InstrumentRef, ...]
    disabled: tuple[InstrumentRef, ...]
    unchanged_count: int


async def sync_collection_subscriptions(
    subscription_client: CollectionSubscriptionClient,
    desired: Iterable[InstrumentRef],
    *,
    reason: str,
) -> SubscriptionSyncResult:
    """Diff ``desired`` against `market-info-service`'s current subscriptions and
    reconcile: create missing ones, re-enable disabled-but-desired ones, disable
    enabled-but-no-longer-desired ones. Never deletes a subscription row (the API has
    no `DELETE`, §4.3).

    Identity for the diff is ``(provider, instrument_code, interval)`` --
    the immutable fields that make up a subscription's identity (§4.3).
    Each create/enable/disable is a separate API call; a failure partway
    through raises ``SubscriptionSyncError`` (propagated from
    ``CollectionSubscriptionClient``) after whatever calls already succeeded
    -- callers that need atomicity should re-run the sync, which is
    idempotent (a second run only touches whatever the first run didn't
    reach).
    """
    desired_set = set(desired)
    current = await subscription_client.list_subscriptions()
    current_by_identity = {
        (sub.provider, sub.instrument_code, sub.interval): sub for sub in current
    }

    created: list[InstrumentRef] = []
    enabled: list[InstrumentRef] = []
    disabled: list[InstrumentRef] = []
    unchanged_count = 0

    for ref in desired_set:
        identity = (ref.provider, ref.instrument_code, ref.interval)
        existing = current_by_identity.get(identity)
        if existing is None:
            await subscription_client.create_subscription(ref, reason=reason)
            created.append(ref)
        elif not existing.enabled:
            await subscription_client.set_enabled(
                existing.subscription_id, enabled=True, reason=reason
            )
            enabled.append(ref)
        else:
            unchanged_count += 1

    for identity, sub in current_by_identity.items():
        ref = InstrumentRef(provider=identity[0], instrument_code=identity[1], interval=identity[2])
        if sub.enabled and ref not in desired_set:
            await subscription_client.set_enabled(sub.subscription_id, enabled=False, reason=reason)
            disabled.append(ref)

    return SubscriptionSyncResult(
        created=tuple(created),
        enabled=tuple(enabled),
        disabled=tuple(disabled),
        unchanged_count=unchanged_count,
    )


__all__ = [
    "AssetInstrumentResolver",
    "CollectionSubscriptionClient",
    "SubscriptionSyncResult",
    "compute_desired_instruments",
    "sync_collection_subscriptions",
]
