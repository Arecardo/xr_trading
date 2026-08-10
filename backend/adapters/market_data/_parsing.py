"""Shared field-parsing helpers for decoding `market-info-service` JSON payloads.

Internal module -- see ``_http.py``'s docstring for why parsing helpers used
by more than one client in this package live in one place. Behaviour
mirrors `backend/backtest/market_data_client.py`'s private helpers
(decimal-as-string, RFC3339-with-timezone, fail loud rather than guess).
"""

from __future__ import annotations

from collections.abc import Mapping
from datetime import UTC, datetime
from decimal import Decimal, InvalidOperation
from typing import Any
from uuid import UUID

from .errors import MarketDataResponseError


def parse_decimal(value: Any, *, field: str) -> Decimal:
    """Parse a required decimal-as-string field."""
    if not isinstance(value, str):
        raise MarketDataResponseError(
            f"expected field {field!r} to be a decimal-as-string, got {type(value).__name__}"
        )
    try:
        return Decimal(value)
    except InvalidOperation as exc:
        raise MarketDataResponseError(f"field {field!r} is not a valid decimal: {value!r}") from exc


def parse_optional_decimal(value: Any, *, field: str) -> Decimal | None:
    """Parse an optional decimal-as-string field; ``None``/absent passes through as ``None``."""
    if value is None:
        return None
    return parse_decimal(value, field=field)


def parse_datetime(value: Any, *, field: str) -> datetime:
    """Parse a required RFC3339 timestamp field, normalized to UTC."""
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


def parse_optional_datetime(value: Any, *, field: str) -> datetime | None:
    """Parse an optional RFC3339 timestamp field; ``None``/absent passes through as ``None``."""
    if value is None:
        return None
    return parse_datetime(value, field=field)


def parse_uuid(value: Any, *, field: str) -> UUID:
    """Parse a required UUID field."""
    if not isinstance(value, str):
        raise MarketDataResponseError(
            f"expected field {field!r} to be a UUID string, got {type(value).__name__}"
        )
    try:
        return UUID(value)
    except ValueError as exc:
        raise MarketDataResponseError(f"field {field!r} is not a valid UUID: {value!r}") from exc


def parse_str(value: Any, *, field: str) -> str:
    """Parse a required string field."""
    if not isinstance(value, str):
        raise MarketDataResponseError(
            f"expected field {field!r} to be a string, got {type(value).__name__}"
        )
    return value


def parse_int(value: Any, *, field: str) -> int:
    """Parse a required integer field.

    ``bool`` is rejected -- it is an ``int`` subclass in Python.
    """
    if isinstance(value, bool) or not isinstance(value, int):
        raise MarketDataResponseError(
            f"expected field {field!r} to be an integer, got {type(value).__name__}"
        )
    return value


def parse_optional_int(value: Any, *, field: str) -> int | None:
    """Parse an optional integer field; ``None``/absent passes through as ``None``."""
    if value is None:
        return None
    return parse_int(value, field=field)


def parse_bool(value: Any, *, field: str) -> bool:
    """Parse a required boolean field."""
    if not isinstance(value, bool):
        raise MarketDataResponseError(
            f"expected field {field!r} to be a boolean, got {type(value).__name__}"
        )
    return value


def get_field(entry: Mapping[str, Any], field: str) -> Any:
    """Look up ``field`` in ``entry``, raising ``MarketDataResponseError`` if the whole entry
    is malformed enough that indexing itself fails (defensive; callers still validate the
    extracted value's type via the ``parse_*`` helpers above)."""
    try:
        return entry[field]
    except (KeyError, TypeError) as exc:
        raise MarketDataResponseError(f"missing required field {field!r} in {entry!r}") from exc


def require_aware_utc(moment: datetime, *, param_name: str) -> datetime:
    """Reject naive datetimes rather than silently assuming UTC (caller-input validation)."""
    if moment.tzinfo is None:
        raise ValueError(f"{param_name} must be timezone-aware (UTC); got a naive datetime")
    return moment.astimezone(UTC)


def format_rfc3339(moment: datetime) -> str:
    return moment.strftime("%Y-%m-%dT%H:%M:%SZ")


__all__ = [
    "format_rfc3339",
    "get_field",
    "parse_bool",
    "parse_datetime",
    "parse_decimal",
    "parse_int",
    "parse_optional_datetime",
    "parse_optional_decimal",
    "parse_optional_int",
    "parse_str",
    "parse_uuid",
    "require_aware_utc",
]
