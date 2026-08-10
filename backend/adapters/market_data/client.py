"""Production `market-info-service` public query client (BE-004).

Wraps the public, unauthenticated query routes documented in
`doc/technical/market_information_service/07_api_and_admin_ui.md` §2:
latest quotes (§2.1), K-line bars (§2.2), instrument/provider options
(§2.3), and the CONTRACT-005 batch precision endpoint (§2.4). These four
routes "不经过管理权限中间件" (§2.5) -- no bearer token is sent by this
client.

This is the general-purpose production client; it is deliberately separate
from `backend/backtest/market_data_client.py`'s `MarketInfoBarsClient`,
which is a minimal, backtest-scoped `/bars` + `/instruments` reader. Per
that module's own docstring, duplication between the two is expected and
acceptable until a later refactor unifies them -- this task does not touch
`backend/backtest/`.
"""

from __future__ import annotations

from collections.abc import Sequence
from datetime import datetime
from typing import Any, Final, Literal
from uuid import UUID

import httpx

from ._http import send_json_request
from ._parsing import (
    format_rfc3339,
    parse_bool,
    parse_datetime,
    parse_decimal,
    parse_int,
    parse_optional_decimal,
    parse_optional_int,
    parse_str,
    parse_uuid,
    require_aware_utc,
)
from .errors import MarketDataResponseError
from .models import (
    Bar,
    InstrumentOption,
    InstrumentPrecision,
    InstrumentProviderOption,
    LatestQuote,
    LatestQuotesResult,
    PrecisionBatchResult,
)

_QUOTES_LATEST_PATH: Final = "/api/market-info/v1/quotes/latest"
_BARS_PATH: Final = "/api/market-info/v1/bars"
_INSTRUMENTS_PATH: Final = "/api/market-info/v1/instruments"
_PRECISION_BATCH_PATH: Final = "/api/market-info/v1/instruments/precision:batch"

_MAX_BATCH_PRECISION_IDS: Final = 100
"""Server-enforced limit (§2.4): more than this per request is `400 INVALID_ARGUMENT`.
`precision_cache.PrecisionCache` chunks larger requests; this client does not."""


def _parse_quote(entry: dict[str, Any]) -> LatestQuote:
    try:
        return LatestQuote(
            instrument_id=parse_uuid(entry.get("instrument_id"), field="instrument_id"),
            instrument_code=parse_str(entry.get("instrument_code"), field="instrument_code"),
            provider=parse_str(entry.get("provider"), field="provider"),
            provider_instrument_id=parse_uuid(
                entry.get("provider_instrument_id"), field="provider_instrument_id"
            ),
            provider_instrument_code=parse_str(
                entry.get("provider_instrument_code"), field="provider_instrument_code"
            ),
            provider_symbol=parse_str(entry.get("provider_symbol"), field="provider_symbol"),
            price=parse_decimal(entry.get("price"), field="price"),
            bid_price=parse_optional_decimal(entry.get("bid_price"), field="bid_price"),
            bid_size=parse_optional_decimal(entry.get("bid_size"), field="bid_size"),
            ask_price=parse_optional_decimal(entry.get("ask_price"), field="ask_price"),
            ask_size=parse_optional_decimal(entry.get("ask_size"), field="ask_size"),
            open_24h=parse_optional_decimal(entry.get("open_24h"), field="open_24h"),
            high_24h=parse_optional_decimal(entry.get("high_24h"), field="high_24h"),
            low_24h=parse_optional_decimal(entry.get("low_24h"), field="low_24h"),
            base_volume_24h=parse_optional_decimal(
                entry.get("base_volume_24h"), field="base_volume_24h"
            ),
            quote_volume_24h=parse_optional_decimal(
                entry.get("quote_volume_24h"), field="quote_volume_24h"
            ),
            quote_currency=parse_str(entry.get("quote_currency"), field="quote_currency"),
            market_time=parse_datetime(entry.get("market_time"), field="market_time"),
            received_at=parse_datetime(entry.get("received_at"), field="received_at"),
            quality_status=parse_str(entry.get("quality_status"), field="quality_status"),
        )
    except MarketDataResponseError:
        raise
    except (KeyError, TypeError) as exc:
        raise MarketDataResponseError(f"malformed quote entry: {entry!r}") from exc


def _parse_bar(entry: dict[str, Any]) -> Bar:
    try:
        return Bar(
            open_time=parse_datetime(entry.get("open_time"), field="open_time"),
            close_time=parse_datetime(entry.get("close_time"), field="close_time"),
            open=parse_decimal(entry.get("open"), field="open"),
            high=parse_decimal(entry.get("high"), field="high"),
            low=parse_decimal(entry.get("low"), field="low"),
            close=parse_decimal(entry.get("close"), field="close"),
            volume=parse_decimal(entry.get("volume"), field="volume"),
            quote_volume=parse_optional_decimal(entry.get("quote_volume"), field="quote_volume"),
            trade_count=parse_optional_int(entry.get("trade_count"), field="trade_count"),
            revision=parse_int(entry.get("revision"), field="revision"),
            is_closed=parse_bool(entry.get("is_closed"), field="is_closed"),
            quality_status=parse_str(entry.get("quality_status"), field="quality_status"),
            provider_updated_at=parse_datetime(
                entry.get("provider_updated_at"), field="provider_updated_at"
            ),
            collected_at=parse_datetime(entry.get("collected_at"), field="collected_at"),
        )
    except MarketDataResponseError:
        raise
    except (KeyError, TypeError) as exc:
        raise MarketDataResponseError(f"malformed bar entry: {entry!r}") from exc


def _parse_instrument_option(entry: dict[str, Any]) -> InstrumentOption:
    try:
        raw_providers = entry.get("providers")
        if not isinstance(raw_providers, list):
            raise MarketDataResponseError(
                f"expected 'providers' to be a list, got {type(raw_providers).__name__}"
            )
        providers = tuple(_parse_instrument_provider_option(p) for p in raw_providers)
        return InstrumentOption(
            instrument_id=parse_uuid(entry.get("instrument_id"), field="instrument_id"),
            instrument_code=parse_str(entry.get("instrument_code"), field="instrument_code"),
            display_name=parse_str(entry.get("display_name"), field="display_name"),
            providers=providers,
        )
    except MarketDataResponseError:
        raise
    except (KeyError, TypeError) as exc:
        raise MarketDataResponseError(f"malformed instrument option entry: {entry!r}") from exc


def _parse_instrument_provider_option(entry: dict[str, Any]) -> InstrumentProviderOption:
    raw_intervals = entry.get("supported_intervals")
    if not isinstance(raw_intervals, list):
        raise MarketDataResponseError(
            f"expected 'supported_intervals' to be a list, got {type(raw_intervals).__name__}"
        )
    return InstrumentProviderOption(
        provider_code=parse_str(entry.get("provider_code"), field="provider_code"),
        display_name=parse_str(entry.get("display_name"), field="display_name"),
        is_default=parse_bool(entry.get("is_default"), field="is_default"),
        priority=parse_int(entry.get("priority"), field="priority"),
        supported_intervals=tuple(str(i) for i in raw_intervals),
    )


def _parse_precision_item(entry: dict[str, Any]) -> InstrumentPrecision:
    try:
        return InstrumentPrecision(
            instrument_id=parse_uuid(entry.get("instrument_id"), field="instrument_id"),
            instrument_code=parse_str(entry.get("instrument_code"), field="instrument_code"),
            price_scale=parse_int(entry.get("price_scale"), field="price_scale"),
            quantity_scale=parse_int(entry.get("quantity_scale"), field="quantity_scale"),
            lot_size=parse_decimal(entry.get("lot_size"), field="lot_size"),
            min_quantity=parse_decimal(entry.get("min_quantity"), field="min_quantity"),
            as_of=parse_datetime(entry.get("as_of"), field="as_of"),
        )
    except MarketDataResponseError:
        raise
    except (KeyError, TypeError) as exc:
        raise MarketDataResponseError(f"malformed precision entry: {entry!r}") from exc


class MarketInfoClient:
    """Read-only client for `market-info-service`'s public query API (§2).

    ``client`` must already be configured with the market-info-service base
    URL (``httpx.AsyncClient(base_url=...)``, see ``config.load_client_config``);
    this class owns no connection lifecycle of its own and does not close
    ``client``. No auth header is required or sent for these four routes
    (§2.5).
    """

    def __init__(self, client: httpx.AsyncClient) -> None:
        self._client = client

    async def get_latest_quotes(
        self,
        *,
        asset_code: str | None = None,
        instrument_code: str | None = None,
        provider: str | None = None,
    ) -> LatestQuotesResult:
        """Fetch latest quotes via `GET /quotes/latest` (§2.1).

        At least one of ``asset_code``/``instrument_code`` is required
        (raises ``ValueError`` otherwise, mirroring the server's own
        `400 INVALID_ARGUMENT` rule but caught before a network call).
        Returns an empty ``quotes`` tuple (not an error) when the
        asset/instrument is known but has no matching quotes; raises
        ``MarketDataResponseError`` for an unknown asset/instrument
        (`404 ASSET_NOT_FOUND`/`404 INSTRUMENT_NOT_FOUND`) or any other
        non-2xx response.
        """
        if asset_code is None and instrument_code is None:
            raise ValueError("at least one of asset_code or instrument_code is required")

        params: dict[str, str] = {}
        if asset_code is not None:
            params["asset_code"] = asset_code
        if instrument_code is not None:
            params["instrument_code"] = instrument_code
        if provider is not None:
            params["provider"] = provider

        payload = await send_json_request(self._client, "GET", _QUOTES_LATEST_PATH, params=params)

        raw_quotes = payload.get("quotes")
        if not isinstance(raw_quotes, list):
            raise MarketDataResponseError(
                f"expected 'quotes' to be a list, got {type(raw_quotes).__name__}"
            )
        quotes = tuple(_parse_quote(entry) for entry in raw_quotes)

        raw_asset = payload.get("asset")
        asset_id: UUID | None = None
        asset_code_out: str | None = None
        asset_type: str | None = None
        if isinstance(raw_asset, dict):
            asset_id = parse_uuid(raw_asset.get("asset_id"), field="asset.asset_id")
            asset_code_out = parse_str(raw_asset.get("asset_code"), field="asset.asset_code")
            asset_type = parse_str(raw_asset.get("asset_type"), field="asset.asset_type")

        return LatestQuotesResult(
            asset_id=asset_id,
            asset_code=asset_code_out,
            asset_type=asset_type,
            quotes=quotes,
        )

    async def get_bars(
        self,
        *,
        instrument_code: str,
        provider: str,
        interval: str,
        start_time: datetime | None = None,
        end_time: datetime | None = None,
        order: Literal["asc", "desc"] = "desc",
        limit: int = 200,
    ) -> list[Bar]:
        """Fetch every bar in range via `GET /bars` (§2.2), fully paginated.

        ``start_time``/``end_time`` are optional (open-ended range) but must
        be timezone-aware if given -- naive datetimes raise ``ValueError``
        rather than being silently assumed to be UTC. Range semantics
        (``[start_time, end_time)``) come directly from the `/bars` contract
        and are not reinterpreted here.

        Raises ``MarketDataResponseError`` on any non-2xx response
        (`404 INSTRUMENT_NOT_FOUND`, `400 INVALID_ARGUMENT`/
        `UNSUPPORTED_INTERVAL`/`INVALID_TIME_RANGE`, etc.) or a response that
        does not match the documented shape.
        """
        params: dict[str, str | int] = {
            "instrument_code": instrument_code,
            "provider": provider,
            "interval": interval,
            "order": order,
            "limit": min(limit, 1000),
        }
        if start_time is not None:
            params["start_time"] = format_rfc3339(
                require_aware_utc(start_time, param_name="start_time")
            )
        if end_time is not None:
            params["end_time"] = format_rfc3339(require_aware_utc(end_time, param_name="end_time"))

        bars: list[Bar] = []
        cursor: str | None = None
        while True:
            page_params = dict(params)
            if cursor is not None:
                page_params["cursor"] = cursor

            payload = await send_json_request(self._client, "GET", _BARS_PATH, params=page_params)

            raw_bars = payload.get("bars")
            if not isinstance(raw_bars, list):
                raise MarketDataResponseError(
                    f"expected 'bars' to be a list, got {type(raw_bars).__name__}"
                )
            bars.extend(_parse_bar(entry) for entry in raw_bars)

            cursor = payload.get("next_cursor")
            if cursor is None:
                break

        return bars

    async def get_instrument_options(
        self, *, asset_code: str, enabled: bool = True
    ) -> list[InstrumentOption]:
        """Fetch enabled Instrument/Provider options via `GET /instruments` (§2.3), fully paginated.

        ``enabled`` first-phase only accepts ``True`` per the contract; kept
        as a parameter (rather than hardcoded) so a future relaxation of
        that constraint does not require an API change here.
        """
        params: dict[str, str] = {"asset_code": asset_code, "enabled": str(enabled).lower()}

        items: list[InstrumentOption] = []
        cursor: str | None = None
        while True:
            page_params = dict(params)
            if cursor is not None:
                page_params["cursor"] = cursor

            payload = await send_json_request(
                self._client, "GET", _INSTRUMENTS_PATH, params=page_params
            )

            raw_items = payload.get("items")
            if not isinstance(raw_items, list):
                raise MarketDataResponseError(
                    f"expected 'items' to be a list, got {type(raw_items).__name__}"
                )
            items.extend(_parse_instrument_option(entry) for entry in raw_items)

            cursor = payload.get("next_cursor")
            if cursor is None:
                break

        return items

    async def get_precision_batch(self, instrument_ids: Sequence[UUID]) -> PrecisionBatchResult:
        """Raw call to `POST /instruments/precision:batch` (CONTRACT-005, §2.4).

        A single request only -- does not chunk. ``instrument_ids`` longer
        than 100 (the server's documented batch limit) will get a
        `400 INVALID_ARGUMENT` from the service, surfaced as
        ``MarketDataResponseError``.
        :class:`~adapters.market_data.precision_cache.PrecisionCache` is the
        intended caller for order-sizing/risk code; it chunks larger
        requests and adds TTL caching + fail-closed semantics on top of this
        raw call.
        """
        if not instrument_ids:
            raise ValueError("instrument_ids must not be empty")
        if len(instrument_ids) > _MAX_BATCH_PRECISION_IDS:
            raise ValueError(
                f"instrument_ids has {len(instrument_ids)} entries, exceeding the "
                f"server's batch limit of {_MAX_BATCH_PRECISION_IDS}"
            )

        payload = await send_json_request(
            self._client,
            "POST",
            _PRECISION_BATCH_PATH,
            json={"instrument_ids": [str(i) for i in instrument_ids]},
        )

        raw_items = payload.get("items")
        if not isinstance(raw_items, list):
            raise MarketDataResponseError(
                f"expected 'items' to be a list, got {type(raw_items).__name__}"
            )
        raw_missing = payload.get("missing_instrument_ids")
        if not isinstance(raw_missing, list):
            raise MarketDataResponseError(
                f"expected 'missing_instrument_ids' to be a list, got {type(raw_missing).__name__}"
            )

        items = tuple(_parse_precision_item(entry) for entry in raw_items)
        missing = tuple(
            parse_uuid(entry, field="missing_instrument_ids[]") for entry in raw_missing
        )
        return PrecisionBatchResult(items=items, missing_instrument_ids=missing)


__all__ = ["MarketInfoClient"]
