"""Unit tests for domain.assets: instantiation and frozen-dataclass behaviour."""

from __future__ import annotations

import dataclasses

import pytest

from domain.assets import Asset


def _make_asset(**overrides: object) -> Asset:
    defaults: dict[str, object] = {
        "asset_id": "equity:nasdaq:NVDA",
        "asset_type": "STOCK",
        "symbol": "NVDA",
        "venue": "nasdaq",
        "quote_currency": "USD",
        "provider_symbols": {"longbridge": "NVDA.US"},
        "trading_status": "tradable",
    }
    defaults.update(overrides)
    return Asset(**defaults)  # type: ignore[arg-type]


def test_asset_instantiates_with_expected_field_types() -> None:
    asset = _make_asset()

    assert asset.asset_id == "equity:nasdaq:NVDA"
    assert asset.asset_type == "STOCK"
    assert asset.symbol == "NVDA"
    assert asset.venue == "nasdaq"
    assert asset.quote_currency == "USD"
    assert asset.provider_symbols == {"longbridge": "NVDA.US"}
    assert asset.trading_status == "tradable"


@pytest.mark.parametrize("asset_type", ["STOCK", "ETF", "CRYPTO", "CASH"])
def test_asset_accepts_every_asset_type(asset_type: str) -> None:
    asset = _make_asset(asset_type=asset_type)
    assert asset.asset_type == asset_type


@pytest.mark.parametrize("trading_status", ["watch", "tradable", "paused", "blacklist"])
def test_asset_accepts_every_trading_status(trading_status: str) -> None:
    asset = _make_asset(trading_status=trading_status)
    assert asset.trading_status == trading_status


def test_asset_is_frozen() -> None:
    asset = _make_asset()
    with pytest.raises(dataclasses.FrozenInstanceError):
        asset.symbol = "QQQ"  # type: ignore[misc]
