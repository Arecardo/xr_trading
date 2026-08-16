"""Integration-style tests for application.backtest.engine.BacktestEngine (BT-003).

Drives the full day loop with fake historical data (``httpx.MockTransport``,
matching ``tests/backtest/test_loader.py``'s established pattern -- no real
network call) and, where the test needs full control over the decision
rather than depending on ``SimpleRuleStrategy``'s indicator warmup timing, a
small fake ``Strategy``. Uses the real ``SimpleRuleStrategy``/
``SimpleRiskPolicy`` for the broader scenarios so this suite exercises the
actual BT-004/BT-005 code the engine is meant to orchestrate, not a mock of
it.

Synthetic universe: NVDA (monotonically uptrending close, so
``SimpleRuleStrategy`` eventually scores >= 2 and buys) and QQQ (flat,
serving as both a portfolio member and the benchmark) -- both quote in USD,
the portfolio's base currency, so no FX series is needed for this suite.
The BTC-USDT/FX conversion path (``_price_and_status_at``'s non-1.0-rate
branch) is exercised by ``application.backtest.engine``'s own logic being
straightforward pass-through arithmetic, but is not covered by a dedicated
end-to-end test here -- flagged as an open gap in the BT-003 task report.
"""

from __future__ import annotations

from collections.abc import Callable, Sequence
from datetime import UTC, date, datetime, timedelta
from decimal import Decimal
from uuid import UUID, uuid4

import httpx
import pytest

from adapters.market_data.models import InstrumentPrecision, PrecisionBatchResult
from adapters.market_data.precision_cache import PrecisionCache
from application.backtest.config import BacktestConfig
from application.backtest.engine import BacktestEngine
from application.backtest.models import BacktestResult
from backtest.calendar import is_us_equity_trading_day
from backtest.instrument_ids import INSTRUMENT_ID_BY_CODE
from backtest.loader import HistoricalDataLoader
from backtest.market_data_client import MarketInfoBarsClient
from domain.assets.models import Asset
from domain.portfolios.models import Portfolio, PortfolioMember
from domain.risk.models import OrderIntent, RiskCheckResult, RiskPolicy
from domain.risk.simple_risk_policy import SimpleRiskPolicy
from domain.strategies.models import AnalysisResult, StrategyOutput
from domain.strategies.simple_rule_strategy import SimpleRuleStrategy
from domain.valuation.models import PortfolioState

_PORTFOLIO_ID = uuid4()
_NVDA_ID = "equity:nasdaq:NVDA"
_QQQ_ID = "equity:nasdaq:QQQ"
_NVDA_CODE = "instrument.nasdaq.equity.nvda"
_QQQ_CODE = "instrument.nasdaq.etf.qqq"

_START = date(2024, 1, 2)
_END = _START + timedelta(days=100)


def _trading_days(start: date, end: date) -> list[date]:
    days: list[date] = []
    day = start
    while day <= end:
        if is_us_equity_trading_day(day):
            days.append(day)
        day += timedelta(days=1)
    return days


def _nvda_prices() -> list[tuple[date, Decimal]]:
    # Monotonically uptrending -- eventually pushes SimpleRuleStrategy's
    # score to >= 2 (ma_trend up, close>ma20/ma50, positive relative
    # strength vs the flat QQQ benchmark).
    return [
        (day, Decimal("100") + Decimal(i) * Decimal("2"))
        for i, day in enumerate(_trading_days(_START, _END))
    ]


def _qqq_prices() -> list[tuple[date, Decimal]]:
    return [(day, Decimal("100")) for day in _trading_days(_START, _END)]


def _bars_payload(entries: Sequence[tuple[date, Decimal]]) -> dict[str, object]:
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
            for day, price in entries
        ],
        "next_cursor": None,
    }


def _make_handler() -> Callable[[httpx.Request], httpx.Response]:
    payloads = {
        _NVDA_CODE: _bars_payload(_nvda_prices()),
        _QQQ_CODE: _bars_payload(_qqq_prices()),
    }

    def handler(request: httpx.Request) -> httpx.Response:
        instrument_code = dict(request.url.params)["instrument_code"]
        return httpx.Response(200, json=payloads[instrument_code])

    return handler


def _make_fixed_handler(payload: dict[str, object]) -> Callable[[httpx.Request], httpx.Response]:
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(200, json=payload)

    return handler


def _portfolio(benchmark_asset_id: str | None = _QQQ_ID) -> Portfolio:
    return Portfolio(
        portfolio_id=_PORTFOLIO_ID,
        name="BT-003 Test Growth",
        base_currency="USD",
        benchmark_asset_id=benchmark_asset_id,
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


async def _run(
    config: BacktestConfig,
    *,
    members: tuple[PortfolioMember, ...] | None = None,
    portfolio: Portfolio | None = None,
    strategy: SimpleRuleStrategy | _FixedWeightStrategy | None = None,
    risk_policy: RiskPolicy | None = None,
    precision_cache: PrecisionCache | None = None,
    handler: Callable[[httpx.Request], httpx.Response] | None = None,
) -> BacktestResult:
    async with httpx.AsyncClient(
        transport=httpx.MockTransport(handler or _make_handler()),
        base_url="http://market-info.test",
    ) as http_client:
        loader = HistoricalDataLoader(MarketInfoBarsClient(http_client))
        engine = BacktestEngine(
            config=config,
            portfolio=portfolio or _portfolio(),
            members=members if members is not None else _members(),
            assets=_assets(),
            strategy=strategy or SimpleRuleStrategy(),
            risk_policy=risk_policy or _permissive_risk_policy(),
            historical_data_loader=loader,
            precision_cache=precision_cache,
        )
        return await engine.run()


def _permissive_risk_policy() -> SimpleRiskPolicy:
    """A ``SimpleRiskPolicy`` wide enough to let a full-size rebalance through, unshrunk.

    ``SimpleRiskPolicy``'s default ``max_order_risk_pct_of_nav`` (0.5% of
    NAV) is a *capital-at-risk* budget (2026-08-16 revision:
    ``order_notional_pct * assumed_stop_loss_pct``, not raw order notional --
    see ``domain.risk.simple_risk_policy``'s module docstring), but even
    under that more forgiving interpretation, ``SimpleRuleStrategy``'s
    default single-asset ceiling (20%) times the default equity stop-loss
    assumption (8%) is 1.6%, still above the conservative 0.5% default
    budget -- a "buy" score proposing to reach the full 20% ceiling in one
    order does not clear the default budget *at full size*. As of
    2026-08-16 this is no longer an outright reject: ``BacktestEngine``
    binary-halves a risk-rejected buy until it clears the budget (see
    ``_shrink_buy_to_approved``/`08_backtest_engine.md` §9a and
    ``test_default_risk_policy_shrinks_a_full_weight_cap_buy_instead_of_only_rejecting``
    below), so a smaller fill now happens even under unmodified defaults.
    Tests below that specifically need the *full, unshrunk* desired size to
    fill in one order (to keep their arithmetic simple, or to isolate a
    different behaviour from the shrink mechanism) still use this wider
    per-order budget so shrinking never triggers; every other threshold
    stays at ``SimpleRiskPolicy``'s own conservative default.
    """
    return SimpleRiskPolicy(environment="backtest", max_order_risk_pct_of_nav=Decimal("0.25"))


class _FixedWeightStrategy:
    """A minimal ``Strategy`` that always proposes the same fixed target weight.

    Used only for the last-day-in-range edge case test, where we need full
    control over exactly when a fresh buy decision is proposed --
    ``SimpleRuleStrategy``'s indicator-warmup timing is not something a test
    should hardcode a specific day against.
    """

    strategy_id = "fixed_weight_test"
    version = "1.0.0"

    def __init__(self, asset_id: str, weight: Decimal, base_currency: str) -> None:
        self._asset_id = asset_id
        self._weight = weight
        self._base_currency = base_currency

    def generate_targets(
        self, analysis: AnalysisResult, portfolio_state: PortfolioState
    ) -> StrategyOutput:
        cash_id = f"cash:{self._base_currency}"
        return StrategyOutput(
            portfolio_id=portfolio_state.portfolio.portfolio_id,
            as_of=analysis.as_of,
            asset_scores=(),
            target_weights={self._asset_id: self._weight, cash_id: Decimal("1") - self._weight},
            rebalance_notes=(),
        )


class _QuantityCapRiskPolicy:
    """A minimal fake ``RiskPolicy``: approves a buy iff its quantity is at or under a fixed cap.

    Used to deterministically exercise ``BacktestEngine``'s shrink-to-approved
    order-sizing logic (`08_backtest_engine.md` §9a) without depending on
    ``SimpleRiskPolicy``'s several interacting thresholds -- this fake has
    exactly one, quantity-only rule, so a test can pick out precisely which
    order size gets approved. Sells are always approved (nothing to shrink).
    """

    def __init__(self, max_buy_quantity: Decimal) -> None:
        self._max_buy_quantity = max_buy_quantity

    def check_order(self, intent: OrderIntent, portfolio_state: PortfolioState) -> RiskCheckResult:
        if intent.side == "sell" or intent.quantity <= self._max_buy_quantity:
            return RiskCheckResult(
                approved=True, rejection_reasons=(), checked_rules=("quantity_cap",), context={}
            )
        return RiskCheckResult(
            approved=False,
            rejection_reasons=(f"quantity {intent.quantity} exceeds cap {self._max_buy_quantity}",),
            checked_rules=("quantity_cap",),
            context={"max_buy_quantity": str(self._max_buy_quantity)},
        )

    def check_target_weights(
        self, target_weights: dict[str, Decimal], portfolio_state: PortfolioState
    ) -> RiskCheckResult:
        return RiskCheckResult(approved=True, rejection_reasons=(), checked_rules=(), context={})

    def replaced_checks(self) -> tuple[str, ...]:
        return ()


class _FakePrecisionFetcher:
    """Fake ``PrecisionBatchFetcher`` -- structural, no real network call."""

    def __init__(self, precisions: dict[UUID, InstrumentPrecision]) -> None:
        self._precisions = precisions

    async def get_precision_batch(self, instrument_ids: Sequence[UUID]) -> PrecisionBatchResult:
        items = tuple(self._precisions[i] for i in instrument_ids if i in self._precisions)
        missing = tuple(i for i in instrument_ids if i not in self._precisions)
        return PrecisionBatchResult(items=items, missing_instrument_ids=missing)


# --- tests ------------------------------------------------------------------


@pytest.mark.anyio
async def test_backtest_is_reproducible_given_identical_input() -> None:
    config = BacktestConfig(portfolio_id=_PORTFOLIO_ID, start_date=_START, end_date=_END)
    result_a = await _run(config)
    result_b = await _run(config)

    assert result_a.equity_curve == result_b.equity_curve
    assert result_a.trades == result_b.trades
    assert result_a.final_positions == result_b.final_positions
    assert result_a.final_cash == result_b.final_cash
    assert result_a.daily_detail == result_b.daily_detail


@pytest.mark.anyio
async def test_uptrend_eventually_triggers_a_filled_nvda_buy() -> None:
    config = BacktestConfig(portfolio_id=_PORTFOLIO_ID, start_date=_START, end_date=_END)
    result = await _run(config)

    filled_buys = [
        t
        for t in result.trades
        if t.status == "filled" and t.side == "buy" and t.asset_id == _NVDA_ID
    ]
    assert filled_buys, "expected at least one filled NVDA buy over the uptrending series"
    assert result.final_positions[_NVDA_ID] > Decimal("0")
    # Every equity-curve entry is UTC-calendar-day gapless per DEC-002.
    assert len(result.equity_curve) == (_END - _START).days + 1


@pytest.mark.anyio
async def test_commission_and_slippage_measurably_reduce_final_equity() -> None:
    zero_cost_config = BacktestConfig(
        portfolio_id=_PORTFOLIO_ID,
        start_date=_START,
        end_date=_END,
        commission_pct=Decimal("0"),
        min_commission=Decimal("0"),
        slippage_pct=Decimal("0"),
    )
    costly_config = BacktestConfig(
        portfolio_id=_PORTFOLIO_ID,
        start_date=_START,
        end_date=_END,
        commission_pct=Decimal("0.01"),
        min_commission=Decimal("1"),
        slippage_pct=Decimal("0.01"),
    )

    zero_cost_result = await _run(zero_cost_config)
    costly_result = await _run(costly_config)

    assert any(t.status == "filled" for t in zero_cost_result.trades)
    assert any(t.status == "filled" for t in costly_result.trades)

    zero_cost_final_nav = zero_cost_result.equity_curve[-1].net_asset_value
    costly_final_nav = costly_result.equity_curve[-1].net_asset_value
    assert costly_final_nav < zero_cost_final_nav


@pytest.mark.anyio
async def test_risk_rejected_order_never_fills() -> None:
    config = BacktestConfig(portfolio_id=_PORTFOLIO_ID, start_date=_START, end_date=_END)
    strict_risk_policy = SimpleRiskPolicy(
        environment="backtest",
        max_order_risk_pct_of_nav=Decimal("0.0000001"),  # blocks every realistic order
    )

    result = await _run(config, risk_policy=strict_risk_policy)

    assert not any(t.status == "filled" for t in result.trades)
    assert any(t.status == "rejected" for t in result.trades)
    assert result.final_positions[_NVDA_ID] == Decimal("0")
    assert result.final_cash == config.initial_cash


@pytest.mark.anyio
async def test_restricted_mode_below_min_quantity_never_fills_and_records_reason() -> None:
    nvda_uuid = INSTRUMENT_ID_BY_CODE[_NVDA_CODE]
    qqq_uuid = INSTRUMENT_ID_BY_CODE[_QQQ_CODE]
    as_of = datetime(2024, 1, 1, tzinfo=UTC)
    fake_fetcher = _FakePrecisionFetcher(
        {
            nvda_uuid: InstrumentPrecision(
                instrument_id=nvda_uuid,
                instrument_code=_NVDA_CODE,
                price_scale=2,
                quantity_scale=0,
                lot_size=Decimal("1000"),  # absurdly coarse -- any realistic order rounds to 0
                min_quantity=Decimal("1000"),
                as_of=as_of,
            ),
            qqq_uuid: InstrumentPrecision(
                instrument_id=qqq_uuid,
                instrument_code=_QQQ_CODE,
                price_scale=2,
                quantity_scale=0,
                lot_size=Decimal("1"),
                min_quantity=Decimal("1"),
                as_of=as_of,
            ),
        }
    )
    precision_cache = PrecisionCache(fake_fetcher)
    config = BacktestConfig(
        portfolio_id=_PORTFOLIO_ID,
        start_date=_START,
        end_date=_END,
        precision_mode="restricted",
    )

    result = await _run(config, precision_cache=precision_cache)

    nvda_trades = [t for t in result.trades if t.asset_id == _NVDA_ID]
    assert nvda_trades, "expected at least one NVDA order attempt"
    assert not any(t.status == "filled" for t in nvda_trades)
    assert any(t.status == "skipped_min_quantity" for t in nvda_trades)
    assert result.final_positions[_NVDA_ID] == Decimal("0")


@pytest.mark.anyio
async def test_stale_equity_day_has_no_new_order_but_still_values_holdings() -> None:
    config = BacktestConfig(portfolio_id=_PORTFOLIO_ID, start_date=_START, end_date=_END)
    result = await _run(config)

    first_fill = next(t for t in result.trades if t.status == "filled" and t.asset_id == _NVDA_ID)
    assert first_fill.trade_date is not None

    stale_snapshot_after_fill = next(
        snap
        for snap in result.equity_curve
        if snap.valuation_date > first_fill.trade_date and snap.price_status == "stale"
    )
    assert stale_snapshot_after_fill.positions_value > Decimal("0")
    assert not any(
        t.asset_id == _NVDA_ID and t.decision_date == stale_snapshot_after_fill.valuation_date
        for t in result.trades
    )


@pytest.mark.anyio
async def test_last_day_decision_is_recorded_as_skipped_not_dropped() -> None:
    start = date(2024, 1, 2)  # Tuesday
    end = date(2024, 1, 3)  # Wednesday -- two consecutive trading days, no weekend involved
    config = BacktestConfig(portfolio_id=_PORTFOLIO_ID, start_date=start, end_date=end)
    permissive_risk_policy = SimpleRiskPolicy(
        environment="backtest",
        max_single_equity_weight=Decimal("1"),
        max_order_risk_pct_of_nav=Decimal("1"),
        min_cash_weight=Decimal("0"),
        drawdown_stop_line=Decimal("1"),
    )
    strategy = _FixedWeightStrategy(_NVDA_ID, Decimal("0.95"), "USD")
    handler = _make_fixed_handler(_bars_payload([(start, Decimal("100")), (end, Decimal("101"))]))

    result = await _run(
        config,
        members=(_members()[0],),  # NVDA only -- no benchmark needed
        portfolio=_portfolio(benchmark_asset_id=None),
        strategy=strategy,
        risk_policy=permissive_risk_policy,
        handler=handler,
    )

    day0_trade = next(t for t in result.trades if t.decision_date == start)
    assert day0_trade.status == "filled"
    assert day0_trade.trade_date == end

    day1_trade = next(t for t in result.trades if t.decision_date == end)
    assert day1_trade.status == "skipped_no_next_day"
    assert day1_trade.trade_date is None
    assert day1_trade.quantity == Decimal("0")


@pytest.mark.anyio
async def test_daily_detail_records_target_weights_and_position_values_every_day() -> None:
    config = BacktestConfig(portfolio_id=_PORTFOLIO_ID, start_date=_START, end_date=_END)
    result = await _run(config)

    # One DailyPortfolioDetail per equity_curve day, same dates, gapless.
    assert len(result.daily_detail) == len(result.equity_curve)
    assert [d.valuation_date for d in result.daily_detail] == [
        s.valuation_date for s in result.equity_curve
    ]

    # SimpleRuleStrategy always proposes a full target_weights mapping
    # (asset scores plus the cash residual) summing to exactly 1.
    for detail in result.daily_detail:
        assert detail.target_weights
        assert sum(detail.target_weights.values(), Decimal("0")) == Decimal("1")

    # NVDA/QQQ both quote in USD == the portfolio's base_currency, so this
    # suite's universe never applies an FX conversion.
    assert all(detail.fx_rates_applied == {} for detail in result.daily_detail)

    # position_values must reconcile against the aggregate equity_curve total,
    # and only appear once a fill has actually happened.
    detail_by_date = {d.valuation_date: d for d in result.daily_detail}
    for snapshot in result.equity_curve:
        detail = detail_by_date[snapshot.valuation_date]
        assert sum(detail.position_values.values(), Decimal("0")) == snapshot.positions_value

    first_fill = next(t for t in result.trades if t.status == "filled" and t.asset_id == _NVDA_ID)
    assert first_fill.trade_date is not None
    detail_on_fill_day = detail_by_date[first_fill.trade_date]
    assert detail_on_fill_day.position_values.get(_NVDA_ID, Decimal("0")) > Decimal("0")


@pytest.mark.anyio
async def test_a_risk_rejected_buy_shrinks_and_fills_at_the_largest_approved_size() -> None:
    start = date(2024, 1, 2)
    end = date(2024, 1, 3)
    config = BacktestConfig(portfolio_id=_PORTFOLIO_ID, start_date=start, end_date=end)
    # day0 close = 100 (see _nvda_prices), nav = initial_cash = 2500 (no positions yet):
    # target_value = 0.95 * 2500 = 2375; full-gap quantity = 2375 / 100 = 23.75.
    # Halving sequence: 11.875 -> 5.9375 -> 2.96875 (<= cap=3, first approved size).
    strategy = _FixedWeightStrategy(_NVDA_ID, Decimal("0.95"), "USD")
    risk_policy = _QuantityCapRiskPolicy(max_buy_quantity=Decimal("3"))
    handler = _make_fixed_handler(_bars_payload([(start, Decimal("100")), (end, Decimal("101"))]))

    result = await _run(
        config,
        members=(_members()[0],),  # NVDA only
        portfolio=_portfolio(benchmark_asset_id=None),
        strategy=strategy,
        risk_policy=risk_policy,
        handler=handler,
    )

    first_trade = next(t for t in result.trades if t.decision_date == start)
    assert first_trade.status == "filled"
    assert first_trade.planned_quantity == Decimal("2.96875")
    assert first_trade.quantity == Decimal("2.96875")  # unrestricted precision -- no rounding
    assert any("order size reduced from 23.75 to 2.96875" in r for r in first_trade.reason)


@pytest.mark.anyio
async def test_a_buy_stays_rejected_when_even_the_smallest_shrunk_size_fails_risk() -> None:
    start = date(2024, 1, 2)
    end = date(2024, 1, 3)
    config = BacktestConfig(portfolio_id=_PORTFOLIO_ID, start_date=start, end_date=end)
    # Full-gap quantity is 23.75 (see the sibling test); even 256x smaller
    # (23.75 / 2**8 ~= 0.0928) still exceeds this cap -- shrinking cannot help.
    strategy = _FixedWeightStrategy(_NVDA_ID, Decimal("0.95"), "USD")
    risk_policy = _QuantityCapRiskPolicy(max_buy_quantity=Decimal("0.05"))
    handler = _make_fixed_handler(_bars_payload([(start, Decimal("100")), (end, Decimal("101"))]))

    result = await _run(
        config,
        members=(_members()[0],),
        portfolio=_portfolio(benchmark_asset_id=None),
        strategy=strategy,
        risk_policy=risk_policy,
        handler=handler,
    )

    first_trade = next(t for t in result.trades if t.decision_date == start)
    assert first_trade.status == "rejected"
    assert first_trade.quantity == Decimal("0")
    assert result.final_positions[_NVDA_ID] == Decimal("0")


@pytest.mark.anyio
async def test_default_risk_policy_shrinks_a_full_weight_cap_buy_instead_of_only_rejecting() -> (
    None
):
    """Under SimpleRiskPolicy's own conservative defaults (no test-only widening),
    a buy proposing SimpleRuleStrategy's default 20% single-asset ceiling exceeds
    the 0.5% capital-at-risk budget at full size (20% * 8% assumed equity stop-loss
    = 1.6% > 0.5%) -- before the shrink mechanism existed this was an outright
    reject every time (see the now-superseded rationale in _permissive_risk_policy's
    docstring). With shrinking, two halvings (1.6% / 4 = 0.4% <= 0.5%) clear the
    budget, so a real fill should occur using the engine's and risk policy's
    unmodified defaults.
    """
    config = BacktestConfig(portfolio_id=_PORTFOLIO_ID, start_date=_START, end_date=_END)

    result = await _run(config, risk_policy=SimpleRiskPolicy(environment="backtest"))

    filled_buys = [
        t
        for t in result.trades
        if t.status == "filled" and t.side == "buy" and t.asset_id == _NVDA_ID
    ]
    assert filled_buys, "expected at least one filled NVDA buy under default risk settings"
    assert any("order size reduced from" in r for t in result.trades for r in t.reason)
