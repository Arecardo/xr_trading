"""Unit tests for application.valuation.service.SqlValuationService (BE-003).

No real network/DB access (python-backend-standards.md §9): every dependency
is a hand-rolled fake matching the relevant Protocol's public method
signatures, per the task brief's explicit allowance ("httpx.MockTransport or
a fake matching MarketInfoClient's public method signatures"). Using a fake
(rather than httpx.MockTransport) here because these tests exercise
orchestration across several different asset/day/currency combinations,
which is far more legible as an in-memory bar store than as HTTP fixtures.
"""

from __future__ import annotations

import asyncio
from collections.abc import Sequence
from datetime import UTC, date, datetime, time, timedelta
from decimal import Decimal
from uuid import UUID, uuid4

import pytest

from adapters.market_data.models import Bar, InstrumentRef
from application.valuation.errors import (
    AsyncBridgingError,
    MissingPriceDataError,
    UnresolvedInstrumentError,
    UnsupportedFxPairError,
)
from application.valuation.service import SqlValuationService
from domain.assets.models import Asset
from domain.portfolios.models import Portfolio, PortfolioMember
from domain.valuation.models import CashBalance, Position, ValuationSnapshot
from repository.errors import NotFoundError

# --- fakes -------------------------------------------------------------


class FakePortfolioRepository:
    def __init__(self, portfolios: dict[UUID, Portfolio]) -> None:
        self._portfolios = portfolios
        self._members: dict[UUID, list[PortfolioMember]] = {}

    def get(self, portfolio_id: UUID) -> Portfolio:
        try:
            return self._portfolios[portfolio_id]
        except KeyError as exc:
            raise NotFoundError(f"portfolio not found: {portfolio_id}") from exc

    def list_members(self, portfolio_id: UUID, status: str | None = None) -> list[PortfolioMember]:
        members = self._members.get(portfolio_id, [])
        if status is None:
            return list(members)
        return [m for m in members if m.member_status == status]

    def set_members(self, portfolio_id: UUID, members: list[PortfolioMember]) -> None:
        self._members[portfolio_id] = members


class FakeAssetRepository:
    def __init__(self, assets: dict[str, Asset]) -> None:
        self._assets = assets

    def get(self, asset_id: str) -> Asset:
        try:
            return self._assets[asset_id]
        except KeyError as exc:
            raise NotFoundError(f"asset not found: {asset_id}") from exc

    def list_by_ids(self, asset_ids: Sequence[str]) -> list[Asset]:
        return [self._assets[a] for a in asset_ids if a in self._assets]


class FakePositionRepository:
    def __init__(self, positions: list[Position]) -> None:
        self._positions = positions

    def list_by_portfolio(self, portfolio_id: UUID) -> list[Position]:
        return [p for p in self._positions if p.portfolio_id == portfolio_id]

    def upsert(self, position: Position) -> None:
        raise NotImplementedError("not exercised by SqlValuationService")


class FakeCashBalanceRepository:
    def __init__(self, cash: list[CashBalance]) -> None:
        self._cash = cash

    def list_by_portfolio(self, portfolio_id: UUID) -> list[CashBalance]:
        return [c for c in self._cash if c.portfolio_id == portfolio_id]

    def upsert(self, cash_balance: CashBalance) -> None:
        raise NotImplementedError("not exercised by SqlValuationService")


class FakeValuationSnapshotRepository:
    def __init__(self) -> None:
        self.upserted: list[ValuationSnapshot] = []
        self._latest: dict[UUID, ValuationSnapshot] = {}

    def get_latest(self, portfolio_id: UUID) -> ValuationSnapshot:
        try:
            return self._latest[portfolio_id]
        except KeyError as exc:
            raise NotFoundError(
                f"no valuation snapshot exists for portfolio_id={portfolio_id}"
            ) from exc

    def get_by_date(self, portfolio_id: UUID, valuation_date: date) -> ValuationSnapshot | None:
        snap = self._latest.get(portfolio_id)
        if snap is not None and snap.valuation_date == valuation_date:
            return snap
        return None

    def upsert(self, snapshot: ValuationSnapshot) -> None:
        self.upserted.append(snapshot)
        self._latest[snapshot.portfolio_id] = snapshot

    def seed_latest(self, snapshot: ValuationSnapshot) -> None:
        self._latest[snapshot.portfolio_id] = snapshot


class FakeResolver:
    def __init__(self, mapping: dict[str, tuple[str, str]]) -> None:
        self._mapping = mapping

    def resolve(self, asset_id: str) -> Sequence[InstrumentRef]:
        entry = self._mapping.get(asset_id)
        if entry is None:
            return ()
        instrument_code, provider = entry
        return (InstrumentRef(provider=provider, instrument_code=instrument_code, interval="1d"),)


class FakeMarketInfoClient:
    """Matches MarketInfoClient.get_bars's public signature; in-memory bar store."""

    def __init__(self, bars: dict[tuple[str, str], list[Bar]]) -> None:
        self._bars = bars
        self.calls: list[tuple[str, str]] = []

    async def get_bars(
        self,
        *,
        instrument_code: str,
        provider: str,
        interval: str,
        start_time: datetime | None = None,
        end_time: datetime | None = None,
        order: str = "desc",
        limit: int = 200,
    ) -> list[Bar]:
        self.calls.append((instrument_code, provider))
        candidates = list(self._bars.get((instrument_code, provider), []))
        if start_time is not None:
            candidates = [b for b in candidates if b.open_time >= start_time]
        if end_time is not None:
            candidates = [b for b in candidates if b.open_time < end_time]
        candidates.sort(key=lambda b: b.open_time, reverse=(order == "desc"))
        return candidates[:limit]


def _bar(day: date, close: str) -> Bar:
    open_time = datetime.combine(day, time.min, tzinfo=UTC)
    return Bar(
        open_time=open_time,
        close_time=open_time + timedelta(days=1),
        open=Decimal(close),
        high=Decimal(close),
        low=Decimal(close),
        close=Decimal(close),
        volume=Decimal("1"),
        quote_volume=None,
        trade_count=None,
        revision=1,
        is_closed=True,
        quality_status="valid",
        provider_updated_at=open_time,
        collected_at=open_time,
    )


def _portfolio(portfolio_id: UUID, base_currency: str = "USD") -> Portfolio:
    return Portfolio(
        portfolio_id=portfolio_id,
        name="Core Growth",
        base_currency=base_currency,
        benchmark_asset_id=None,
        risk_level="moderate",
        execution_mode="research",
        status="active",
    )


def _asset(asset_id: str, quote_currency: str) -> Asset:
    return Asset(
        asset_id=asset_id,
        asset_type="STOCK",
        symbol=asset_id.rsplit(":", 1)[-1],
        venue="nasdaq",
        quote_currency=quote_currency,
        provider_symbols={},
        trading_status="tradable",
    )


class _Fixture:
    """Bundles the fakes so each test only overrides what it needs."""

    def __init__(self) -> None:
        self.portfolio_id = uuid4()
        self.portfolio_repo = FakePortfolioRepository(
            {self.portfolio_id: _portfolio(self.portfolio_id)}
        )
        self.asset_repo = FakeAssetRepository(
            {
                "equity:nasdaq:NVDA": _asset("equity:nasdaq:NVDA", "USD"),
                "crypto:bybit:BTC-USDT": _asset("crypto:bybit:BTC-USDT", "USDT"),
            }
        )
        self.position_repo = FakePositionRepository([])
        self.cash_repo = FakeCashBalanceRepository([])
        self.snapshot_repo = FakeValuationSnapshotRepository()
        self.resolver = FakeResolver(
            {
                "equity:nasdaq:NVDA": ("instrument.nasdaq.equity.nvda", "longbridge"),
                "crypto:bybit:BTC-USDT": ("instrument.bybit.spot.btc-usdt", "bybit"),
            }
        )
        self.market_client = FakeMarketInfoClient({})

    def service(self) -> SqlValuationService:
        return SqlValuationService(
            portfolio_repository=self.portfolio_repo,
            asset_repository=self.asset_repo,
            position_repository=self.position_repo,
            cash_balance_repository=self.cash_repo,
            valuation_snapshot_repository=self.snapshot_repo,
            market_info_client=self.market_client,  # type: ignore[arg-type]
            resolver=self.resolver,
        )


# --- generate_snapshot: happy paths -------------------------------------


class TestGenerateSnapshotHappyPaths:
    def test_single_position_same_currency_as_base(self) -> None:
        fx = _Fixture()
        as_of = date(2026, 8, 8)
        fx.position_repo = FakePositionRepository(
            [Position(fx.portfolio_id, "equity:nasdaq:NVDA", Decimal("10"), Decimal("100"))]
        )
        fx.cash_repo = FakeCashBalanceRepository(
            [CashBalance(fx.portfolio_id, "USD", Decimal("500"))]
        )
        fx.market_client = FakeMarketInfoClient(
            {("instrument.nasdaq.equity.nvda", "longbridge"): [_bar(as_of, "120")]}
        )

        snapshot = fx.service().generate_snapshot(fx.portfolio_id, as_of)

        assert snapshot.positions_value == Decimal("1200")  # 10 * 120
        assert snapshot.cash_value == Decimal("500")
        assert snapshot.net_asset_value == Decimal("1700")
        assert snapshot.base_currency == "USD"
        assert snapshot.price_status == "fresh"
        assert snapshot.valuation_date == as_of
        assert fx.snapshot_repo.upserted == [snapshot]

    def test_fx_conversion_applied_for_usdt_quoted_position(self) -> None:
        fx = _Fixture()
        as_of = date(2026, 8, 8)
        fx.position_repo = FakePositionRepository(
            [Position(fx.portfolio_id, "crypto:bybit:BTC-USDT", Decimal("2"), Decimal("60000"))]
        )
        fx.market_client = FakeMarketInfoClient(
            {
                ("instrument.bybit.spot.btc-usdt", "bybit"): [_bar(as_of, "60000")],
                ("instrument.coingecko.fx.usdt-usd", "coingecko"): [_bar(as_of, "0.999")],
            }
        )

        snapshot = fx.service().generate_snapshot(fx.portfolio_id, as_of)

        # 2 BTC * 60000 USDT/BTC = 120000 USDT; * 0.999 USD/USDT = 119880.
        assert snapshot.positions_value == Decimal("119880.000")
        assert snapshot.price_status == "fresh"

    def test_cash_in_non_base_currency_is_converted(self) -> None:
        fx = _Fixture()
        as_of = date(2026, 8, 8)
        fx.cash_repo = FakeCashBalanceRepository(
            [CashBalance(fx.portfolio_id, "USDT", Decimal("1000"))]
        )
        fx.market_client = FakeMarketInfoClient(
            {("instrument.coingecko.fx.usdt-usd", "coingecko"): [_bar(as_of, "1.001")]}
        )

        snapshot = fx.service().generate_snapshot(fx.portfolio_id, as_of)

        assert snapshot.cash_value == Decimal("1001.000")
        assert snapshot.net_asset_value == Decimal("1001.000")

    def test_zero_quantity_position_and_zero_cash_are_skipped_without_lookup(self) -> None:
        fx = _Fixture()
        as_of = date(2026, 8, 8)
        fx.position_repo = FakePositionRepository(
            [Position(fx.portfolio_id, "equity:nasdaq:NVDA", Decimal("0"), Decimal("100"))]
        )
        fx.cash_repo = FakeCashBalanceRepository(
            [CashBalance(fx.portfolio_id, "USD", Decimal("0"))]
        )
        fx.market_client = FakeMarketInfoClient({})  # no bars registered at all

        snapshot = fx.service().generate_snapshot(fx.portfolio_id, as_of)

        assert snapshot.positions_value == Decimal("0")
        assert snapshot.cash_value == Decimal("0")
        assert snapshot.net_asset_value == Decimal("0")
        assert snapshot.price_status == "fresh"
        assert fx.market_client.calls == []

    def test_empty_portfolio_produces_fresh_zero_snapshot(self) -> None:
        fx = _Fixture()
        as_of = date(2026, 8, 8)

        snapshot = fx.service().generate_snapshot(fx.portfolio_id, as_of)

        assert snapshot.net_asset_value == Decimal("0")
        assert snapshot.price_status == "fresh"


# --- generate_snapshot: DEC-002 carry-forward / stale marking -----------


class TestGenerateSnapshotStaleCarryForward:
    def test_no_bar_on_as_of_carries_forward_prior_close_and_marks_stale(self) -> None:
        fx = _Fixture()
        as_of = date(2026, 8, 9)  # a Sunday; no fresh bar expected
        fx.position_repo = FakePositionRepository(
            [Position(fx.portfolio_id, "equity:nasdaq:NVDA", Decimal("10"), Decimal("100"))]
        )
        fx.market_client = FakeMarketInfoClient(
            {
                ("instrument.nasdaq.equity.nvda", "longbridge"): [
                    _bar(date(2026, 8, 7), "120"),  # Friday close, carried forward
                ]
            }
        )

        snapshot = fx.service().generate_snapshot(fx.portfolio_id, as_of)

        assert snapshot.positions_value == Decimal("1200")
        assert snapshot.price_status == "stale"

    def test_portfolio_level_status_is_stale_if_any_line_is_stale(self) -> None:
        fx = _Fixture()
        as_of = date(2026, 8, 9)
        fx.position_repo = FakePositionRepository(
            [
                Position(fx.portfolio_id, "equity:nasdaq:NVDA", Decimal("10"), Decimal("100")),
                Position(fx.portfolio_id, "crypto:bybit:BTC-USDT", Decimal("1"), Decimal("60000")),
            ]
        )
        fx.market_client = FakeMarketInfoClient(
            {
                # NVDA fresh on as_of...
                ("instrument.nasdaq.equity.nvda", "longbridge"): [_bar(as_of, "120")],
                # ...but BTC-USDT only has an older bar (crypto is 24/7, so
                # this would be a genuine gap, not a normal non-trading day
                # -- still exercises the same carry-forward/stale mechanics).
                ("instrument.bybit.spot.btc-usdt", "bybit"): [_bar(date(2026, 8, 8), "61000")],
                ("instrument.coingecko.fx.usdt-usd", "coingecko"): [_bar(as_of, "1")],
            }
        )

        snapshot = fx.service().generate_snapshot(fx.portfolio_id, as_of)

        assert snapshot.price_status == "stale"


# --- generate_snapshot: fail-closed paths --------------------------------


class TestGenerateSnapshotFailClosed:
    def test_raises_not_found_for_unknown_portfolio(self) -> None:
        fx = _Fixture()
        with pytest.raises(NotFoundError):
            fx.service().generate_snapshot(uuid4(), date(2026, 8, 8))

    def test_raises_missing_price_data_when_no_bar_exists_at_all(self) -> None:
        fx = _Fixture()
        as_of = date(2026, 8, 8)
        fx.position_repo = FakePositionRepository(
            [Position(fx.portfolio_id, "equity:nasdaq:NVDA", Decimal("10"), Decimal("100"))]
        )
        fx.market_client = FakeMarketInfoClient({})  # no bars for this instrument at all

        with pytest.raises(MissingPriceDataError):
            fx.service().generate_snapshot(fx.portfolio_id, as_of)
        assert fx.snapshot_repo.upserted == []

    def test_raises_unresolved_instrument_for_unmapped_asset(self) -> None:
        fx = _Fixture()
        as_of = date(2026, 8, 8)
        fx.asset_repo = FakeAssetRepository(
            {"equity:nasdaq:UNKNOWN": _asset("equity:nasdaq:UNKNOWN", "USD")}
        )
        fx.position_repo = FakePositionRepository(
            [Position(fx.portfolio_id, "equity:nasdaq:UNKNOWN", Decimal("10"), Decimal("100"))]
        )
        # resolver has no entry for this asset_id -> resolve() returns ()

        with pytest.raises(UnresolvedInstrumentError):
            fx.service().generate_snapshot(fx.portfolio_id, as_of)
        assert fx.snapshot_repo.upserted == []

    def test_raises_unsupported_fx_pair_for_unknown_currency(self) -> None:
        fx = _Fixture()
        as_of = date(2026, 8, 8)
        fx.asset_repo = FakeAssetRepository({"equity:lse:VOD": _asset("equity:lse:VOD", "GBP")})
        fx.resolver = FakeResolver({"equity:lse:VOD": ("instrument.lse.equity.vod", "longbridge")})
        fx.position_repo = FakePositionRepository(
            [Position(fx.portfolio_id, "equity:lse:VOD", Decimal("10"), Decimal("100"))]
        )
        fx.market_client = FakeMarketInfoClient(
            {("instrument.lse.equity.vod", "longbridge"): [_bar(as_of, "100")]}
        )

        with pytest.raises(UnsupportedFxPairError):
            fx.service().generate_snapshot(fx.portfolio_id, as_of)
        assert fx.snapshot_repo.upserted == []

    def test_raises_not_found_when_position_references_unknown_asset(self) -> None:
        fx = _Fixture()
        as_of = date(2026, 8, 8)
        fx.position_repo = FakePositionRepository(
            [Position(fx.portfolio_id, "equity:nasdaq:GHOST", Decimal("10"), Decimal("100"))]
        )

        with pytest.raises(NotFoundError):
            fx.service().generate_snapshot(fx.portfolio_id, as_of)
        assert fx.snapshot_repo.upserted == []


# --- generate_snapshot: idempotent re-run --------------------------------


class TestGenerateSnapshotIdempotency:
    def test_rerunning_for_the_same_date_upserts_again(self) -> None:
        fx = _Fixture()
        as_of = date(2026, 8, 8)
        fx.position_repo = FakePositionRepository(
            [Position(fx.portfolio_id, "equity:nasdaq:NVDA", Decimal("10"), Decimal("100"))]
        )
        fx.market_client = FakeMarketInfoClient(
            {("instrument.nasdaq.equity.nvda", "longbridge"): [_bar(as_of, "120")]}
        )
        service = fx.service()

        first = service.generate_snapshot(fx.portfolio_id, as_of)
        second = service.generate_snapshot(fx.portfolio_id, as_of)

        assert first.net_asset_value == second.net_asset_value
        assert len(fx.snapshot_repo.upserted) == 2  # repository.valuation's upsert dedupes by PK


# --- current_state ---------------------------------------------------------


class TestCurrentState:
    def test_assembles_portfolio_state_from_repositories(self) -> None:
        fx = _Fixture()
        member = PortfolioMember(
            portfolio_id=fx.portfolio_id,
            asset_id="equity:nasdaq:NVDA",
            member_status="held",
            target_weight_min=None,
            target_weight_max=None,
        )
        fx.portfolio_repo.set_members(fx.portfolio_id, [member])
        position = Position(fx.portfolio_id, "equity:nasdaq:NVDA", Decimal("10"), Decimal("100"))
        fx.position_repo = FakePositionRepository([position])
        cash = CashBalance(fx.portfolio_id, "USD", Decimal("500"))
        fx.cash_repo = FakeCashBalanceRepository([cash])
        snapshot = ValuationSnapshot(
            portfolio_id=fx.portfolio_id,
            valuation_date=date(2026, 8, 8),
            positions_value=Decimal("1200"),
            cash_value=Decimal("500"),
            net_asset_value=Decimal("1700"),
            base_currency="USD",
            price_status="fresh",
        )
        fx.snapshot_repo.seed_latest(snapshot)

        state = fx.service().current_state(fx.portfolio_id)

        assert state.portfolio.portfolio_id == fx.portfolio_id
        assert state.members == (member,)
        assert state.positions == (position,)
        assert state.cash == (cash,)
        assert state.latest_snapshot == snapshot

    def test_raises_not_found_when_no_snapshot_ever_generated(self) -> None:
        fx = _Fixture()
        with pytest.raises(NotFoundError):
            fx.service().current_state(fx.portfolio_id)

    def test_raises_not_found_for_unknown_portfolio(self) -> None:
        fx = _Fixture()
        with pytest.raises(NotFoundError):
            fx.service().current_state(uuid4())


# --- sync/async bridging ---------------------------------------------------


class TestAsyncBridging:
    def test_generate_snapshot_from_a_plain_sync_context_succeeds(self) -> None:
        fx = _Fixture()
        as_of = date(2026, 8, 8)
        fx.market_client = FakeMarketInfoClient({})

        snapshot = fx.service().generate_snapshot(fx.portfolio_id, as_of)

        assert snapshot.net_asset_value == Decimal("0")

    def test_calling_from_a_running_event_loop_raises_async_bridging_error(self) -> None:
        fx = _Fixture()
        as_of = date(2026, 8, 8)
        service = fx.service()

        async def _invoke_incorrectly() -> ValuationSnapshot:
            # Simulates the misuse this class's docstring warns about: a
            # caller inside `async def` code invoking the sync method
            # directly instead of `await asyncio.to_thread(...)`.
            return service.generate_snapshot(fx.portfolio_id, as_of)

        with pytest.raises(AsyncBridgingError):
            asyncio.run(_invoke_incorrectly())

    def test_calling_via_asyncio_to_thread_succeeds(self) -> None:
        fx = _Fixture()
        as_of = date(2026, 8, 8)
        service = fx.service()

        async def _invoke_correctly() -> ValuationSnapshot:
            return await asyncio.to_thread(service.generate_snapshot, fx.portfolio_id, as_of)

        snapshot = asyncio.run(_invoke_correctly())
        assert snapshot.net_asset_value == Decimal("0")
