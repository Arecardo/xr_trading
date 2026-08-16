"""BT-003a: `unrestricted` vs `restricted` precision-mode comparison for NVDA/QQQ.

Answers the question `doc/technical/implementation_plan/04_backtest_matching_strategy_risk.md`
§3.1 (BT-003a) and `doc/ai_quant_trading_system_requirements.md` §5.1.1 pose:
at the platform's ~$2,500 initial capital, how much does whole-share
rounding (`precision_mode="restricted"`, `lot_size=1`, matching NVDA/QQQ's
real, already-collected Longbridge precision -- BE-004a) actually hurt
target-weight tracking error and returns, compared to unrounded
(`precision_mode="unrestricted"`) fills?

Run: ``python -m scripts.bt003a_precision_comparison`` from ``backend/``
(with the project's venv active).

IMPORTANT -- data source honesty
---------------------------------
This script does **not** call a real market-info-service or Longbridge --
this sandbox has no ``.env`` with real provider credentials (checked before
writing this script) and no running market-info-service with historical
NVDA/QQQ bars already ingested. Fetching genuinely fresh real market data
was not available here. Using it anyway would also fight the "两轮回测均可
复现" (both runs must be reproducible) requirement this task itself sets --
live-fetched data can change or become unavailable on a later re-run.

Instead this generates a **seeded, deterministic, illustrative** daily
price series for NVDA and QQQ (geometric random walk, fixed seed --
python-backend-standards.md §9's reproducibility rule: same seed, same
series, every run) at price levels representative of NVDA/QQQ's real recent
trading range, so the *rounding-error* question this analysis targets is
representative -- but the specific price *path*, and therefore the specific
return/drawdown numbers below, are illustrative, not a claim about actual
historical NVDA/QQQ performance. Do not read the resulting total_return_pct/
max_drawdown_pct as real backtested historical performance; read the
*tracking-error comparison between the two precision modes on identical
data* as the finding, which is what this task needs.

NVDA/QQQ precision (`price_scale=2`, `quantity_scale=0`, `lot_size=1`,
`min_quantity=1`) is the real, already-collected Longbridge value (BE-004a,
`market-info-service/deploy/postgres/init/002_core_catalog_seed.sql`), not
invented here.
"""

from __future__ import annotations

import asyncio
import random
from collections.abc import Callable, Sequence
from datetime import UTC, date, datetime, timedelta
from decimal import ROUND_HALF_UP, Decimal
from uuid import UUID

import httpx

from adapters.market_data.models import InstrumentPrecision, PrecisionBatchResult
from adapters.market_data.precision_cache import PrecisionCache
from application.backtest.config import BacktestConfig
from application.backtest.engine import BacktestEngine
from application.backtest.metrics import BacktestMetrics, compute_backtest_metrics
from application.backtest.models import BacktestResult
from application.backtest.tracking_error import AssetTrackingError, compute_tracking_error
from backtest.calendar import is_us_equity_trading_day
from backtest.instrument_ids import INSTRUMENT_ID_BY_CODE
from backtest.loader import HistoricalDataLoader
from backtest.market_data_client import MarketInfoBarsClient
from domain.assets.models import Asset
from domain.portfolios.models import Portfolio, PortfolioMember
from domain.risk.simple_risk_policy import SimpleRiskPolicy
from domain.strategies.simple_rule_strategy import SimpleRuleStrategy

_PORTFOLIO_ID = UUID("019fdf90-0000-7000-8000-0000000b003a")
_NVDA_ID = "equity:nasdaq:NVDA"
_QQQ_ID = "equity:nasdaq:QQQ"
_NVDA_CODE = "instrument.nasdaq.equity.nvda"
_QQQ_CODE = "instrument.nasdaq.etf.qqq"

_START = date(2025, 1, 2)
_END = _START + timedelta(days=365)
_SEED = 20260816  # fixed -- reproducibility (python-backend-standards.md §9)


def _trading_days() -> list[date]:
    days: list[date] = []
    day = _START
    while day <= _END:
        if is_us_equity_trading_day(day):
            days.append(day)
        day += timedelta(days=1)
    return days


def _seeded_walk(*, start_price: Decimal, mu: float, sigma: float, seed: int) -> list[Decimal]:
    """A deterministic (fixed-seed) geometric random walk over ``_trading_days()``.

    See module docstring -- illustrative, not real market data. ``random.Random(seed)``
    is a locally-scoped generator (never the shared/global ``random`` module), so this
    is isolated from anything else in the process and always reproduces the identical
    sequence for the same seed.
    """
    rng = random.Random(seed)
    price = float(start_price)
    prices: list[Decimal] = []
    for _ in _trading_days():
        price *= 1 + rng.gauss(mu, sigma)
        price = max(price, 1.0)
        prices.append(Decimal(str(price)).quantize(Decimal("0.01"), rounding=ROUND_HALF_UP))
    return prices


def _bars_payload(prices: Sequence[Decimal]) -> dict[str, object]:
    return {
        "bars": [
            {
                "open_time": f"{day.isoformat()}T00:00:00Z",
                "close_time": f"{day.isoformat()}T00:00:00Z",
                "open": str(price),
                "high": str(price),
                "low": str(price),
                "close": str(price),
                "volume": "1000",
                "quality_status": "valid",
            }
            for day, price in zip(_trading_days(), prices, strict=True)
        ],
        "next_cursor": None,
    }


def _make_handler(
    nvda_prices: Sequence[Decimal], qqq_prices: Sequence[Decimal]
) -> Callable[[httpx.Request], httpx.Response]:
    payloads = {
        _NVDA_CODE: _bars_payload(nvda_prices),
        _QQQ_CODE: _bars_payload(qqq_prices),
    }

    def handler(request: httpx.Request) -> httpx.Response:
        instrument_code = dict(request.url.params)["instrument_code"]
        return httpx.Response(200, json=payloads[instrument_code])

    return handler


def _portfolio() -> Portfolio:
    return Portfolio(
        portfolio_id=_PORTFOLIO_ID,
        name="BT-003a Precision Comparison",
        base_currency="USD",
        benchmark_asset_id=_QQQ_ID,
        risk_level="moderate",
        execution_mode="backtest",
        status="active",
    )


def _members() -> tuple[PortfolioMember, ...]:
    return (
        PortfolioMember(
            portfolio_id=_PORTFOLIO_ID,
            asset_id=_NVDA_ID,
            member_status="approved",
            target_weight_min=None,
            target_weight_max=None,
        ),
        PortfolioMember(
            portfolio_id=_PORTFOLIO_ID,
            asset_id=_QQQ_ID,
            member_status="approved",
            target_weight_min=None,
            target_weight_max=None,
        ),
    )


def _assets() -> dict[str, Asset]:
    return {
        _NVDA_ID: Asset(
            asset_id=_NVDA_ID,
            asset_type="STOCK",
            symbol="NVDA",
            venue="nasdaq",
            quote_currency="USD",
            provider_symbols={"longbridge": "NVDA.US"},
            trading_status="tradable",
        ),
        _QQQ_ID: Asset(
            asset_id=_QQQ_ID,
            asset_type="ETF",
            symbol="QQQ",
            venue="nasdaq",
            quote_currency="USD",
            provider_symbols={"longbridge": "QQQ.US"},
            trading_status="tradable",
        ),
    }


class _FixedPrecisionFetcher:
    """Fake ``PrecisionBatchFetcher`` -- structural, no real network call.

    NVDA/QQQ values (``price_scale=2``, ``quantity_scale=0``, ``lot_size=1``,
    ``min_quantity=1``) are the real, already-collected Longbridge precision
    (BE-004a) -- see module docstring.
    """

    def __init__(self) -> None:
        as_of = datetime(2026, 1, 1, tzinfo=UTC)
        self._precisions = {
            INSTRUMENT_ID_BY_CODE[_NVDA_CODE]: InstrumentPrecision(
                instrument_id=INSTRUMENT_ID_BY_CODE[_NVDA_CODE],
                instrument_code=_NVDA_CODE,
                price_scale=2,
                quantity_scale=0,
                lot_size=Decimal("1"),
                min_quantity=Decimal("1"),
                as_of=as_of,
            ),
            INSTRUMENT_ID_BY_CODE[_QQQ_CODE]: InstrumentPrecision(
                instrument_id=INSTRUMENT_ID_BY_CODE[_QQQ_CODE],
                instrument_code=_QQQ_CODE,
                price_scale=2,
                quantity_scale=0,
                lot_size=Decimal("1"),
                min_quantity=Decimal("1"),
                as_of=as_of,
            ),
        }

    async def get_precision_batch(self, instrument_ids: Sequence[UUID]) -> PrecisionBatchResult:
        items = tuple(self._precisions[i] for i in instrument_ids if i in self._precisions)
        missing = tuple(i for i in instrument_ids if i not in self._precisions)
        return PrecisionBatchResult(items=items, missing_instrument_ids=missing)


async def _run_one(
    *, precision_mode: str, handler: Callable[[httpx.Request], httpx.Response]
) -> BacktestResult:
    config = BacktestConfig(
        portfolio_id=_PORTFOLIO_ID,
        start_date=_START,
        end_date=_END,
        precision_mode=precision_mode,  # type: ignore[arg-type]
    )
    precision_cache = (
        PrecisionCache(_FixedPrecisionFetcher()) if precision_mode == "restricted" else None
    )
    async with httpx.AsyncClient(
        transport=httpx.MockTransport(handler), base_url="http://market-info.test"
    ) as http_client:
        loader = HistoricalDataLoader(MarketInfoBarsClient(http_client))
        engine = BacktestEngine(
            config=config,
            portfolio=_portfolio(),
            members=_members(),
            assets=_assets(),
            # SimpleRuleStrategy/SimpleRiskPolicy at their own unmodified
            # defaults -- BT-003b's shrink-to-approved sizing (2026-08-16)
            # is what makes a meaningful, gradually-built position possible
            # here at all under the default risk budget.
            strategy=SimpleRuleStrategy(),
            risk_policy=SimpleRiskPolicy(environment="backtest"),
            historical_data_loader=loader,
            precision_cache=precision_cache,
        )
        return await engine.run()


def _fmt_pct(value: Decimal | None) -> str:
    if value is None:
        return "N/A"
    return f"{(value * 100).quantize(Decimal('0.01'))}%"


def _print_metrics(label: str, result: BacktestResult, metrics: BacktestMetrics) -> None:
    perf = metrics.performance
    print(f"\n--- {label} ---")
    print(f"final_equity:        {metrics.final_equity}")
    print(f"total_return_pct:    {_fmt_pct(perf.total_return_pct)}")
    print(f"max_drawdown_pct:    {_fmt_pct(perf.max_drawdown_pct)}")
    print(f"sharpe_ratio:        {perf.sharpe_ratio}")
    print(f"trade_count (filled):{metrics.trade_count}")
    print(f"turnover:            {_fmt_pct(metrics.turnover)}")
    skipped = sum(1 for t in result.trades if t.status.startswith("skipped"))
    rejected = sum(1 for t in result.trades if t.status == "rejected")
    print(f"skipped decisions:   {skipped}")
    print(f"rejected decisions:  {rejected}")


def _print_tracking_error(label: str, errors: dict[str, AssetTrackingError]) -> None:
    print(f"\n--- {label}: tracking error (mean|max absolute weight deviation) ---")
    for asset_id, e in sorted(errors.items()):
        print(
            f"{asset_id}: mean={_fmt_pct(e.mean_absolute_error)} "
            f"max={_fmt_pct(e.max_absolute_error)} (n={e.days_considered})"
        )


async def main() -> None:
    nvda_prices = _seeded_walk(start_price=Decimal("120"), mu=0.0006, sigma=0.020, seed=_SEED)
    qqq_prices = _seeded_walk(start_price=Decimal("480"), mu=0.0004, sigma=0.010, seed=_SEED + 1)
    handler = _make_handler(nvda_prices, qqq_prices)

    unrestricted = await _run_one(precision_mode="unrestricted", handler=handler)
    restricted = await _run_one(precision_mode="restricted", handler=handler)

    unrestricted_metrics = compute_backtest_metrics(unrestricted)
    restricted_metrics = compute_backtest_metrics(restricted)

    _print_metrics("unrestricted", unrestricted, unrestricted_metrics)
    _print_metrics("restricted (lot_size=1)", restricted, restricted_metrics)

    _print_tracking_error("unrestricted", compute_tracking_error(unrestricted))
    _print_tracking_error("restricted (lot_size=1)", compute_tracking_error(restricted))


if __name__ == "__main__":
    asyncio.run(main())
