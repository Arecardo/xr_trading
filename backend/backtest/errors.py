"""Error hierarchy for BT-001 historical data loading.

Mirrors the repo-wide convention (python-backend-standards.md §5) of a
domain exception hierarchy with a stable base type, rather than surfacing
raw ``httpx``/JSON-decoding errors to callers.
"""

from __future__ import annotations


class BacktestDataError(Exception):
    """Base class for all BT-001 historical data loading errors."""


class MarketDataResponseError(BacktestDataError):
    """`market-info-service` returned an error, or a response that does not
    match the documented `/bars` or `/instruments` shape
    (`doc/technical/market_information_service/07_api_and_admin_ui.md` §2.2,
    §2.3).

    Covers: non-2xx HTTP status, malformed JSON, missing/invalid fields,
    and decimal fields that fail to parse.
    """


class MissingTradingDayBarError(BacktestDataError):
    """A UTC calendar day that was expected to carry fresh data had none.

    Raised for:
    - Any crypto day with no matching bar (crypto is 24/7; a gap indicates
      a real provider/data problem, not an ordinary non-trading day, and
      must never be silently carried forward as if it were a US-equity
      holiday).
    - A scheduled US equity trading day (per the internal NASDAQ/NYSE
      calendar, see ``backtest.calendar``) with no matching bar.
    - A non-trading day at the very start of the requested range, before
      any fresh bar has been seen, so there is no prior close to carry
      forward.

    This is a fail-closed choice (CLAUDE.md 黄金规则, python-backend-standards.md
    §5): silently carrying forward in these cases would mask a real data gap
    as an ordinary, expected non-trading day.
    """
