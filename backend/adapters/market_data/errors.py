"""Error hierarchy for the BE-004 production `market-info-service` client.

Mirrors the repo-wide convention (python-backend-standards.md §5,
`backend/backtest/errors.py`) of a domain exception hierarchy with a stable
base type, rather than surfacing raw ``httpx``/JSON-decoding errors to
callers.
"""

from __future__ import annotations


class MarketDataError(Exception):
    """Base class for all ``adapters.market_data`` errors."""


class MarketDataConfigError(MarketDataError):
    """Required configuration (base URL / admin bearer token) is missing.

    Fail-closed (security-standards.md §4): raised at the point a required
    environment variable is read, never papered over with a guessable
    default (e.g. a hardcoded ``http://127.0.0.1:8090``).
    """


class MarketDataResponseError(MarketDataError):
    """`market-info-service` returned a non-2xx response, or a response body
    that does not match its documented shape
    (`doc/technical/market_information_service/07_api_and_admin_ui.md`).

    Covers: the `error` envelope (§8), non-JSON bodies, missing/invalid
    fields, and decimal/timestamp/UUID fields that fail to parse.
    """


class MarketDataUnavailableError(MarketDataError):
    """`market-info-service` could not be reached at all.

    Covers connection failures, DNS errors, and request timeouts -- i.e.
    ``httpx.RequestError`` and its subclasses. Distinguished from
    ``MarketDataResponseError`` (which means the service *did* respond, just
    not successfully) because callers may want to treat "service down" and
    "service returned 404" differently for logging/alerting -- both must
    still fail closed for precision lookups.
    """


class PrecisionUnavailableError(MarketDataError):
    """Fail-closed: instrument precision data could not be obtained.

    Raised by :class:`~adapters.market_data.precision_cache.PrecisionCache`
    instead of returning a stale cache entry or a guessed default, whenever:

    - the batch endpoint reports the instrument in ``missing_instrument_ids``
      (unknown id, no precision data in the catalog, ``status != active``, or
      outside its ``[valid_from, valid_to)`` window -- see
      `07_api_and_admin_ui.md` §2.4), or
    - a cache miss/expiry could not be refreshed because
      `market-info-service` returned an error
      (``MarketDataResponseError``) or was unreachable
      (``MarketDataUnavailableError``).

    This mirrors the fail-closed principle already established for
    precision data (`doc/ai_quant_trading_system_requirements.md` §5.1.1:
    "目录中对应 Instrument 缺失或精度数据过期时，下单前必须 fail-closed
    （拒绝生成订单），不得使用猜测默认值兜底"). Callers (order sizing, risk
    checks) must treat this the same as any other fail-closed
    credential/config error: refuse to proceed.
    """


class SubscriptionSyncError(MarketDataError):
    """Collection-subscription sync (admin API) failed.

    Raised when creating or modifying a `market-info-service`
    collection-subscription errors out or the service is unreachable. Unlike
    ``PrecisionUnavailableError``, a sync failure does not by itself block
    order placement -- it means the *collection scope* (what
    `market-info-service` is actively gathering) may be stale, which
    surfaces later as missing quotes/bars/precision for the affected
    instrument.
    """


__all__ = [
    "MarketDataConfigError",
    "MarketDataError",
    "MarketDataResponseError",
    "MarketDataUnavailableError",
    "PrecisionUnavailableError",
    "SubscriptionSyncError",
]
