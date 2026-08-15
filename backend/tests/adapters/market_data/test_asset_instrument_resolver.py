"""Unit tests for adapters.market_data.asset_instrument_resolver (BE-003)."""

from __future__ import annotations

import pytest

from adapters.market_data.asset_instrument_resolver import (
    StaticAssetInstrumentResolver,
    resolve_fx_instrument,
)
from adapters.market_data.models import InstrumentRef


class TestStaticAssetInstrumentResolver:
    @pytest.mark.parametrize(
        ("asset_id", "expected_instrument_code", "expected_provider"),
        [
            ("equity:nasdaq:NVDA", "instrument.nasdaq.equity.nvda", "longbridge"),
            ("equity:nasdaq:QQQ", "instrument.nasdaq.etf.qqq", "longbridge"),
            ("crypto:bybit:BTC-USDT", "instrument.bybit.spot.btc-usdt", "bybit"),
        ],
    )
    def test_resolves_known_assets_to_daily_instrument_ref(
        self, asset_id: str, expected_instrument_code: str, expected_provider: str
    ) -> None:
        resolver = StaticAssetInstrumentResolver()

        refs = resolver.resolve(asset_id)

        assert refs == (
            InstrumentRef(
                provider=expected_provider,
                instrument_code=expected_instrument_code,
                interval="1d",
            ),
        )

    def test_unknown_asset_returns_empty_sequence(self) -> None:
        resolver = StaticAssetInstrumentResolver()

        assert resolver.resolve("cash:USD") == ()
        assert resolver.resolve("equity:nyse:UNKNOWN") == ()

    def test_custom_intervals_produce_one_ref_per_interval(self) -> None:
        resolver = StaticAssetInstrumentResolver(intervals=("1d", "1h"))

        refs = resolver.resolve("equity:nasdaq:NVDA")

        assert refs == (
            InstrumentRef(
                provider="longbridge",
                instrument_code="instrument.nasdaq.equity.nvda",
                interval="1d",
            ),
            InstrumentRef(
                provider="longbridge",
                instrument_code="instrument.nasdaq.equity.nvda",
                interval="1h",
            ),
        )

    def test_rejects_empty_intervals(self) -> None:
        with pytest.raises(ValueError, match="intervals must not be empty"):
            StaticAssetInstrumentResolver(intervals=())


class TestResolveFxInstrument:
    def test_resolves_usdt_to_usd(self) -> None:
        ref = resolve_fx_instrument("USDT", "USD")

        assert ref == InstrumentRef(
            provider="coingecko", instrument_code="instrument.coingecko.fx.usdt-usd", interval="1d"
        )

    @pytest.mark.parametrize(
        ("from_currency", "to_currency"),
        [
            ("USD", "USDT"),  # reverse direction not wired -- must not be inferred
            ("EUR", "USD"),
            ("USD", "USD"),  # identity pairs are the caller's job to skip, not this function's
        ],
    )
    def test_unknown_pair_returns_none(self, from_currency: str, to_currency: str) -> None:
        assert resolve_fx_instrument(from_currency, to_currency) is None
