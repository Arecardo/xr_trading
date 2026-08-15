"""Unit tests for domain.strategies.simple_rule_strategy.SimpleRuleStrategy (BT-004).

Covers the four CONTRACT-002 constraints explicitly (parametrized where it
keeps cases readable) plus one realistic multi-asset end-to-end scenario
exercising the actual scoring/bucket logic.
"""

from __future__ import annotations

from datetime import date
from decimal import Decimal
from typing import Literal
from uuid import UUID, uuid4

import pytest

from backtest.indicators import IndicatorBar
from domain.portfolios import Portfolio, PortfolioMember
from domain.strategies.errors import InconsistentInputError, InvalidTargetWeightsError
from domain.strategies.models import AnalysisResult, AssetAnalysis
from domain.strategies.simple_rule_strategy import SimpleRuleStrategy, _ensure_weights_sum_to_one
from domain.valuation import CashBalance, PortfolioState, Position, ValuationSnapshot

_AS_OF = date(2026, 8, 8)
_NVDA = "equity:nasdaq:NVDA"
_QQQ = "equity:nasdaq:QQQ"
_BTC = "crypto:bybit:BTC-USDT"


def _portfolio(portfolio_id: UUID, base_currency: str = "USD") -> Portfolio:
    return Portfolio(
        portfolio_id=portfolio_id,
        name="Core Growth",
        base_currency=base_currency,
        benchmark_asset_id=_QQQ,
        risk_level="moderate",
        execution_mode="backtest",
        status="active",
    )


def _member(
    portfolio_id: UUID,
    asset_id: str,
    member_status: Literal["candidate", "approved", "held", "restricted"] = "approved",
    target_weight_max: Decimal | None = None,
) -> PortfolioMember:
    return PortfolioMember(
        portfolio_id=portfolio_id,
        asset_id=asset_id,
        member_status=member_status,
        target_weight_min=None,
        target_weight_max=target_weight_max,
    )


def _position(portfolio_id: UUID, asset_id: str, quantity: str, average_cost: str) -> Position:
    return Position(
        portfolio_id=portfolio_id,
        asset_id=asset_id,
        quantity=Decimal(quantity),
        average_cost=Decimal(average_cost),
    )


def _snapshot(
    portfolio_id: UUID, nav: str, positions_value: str, cash_value: str
) -> ValuationSnapshot:
    return ValuationSnapshot(
        portfolio_id=portfolio_id,
        valuation_date=_AS_OF,
        positions_value=Decimal(positions_value),
        cash_value=Decimal(cash_value),
        net_asset_value=Decimal(nav),
        base_currency="USD",
        price_status="fresh",
    )


def _indicator_bar(
    ma5: str | Decimal | None = None,
    ma20: str | Decimal | None = None,
    ma50: str | Decimal | None = None,
    ma_trend: Literal["up", "down", "flat"] | None = None,
    rsi14: str | Decimal | None = None,
    price_position_20d: str | Decimal | None = None,
    relative_strength_20d: str | Decimal | None = None,
) -> IndicatorBar:
    def _d(v: str | Decimal | None) -> Decimal | None:
        if v is None:
            return None
        return v if isinstance(v, Decimal) else Decimal(v)

    return IndicatorBar(
        trading_day=_AS_OF,
        daily_return=Decimal("0.01"),
        ma5=_d(ma5),
        ma20=_d(ma20),
        ma50=_d(ma50),
        ma_trend=ma_trend,
        rsi14=_d(rsi14),
        atr14=Decimal("1.0"),
        volume_change=Decimal("0.0"),
        price_position_20d=_d(price_position_20d),
        relative_strength_20d=_d(relative_strength_20d),
    )


def _asset_analysis(
    indicators: IndicatorBar,
    close: str,
    trading_status: Literal["watch", "tradable", "paused", "blacklist"] = "tradable",
    price_status: Literal["fresh", "stale"] = "fresh",
) -> AssetAnalysis:
    return AssetAnalysis(
        indicators=indicators,
        close=Decimal(close),
        price_status=price_status,
        trading_status=trading_status,
    )


def _strong_buy_indicators() -> IndicatorBar:
    """score = +1 (ma_trend up) +1 (close>ma20) +1 (close>ma50) +1 (rel strength) = 4 -> buy."""
    return _indicator_bar(
        ma5=Decimal("102"),
        ma20=Decimal("95"),
        ma50=Decimal("90"),
        ma_trend="up",
        rsi14=Decimal("55"),
        price_position_20d=Decimal("0.9"),
        relative_strength_20d=Decimal("0.05"),
    )


def _strong_sell_indicators() -> IndicatorBar:
    """score = -1 -1 -1 -2 -1 = -6 (down, close<ma20, close<ma50, breakdown, rel weak) -> sell."""
    return _indicator_bar(
        ma5=Decimal("80"),
        ma20=Decimal("95"),
        ma50=Decimal("100"),
        ma_trend="down",
        rsi14=Decimal("40"),
        price_position_20d=Decimal("0.02"),
        relative_strength_20d=Decimal("-0.05"),
    )


def _neutral_hold_indicators() -> IndicatorBar:
    """score = 0 -> hold."""
    return _indicator_bar(
        ma5=Decimal("100"),
        ma20=Decimal("100"),
        ma50=None,
        ma_trend="flat",
        rsi14=Decimal("50"),
        price_position_20d=Decimal("0.5"),
        relative_strength_20d=Decimal("0"),
    )


# --- constraint (a): target_weights must sum to exactly 1 -----------------


def test_ensure_weights_sum_to_one_accepts_a_valid_mapping() -> None:
    _ensure_weights_sum_to_one({"a": Decimal("0.4"), "b": Decimal("0.6")})  # must not raise


def test_ensure_weights_sum_to_one_rejects_a_mapping_that_does_not_sum_to_one() -> None:
    with pytest.raises(InvalidTargetWeightsError):
        _ensure_weights_sum_to_one({"a": Decimal("0.4"), "b": Decimal("0.7")})


@pytest.mark.parametrize(
    "buckets",
    [
        pytest.param(
            [_strong_buy_indicators(), _strong_buy_indicators(), _strong_buy_indicators()],
            id="all_buy",
        ),
        pytest.param([_strong_sell_indicators(), _neutral_hold_indicators()], id="sell_and_hold"),
        pytest.param([_neutral_hold_indicators()], id="single_hold"),
    ],
)
def test_generate_targets_output_always_sums_to_exactly_one(buckets: list[IndicatorBar]) -> None:
    portfolio_id = uuid4()
    portfolio = _portfolio(portfolio_id)
    asset_ids = [f"equity:nasdaq:SYM{i}" for i in range(len(buckets))]
    members = tuple(
        _member(portfolio_id, aid, target_weight_max=Decimal("0.5")) for aid in asset_ids
    )
    analysis = AnalysisResult(
        portfolio_id=portfolio_id,
        as_of=_AS_OF,
        assets={
            aid: _asset_analysis(ind, close="100")
            for aid, ind in zip(asset_ids, buckets, strict=True)
        },
    )
    state = PortfolioState(
        portfolio=portfolio,
        members=members,
        positions=(),
        cash=(CashBalance(portfolio_id=portfolio_id, currency="USD", amount=Decimal("1000")),),
        latest_snapshot=_snapshot(portfolio_id, nav="1000", positions_value="0", cash_value="1000"),
    )

    output = SimpleRuleStrategy().generate_targets(analysis, state)

    assert sum(output.target_weights.values()) == Decimal("1")


# --- constraint (b): restricted / blacklisted assets never increase -------


@pytest.mark.parametrize(
    "member_status,trading_status",
    [
        ("restricted", "tradable"),
        ("approved", "blacklist"),
        ("restricted", "blacklist"),
    ],
)
def test_restricted_or_blacklisted_asset_is_never_increased_above_current_weight(
    member_status: Literal["candidate", "approved", "held", "restricted"],
    trading_status: Literal["watch", "tradable", "paused", "blacklist"],
) -> None:
    portfolio_id = uuid4()
    portfolio = _portfolio(portfolio_id)
    # NVDA is currently held at weight 10/100*100 / nav; give it a strong-buy
    # score so the unclamped bucket logic would want to increase it.
    member = _member(
        portfolio_id, _NVDA, member_status=member_status, target_weight_max=Decimal("0.5")
    )
    position = _position(portfolio_id, _NVDA, quantity="1", average_cost="90")
    analysis = AnalysisResult(
        portfolio_id=portfolio_id,
        as_of=_AS_OF,
        assets={
            _NVDA: _asset_analysis(
                _strong_buy_indicators(), close="100", trading_status=trading_status
            )
        },
    )
    state = PortfolioState(
        portfolio=portfolio,
        members=(member,),
        positions=(position,),
        cash=(CashBalance(portfolio_id=portfolio_id, currency="USD", amount=Decimal("900")),),
        latest_snapshot=_snapshot(
            portfolio_id, nav="1000", positions_value="100", cash_value="900"
        ),
    )
    current_weight = Decimal("100") / Decimal("1000")  # 0.1

    output = SimpleRuleStrategy().generate_targets(analysis, state)

    assert output.target_weights[_NVDA] <= current_weight
    assert output.target_weights[_NVDA] == current_weight  # score=buy but capped: hold, not reduce


def test_restricted_asset_can_still_be_reduced_by_a_sell_signal() -> None:
    portfolio_id = uuid4()
    portfolio = _portfolio(portfolio_id)
    member = _member(portfolio_id, _NVDA, member_status="restricted")
    position = _position(portfolio_id, _NVDA, quantity="1", average_cost="90")
    analysis = AnalysisResult(
        portfolio_id=portfolio_id,
        as_of=_AS_OF,
        assets={_NVDA: _asset_analysis(_strong_sell_indicators(), close="100")},
    )
    state = PortfolioState(
        portfolio=portfolio,
        members=(member,),
        positions=(position,),
        cash=(CashBalance(portfolio_id=portfolio_id, currency="USD", amount=Decimal("900")),),
        latest_snapshot=_snapshot(
            portfolio_id, nav="1000", positions_value="100", cash_value="900"
        ),
    )

    output = SimpleRuleStrategy().generate_targets(analysis, state)

    # A sell signal already produces desired=0 <= current weight, so the
    # restricted clamp is a no-op here -- reduction/exit is still allowed.
    assert _NVDA not in output.target_weights or output.target_weights[_NVDA] == Decimal("0")


# --- constraint (c): missing/insufficient indicator history blocks new buys


def test_asset_not_currently_held_with_insufficient_history_gets_no_buy() -> None:
    portfolio_id = uuid4()
    portfolio = _portfolio(portfolio_id)
    member = _member(portfolio_id, _NVDA, target_weight_max=Decimal("0.5"))
    # ma20/price_position_20d both None -> insufficient history.
    incomplete_indicators = _indicator_bar(ma5=Decimal("100"))
    analysis = AnalysisResult(
        portfolio_id=portfolio_id,
        as_of=_AS_OF,
        assets={_NVDA: _asset_analysis(incomplete_indicators, close="100")},
    )
    state = PortfolioState(
        portfolio=portfolio,
        members=(member,),
        positions=(),
        cash=(CashBalance(portfolio_id=portfolio_id, currency="USD", amount=Decimal("1000")),),
        latest_snapshot=_snapshot(portfolio_id, nav="1000", positions_value="0", cash_value="1000"),
    )

    output = SimpleRuleStrategy().generate_targets(analysis, state)

    assert output.target_weights.get(_NVDA, Decimal("0")) == Decimal("0")
    assert output.target_weights["cash:USD"] == Decimal("1")


def test_asset_entirely_absent_from_analysis_and_not_held_gets_no_buy() -> None:
    portfolio_id = uuid4()
    portfolio = _portfolio(portfolio_id)
    member = _member(portfolio_id, _NVDA, target_weight_max=Decimal("0.5"))
    analysis = AnalysisResult(portfolio_id=portfolio_id, as_of=_AS_OF, assets={})
    state = PortfolioState(
        portfolio=portfolio,
        members=(member,),
        positions=(),
        cash=(CashBalance(portfolio_id=portfolio_id, currency="USD", amount=Decimal("1000")),),
        latest_snapshot=_snapshot(portfolio_id, nav="1000", positions_value="0", cash_value="1000"),
    )

    output = SimpleRuleStrategy().generate_targets(analysis, state)

    assert output.target_weights.get(_NVDA, Decimal("0")) == Decimal("0")


def test_held_asset_with_insufficient_history_is_maintained_not_increased() -> None:
    portfolio_id = uuid4()
    portfolio = _portfolio(portfolio_id)
    member = _member(portfolio_id, _NVDA, target_weight_max=Decimal("0.5"))
    position = _position(portfolio_id, _NVDA, quantity="1", average_cost="90")
    incomplete_indicators = _indicator_bar(ma5=Decimal("100"))  # ma20 missing
    analysis = AnalysisResult(
        portfolio_id=portfolio_id,
        as_of=_AS_OF,
        assets={_NVDA: _asset_analysis(incomplete_indicators, close="100")},
    )
    state = PortfolioState(
        portfolio=portfolio,
        members=(member,),
        positions=(position,),
        cash=(CashBalance(portfolio_id=portfolio_id, currency="USD", amount=Decimal("900")),),
        latest_snapshot=_snapshot(
            portfolio_id, nav="1000", positions_value="100", cash_value="900"
        ),
    )

    output = SimpleRuleStrategy().generate_targets(analysis, state)

    assert output.target_weights[_NVDA] == Decimal("100") / Decimal("1000")


def test_held_position_missing_from_analysis_fails_closed_with_missing_price_error() -> None:
    from domain.strategies.errors import MissingPriceDataError

    portfolio_id = uuid4()
    portfolio = _portfolio(portfolio_id)
    member = _member(portfolio_id, _NVDA)
    position = _position(portfolio_id, _NVDA, quantity="1", average_cost="90")
    analysis = AnalysisResult(
        portfolio_id=portfolio_id, as_of=_AS_OF, assets={}
    )  # no price for NVDA
    state = PortfolioState(
        portfolio=portfolio,
        members=(member,),
        positions=(position,),
        cash=(CashBalance(portfolio_id=portfolio_id, currency="USD", amount=Decimal("900")),),
        latest_snapshot=_snapshot(
            portfolio_id, nav="1000", positions_value="100", cash_value="900"
        ),
    )

    with pytest.raises(MissingPriceDataError):
        SimpleRuleStrategy().generate_targets(analysis, state)


# --- constraint (d): pure function, deterministic, no side effects --------


def test_generate_targets_is_deterministic_across_repeated_calls() -> None:
    portfolio_id = uuid4()
    portfolio = _portfolio(portfolio_id)
    member = _member(portfolio_id, _NVDA, target_weight_max=Decimal("0.3"))
    analysis = AnalysisResult(
        portfolio_id=portfolio_id,
        as_of=_AS_OF,
        assets={_NVDA: _asset_analysis(_strong_buy_indicators(), close="100")},
    )
    state = PortfolioState(
        portfolio=portfolio,
        members=(member,),
        positions=(),
        cash=(CashBalance(portfolio_id=portfolio_id, currency="USD", amount=Decimal("1000")),),
        latest_snapshot=_snapshot(portfolio_id, nav="1000", positions_value="0", cash_value="1000"),
    )

    strategy = SimpleRuleStrategy()
    first = strategy.generate_targets(analysis, state)
    second = strategy.generate_targets(analysis, state)

    assert first == second


def test_generate_targets_does_not_mutate_its_inputs() -> None:
    portfolio_id = uuid4()
    portfolio = _portfolio(portfolio_id)
    member = _member(portfolio_id, _NVDA, target_weight_max=Decimal("0.3"))
    analysis = AnalysisResult(
        portfolio_id=portfolio_id,
        as_of=_AS_OF,
        assets={_NVDA: _asset_analysis(_strong_buy_indicators(), close="100")},
    )
    state = PortfolioState(
        portfolio=portfolio,
        members=(member,),
        positions=(),
        cash=(CashBalance(portfolio_id=portfolio_id, currency="USD", amount=Decimal("1000")),),
        latest_snapshot=_snapshot(portfolio_id, nav="1000", positions_value="0", cash_value="1000"),
    )
    analysis_before = analysis
    state_before = state

    SimpleRuleStrategy().generate_targets(analysis, state)

    # Frozen dataclasses can't mutate in place, but the analysis.assets dict
    # is a plain mutable dict -- assert it is untouched by identity + equality.
    assert analysis is analysis_before
    assert state is state_before
    assert analysis.assets == {_NVDA: _asset_analysis(_strong_buy_indicators(), close="100")}


def test_inconsistent_portfolio_id_between_analysis_and_state_is_rejected() -> None:
    portfolio_id = uuid4()
    other_portfolio_id = uuid4()
    portfolio = _portfolio(portfolio_id)
    member = _member(portfolio_id, _NVDA)
    analysis = AnalysisResult(portfolio_id=other_portfolio_id, as_of=_AS_OF, assets={})
    state = PortfolioState(
        portfolio=portfolio,
        members=(member,),
        positions=(),
        cash=(),
        latest_snapshot=_snapshot(portfolio_id, nav="1000", positions_value="0", cash_value="1000"),
    )

    with pytest.raises(InconsistentInputError):
        SimpleRuleStrategy().generate_targets(analysis, state)


# --- realistic multi-asset end-to-end scenario -----------------------------


def test_realistic_multi_asset_scenario_scores_and_allocates_as_expected() -> None:
    """NVDA (strong buy, not held) / QQQ (neutral, held) / BTC (strong sell, held)."""
    portfolio_id = uuid4()
    portfolio = _portfolio(portfolio_id)
    members = (
        _member(portfolio_id, _NVDA, member_status="candidate", target_weight_max=Decimal("0.25")),
        _member(portfolio_id, _QQQ, member_status="held", target_weight_max=Decimal("0.5")),
        _member(portfolio_id, _BTC, member_status="held", target_weight_max=Decimal("0.5")),
    )
    qqq_position = _position(portfolio_id, _QQQ, quantity="2", average_cost="180")
    btc_position = _position(portfolio_id, _BTC, quantity="0.1", average_cost="50000")
    analysis = AnalysisResult(
        portfolio_id=portfolio_id,
        as_of=_AS_OF,
        assets={
            _NVDA: _asset_analysis(_strong_buy_indicators(), close="120"),
            _QQQ: _asset_analysis(_neutral_hold_indicators(), close="200"),
            _BTC: _asset_analysis(_strong_sell_indicators(), close="60000"),
        },
    )
    # nav = cash(4000) + qqq(2*200=400) + btc(0.1*60000=6000) = 10400
    state = PortfolioState(
        portfolio=portfolio,
        members=members,
        positions=(qqq_position, btc_position),
        cash=(CashBalance(portfolio_id=portfolio_id, currency="USD", amount=Decimal("4000")),),
        latest_snapshot=_snapshot(
            portfolio_id, nav="10400", positions_value="6400", cash_value="4000"
        ),
    )

    output = SimpleRuleStrategy().generate_targets(analysis, state)

    assert sum(output.target_weights.values()) == Decimal("1")
    # NVDA: strong buy, previously unheld -> gets a new nonzero allocation.
    assert output.target_weights.get(_NVDA, Decimal("0")) == Decimal("0.25")
    # QQQ: neutral score -> held at its current weight (400/10400).
    qqq_current_weight = Decimal("400") / Decimal("10400")
    assert output.target_weights[_QQQ] == qqq_current_weight
    # BTC: strong sell -> fully exited (weight 0, no entry in target_weights).
    assert output.target_weights.get(_BTC, Decimal("0")) == Decimal("0")

    scores_by_asset = {s.asset_id: s.score for s in output.asset_scores}
    assert scores_by_asset[_NVDA] >= Decimal("2")  # buy bucket
    assert scores_by_asset[_BTC] <= Decimal("-2")  # sell bucket
    assert Decimal("-2") < scores_by_asset[_QQQ] < Decimal("2")  # hold bucket


def test_strategy_id_and_version_are_stable_attributes() -> None:
    strategy = SimpleRuleStrategy()
    assert isinstance(strategy.strategy_id, str) and strategy.strategy_id
    assert isinstance(strategy.version, str) and strategy.version
