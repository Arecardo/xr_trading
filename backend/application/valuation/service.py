"""``SqlValuationService``: the BE-003 ``ValuationService`` implementation.

Orchestrates ``domain``/``repository``/``adapters`` (python-backend-standards.md
§2: "application layer orchestrates domain + repository + adapters; domain
stays infra-free") to implement the frozen ``domain.valuation.models.
ValuationService`` Protocol's two methods:

- ``generate_snapshot``: load a portfolio's positions/cash, price each one
  as of a given UTC calendar day (carrying the prior close forward and
  marking ``price_status="stale"`` on non-trading days, per DEC-002),
  convert non-base-currency values via the DEC-006 FX Instrument, sum into
  a ``ValuationSnapshot``, and persist it (idempotent upsert, see
  ``repository.valuation``'s module docstring).
- ``current_state``: assemble the read-only ``PortfolioState`` projection
  from the same repositories plus the latest persisted snapshot.

Sync/async bridging decision (BE-003)
--------------------------------------
CONTRACT-001 freezes ``ValuationService`` as *synchronous*::

    class ValuationService(Protocol):
        def generate_snapshot(self, portfolio_id: UUID, as_of: date) -> ValuationSnapshot: ...
        def current_state(self, portfolio_id: UUID) -> PortfolioState: ...

This task does not have license to change that signature (mirroring BE-002's
identical constraint on the three CONTRACT-001 repository Protocols -- see
``repository/database.py``'s module docstring). But pricing a position or an
FX rate means calling ``adapters.market_data.client.MarketInfoClient``,
which is ``httpx.AsyncClient``-based (BE-004). Two options were considered:

1. Add a "pre-fetched price/FX map" parameter and keep all async fetching
   one layer up in ``application``, outside this class. Rejected: the
   frozen signature is exactly ``(portfolio_id, as_of) -> ValuationSnapshot``
   -- there is no parameter slot for a caller-supplied price map without
   changing CONTRACT-001, which this task may not do.
2. Bridge sync -> async internally, at the point ``generate_snapshot``/
   ``current_state`` need a price. **Chosen.** Each public method gathers
   its own async work (position pricing, FX conversion) into a single
   coroutine and drives it with ``asyncio.run()`` via the ``_run_sync``
   helper below, exactly once per call.

The risk this creates -- and BE-002's own decision doc calls out the same
shape of risk for FastAPI's sync-endpoint-in-a-threadpool model -- is that
``asyncio.run()`` raises if the calling thread already has a running event
loop (e.g. this method is called directly from inside ``async def``
application code without ``asyncio.to_thread``). Rather than let that
surface as ``asyncio``'s own generic, hard-to-diagnose ``RuntimeError``,
``_run_sync`` checks for a running loop first and raises
``application.valuation.errors.AsyncBridgingError`` with an explicit
explanation and the correct fix. This turns a possible silent-deadlock-or-
cryptic-error footgun into a fail-fast, self-documenting error instead --
it does not eliminate the constraint (this class's methods still must not
be called from async code without ``asyncio.to_thread``), but it makes a
violation obvious immediately rather than intermittently.

The expected caller is a FastAPI path operation declared as plain ``def``
(not ``async def``): FastAPI dispatches those to its own worker threadpool
(BE-002's decision doc: "FastAPI 对同步端点自动走线程池"), which has no
event loop running in that thread, so ``asyncio.run()`` there is safe. An
async caller that must call this from within already-async code should use
``await asyncio.to_thread(service.generate_snapshot, portfolio_id, as_of)``.

Positions/cash items within one ``generate_snapshot`` call are priced
sequentially (not gathered concurrently) -- deliberate simplicity for the
current 2-3-asset universe (DEC-001), where a handful of sequential HTTP
round trips is a negligible latency cost. Concurrent fetching
(``asyncio.gather``) is a reasonable future optimization if the universe
grows, not a correctness fix needed today.

Failure paths propagate directly rather than being caught and re-wrapped
here: ``repository.errors.NotFoundError`` (unknown portfolio, unknown
asset, or -- for ``current_state`` -- no snapshot ever generated for this
portfolio) and ``adapters.market_data.errors.MarketDataResponseError``/
``MarketDataUnavailableError`` (market-info-service reachable-but-erroring,
or unreachable) all bubble up unmodified. Every one of these already means
"no valuation snapshot was computed or persisted" (this module never
catches an error partway through pricing and persists a partial result),
which is the fail-closed behavior CONTRACT-001's own docstring requires.
"""

from __future__ import annotations

import asyncio
from collections.abc import Coroutine, Sequence
from datetime import UTC, date, datetime, time, timedelta
from decimal import Decimal
from typing import Any, Literal, TypeVar
from uuid import UUID

from adapters.market_data.asset_instrument_resolver import resolve_fx_instrument
from adapters.market_data.client import MarketInfoClient
from adapters.market_data.models import InstrumentRef
from adapters.market_data.subscriptions import AssetInstrumentResolver
from domain.assets.models import AssetRepository
from domain.portfolios.models import PortfolioRepository
from domain.valuation.models import (
    CashBalance,
    CashBalanceRepository,
    PortfolioState,
    Position,
    PositionRepository,
    ValuationSnapshot,
    ValuationSnapshotRepository,
)

from .errors import (
    AsyncBridgingError,
    MissingPriceDataError,
    UnresolvedInstrumentError,
    UnsupportedFxPairError,
)

_T = TypeVar("_T")
_ZERO = Decimal("0")
_DAILY_INTERVAL = "1d"
_PriceStatus = Literal["fresh", "stale"]


def _run_sync(coro: Coroutine[Any, Any, _T]) -> _T:
    """Drive ``coro`` to completion from this (synchronous) call site.

    See the module docstring's "Sync/async bridging decision" section for
    why this exists instead of a bare ``asyncio.run(coro)`` call.
    """
    try:
        asyncio.get_running_loop()
    except RuntimeError:
        return asyncio.run(coro)
    # A running loop means we must not await coro ourselves (that would be
    # the actual deadlock this function exists to avoid) -- but the caller
    # already constructed it as our argument, so it must be closed
    # explicitly here or Python emits a "coroutine was never awaited"
    # RuntimeWarning (and leaks whatever resources it held) once it is
    # garbage-collected.
    coro.close()
    raise AsyncBridgingError(
        "SqlValuationService methods are synchronous (CONTRACT-001) but drive an async "
        "MarketInfoClient internally; this thread already has a running asyncio event loop. "
        "Call from a plain sync context (e.g. a `def` FastAPI path operation, which FastAPI "
        "dispatches to its own threadpool), or from async code via "
        "`await asyncio.to_thread(service.generate_snapshot, portfolio_id, as_of)`."
    )


def _pick_daily_ref(refs: Sequence[InstrumentRef], asset_id: str) -> InstrumentRef:
    for ref in refs:
        if ref.interval == _DAILY_INTERVAL:
            return ref
    raise UnresolvedInstrumentError(
        f"no market-info-service instrument resolved for asset_id={asset_id!r} at "
        f"interval={_DAILY_INTERVAL!r} (resolver returned {list(refs)!r})"
    )


async def _fetch_price(
    client: MarketInfoClient, ref: InstrumentRef, as_of: date
) -> tuple[Decimal, _PriceStatus]:
    """Fetch the closing price on or before ``as_of`` for ``ref``, and whether it is fresh.

    Implements DEC-002's day-cutover/carry-forward rule with a single
    `/bars` call: request the most recent (``order="desc"``, ``limit=1``)
    bar with ``open_time`` strictly before midnight UTC the day *after*
    ``as_of`` -- i.e. the newest bar dated on or before ``as_of``. If its
    date equals ``as_of`` the price is ``"fresh"``; if it is an earlier
    date (a non-trading day for that instrument), the price is the prior
    close carried forward and marked ``"stale"``. This works for both a
    genuine market holiday and a raw data gap identically (both surface as
    "the most recent prior bar"), which matches DEC-002's own carry-forward
    rule -- it does not require this backend to know a trading calendar the
    way ``backend/backtest/calendar.py`` does for BT-001's *gapless daily
    series* use case; a single point-in-time lookup has no such need.

    Raises ``MissingPriceDataError`` if no bar exists at all up to and
    including ``as_of`` (fail-closed: no prior close to carry forward).
    """
    end_time = datetime.combine(as_of + timedelta(days=1), time.min, tzinfo=UTC)
    bars = await client.get_bars(
        instrument_code=ref.instrument_code,
        provider=ref.provider,
        interval=ref.interval,
        end_time=end_time,
        order="desc",
        limit=1,
    )
    if not bars:
        raise MissingPriceDataError(
            f"no bar found for instrument_code={ref.instrument_code!r} "
            f"provider={ref.provider!r} interval={ref.interval!r} on or before {as_of}"
        )
    bar = bars[0]
    bar_date = bar.open_time.astimezone(UTC).date()
    status: _PriceStatus = "fresh" if bar_date == as_of else "stale"
    return bar.close, status


def _combine_status(a: _PriceStatus, b: _PriceStatus) -> _PriceStatus:
    """Conservative combination: ``"stale"`` if either input is ``"stale"``."""
    return "fresh" if a == "fresh" and b == "fresh" else "stale"


class SqlValuationService:
    """``ValuationService`` implementation built on the BE-002/BE-003 repositories.

    See the module docstring for the sync/async bridging decision this
    class's two Protocol methods implement.
    """

    def __init__(
        self,
        *,
        portfolio_repository: PortfolioRepository,
        asset_repository: AssetRepository,
        position_repository: PositionRepository,
        cash_balance_repository: CashBalanceRepository,
        valuation_snapshot_repository: ValuationSnapshotRepository,
        market_info_client: MarketInfoClient,
        resolver: AssetInstrumentResolver,
    ) -> None:
        self._portfolio_repository = portfolio_repository
        self._asset_repository = asset_repository
        self._position_repository = position_repository
        self._cash_balance_repository = cash_balance_repository
        self._valuation_snapshot_repository = valuation_snapshot_repository
        self._market_info_client = market_info_client
        self._resolver = resolver

    def generate_snapshot(self, portfolio_id: UUID, as_of: date) -> ValuationSnapshot:
        """Generate (or idempotently recompute) the snapshot for ``as_of`` and persist it.

        Raises (never persists a partial/spuriously precise result):
        ``repository.errors.NotFoundError`` if ``portfolio_id`` does not
        exist; ``application.valuation.errors.UnresolvedInstrumentError`` if
        a held asset (or the FX conversion it needs) has no known
        market-info-service instrument;
        ``application.valuation.errors.MissingPriceDataError`` if no price
        exists at all up to ``as_of`` for a required instrument;
        ``application.valuation.errors.UnsupportedFxPairError`` if a
        position's currency differs from the portfolio's base currency and
        no FX conversion path is known; or a market-info-service transport/
        response error (``adapters.market_data.errors``).
        """
        portfolio = self._portfolio_repository.get(portfolio_id)
        positions = self._position_repository.list_by_portfolio(portfolio_id)
        cash_balances = self._cash_balance_repository.list_by_portfolio(portfolio_id)

        snapshot = _run_sync(
            self._compute_snapshot(
                portfolio_id=portfolio_id,
                base_currency=portfolio.base_currency,
                positions=positions,
                cash_balances=cash_balances,
                as_of=as_of,
            )
        )
        self._valuation_snapshot_repository.upsert(snapshot)
        return snapshot

    def current_state(self, portfolio_id: UUID) -> PortfolioState:
        """Assemble the current read-only state projection for ``portfolio_id``.

        Raises ``repository.errors.NotFoundError`` if ``portfolio_id`` does
        not exist, or if no valuation snapshot has ever been generated for
        it (``PortfolioState.latest_snapshot`` is non-optional per
        CONTRACT-001, so there is no snapshot-less state to return -- call
        ``generate_snapshot`` at least once first).
        """
        portfolio = self._portfolio_repository.get(portfolio_id)
        members = tuple(self._portfolio_repository.list_members(portfolio_id))
        positions = tuple(self._position_repository.list_by_portfolio(portfolio_id))
        cash = tuple(self._cash_balance_repository.list_by_portfolio(portfolio_id))
        latest_snapshot = self._valuation_snapshot_repository.get_latest(portfolio_id)
        return PortfolioState(
            portfolio=portfolio,
            members=members,
            positions=positions,
            cash=cash,
            latest_snapshot=latest_snapshot,
        )

    async def _compute_snapshot(
        self,
        *,
        portfolio_id: UUID,
        base_currency: str,
        positions: list[Position],
        cash_balances: list[CashBalance],
        as_of: date,
    ) -> ValuationSnapshot:
        statuses: list[_PriceStatus] = []
        positions_value = _ZERO
        for position in positions:
            if position.quantity == _ZERO:
                # No economic impact and nothing to price -- skip the
                # instrument/price lookup entirely rather than fail closed
                # on a technically-zero holding (e.g. a fully-exited
                # position whose row was never deleted).
                continue
            value, status = await self._value_position(position, base_currency, as_of)
            positions_value += value
            statuses.append(status)

        cash_value = _ZERO
        for cash in cash_balances:
            if cash.amount == _ZERO:
                continue
            value, status = await self._value_cash(cash, base_currency, as_of)
            cash_value += value
            statuses.append(status)

        # Portfolio-level price_status takes the most conservative value
        # across every priced position/cash line (CONTRACT-001's
        # ValuationSnapshot docstring: "组合级取最保守值"). A portfolio
        # with nothing to price (all-zero or empty) has nothing to be stale
        # about, so it defaults to "fresh".
        price_status: _PriceStatus = "fresh"
        for status in statuses:
            price_status = _combine_status(price_status, status)

        return ValuationSnapshot(
            portfolio_id=portfolio_id,
            valuation_date=as_of,
            positions_value=positions_value,
            cash_value=cash_value,
            net_asset_value=positions_value + cash_value,
            base_currency=base_currency,
            price_status=price_status,
        )

    async def _value_position(
        self, position: Position, base_currency: str, as_of: date
    ) -> tuple[Decimal, _PriceStatus]:
        # AssetRepository.get is a synchronous, blocking repository call
        # (CONTRACT-001), made here from inside a coroutine driven by
        # asyncio.run() -- acceptable for this single-user platform's tiny
        # per-portfolio asset counts (BE-002's precedent for mixing sync
        # repository calls into async-adjacent code).
        asset = self._asset_repository.get(position.asset_id)
        refs = self._resolver.resolve(position.asset_id)
        ref = _pick_daily_ref(refs, position.asset_id)
        price, status = await _fetch_price(self._market_info_client, ref, as_of)

        raw_value = position.quantity * price
        if asset.quote_currency == base_currency:
            return raw_value, status

        fx_value, fx_status = await self._convert(
            raw_value, asset.quote_currency, base_currency, as_of
        )
        return fx_value, _combine_status(status, fx_status)

    async def _value_cash(
        self, cash: CashBalance, base_currency: str, as_of: date
    ) -> tuple[Decimal, _PriceStatus]:
        if cash.currency == base_currency:
            return cash.amount, "fresh"
        return await self._convert(cash.amount, cash.currency, base_currency, as_of)

    async def _convert(
        self, amount: Decimal, from_currency: str, to_currency: str, as_of: date
    ) -> tuple[Decimal, _PriceStatus]:
        ref = resolve_fx_instrument(from_currency, to_currency)
        if ref is None:
            raise UnsupportedFxPairError(
                f"no known FX conversion from {from_currency!r} to {to_currency!r} "
                f"(portfolio base_currency); refusing to assume a 1:1 rate"
            )
        rate, status = await _fetch_price(self._market_info_client, ref, as_of)
        return amount * rate, status


__all__ = ["SqlValuationService"]
