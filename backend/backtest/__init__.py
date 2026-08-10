"""Historical data loading for the backtest engine (BT-001).

Responsibility (`doc/technical/implementation_plan/03_backtest_data_and_indicators.md`
BT-001, `doc/technical/08_backtest_engine.md` §3/§5): pull historical daily
bars for a single instrument from `market-info-service`'s already-existing
public query API and align them into a gapless, one-entry-per-UTC-calendar-day
series -- US equities carry forward the prior close and are marked
`price_status="stale"` on non-trading days (weekends and NASDAQ/NYSE
holidays); crypto is expected to have fresh data every day (24/7).

Explicitly NOT this package's job:
- Indicator calculation (MA/RSI/ATR/...) -- that is BT-002, a separate task.
- The shared production market-data client with TTL caching used by the
  live/paper order-precision path (future BE-004,
  `backend/adapters/market_data/`) -- this package's
  :class:`~backend.backtest.market_data_client.MarketInfoBarsClient` is a
  minimal, read-only, backtest-scoped client. Duplication between the two is
  expected until a later refactor unifies them.
- Portfolio/asset_id resolution -- callers pass an explicit
  `instrument_code`/`provider`/`asset_type` (mirroring the `/bars` API,
  which deliberately does not accept `asset_code` alone; see
  `doc/technical/market_information_service/07_api_and_admin_ui.md` §2.2).

Public surface for downstream consumers (BT-002, BT-004+):
``AlignedBar``, ``AlignedSeries`` (data shape), ``HistoricalDataLoader``
(orchestration), the error hierarchy in ``errors``, and (BT-002)
``IndicatorBar``/``compute_indicators`` plus the individual per-indicator
functions in ``indicators`` for callers that want a single indicator rather
than the full set.
"""

from __future__ import annotations

from .errors import (
    BacktestDataError,
    MarketDataResponseError,
    MissingTradingDayBarError,
)
from .indicators import (
    IndicatorBar,
    MaTrend,
    compute_atr,
    compute_daily_returns,
    compute_indicators,
    compute_ma_trend,
    compute_moving_average,
    compute_price_position,
    compute_relative_strength,
    compute_rsi,
    compute_volume_change,
)
from .loader import HistoricalDataLoader
from .market_data_client import MarketInfoBarsClient
from .models import AlignedBar, AlignedSeries, AssetClass, PriceStatus, RawBar

__all__ = [
    "AlignedBar",
    "AlignedSeries",
    "AssetClass",
    "BacktestDataError",
    "HistoricalDataLoader",
    "IndicatorBar",
    "MarketDataResponseError",
    "MarketInfoBarsClient",
    "MaTrend",
    "MissingTradingDayBarError",
    "PriceStatus",
    "RawBar",
    "compute_atr",
    "compute_daily_returns",
    "compute_indicators",
    "compute_ma_trend",
    "compute_moving_average",
    "compute_price_position",
    "compute_relative_strength",
    "compute_rsi",
    "compute_volume_change",
]
