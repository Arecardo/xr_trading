"""``BacktestEngine`` (BT-003): the event-driven backtest orchestration loop.

Implements `doc/technical/08_backtest_engine.md` §5's per-UTC-calendar-day
event loop (RM0 DEC-002: 365 days/year, never skipping US non-trading days)
by wiring together the four already-built BT-003 pure modules
(``backtest.matching``, ``backtest.precision``, ``backtest.rebalance``,
``backtest.instrument_ids``) with BT-001/BT-002's historical data pipeline
(``backtest.loader``/``backtest.indicators``) and BT-004/BT-005's domain
``Strategy``/``RiskPolicy`` implementations. This module is the one place
those pieces get assembled -- none of the pieces it calls know about the
others.

Judgment calls made here, documented per the task brief's instruction not to
leave them implicit
------------------------------------------------------------------------

**1. Fill-price timing: day-D decision, day-D+1 execution.**
``backtest.matching``'s own docstring already commits to "next-day open, not
same-day close" to avoid look-ahead bias, but leaves the actual
day-sequencing to this module. The loop below threads it like this, one
iteration per UTC calendar day ``D``:

  a. Apply any order *decided* on day ``D-1`` at day ``D``'s **open** price
     (slippage/commission applied here) -- updates positions/cash.
  b. Value the portfolio at day ``D``'s **close** (post step a's fill).
  c. Compute indicators/analysis/strategy signal from day ``D``'s close data.
  d. Diff the strategy's target weights into ``PlannedOrder``s and risk-check
     each one against the day-``D`` portfolio state from step b.
  e. Queue approved orders to be applied at day ``D+1``'s open (step a of the
     *next* iteration) -- unless ``D`` is the last day in the configured
     range, in which case there is no ``D+1`` to execute against, and the
     decision is recorded with ``status="skipped_no_next_day"`` rather than
     silently dropped (the task brief's explicit instruction).
  f. Record day ``D``'s equity snapshot (from step b).

This means a decision is always priced (for risk-check purposes) off data
that was genuinely available at decision time (day ``D``'s close) and always
executes off a later, independently-observed price (day ``D+1``'s open) --
no future data ever leaks into either the decision or the fill.

**2. Risk gate: ``check_order`` only, not ``check_target_weights``.**
CONTRACT-003 offers two check methods; this engine uses ``check_order``
exclusively as the blocking gate, called once per ``PlannedOrder`` and routed
through ``BacktestExecutionService.submit`` (CONTRACT-001's non-bypassable
"approved=False must never fill" rule). ``check_target_weights`` is
deliberately **not** called from the day loop: it evaluates the strategy's
*raw* proposed ``target_weights`` (before ``rebalance_threshold_pct``
filtering and the stale-day no-new-equity-signal rule are applied in
``backtest.rebalance.diff_target_weights_to_orders``), which is a
hypothetical allocation the engine may not even attempt to reach that day --
rejecting on that basis would be evaluating the wrong artifact.
``check_order`` evaluates the literal, already-filtered order about to be
submitted, which is the correct granularity for a per-order, non-bypassable
gate, and is what plugs directly into ``ExecutionService.submit``'s
single-``RiskCheckResult``-per-``Order`` contract. This still satisfies §9's
"尽量复用风控规则" -- every proposed order passes through the same
``SimpleRiskPolicy`` instance ``paper``/``live`` would use, with no
backtest-specific carve-out.

**3. FX series ``asset_type``: reuse ``"CRYPTO"``, do not extend
``backtest.models.AssetClass``.** The task brief flags this as an open
choice with two pre-approved options. This module takes the narrower one:
``HistoricalDataLoader.load(..., asset_type="CRYPTO", ...)`` for the
``fx:coingecko:usdt-usd`` series. Reasoning: ``AssetClass`` only controls one
thing -- ``backtest.alignment.align_daily_series``'s calendar behaviour
(``CRYPTO``/24-7-fresh vs. ``STOCK``/``ETF``-follows-the-US-equity-calendar).
DEC-006 already establishes CoinGecko FX data is collected daily, every day,
with no equity-style non-trading-day gaps (``open=high=low=close`` = that
day's single price) -- i.e. exactly the 24/7-fresh behaviour ``"CRYPTO"``
already implements, not a new behaviour. Adding a fourth ``AssetClass`` value
would touch ``backtest/alignment.py``'s ``is_expected_fresh`` branch (a
BT-001-owned file this task does not otherwise need to modify) for no
behavioural difference from reusing ``"CRYPTO"``. This module never treats
the FX series as an ``Asset`` with ``asset_type="CRYPTO"`` anywhere else (it
is not a portfolio member, not scored, not traded) -- ``asset_type`` here is
purely the ``HistoricalDataLoader`` parameter that selects calendar
behaviour, and reusing ``"CRYPTO"`` for that one parameter is documented,
not silent.

**4. Indicators are computed on each asset's *native* (unconverted)
currency series, not the base-currency-converted one.** BT-002's
``compute_indicators`` runs once, before the day loop, directly on the
``AlignedSeries`` returned by ``HistoricalDataLoader`` (e.g. BTC-USDT's own
USDT-denominated close series) -- never re-run on an FX-converted series.
Only ``AssetAnalysis.close`` (the raw current price CONTRACT-002 needs for
weight math against a base-currency NAV) is FX-converted, per asset per day,
inside the loop. Rationale: USDT/USD is a near-1.0, low-volatility rate
(DEC-006), so converting the *indicator inputs* would only inject FX noise
into technical indicators that are meant to describe the asset's own price
action, not double-count a currency effect the FX series already isolates
separately.

**5. Precision fail-closed is per-asset, fetched once per run, not per
day.** `08_backtest_engine.md` §3's "缺失或过期时该标的当期 fail-closed，不生成
撮合" is read here as *per instrument*, not "abort the whole run": each
tradable asset's ``InstrumentPrecision`` is looked up individually (one
``PrecisionCache.get_many([single_id])`` call per asset, still just once per
run, not once per day) so that one asset's missing/expired precision data
does not prevent trading a *different* asset whose precision is fine. An
asset with no usable precision in ``precision_mode="restricted"`` has every
one of its orders recorded with ``status="skipped_precision_unavailable"``
for the whole run -- never a fabricated default (DEC-003).

**6. Reproducibility: no wall-clock time, no unseeded randomness.**
``Order``/trade identifiers are derived from ``uuid.uuid5`` (a deterministic
hash over ``(portfolio_id, decision_date, asset_id, sequence_number)``), not
``uuid4()`` -- ``uuid4`` is non-deterministic and would break
bit-for-bit-identical reruns (python-backend-standards.md §9,
`08_backtest_engine.md` §10 "固定输入下回测结果应可重复").

**7. Position average cost.** A simple running weighted average is
maintained on every buy fill (``new_avg = (old_qty*old_avg + fill_qty*
fill_price) / (old_qty+fill_qty)``); a sell leaves it unchanged (standard
cost-basis treatment) and it resets to zero once a position is fully closed.
Nothing else in this codebase currently reads ``Position.average_cost`` (the
strategy and risk implementations both use ``AssetAnalysis.close``, a real
current price, for weight math -- see their own docstrings on refusing to
use cost basis as a price stand-in) -- this is maintained only so
``PortfolioState.positions`` is a complete, realistic ``Position`` per
CONTRACT-001's own shape, not because anything in this run depends on it.

**8. A known, accepted MVP limitation on the cash floor.** Risk's
``cash_floor_post_trade``/``max_order_risk_pct_of_nav`` checks evaluate a
``PlannedOrder`` using day-``D``'s close as ``estimated_price`` (the best
price known at decision time), but the fill actually executes at day
``D+1``'s open plus slippage -- which can differ slightly. A buy that passes
the cash-floor check by a thin margin on day ``D`` could therefore, in a
sufficiently adverse one-day price move, leave cash *very slightly* below
the configured floor after the day ``D+1`` fill. This mirrors a real broker's
identical risk (an intraday price move between order placement and fill) and
is not specific to this simulation; the conservative default thresholds
(0.5% per-order risk, 20% cash floor) make the realistic overshoot
negligible. Not fixed here -- flagged as an open item for BT-003a/BT-006 if
it ever proves material.

**9. ``BacktestResult.benchmark_price_series`` (BT-006 addition).** BT-006
needed the benchmark asset's own price series (to compute a buy-and-hold
``benchmark_return_pct``) but this engine did not previously expose the
benchmark ``AlignedSeries`` it already loads internally in
``_load_asset_series``/uses in ``_compute_all_indicators``. Per
``application.backtest.models``'s explicit "not a frozen cross-task
contract" note, this is a narrow additive field
(``BacktestResult.benchmark_price_series``), populated at the end of
``_run_day_loop`` from data already in memory -- no new network call, no
change to the day loop's own logic or existing fields.
"""

from __future__ import annotations

import uuid
from collections.abc import Mapping, Sequence
from datetime import date, timedelta
from decimal import Decimal
from typing import Literal, cast

from adapters.market_data.asset_instrument_resolver import (
    StaticAssetInstrumentResolver,
    resolve_fx_instrument,
)
from adapters.market_data.errors import PrecisionUnavailableError
from adapters.market_data.models import InstrumentPrecision, InstrumentRef
from adapters.market_data.precision_cache import PrecisionCache
from backtest.indicators import IndicatorBar, compute_indicators
from backtest.instrument_ids import INSTRUMENT_ID_BY_CODE
from backtest.loader import HistoricalDataLoader
from backtest.matching import apply_slippage, compute_commission
from backtest.models import AlignedSeries, AssetClass, PriceStatus
from backtest.precision import round_quantity_to_lot
from backtest.rebalance import PlannedOrder, diff_target_weights_to_orders
from domain.assets.models import Asset
from domain.execution.models import ExecutionService, Order
from domain.portfolios.models import Portfolio, PortfolioMember
from domain.risk.models import OrderIntent, RiskPolicy
from domain.strategies.models import AnalysisResult, AssetAnalysis, Strategy
from domain.valuation.models import CashBalance, PortfolioState, Position, ValuationSnapshot

from .config import BacktestConfig
from .errors import InvalidBacktestConfigError, UnresolvedInstrumentError, UnsupportedFxPairError
from .execution import BacktestExecutionService
from .models import BacktestResult, TradeRecord, TradeStatus

_ZERO = Decimal("0")
_ONE = Decimal("1")
_ORDER_ID_NAMESPACE = uuid.UUID("019fdf90-0000-7000-8000-0000000b7003")
"""Fixed namespace UUID for ``uuid.uuid5`` order-id derivation -- an arbitrary
but fixed constant (not sourced from any external system), chosen once so
every ``BacktestEngine`` run derives ids from the same namespace. Never used
for anything other than seeding a deterministic hash."""


def _pick_daily_ref(refs: Sequence[InstrumentRef], asset_id: str) -> InstrumentRef:
    for ref in refs:
        if ref.interval == "1d":
            return ref
    raise UnresolvedInstrumentError(
        f"no market-info-service instrument resolved for asset_id={asset_id!r} at interval='1d'"
    )


def _to_asset_class(asset_type: str, *, asset_id: str) -> AssetClass:
    if asset_type not in ("STOCK", "ETF", "CRYPTO"):
        raise InvalidBacktestConfigError(
            f"asset_id={asset_id!r} has asset_type={asset_type!r}, which is not a tradable "
            "AssetClass (STOCK/ETF/CRYPTO) -- cash must never be a PortfolioMember"
        )
    return cast(AssetClass, asset_type)


class BacktestEngine:
    """Runs one event-driven backtest for a single portfolio over a fixed date range.

    All non-deterministic capabilities (historical data loading, precision
    lookups) are injected (python-backend-standards.md §1) so this class can
    be driven entirely by fakes in tests -- no real network call is ever made
    by this class itself.
    """

    def __init__(
        self,
        *,
        config: BacktestConfig,
        portfolio: Portfolio,
        members: tuple[PortfolioMember, ...],
        assets: Mapping[str, Asset],
        strategy: Strategy,
        risk_policy: RiskPolicy,
        historical_data_loader: HistoricalDataLoader,
        precision_cache: PrecisionCache | None = None,
        execution_service: ExecutionService | None = None,
        resolver: StaticAssetInstrumentResolver | None = None,
    ) -> None:
        if config.portfolio_id != portfolio.portfolio_id:
            raise InvalidBacktestConfigError(
                f"config.portfolio_id={config.portfolio_id} != "
                f"portfolio.portfolio_id={portfolio.portfolio_id}"
            )
        if config.base_currency != portfolio.base_currency:
            raise InvalidBacktestConfigError(
                f"config.base_currency={config.base_currency!r} != "
                f"portfolio.base_currency={portfolio.base_currency!r}"
            )
        for member in members:
            if member.asset_id not in assets:
                raise UnresolvedInstrumentError(
                    f"portfolio member {member.asset_id!r} has no entry in the `assets` mapping "
                    "(needed for asset_type/quote_currency/trading_status)"
                )
        if config.precision_mode == "restricted" and precision_cache is None:
            raise InvalidBacktestConfigError(
                "precision_cache is required when config.precision_mode == 'restricted'"
            )

        self._config = config
        self._portfolio = portfolio
        self._members = members
        self._assets = dict(assets)
        self._strategy = strategy
        self._risk_policy = risk_policy
        self._loader = historical_data_loader
        self._precision_cache = precision_cache
        self._execution_service = execution_service or BacktestExecutionService()
        self._resolver = resolver or StaticAssetInstrumentResolver()

    async def run(self) -> BacktestResult:
        """Run the full event-driven backtest and return its result.

        Raises ``UnresolvedInstrumentError``/``UnsupportedFxPairError`` if
        the tradable universe cannot be resolved to market-info-service
        instruments/FX conversion paths at setup; propagates any
        ``backtest.errors.BacktestDataError`` the underlying
        ``HistoricalDataLoader`` raises (e.g. a genuine data gap). None of
        these are caught and downgraded to a partial result -- a backtest
        that cannot load its full input data does not produce output at all
        (fail-closed, matching ``application.valuation.service``'s same
        principle).
        """
        instrument_refs = self._resolve_instrument_refs()
        asset_series = await self._load_asset_series(instrument_refs)
        asset_indicators = self._compute_all_indicators(asset_series)
        fx_series_by_currency = await self._load_fx_series(asset_series)
        precision_by_asset = await self._load_precision(instrument_refs)

        return self._run_day_loop(
            asset_series=asset_series,
            asset_indicators=asset_indicators,
            fx_series_by_currency=fx_series_by_currency,
            precision_by_asset=precision_by_asset,
        )

    # ------------------------------------------------------------------
    # Setup (once per run, not per day)
    # ------------------------------------------------------------------

    def _resolve_instrument_refs(self) -> dict[str, InstrumentRef]:
        return {
            member.asset_id: _pick_daily_ref(
                self._resolver.resolve(member.asset_id), member.asset_id
            )
            for member in self._members
        }

    async def _load_asset_series(
        self, instrument_refs: Mapping[str, InstrumentRef]
    ) -> dict[str, AlignedSeries]:
        series: dict[str, AlignedSeries] = {}
        for member in self._members:
            asset = self._assets[member.asset_id]
            ref = instrument_refs[member.asset_id]
            series[member.asset_id] = await self._loader.load(
                instrument_code=ref.instrument_code,
                provider=ref.provider,
                asset_type=_to_asset_class(asset.asset_type, asset_id=member.asset_id),
                start_date=self._config.start_date,
                end_date=self._config.end_date,
            )

        benchmark_asset_id = self._portfolio.benchmark_asset_id
        if benchmark_asset_id is not None and benchmark_asset_id not in series:
            # Benchmark is not itself a tradable member -- load it read-only,
            # purely to feed compute_indicators' relative-strength input.
            benchmark_asset = self._assets.get(benchmark_asset_id)
            if benchmark_asset is None:
                raise UnresolvedInstrumentError(
                    f"portfolio.benchmark_asset_id={benchmark_asset_id!r} has no entry in the "
                    "`assets` mapping"
                )
            ref = _pick_daily_ref(self._resolver.resolve(benchmark_asset_id), benchmark_asset_id)
            series[benchmark_asset_id] = await self._loader.load(
                instrument_code=ref.instrument_code,
                provider=ref.provider,
                asset_type=_to_asset_class(benchmark_asset.asset_type, asset_id=benchmark_asset_id),
                start_date=self._config.start_date,
                end_date=self._config.end_date,
            )
        return series

    def _compute_all_indicators(
        self, asset_series: Mapping[str, AlignedSeries]
    ) -> dict[str, tuple[IndicatorBar, ...]]:
        benchmark_asset_id = self._portfolio.benchmark_asset_id
        benchmark_series = (
            asset_series[benchmark_asset_id] if benchmark_asset_id is not None else None
        )
        indicators: dict[str, tuple[IndicatorBar, ...]] = {}
        for member in self._members:
            series = asset_series[member.asset_id]
            # See module docstring, decision 4: computed on the native
            # (unconverted) series -- never on FX-converted prices.
            bench = benchmark_series if benchmark_series is not None else series
            indicators[member.asset_id] = compute_indicators(series, bench)
        return indicators

    async def _load_fx_series(
        self, asset_series: Mapping[str, AlignedSeries]
    ) -> dict[str, AlignedSeries]:
        fx_series_by_currency: dict[str, AlignedSeries] = {}
        for member in self._members:
            asset = self._assets[member.asset_id]
            currency = asset.quote_currency
            if currency == self._config.base_currency or currency in fx_series_by_currency:
                continue
            fx_ref = resolve_fx_instrument(currency, self._config.base_currency)
            if fx_ref is None:
                raise UnsupportedFxPairError(
                    f"asset_id={member.asset_id!r} quotes in {currency!r}, but no FX conversion "
                    f"path to base_currency={self._config.base_currency!r} is known "
                    "(refusing to assume a 1:1 rate)"
                )
            # See module docstring, decision 3: reuses "CRYPTO" purely to
            # select 24/7-fresh calendar alignment behaviour, not because
            # this series is a crypto asset.
            fx_series_by_currency[currency] = await self._loader.load(
                instrument_code=fx_ref.instrument_code,
                provider=fx_ref.provider,
                asset_type="CRYPTO",
                start_date=self._config.start_date,
                end_date=self._config.end_date,
            )
        return fx_series_by_currency

    async def _load_precision(
        self, instrument_refs: Mapping[str, InstrumentRef]
    ) -> dict[str, InstrumentPrecision | None]:
        precision_by_asset: dict[str, InstrumentPrecision | None] = {}
        if self._config.precision_mode != "restricted":
            return precision_by_asset
        assert self._precision_cache is not None  # enforced in __init__

        for member in self._members:
            ref = instrument_refs[member.asset_id]
            instrument_id = INSTRUMENT_ID_BY_CODE.get(ref.instrument_code)
            if instrument_id is None:
                precision_by_asset[member.asset_id] = None
                continue
            try:
                result = await self._precision_cache.get_many([instrument_id])
                precision_by_asset[member.asset_id] = result[instrument_id]
            except PrecisionUnavailableError:
                # Fail-closed per asset, not per run -- see module docstring,
                # decision 5.
                precision_by_asset[member.asset_id] = None
        return precision_by_asset

    # ------------------------------------------------------------------
    # Day loop
    # ------------------------------------------------------------------

    def _run_day_loop(
        self,
        *,
        asset_series: Mapping[str, AlignedSeries],
        asset_indicators: Mapping[str, tuple[IndicatorBar, ...]],
        fx_series_by_currency: Mapping[str, AlignedSeries],
        precision_by_asset: Mapping[str, InstrumentPrecision | None],
    ) -> BacktestResult:
        total_days = (self._config.end_date - self._config.start_date).days + 1

        positions: dict[str, Decimal] = {m.asset_id: _ZERO for m in self._members}
        average_cost: dict[str, Decimal] = {m.asset_id: _ZERO for m in self._members}
        cash = self._config.initial_cash

        equity_curve: list[ValuationSnapshot] = []
        trades: list[TradeRecord] = []
        pending_orders: list[PlannedOrder] = []
        pending_context: dict[str, tuple[Decimal | None, tuple[str, ...], date]] = {}
        trade_seq = 0

        tradable_asset_ids: dict[str, AssetClass] = {
            m.asset_id: _to_asset_class(self._assets[m.asset_id].asset_type, asset_id=m.asset_id)
            for m in self._members
        }

        for day_index in range(total_days):
            day = self._config.start_date + timedelta(days=day_index)
            is_last_day = day_index == total_days - 1

            # Step (a): apply orders decided on the prior day, at today's open.
            if pending_orders:
                for planned in pending_orders:
                    trade_seq += 1
                    open_price = self._price_at(
                        asset_series, fx_series_by_currency, planned.asset_id, day_index, "open"
                    )
                    record, position_delta, cash_delta = self._simulate_fill(
                        planned=planned,
                        execution_price=open_price,
                        trade_date=day,
                        precision=precision_by_asset.get(planned.asset_id),
                        trade_seq=trade_seq,
                        decision_context=pending_context[planned.asset_id],
                    )
                    trades.append(record)
                    if record.status == "filled":
                        cash += cash_delta
                        old_qty = positions[planned.asset_id]
                        new_qty = old_qty + position_delta
                        if planned.side == "buy" and record.fill_price is not None:
                            average_cost[planned.asset_id] = (
                                (old_qty * average_cost[planned.asset_id])
                                + (position_delta * record.fill_price)
                            ) / new_qty
                        if new_qty == _ZERO:
                            average_cost[planned.asset_id] = _ZERO
                        positions[planned.asset_id] = new_qty
                pending_orders = []
                pending_context = {}

            # Step (b): value the portfolio at today's close.
            prices_base: dict[str, Decimal] = {}
            is_stale: dict[str, bool] = {}
            statuses: list[PriceStatus] = []
            positions_value = _ZERO
            for member in self._members:
                asset_id = member.asset_id
                bar = asset_series[asset_id].bars[day_index]
                price_base, fx_status = self._price_and_status_at(
                    asset_series, fx_series_by_currency, asset_id, day_index, "close"
                )
                prices_base[asset_id] = price_base
                is_stale[asset_id] = bar.price_status == "stale"
                if positions[asset_id] != _ZERO:
                    positions_value += positions[asset_id] * price_base
                    statuses.append(bar.price_status)
                    if fx_status is not None:
                        statuses.append(fx_status)

            nav = positions_value + cash
            price_status: PriceStatus = "fresh" if all(s == "fresh" for s in statuses) else "stale"
            snapshot = ValuationSnapshot(
                portfolio_id=self._portfolio.portfolio_id,
                valuation_date=day,
                positions_value=positions_value,
                cash_value=cash,
                net_asset_value=nav,
                base_currency=self._config.base_currency,
                price_status=price_status,
            )
            equity_curve.append(snapshot)

            # Step (c): assemble AnalysisResult and run the strategy.
            assets_analysis: dict[str, AssetAnalysis] = {}
            for member in self._members:
                asset_id = member.asset_id
                bar = asset_series[asset_id].bars[day_index]
                assets_analysis[asset_id] = AssetAnalysis(
                    indicators=asset_indicators[asset_id][day_index],
                    close=prices_base[asset_id],
                    price_status=bar.price_status,
                    trading_status=self._assets[asset_id].trading_status,
                )
            analysis = AnalysisResult(
                portfolio_id=self._portfolio.portfolio_id, as_of=day, assets=assets_analysis
            )

            positions_tuple = tuple(
                Position(
                    portfolio_id=self._portfolio.portfolio_id,
                    asset_id=asset_id,
                    quantity=quantity,
                    average_cost=average_cost[asset_id],
                )
                for asset_id, quantity in positions.items()
            )
            cash_tuple = (
                CashBalance(
                    portfolio_id=self._portfolio.portfolio_id,
                    currency=self._config.base_currency,
                    amount=cash,
                ),
            )
            portfolio_state = PortfolioState(
                portfolio=self._portfolio,
                members=self._members,
                positions=positions_tuple,
                cash=cash_tuple,
                latest_snapshot=snapshot,
            )

            strategy_output = self._strategy.generate_targets(analysis, portfolio_state)
            scores_by_asset = {s.asset_id: s for s in strategy_output.asset_scores}

            # Step (d): diff into orders and risk-check each one.
            planned_orders = diff_target_weights_to_orders(
                target_weights=strategy_output.target_weights,
                current_quantities=positions,
                prices=prices_base,
                nav=nav,
                tradable_asset_ids=tradable_asset_ids,
                is_stale=is_stale,
                rebalance_threshold_pct=self._config.rebalance_threshold_pct,
            )

            new_pending: list[PlannedOrder] = []
            new_pending_context: dict[str, tuple[Decimal | None, tuple[str, ...], date]] = {}
            for planned in planned_orders:
                trade_seq += 1
                score = scores_by_asset.get(planned.asset_id)
                signal_score = score.score if score is not None else None
                reasons = score.reasons if score is not None else ()

                intent = OrderIntent(
                    portfolio_id=self._portfolio.portfolio_id,
                    asset_id=planned.asset_id,
                    side=planned.side,
                    quantity=planned.quantity,
                    estimated_price=planned.estimated_price,
                )
                risk_result = self._risk_policy.check_order(intent, portfolio_state)
                order = Order(
                    order_id=self._deterministic_id(day, planned.asset_id, trade_seq),
                    portfolio_id=self._portfolio.portfolio_id,
                    asset_id=planned.asset_id,
                    side=planned.side,
                    quantity=planned.quantity,
                    order_type="market",
                    limit_price=None,
                    status="pending_risk",
                )
                order = self._execution_service.submit(order, risk_result)
                symbol = self._assets[planned.asset_id].symbol

                if order.status == "rejected":
                    trades.append(
                        TradeRecord(
                            trade_id=f"bt_{day:%Y%m%d}_{symbol}_{trade_seq:03d}",
                            portfolio_id=self._portfolio.portfolio_id,
                            asset_id=planned.asset_id,
                            symbol=symbol,
                            side=planned.side,
                            status="rejected",
                            planned_quantity=planned.quantity,
                            quantity=_ZERO,
                            fill_price=None,
                            commission=_ZERO,
                            slippage=_ZERO,
                            signal_score=signal_score,
                            reason=reasons + risk_result.rejection_reasons,
                            decision_date=day,
                            trade_date=None,
                        )
                    )
                    continue

                if is_last_day:
                    trades.append(
                        TradeRecord(
                            trade_id=f"bt_{day:%Y%m%d}_{symbol}_{trade_seq:03d}",
                            portfolio_id=self._portfolio.portfolio_id,
                            asset_id=planned.asset_id,
                            symbol=symbol,
                            side=planned.side,
                            status="skipped_no_next_day",
                            planned_quantity=planned.quantity,
                            quantity=_ZERO,
                            fill_price=None,
                            commission=_ZERO,
                            slippage=_ZERO,
                            signal_score=signal_score,
                            reason=reasons
                            + (
                                "approved but decided on the last day in the configured range; "
                                "no next day's open exists to execute at",
                            ),
                            decision_date=day,
                            trade_date=None,
                        )
                    )
                    continue

                new_pending.append(planned)
                new_pending_context[planned.asset_id] = (signal_score, reasons, day)

            pending_orders = new_pending
            pending_context = new_pending_context

        return BacktestResult(
            portfolio_id=self._portfolio.portfolio_id,
            config=self._config,
            equity_curve=tuple(equity_curve),
            trades=tuple(trades),
            replaced_risk_checks=self._risk_policy.replaced_checks(),
            final_positions=positions,
            final_cash=cash,
            benchmark_price_series=self._compute_benchmark_price_series(asset_series),
        )

    def _compute_benchmark_price_series(
        self, asset_series: Mapping[str, AlignedSeries]
    ) -> tuple[tuple[date, Decimal], ...] | None:
        """Project the already-loaded benchmark series down to ``(day, close)`` pairs.

        BT-006 addition -- see ``application.backtest.models.BacktestResult
        .benchmark_price_series``'s docstring for the full rationale
        (no extra network call, native quote currency, ``None`` iff no
        benchmark is configured). ``asset_series`` always contains the
        benchmark's series whenever ``benchmark_asset_id`` is set --
        ``_load_asset_series`` loads it unconditionally (either as a
        tradable member or read-only) -- the ``.get`` is defensive, not an
        expected path.
        """
        benchmark_asset_id = self._portfolio.benchmark_asset_id
        if benchmark_asset_id is None:
            return None
        series = asset_series.get(benchmark_asset_id)
        if series is None:
            return None
        return tuple((bar.trading_day, bar.close) for bar in series.bars)

    # ------------------------------------------------------------------
    # Fill simulation
    # ------------------------------------------------------------------

    def _simulate_fill(
        self,
        *,
        planned: PlannedOrder,
        execution_price: Decimal,
        trade_date: date,
        precision: InstrumentPrecision | None,
        trade_seq: int,
        decision_context: tuple[Decimal | None, tuple[str, ...], date],
    ) -> tuple[TradeRecord, Decimal, Decimal]:
        """Simulate one fill; returns ``(record, position_delta, cash_delta)``.

        ``position_delta``/``cash_delta`` are both ``Decimal("0")`` whenever
        ``record.status != "filled"``.
        """
        signal_score, reasons, decision_date = decision_context
        symbol = self._assets[planned.asset_id].symbol
        quantity = planned.quantity

        def _skip(status: TradeStatus, extra_reason: str) -> tuple[TradeRecord, Decimal, Decimal]:
            record = TradeRecord(
                trade_id=f"bt_{trade_date:%Y%m%d}_{symbol}_{trade_seq:03d}",
                portfolio_id=self._portfolio.portfolio_id,
                asset_id=planned.asset_id,
                symbol=symbol,
                side=planned.side,
                status=status,
                planned_quantity=planned.quantity,
                quantity=_ZERO,
                fill_price=None,
                commission=_ZERO,
                slippage=_ZERO,
                signal_score=signal_score,
                reason=reasons + (extra_reason,),
                decision_date=decision_date,
                trade_date=trade_date,
            )
            return record, _ZERO, _ZERO

        if self._config.precision_mode == "restricted":
            if precision is None:
                return _skip(
                    "skipped_precision_unavailable",
                    "precision_mode='restricted' but no InstrumentPrecision is available for "
                    "this asset; fail-closed, refusing to fill without real lot_size/"
                    "min_quantity data (DEC-003)",
                )
            rounded = round_quantity_to_lot(
                quantity, lot_size=precision.lot_size, min_quantity=precision.min_quantity
            )
            if not rounded.fillable:
                return _skip("skipped_min_quantity", rounded.reason or "rounds below min_quantity")
            quantity = rounded.quantity

        fill_price = apply_slippage(
            execution_price, side=planned.side, slippage_pct=self._config.slippage_pct
        )
        notional = quantity * fill_price
        commission = compute_commission(
            notional=notional,
            commission_pct=self._config.commission_pct,
            min_commission=self._config.min_commission,
        )
        slippage_amount = abs(fill_price - execution_price) * quantity

        if planned.side == "buy":
            cash_delta = -(notional + commission)
            position_delta = quantity
        else:
            cash_delta = notional - commission
            position_delta = -quantity

        record = TradeRecord(
            trade_id=f"bt_{trade_date:%Y%m%d}_{symbol}_{trade_seq:03d}",
            portfolio_id=self._portfolio.portfolio_id,
            asset_id=planned.asset_id,
            symbol=symbol,
            side=planned.side,
            status="filled",
            planned_quantity=planned.quantity,
            quantity=quantity,
            fill_price=fill_price,
            commission=commission,
            slippage=slippage_amount,
            signal_score=signal_score,
            reason=reasons,
            decision_date=decision_date,
            trade_date=trade_date,
        )
        return record, position_delta, cash_delta

    # ------------------------------------------------------------------
    # FX / pricing helpers
    # ------------------------------------------------------------------

    def _price_at(
        self,
        asset_series: Mapping[str, AlignedSeries],
        fx_series_by_currency: Mapping[str, AlignedSeries],
        asset_id: str,
        day_index: int,
        field: Literal["open", "close"],
    ) -> Decimal:
        price, _ = self._price_and_status_at(
            asset_series, fx_series_by_currency, asset_id, day_index, field
        )
        return price

    def _price_and_status_at(
        self,
        asset_series: Mapping[str, AlignedSeries],
        fx_series_by_currency: Mapping[str, AlignedSeries],
        asset_id: str,
        day_index: int,
        field: Literal["open", "close"],
    ) -> tuple[Decimal, PriceStatus | None]:
        bar = asset_series[asset_id].bars[day_index]
        native_price = bar.open if field == "open" else bar.close
        currency = self._assets[asset_id].quote_currency
        if currency == self._config.base_currency:
            return native_price, None

        fx_bar = fx_series_by_currency[currency].bars[day_index]
        fx_rate = fx_bar.open if field == "open" else fx_bar.close
        return native_price * fx_rate, fx_bar.price_status

    def _deterministic_id(self, day: date, asset_id: str, seq: int) -> uuid.UUID:
        # See module docstring, decision 6: deterministic, not uuid4().
        name = f"{self._portfolio.portfolio_id}:{day.isoformat()}:{asset_id}:{seq}"
        return uuid.uuid5(_ORDER_ID_NAMESPACE, name)


__all__ = ["BacktestEngine"]
