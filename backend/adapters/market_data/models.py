"""Data shapes returned by the BE-004 `market-info-service` client.

Field names and types transcribe
`doc/technical/market_information_service/07_api_and_admin_ui.md` §2
(public query API) and §4 (collection-subscription admin API), plus
CONTRACT-005 (`doc/technical/implementation_plan/02_portfolio_and_market_integration.md`
§0, the frozen batch precision endpoint). Decimal fields use ``Decimal``
throughout (python-backend-standards.md §1, §6) -- never ``float``.

These are this package's own response shapes, not shared/frozen contracts
with another task -- unlike `backend/backtest/models.py`'s ``AlignedBar``/
``AlignedSeries``, nothing downstream depends on them yet.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from decimal import Decimal
from uuid import UUID

# --- §2.1 latest quotes -------------------------------------------------


@dataclass(frozen=True)
class LatestQuote:
    """One ProviderInstrument's latest-quote snapshot (§2.1)."""

    instrument_id: UUID
    instrument_code: str
    provider: str
    provider_instrument_id: UUID
    provider_instrument_code: str
    provider_symbol: str
    price: Decimal
    bid_price: Decimal | None
    bid_size: Decimal | None
    ask_price: Decimal | None
    ask_size: Decimal | None
    open_24h: Decimal | None
    high_24h: Decimal | None
    low_24h: Decimal | None
    base_volume_24h: Decimal | None
    quote_volume_24h: Decimal | None
    quote_currency: str
    market_time: datetime
    received_at: datetime
    quality_status: str


@dataclass(frozen=True)
class LatestQuotesResult:
    """Response envelope for `GET /quotes/latest` (§2.1).

    ``asset_id``/``asset_code``/``asset_type`` are ``None`` when the
    response has no top-level ``asset`` object -- the documented example
    only covers an ``asset_code`` query; the response shape for an
    ``instrument_code``-only query is not shown, so this is parsed
    defensively rather than assumed (see the BE-004 task report's open
    questions).
    """

    asset_id: UUID | None
    asset_code: str | None
    asset_type: str | None
    quotes: tuple[LatestQuote, ...]


# --- §2.2 bars ------------------------------------------------------------


@dataclass(frozen=True)
class Bar:
    """One K-line entry (§2.2)."""

    open_time: datetime
    close_time: datetime
    open: Decimal
    high: Decimal
    low: Decimal
    close: Decimal
    volume: Decimal
    quote_volume: Decimal | None
    trade_count: int | None
    revision: int
    is_closed: bool
    quality_status: str
    provider_updated_at: datetime
    collected_at: datetime


# --- §2.3 instrument/provider options -------------------------------------


@dataclass(frozen=True)
class InstrumentProviderOption:
    """One entry in an ``InstrumentOption.providers`` list (§2.3)."""

    provider_code: str
    display_name: str
    is_default: bool
    priority: int
    supported_intervals: tuple[str, ...]


@dataclass(frozen=True)
class InstrumentOption:
    """One `GET /instruments` list item (§2.3)."""

    instrument_id: UUID
    instrument_code: str
    display_name: str
    providers: tuple[InstrumentProviderOption, ...]


# --- CONTRACT-005 batch precision -----------------------------------------


@dataclass(frozen=True)
class InstrumentPrecision:
    """One `/instruments/precision:batch` result item (CONTRACT-005)."""

    instrument_id: UUID
    instrument_code: str
    price_scale: int
    quantity_scale: int
    lot_size: Decimal
    min_quantity: Decimal
    as_of: datetime


@dataclass(frozen=True)
class PrecisionBatchResult:
    """Raw response from `POST /instruments/precision:batch` (CONTRACT-005)."""

    items: tuple[InstrumentPrecision, ...]
    missing_instrument_ids: tuple[UUID, ...]


# --- §4 collection subscriptions -------------------------------------------


@dataclass(frozen=True)
class CollectionSubscription:
    """One collection-subscription record (§4.1/§4.2).

    Identity is ``(provider, instrument_code, interval)`` -- §4.3: "provider、
    instrument_code、provider_instrument_id 和 interval 构成订阅身份，不允许
    修改". Only ``enabled``/``priority``/``close_delay_seconds``/
    ``revision_delay_seconds`` are mutable via `PATCH`.
    """

    subscription_id: UUID
    provider: str
    instrument_code: str
    interval: str
    enabled: bool
    priority: int
    close_delay_seconds: int
    revision_delay_seconds: int | None


@dataclass(frozen=True)
class InstrumentRef:
    """A `(provider, instrument_code, interval)` triple identifying the collection
    scope `market-info-service` should have for one instrument at one interval.

    This is the unit of "desired collection state" produced by
    ``subscriptions.compute_desired_instruments`` and diffed against the
    current subscriptions by ``subscriptions.sync_collection_subscriptions``.
    """

    provider: str
    instrument_code: str
    interval: str


__all__ = [
    "Bar",
    "CollectionSubscription",
    "InstrumentOption",
    "InstrumentPrecision",
    "InstrumentProviderOption",
    "InstrumentRef",
    "LatestQuote",
    "LatestQuotesResult",
    "PrecisionBatchResult",
]
