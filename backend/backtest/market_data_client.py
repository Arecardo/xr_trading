"""Minimal, read-only `market-info-service` client for BT-001.

Calls the already-implemented public query API documented in
`doc/technical/market_information_service/07_api_and_admin_ui.md` §2.2
(`GET /bars`) and §2.3 (`GET /instruments`). Uses ``httpx.AsyncClient``, per
python-backend-standards.md §3 (the async stack already declared in
`backend/pyproject.toml`).

Scope note (see the package docstring in ``backtest/__init__.py``): this is
NOT the future BE-004 production market-data adapter
(`backend/adapters/market_data/`, TTL-cached, shared by the live/paper
order-precision path). It is deliberately small and backtest-specific;
duplication with the future BE-004 client is expected and acceptable until a
later refactor unifies them.
"""

from __future__ import annotations

from collections.abc import Mapping
from datetime import UTC, datetime
from decimal import Decimal, InvalidOperation
from typing import Any, Final

import httpx

from .errors import MarketDataResponseError
from .models import RawBar

_BARS_PATH: Final = "/api/market-info/v1/bars"
_INSTRUMENTS_PATH: Final = "/api/market-info/v1/instruments"
_DAILY_INTERVAL: Final = "1d"


def _require_aware_utc(moment: datetime, *, param_name: str) -> datetime:
    if moment.tzinfo is None:
        raise ValueError(f"{param_name} must be timezone-aware (UTC); got a naive datetime")
    return moment.astimezone(UTC)


def _format_rfc3339(moment: datetime) -> str:
    return moment.strftime("%Y-%m-%dT%H:%M:%SZ")


def _parse_decimal(value: Any, *, field: str) -> Decimal:
    if not isinstance(value, str):
        raise MarketDataResponseError(
            f"expected field {field!r} to be a decimal-as-string, got {type(value).__name__}"
        )
    try:
        return Decimal(value)
    except InvalidOperation as exc:
        raise MarketDataResponseError(f"field {field!r} is not a valid decimal: {value!r}") from exc


def _parse_datetime(value: Any, *, field: str) -> datetime:
    if not isinstance(value, str):
        raise MarketDataResponseError(
            f"expected field {field!r} to be an RFC3339 string, got {type(value).__name__}"
        )
    try:
        parsed = datetime.fromisoformat(value)
    except ValueError as exc:
        raise MarketDataResponseError(
            f"field {field!r} is not a valid RFC3339 timestamp: {value!r}"
        ) from exc
    if parsed.tzinfo is None:
        raise MarketDataResponseError(f"field {field!r} is missing timezone info: {value!r}")
    return parsed.astimezone(UTC)


def _parse_bar(entry: Mapping[str, Any]) -> RawBar:
    try:
        return RawBar(
            open_time=_parse_datetime(entry.get("open_time"), field="open_time"),
            close_time=_parse_datetime(entry.get("close_time"), field="close_time"),
            open=_parse_decimal(entry.get("open"), field="open"),
            high=_parse_decimal(entry.get("high"), field="high"),
            low=_parse_decimal(entry.get("low"), field="low"),
            close=_parse_decimal(entry.get("close"), field="close"),
            volume=_parse_decimal(entry.get("volume"), field="volume"),
            quality_status=str(entry.get("quality_status", "")),
        )
    except MarketDataResponseError:
        raise
    except (KeyError, TypeError) as exc:
        raise MarketDataResponseError(f"malformed bar entry: {entry!r}") from exc


class MarketInfoBarsClient:
    """Fetches historical daily bars from `market-info-service`'s `/bars` endpoint.

    Every method call is read-only (`project-conventions.md` §1: "行情查询只读取已落库数据，
    不因一次查询隐式触发采集任务") and never mutates upstream state.
    """

    def __init__(self, client: httpx.AsyncClient) -> None:
        """``client`` must already be configured with the market-info-service
        base URL (``httpx.AsyncClient(base_url=...)``) and any required
        auth headers; this class owns no connection lifecycle of its own and
        does not close ``client``."""
        self._client = client

    async def fetch_daily_bars(
        self,
        *,
        instrument_code: str,
        provider: str,
        start_time: datetime,
        end_time: datetime,
        page_size: int = 1000,
    ) -> list[RawBar]:
        """Fetch every `1d` bar in `[start_time, end_time)`, fully paginated, ascending order.

        ``start_time``/``end_time`` must be timezone-aware; naive datetimes
        raise ``ValueError`` rather than being silently assumed to be UTC.
        The upstream range semantics (start inclusive, end exclusive) come
        directly from the `/bars` contract (§2.2); this method does not
        reinterpret them.

        Raises ``MarketDataResponseError`` on any non-2xx response or a
        response that does not match the documented shape.
        """
        start_time = _require_aware_utc(start_time, param_name="start_time")
        end_time = _require_aware_utc(end_time, param_name="end_time")
        if start_time >= end_time:
            raise ValueError(f"start_time {start_time} must be earlier than end_time {end_time}")

        params: dict[str, str | int] = {
            "instrument_code": instrument_code,
            "provider": provider,
            "interval": _DAILY_INTERVAL,
            "start_time": _format_rfc3339(start_time),
            "end_time": _format_rfc3339(end_time),
            "order": "asc",
            "limit": min(page_size, 1000),
        }

        bars: list[RawBar] = []
        cursor: str | None = None
        while True:
            page_params = dict(params)
            if cursor is not None:
                page_params["cursor"] = cursor

            response = await self._client.get(_BARS_PATH, params=page_params)
            payload = self._decode_json(response)

            for entry in payload.get("bars", []):
                bars.append(_parse_bar(entry))

            cursor = payload.get("next_cursor")
            if cursor is None:
                break

        return bars

    async def list_instruments(self, *, asset_code: str) -> list[dict[str, Any]]:
        """Fetch enabled `Instrument`/`Provider` options for an asset via `/instruments` (§2.3).

        Returns the raw `items` array (Instrument code, display name,
        available Providers and their supported intervals) for a caller that
        needs to resolve an `instrument_code`/`provider` pair before calling
        ``fetch_daily_bars``. BT-001 itself takes an already-resolved
        `instrument_code`/`provider` (see the package docstring), so this is
        provided for a future caller (e.g. portfolio-driven universe
        resolution) rather than used internally by this module.
        """
        items: list[dict[str, Any]] = []
        cursor: str | None = None
        while True:
            params: dict[str, str] = {"asset_code": asset_code}
            if cursor is not None:
                params["cursor"] = cursor

            response = await self._client.get(_INSTRUMENTS_PATH, params=params)
            payload = self._decode_json(response)

            page_items = payload.get("items")
            if not isinstance(page_items, list):
                raise MarketDataResponseError(
                    f"expected 'items' to be a list, got {type(page_items).__name__}"
                )
            items.extend(page_items)

            cursor = payload.get("next_cursor")
            if cursor is None:
                break

        return items

    @staticmethod
    def _decode_json(response: httpx.Response) -> dict[str, Any]:
        try:
            payload = response.json()
        except ValueError as exc:
            raise MarketDataResponseError(
                f"non-JSON response (status {response.status_code}): {response.text!r}"
            ) from exc

        if response.is_error:
            error = payload.get("error") if isinstance(payload, dict) else None
            code = error.get("code") if isinstance(error, dict) else None
            message = error.get("message") if isinstance(error, dict) else None
            raise MarketDataResponseError(
                f"market-info-service returned {response.status_code} "
                f"(code={code!r}, message={message!r})"
            )

        if not isinstance(payload, dict):
            raise MarketDataResponseError(
                f"expected a JSON object response, got {type(payload).__name__}"
            )

        return payload
