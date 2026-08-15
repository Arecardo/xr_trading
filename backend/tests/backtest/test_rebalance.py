"""Unit tests for backtest.rebalance.diff_target_weights_to_orders."""

from __future__ import annotations

from decimal import Decimal

import pytest

from backtest.models import AssetClass
from backtest.rebalance import diff_target_weights_to_orders

_NVDA = "equity:nasdaq:NVDA"
_QQQ = "equity:nasdaq:QQQ"
_BTC = "crypto:bybit:BTC-USDT"
_CASH = "cash:USD"


def _universe() -> dict[str, AssetClass]:
    return {_NVDA: "STOCK", _QQQ: "ETF", _BTC: "CRYPTO"}


def test_buy_when_target_weight_exceeds_current_weight() -> None:
    orders = diff_target_weights_to_orders(
        target_weights={_NVDA: Decimal("0.5"), _CASH: Decimal("0.5")},
        current_quantities={_NVDA: Decimal("0")},
        prices={_NVDA: Decimal("100")},
        nav=Decimal("1000"),
        tradable_asset_ids={_NVDA: "STOCK"},
        is_stale={_NVDA: False},
        rebalance_threshold_pct=Decimal("0.01"),
    )
    assert len(orders) == 1
    order = orders[0]
    assert order.asset_id == _NVDA
    assert order.side == "buy"
    assert order.quantity == Decimal("5")  # (0.5*1000 - 0)/100
    assert order.estimated_price == Decimal("100")


def test_sell_when_target_weight_below_current_weight() -> None:
    orders = diff_target_weights_to_orders(
        target_weights={_NVDA: Decimal("0"), _CASH: Decimal("1")},
        current_quantities={_NVDA: Decimal("10")},
        prices={_NVDA: Decimal("100")},
        nav=Decimal("1000"),
        tradable_asset_ids={_NVDA: "STOCK"},
        is_stale={_NVDA: False},
        rebalance_threshold_pct=Decimal("0.01"),
    )
    assert len(orders) == 1
    assert orders[0].side == "sell"
    assert orders[0].quantity == Decimal("10")  # full exit: 1000/100


def test_diff_below_threshold_produces_no_order() -> None:
    orders = diff_target_weights_to_orders(
        target_weights={_NVDA: Decimal("0.201"), _CASH: Decimal("0.799")},
        current_quantities={_NVDA: Decimal("2")},  # current value 200 -> weight 0.20
        prices={_NVDA: Decimal("100")},
        nav=Decimal("1000"),
        tradable_asset_ids={_NVDA: "STOCK"},
        is_stale={_NVDA: False},
        rebalance_threshold_pct=Decimal("0.05"),  # 5% of NAV = 50, diff is only 10
    )
    assert orders == ()


def test_diff_at_or_above_threshold_produces_an_order() -> None:
    orders = diff_target_weights_to_orders(
        target_weights={_NVDA: Decimal("0.30"), _CASH: Decimal("0.70")},
        current_quantities={_NVDA: Decimal("2")},  # current value 200 -> weight 0.20
        prices={_NVDA: Decimal("100")},
        nav=Decimal("1000"),
        tradable_asset_ids={_NVDA: "STOCK"},
        is_stale={_NVDA: False},
        rebalance_threshold_pct=Decimal("0.05"),  # diff is 100, exactly 10% of NAV
    )
    assert len(orders) == 1
    assert orders[0].side == "buy"


def test_stale_stock_or_etf_is_skipped_entirely() -> None:
    orders = diff_target_weights_to_orders(
        target_weights={_NVDA: Decimal("0.5"), _QQQ: Decimal("0.5")},
        current_quantities={_NVDA: Decimal("0"), _QQQ: Decimal("0")},
        prices={_NVDA: Decimal("100"), _QQQ: Decimal("100")},
        nav=Decimal("1000"),
        tradable_asset_ids={_NVDA: "STOCK", _QQQ: "ETF"},
        is_stale={_NVDA: True, _QQQ: False},
        rebalance_threshold_pct=Decimal("0.01"),
    )
    asset_ids = {o.asset_id for o in orders}
    assert asset_ids == {_QQQ}  # NVDA skipped for being stale; QQQ still traded


def test_stale_crypto_is_not_exempted_from_trading() -> None:
    orders = diff_target_weights_to_orders(
        target_weights={_BTC: Decimal("0.5"), _CASH: Decimal("0.5")},
        current_quantities={_BTC: Decimal("0")},
        prices={_BTC: Decimal("100")},
        nav=Decimal("1000"),
        tradable_asset_ids={_BTC: "CRYPTO"},
        # is_stale=True should never happen for real crypto data, but the
        # function is documented to apply the stale-skip rule only to
        # STOCK/ETF -- verify crypto still trades even if flagged stale.
        is_stale={_BTC: True},
        rebalance_threshold_pct=Decimal("0.01"),
    )
    assert len(orders) == 1
    assert orders[0].asset_id == _BTC


def test_zero_price_asset_is_skipped_rather_than_dividing_by_zero() -> None:
    orders = diff_target_weights_to_orders(
        target_weights={_NVDA: Decimal("0.5"), _CASH: Decimal("0.5")},
        current_quantities={_NVDA: Decimal("0")},
        prices={_NVDA: Decimal("0")},
        nav=Decimal("1000"),
        tradable_asset_ids={_NVDA: "STOCK"},
        is_stale={_NVDA: False},
        rebalance_threshold_pct=Decimal("0.01"),
    )
    assert orders == ()


def test_non_positive_nav_returns_empty_tuple_not_an_error() -> None:
    orders = diff_target_weights_to_orders(
        target_weights={_NVDA: Decimal("0.5")},
        current_quantities={_NVDA: Decimal("0")},
        prices={_NVDA: Decimal("100")},
        nav=Decimal("0"),
        tradable_asset_ids={_NVDA: "STOCK"},
        is_stale={_NVDA: False},
        rebalance_threshold_pct=Decimal("0.01"),
    )
    assert orders == ()


def test_negative_rebalance_threshold_raises() -> None:
    with pytest.raises(ValueError, match="rebalance_threshold_pct must be non-negative"):
        diff_target_weights_to_orders(
            target_weights={},
            current_quantities={},
            prices={},
            nav=Decimal("1000"),
            tradable_asset_ids={},
            is_stale={},
            rebalance_threshold_pct=Decimal("-0.01"),
        )


def test_asset_absent_from_target_weights_is_treated_as_target_zero() -> None:
    orders = diff_target_weights_to_orders(
        target_weights={_CASH: Decimal("1")},  # NVDA absent -> implicit target 0
        current_quantities={_NVDA: Decimal("5")},
        prices={_NVDA: Decimal("100")},
        nav=Decimal("1000"),
        tradable_asset_ids={_NVDA: "STOCK"},
        is_stale={_NVDA: False},
        rebalance_threshold_pct=Decimal("0.01"),
    )
    assert len(orders) == 1
    assert orders[0].side == "sell"
    assert orders[0].quantity == Decimal("5")


def test_multiple_assets_diffed_independently() -> None:
    orders = diff_target_weights_to_orders(
        target_weights={_NVDA: Decimal("0.3"), _QQQ: Decimal("0.0"), _CASH: Decimal("0.7")},
        current_quantities={_NVDA: Decimal("0"), _QQQ: Decimal("2"), _BTC: Decimal("0")},
        prices={_NVDA: Decimal("100"), _QQQ: Decimal("100"), _BTC: Decimal("50000")},
        nav=Decimal("1000"),
        tradable_asset_ids=_universe(),
        is_stale={_NVDA: False, _QQQ: False, _BTC: False},
        rebalance_threshold_pct=Decimal("0.01"),
    )
    by_asset = {o.asset_id: o for o in orders}
    assert by_asset[_NVDA].side == "buy"
    assert by_asset[_QQQ].side == "sell"
    assert _BTC not in by_asset  # target/current both 0 -> no diff
