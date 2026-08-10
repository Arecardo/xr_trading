"""Production client for `market-info-service` (BE-004).

Responsibility (`doc/technical/implementation_plan/02_portfolio_and_market_integration.md`
§3, BE-004): an ``httpx.AsyncClient``-based client for the market-info-
service public query API (latest quotes, K-line bars, instrument/provider
options, CONTRACT-005 batch precision), a TTL-cached fail-closed wrapper
around the precision endpoint, and a sync routine that keeps
`market-info-service`'s collection subscriptions in step with which
instruments the active portfolios actually need (membership + benchmarks).

Explicitly NOT this package's job:
- Valuation snapshot generation (BE-003) -- this package is the client BE-003
  will call, not the valuation logic itself.
- The backtest-scoped `/bars` reader (`backend/backtest/market_data_client.py`,
  BT-001) -- intentionally separate and minimal; not touched or unified here.
- Resolving a domain ``asset_id`` to a `market-info-service`
  ``instrument_code``/``provider`` pair -- see ``subscriptions.py``'s module
  docstring for why that mapping is left to an injected
  ``AssetInstrumentResolver`` rather than guessed at here.

Public surface:
``MarketInfoClient`` (query API), ``PrecisionCache`` (TTL-cached
fail-closed precision), ``CollectionSubscriptionClient`` +
``sync_collection_subscriptions`` + ``compute_desired_instruments``
(subscription sync), the ``config`` loaders, the ``models`` dataclasses, and
the ``errors`` hierarchy.
"""

from __future__ import annotations

from .client import MarketInfoClient
from .config import (
    MarketDataClientConfig,
    SubscriptionSyncConfig,
    load_client_config,
    load_subscription_sync_config,
)
from .errors import (
    MarketDataConfigError,
    MarketDataError,
    MarketDataResponseError,
    MarketDataUnavailableError,
    PrecisionUnavailableError,
    SubscriptionSyncError,
)
from .models import (
    Bar,
    CollectionSubscription,
    InstrumentOption,
    InstrumentPrecision,
    InstrumentProviderOption,
    InstrumentRef,
    LatestQuote,
    LatestQuotesResult,
    PrecisionBatchResult,
)
from .precision_cache import PrecisionCache
from .subscriptions import (
    AssetInstrumentResolver,
    CollectionSubscriptionClient,
    SubscriptionSyncResult,
    compute_desired_instruments,
    sync_collection_subscriptions,
)

__all__ = [
    "AssetInstrumentResolver",
    "Bar",
    "CollectionSubscription",
    "CollectionSubscriptionClient",
    "InstrumentOption",
    "InstrumentPrecision",
    "InstrumentProviderOption",
    "InstrumentRef",
    "LatestQuote",
    "LatestQuotesResult",
    "MarketDataClientConfig",
    "MarketDataConfigError",
    "MarketDataError",
    "MarketDataResponseError",
    "MarketDataUnavailableError",
    "MarketInfoClient",
    "PrecisionBatchResult",
    "PrecisionCache",
    "PrecisionUnavailableError",
    "SubscriptionSyncConfig",
    "SubscriptionSyncError",
    "SubscriptionSyncResult",
    "compute_desired_instruments",
    "load_client_config",
    "load_subscription_sync_config",
    "sync_collection_subscriptions",
]
