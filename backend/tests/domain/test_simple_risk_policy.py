"""Unit tests for domain.risk.simple_risk_policy.SimpleRiskPolicy (BT-005).

Layout mirrors backend/tests/domain/test_simple_rule_strategy.py: small
dataclass-building helpers up top, then tests grouped by the rule they
exercise (normal / boundary / failure paths per parametrized case), plus a
determinism check and a "no environment branching" check tying back to
CONTRACT-003.

``_permissive_policy`` is used throughout the ``check_order`` tests: the
documented default per-order risk budget (0.5% of NAV) is deliberately
tight, and would otherwise trip *before* the specific rule a given test
means to isolate (e.g. a post-trade weight-cap test whose order is 20-30%
of NAV). Each test tightens back exactly the one threshold it exercises.
``check_target_weights`` tests don't need this -- there is no per-order
budget at the portfolio-weights level, and the chosen weights in each test
already avoid tripping unrelated rules (verified inline).
"""

from __future__ import annotations

from datetime import date
from decimal import Decimal
from typing import Literal
from uuid import UUID, uuid4

import pytest

from domain.portfolios import Portfolio, PortfolioMember
from domain.risk.errors import InconsistentInputError, InvalidRiskConfigurationError
from domain.risk.models import OrderIntent
from domain.risk.simple_risk_policy import SimpleRiskPolicy
from domain.valuation import CashBalance, PortfolioState, Position, ValuationSnapshot

_AS_OF = date(2026, 8, 8)
_NVDA = "equity:nasdaq:NVDA"
_QQQ = "equity:nasdaq:QQQ"
_BTC = "crypto:bybit:BTC-USDT"
_CASH = "cash:USD"


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


def _state(
    portfolio_id: UUID,
    members: tuple[PortfolioMember, ...],
    positions: tuple[Position, ...],
    nav: str,
    positions_value: str,
    cash_value: str,
) -> PortfolioState:
    return PortfolioState(
        portfolio=_portfolio(portfolio_id),
        members=members,
        positions=positions,
        cash=(CashBalance(portfolio_id=portfolio_id, currency="USD", amount=Decimal(cash_value)),),
        latest_snapshot=_snapshot(portfolio_id, nav, positions_value, cash_value),
    )


def _order(
    portfolio_id: UUID,
    asset_id: str,
    side: Literal["buy", "sell"],
    quantity: str,
    estimated_price: str,
) -> OrderIntent:
    return OrderIntent(
        portfolio_id=portfolio_id,
        asset_id=asset_id,
        side=side,
        quantity=Decimal(quantity),
        estimated_price=Decimal(estimated_price),
    )


def _permissive_policy(**overrides: object) -> SimpleRiskPolicy:
    """A ``SimpleRiskPolicy`` with every threshold wide open except ``environment``.

    Lets each ``check_order`` test tighten back exactly the one threshold it
    means to exercise, without unrelated defaults (particularly the tight
    0.5% per-order risk budget) tripping first and confusing the assertion.
    """
    defaults: dict[str, object] = {
        "max_single_equity_weight": Decimal("1"),
        "max_single_crypto_weight": Decimal("1"),
        "max_crypto_category_weight": Decimal("1"),
        "min_cash_weight": Decimal("0"),
        "max_order_risk_pct_of_nav": Decimal("1"),
        "max_held_positions": 1_000,
        "drawdown_stop_line": Decimal("1"),
    }
    defaults.update(overrides)
    return SimpleRiskPolicy(**defaults)  # type: ignore[arg-type]


# --- constructor validation -------------------------------------------------


@pytest.mark.parametrize(
    "kwargs",
    [
        pytest.param({"environment": "sandbox"}, id="bad_environment"),
        pytest.param({"max_single_equity_weight": Decimal("-0.01")}, id="negative_weight_cap"),
        pytest.param({"max_single_equity_weight": Decimal("1.01")}, id="weight_cap_above_one"),
        pytest.param({"min_cash_weight": Decimal("1.5")}, id="cash_floor_above_one"),
        pytest.param({"max_order_risk_pct_of_nav": Decimal("-0.1")}, id="negative_order_budget"),
        pytest.param({"drawdown_stop_line": Decimal("1.5")}, id="drawdown_stop_above_one"),
        pytest.param({"max_held_positions": 0}, id="zero_max_positions"),
        pytest.param({"max_held_positions": -1}, id="negative_max_positions"),
    ],
)
def test_invalid_configuration_is_rejected_at_construction(kwargs: dict[str, object]) -> None:
    with pytest.raises(InvalidRiskConfigurationError):
        SimpleRiskPolicy(**kwargs)  # type: ignore[arg-type]


def test_negative_peak_nav_is_rejected_at_construction() -> None:
    portfolio_id = uuid4()
    with pytest.raises(InvalidRiskConfigurationError):
        SimpleRiskPolicy(peak_net_asset_value_by_portfolio={portfolio_id: Decimal("-1")})


def test_valid_configuration_is_accepted() -> None:
    SimpleRiskPolicy(
        environment="live",
        max_single_equity_weight=Decimal("0.25"),
        max_held_positions=1,
        drawdown_stop_line=Decimal("0"),
    )  # must not raise


# --- check_order: input-consistency guard -----------------------------------


def test_check_order_rejects_mismatched_portfolio_ids_with_exception() -> None:
    portfolio_id = uuid4()
    other_portfolio_id = uuid4()
    state = _state(portfolio_id, (), (), nav="1000", positions_value="0", cash_value="1000")
    intent = _order(other_portfolio_id, _NVDA, "buy", "1", "100")

    with pytest.raises(InconsistentInputError):
        SimpleRiskPolicy().check_order(intent, state)


# --- check_order: quantity / price sanity checks ----------------------------


@pytest.mark.parametrize(
    "quantity,expected_approved",
    [("0", False), ("-1", False), ("1", True)],
)
def test_check_order_quantity_must_be_positive(quantity: str, expected_approved: bool) -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA)
    state = _state(
        portfolio_id, (member,), (), nav="10000", positions_value="0", cash_value="10000"
    )
    intent = _order(portfolio_id, _NVDA, "buy", quantity, "100")

    result = _permissive_policy().check_order(intent, state)

    assert result.approved is expected_approved
    assert "quantity_positive" in result.checked_rules
    if not expected_approved:
        assert any("quantity" in r for r in result.rejection_reasons)


@pytest.mark.parametrize(
    "estimated_price,expected_approved",
    [("0", False), ("-5", False), ("100", True)],
)
def test_check_order_estimated_price_must_be_positive(
    estimated_price: str, expected_approved: bool
) -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA)
    state = _state(
        portfolio_id, (member,), (), nav="10000", positions_value="0", cash_value="10000"
    )
    intent = _order(portfolio_id, _NVDA, "buy", "1", estimated_price)

    result = _permissive_policy().check_order(intent, state)

    assert result.approved is expected_approved
    assert "estimated_price_positive" in result.checked_rules


# --- check_order: portfolio-membership + restricted/blacklist gate ---------


def test_check_order_blocks_buying_an_asset_outside_the_portfolio_universe() -> None:
    portfolio_id = uuid4()
    state = _state(portfolio_id, (), (), nav="10000", positions_value="0", cash_value="10000")
    intent = _order(portfolio_id, _NVDA, "buy", "1", "100")

    result = _permissive_policy().check_order(intent, state)

    assert result.approved is False
    assert "asset_must_be_portfolio_member" in result.checked_rules
    assert any("not a member" in r for r in result.rejection_reasons)


def test_check_order_allows_selling_an_asset_outside_the_current_universe() -> None:
    # Selling a position in an asset that's since been dropped from the
    # portfolio's approved universe must still be allowed (exit only).
    portfolio_id = uuid4()
    position = _position(portfolio_id, _NVDA, quantity="5", average_cost="90")
    state = _state(
        portfolio_id, (), (position,), nav="10500", positions_value="500", cash_value="10000"
    )
    intent = _order(portfolio_id, _NVDA, "sell", "5", "100")

    result = _permissive_policy().check_order(intent, state)

    assert result.approved is True
    assert "asset_must_be_portfolio_member" not in result.checked_rules


@pytest.mark.parametrize("side", ["buy", "sell"])
def test_check_order_restricted_member_only_blocks_buy_side(
    side: Literal["buy", "sell"],
) -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA, member_status="restricted")
    position = _position(portfolio_id, _NVDA, quantity="5", average_cost="90")
    state = _state(
        portfolio_id, (member,), (position,), nav="10500", positions_value="500", cash_value="10000"
    )
    intent = _order(portfolio_id, _NVDA, side, "1", "100")

    result = _permissive_policy().check_order(intent, state)

    if side == "buy":
        assert result.approved is False
        assert any("restricted" in r for r in result.rejection_reasons)
    else:
        assert result.approved is True


# --- check_order: per-order risk budget -------------------------------------


@pytest.mark.parametrize(
    "quantity,expected_approved",
    [
        ("5", True),  # 5*1 = 5 / 1000 = 0.5% == budget, at the boundary passes (not >)
        ("6", False),  # 6 / 1000 = 0.6% > 0.5%
    ],
)
def test_check_order_enforces_max_order_risk_pct_of_nav(
    quantity: str, expected_approved: bool
) -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA)
    state = _state(portfolio_id, (member,), (), nav="1000", positions_value="0", cash_value="1000")
    intent = _order(portfolio_id, _NVDA, "buy", quantity, "1")

    result = _permissive_policy(max_order_risk_pct_of_nav=Decimal("0.005")).check_order(
        intent, state
    )

    assert result.approved is expected_approved
    assert "max_order_risk_pct_of_nav" in result.checked_rules


def test_check_order_sell_side_is_not_subject_to_the_order_risk_budget() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA)
    position = _position(portfolio_id, _NVDA, quantity="1000", average_cost="90")
    state = _state(
        portfolio_id,
        (member,),
        (position,),
        nav="100000",
        positions_value="100000",
        cash_value="0",
    )
    # A huge sell (would massively exceed a 0.5% order-risk budget if it applied).
    intent = _order(portfolio_id, _NVDA, "sell", "1000", "100")

    result = _permissive_policy(max_order_risk_pct_of_nav=Decimal("0.005")).check_order(
        intent, state
    )

    assert "max_order_risk_pct_of_nav" not in result.checked_rules
    assert result.approved is True


def test_check_order_order_risk_budget_fails_closed_when_nav_is_zero() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA)
    state = _state(portfolio_id, (member,), (), nav="0", positions_value="0", cash_value="0")
    intent = _order(portfolio_id, _NVDA, "buy", "1", "100")

    result = _permissive_policy(max_order_risk_pct_of_nav=Decimal("0.005")).check_order(
        intent, state
    )

    assert result.approved is False
    assert any("net_asset_value is 0" in r for r in result.rejection_reasons)


# --- check_order: post-trade single-asset weight cap ------------------------


def test_check_order_blocks_a_buy_that_would_exceed_the_single_asset_cap() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA)
    state = _state(portfolio_id, (member,), (), nav="1000", positions_value="0", cash_value="1000")
    # 3 * 100 = 300 / 1000 = 30% > 25% cap.
    intent = _order(portfolio_id, _NVDA, "buy", "3", "100")

    result = _permissive_policy(max_single_equity_weight=Decimal("0.25")).check_order(intent, state)

    assert result.approved is False
    assert "max_single_asset_weight_post_trade" in result.checked_rules
    assert any("post-trade weight" in r for r in result.rejection_reasons)


def test_check_order_allows_a_buy_within_the_single_asset_cap() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA)
    state = _state(portfolio_id, (member,), (), nav="1000", positions_value="0", cash_value="1000")
    intent = _order(portfolio_id, _NVDA, "buy", "2", "100")  # 200/1000 = 20% <= 25%

    result = _permissive_policy(max_single_equity_weight=Decimal("0.25")).check_order(intent, state)

    assert result.approved is True


def test_check_order_uses_the_documented_default_cap_when_no_member_override() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA)  # no target_weight_max override
    state = _state(portfolio_id, (member,), (), nav="1000", positions_value="0", cash_value="1000")
    # Default equity cap is 20%; 3*100=300/1000=30% breaches it.
    intent = _order(portfolio_id, _NVDA, "buy", "3", "100")

    result = SimpleRiskPolicy(
        min_cash_weight=Decimal("0"), max_order_risk_pct_of_nav=Decimal("1")
    ).check_order(intent, state)

    assert result.approved is False
    assert result.context["max_single_asset_weight_post_trade.cap"] == str(Decimal("0.20"))


def test_check_order_uses_the_crypto_default_cap_for_a_crypto_asset() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _BTC)  # no override -> default crypto cap 15%
    state = _state(portfolio_id, (member,), (), nav="1000", positions_value="0", cash_value="1000")
    # 2*100=200/1000=20% > 15% default crypto cap.
    intent = _order(portfolio_id, _BTC, "buy", "2", "100")

    result = SimpleRiskPolicy(
        min_cash_weight=Decimal("0"), max_order_risk_pct_of_nav=Decimal("1")
    ).check_order(intent, state)

    assert result.approved is False
    assert result.context["max_single_asset_weight_post_trade.cap"] == str(Decimal("0.15"))


def test_check_order_sell_reducing_exposure_still_hits_a_symmetric_weight_cap() -> None:
    # The post-trade weight-cap check only looks at the *resulting* weight,
    # not the direction of change -- a sell that still leaves the position
    # above cap is a pre-existing condition, not one this order caused, but
    # the check is symmetric by design and does not special-case that.
    # Documented here so the behavior is explicit, not accidental.
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA)
    position = _position(portfolio_id, _NVDA, quantity="10", average_cost="90")
    state = _state(
        portfolio_id, (member,), (position,), nav="1000", positions_value="1000", cash_value="0"
    )
    intent = _order(
        portfolio_id, _NVDA, "sell", "1", "100"
    )  # post-trade 9*100/1000=90% still > cap

    result = _permissive_policy(max_single_equity_weight=Decimal("0.1")).check_order(intent, state)

    assert result.approved is False
    assert "max_single_asset_weight_post_trade" in result.checked_rules


# --- check_order: cash floor -------------------------------------------------


def test_check_order_blocks_a_buy_that_would_breach_the_cash_floor() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA)
    state = _state(portfolio_id, (member,), (), nav="1000", positions_value="0", cash_value="1000")
    # Spending 850 leaves 150/1000=15% cash, below a 20% floor.
    intent = _order(portfolio_id, _NVDA, "buy", "8.5", "100")

    result = _permissive_policy(min_cash_weight=Decimal("0.20")).check_order(intent, state)

    assert result.approved is False
    assert "cash_floor_post_trade" in result.checked_rules
    assert any("cash floor" in r for r in result.rejection_reasons)


def test_check_order_allows_a_buy_that_respects_the_cash_floor() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA)
    state = _state(portfolio_id, (member,), (), nav="1000", positions_value="0", cash_value="1000")
    intent = _order(portfolio_id, _NVDA, "buy", "1", "100")  # leaves 900/1000=90% cash

    result = _permissive_policy(min_cash_weight=Decimal("0.20")).check_order(intent, state)

    assert result.approved is True


def test_check_order_sell_is_not_subject_to_the_cash_floor_check() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA)
    position = _position(portfolio_id, _NVDA, quantity="5", average_cost="90")
    state = _state(
        portfolio_id, (member,), (position,), nav="1000", positions_value="500", cash_value="500"
    )
    intent = _order(portfolio_id, _NVDA, "sell", "5", "100")

    result = _permissive_policy(min_cash_weight=Decimal("0.20")).check_order(intent, state)

    assert "cash_floor_post_trade" not in result.checked_rules


# --- check_order: max held positions ----------------------------------------


def test_check_order_blocks_opening_a_new_position_beyond_the_position_cap() -> None:
    portfolio_id = uuid4()
    members = (_member(portfolio_id, _NVDA),)
    position = _position(portfolio_id, _QQQ, quantity="1", average_cost="200")
    state = _state(
        portfolio_id, members, (position,), nav="1000", positions_value="200", cash_value="800"
    )
    intent = _order(portfolio_id, _NVDA, "buy", "1", "100")

    result = _permissive_policy(max_held_positions=1).check_order(intent, state)

    assert result.approved is False
    assert "max_held_positions_post_trade" in result.checked_rules
    assert any("maximum of" in r for r in result.rejection_reasons)


def test_check_order_adding_to_an_existing_position_does_not_count_against_the_cap() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA)
    position = _position(portfolio_id, _NVDA, quantity="1", average_cost="90")
    state = _state(
        portfolio_id, (member,), (position,), nav="1000", positions_value="100", cash_value="900"
    )
    intent = _order(portfolio_id, _NVDA, "buy", "1", "100")

    result = _permissive_policy(max_held_positions=1).check_order(intent, state)

    assert "max_held_positions_post_trade" not in result.checked_rules
    assert result.approved is True


# --- check_order: drawdown stop-line -----------------------------------------


def test_check_order_blocks_a_buy_when_drawdown_breaches_the_stop_line() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA)
    state = _state(
        portfolio_id, (member,), (), nav="900", positions_value="0", cash_value="900"
    )  # nav dropped from a peak of 1000 -> 10% drawdown
    intent = _order(portfolio_id, _NVDA, "buy", "1", "100")

    policy = _permissive_policy(
        drawdown_stop_line=Decimal("0.08"),
        peak_net_asset_value_by_portfolio={portfolio_id: Decimal("1000")},
    )
    result = policy.check_order(intent, state)

    assert result.approved is False
    assert "drawdown_stop_line_blocks_buy" in result.checked_rules
    assert any("drawdown" in r for r in result.rejection_reasons)


def test_check_order_allows_a_sell_even_when_drawdown_breaches_the_stop_line() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA)
    position = _position(portfolio_id, _NVDA, quantity="5", average_cost="90")
    state = _state(
        portfolio_id, (member,), (position,), nav="900", positions_value="500", cash_value="400"
    )
    intent = _order(portfolio_id, _NVDA, "sell", "5", "100")

    policy = _permissive_policy(
        drawdown_stop_line=Decimal("0.08"),
        peak_net_asset_value_by_portfolio={portfolio_id: Decimal("1000")},
    )
    result = policy.check_order(intent, state)

    assert "drawdown_stop_line_blocks_buy" not in result.checked_rules
    assert result.approved is True


def test_check_order_drawdown_check_defaults_to_no_drawdown_without_injected_peak() -> None:
    # No peak_net_asset_value_by_portfolio supplied -> current NAV is treated
    # as its own peak (drawdown 0), a documented open gap, not a crash.
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA)
    state = _state(portfolio_id, (member,), (), nav="900", positions_value="0", cash_value="900")
    intent = _order(portfolio_id, _NVDA, "buy", "1", "100")

    result = _permissive_policy(drawdown_stop_line=Decimal("0.08")).check_order(intent, state)

    assert result.approved is True


# --- check_order: a fully valid order is approved with a full audit trail --


def test_check_order_valid_buy_is_approved_with_all_applicable_rules_checked() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA, target_weight_max=Decimal("0.5"))
    state = _state(
        portfolio_id, (member,), (), nav="10000", positions_value="0", cash_value="10000"
    )
    intent = _order(portfolio_id, _NVDA, "buy", "1", "10")  # tiny, well within every default budget

    result = SimpleRiskPolicy().check_order(intent, state)

    assert result.approved is True
    assert result.rejection_reasons == ()
    for rule in (
        "quantity_positive",
        "estimated_price_positive",
        "asset_must_be_portfolio_member",
        "restricted_member_blocks_buy",
        "max_order_risk_pct_of_nav",
        "max_single_asset_weight_post_trade",
        "cash_floor_post_trade",
        "max_held_positions_post_trade",
        "drawdown_stop_line_blocks_buy",
    ):
        assert rule in result.checked_rules


# --- check_target_weights: sum-to-one and non-negativity -------------------


def test_check_target_weights_rejects_weights_not_summing_to_one() -> None:
    portfolio_id = uuid4()
    state = _state(portfolio_id, (), (), nav="1000", positions_value="0", cash_value="1000")

    result = SimpleRiskPolicy().check_target_weights({_CASH: Decimal("0.9")}, state)

    assert result.approved is False
    assert "target_weights_sum_to_one" in result.checked_rules
    assert any("sum to" in r for r in result.rejection_reasons)


def test_check_target_weights_rejects_negative_weights() -> None:
    portfolio_id = uuid4()
    state = _state(portfolio_id, (), (), nav="1000", positions_value="0", cash_value="1000")
    weights = {_NVDA: Decimal("-0.1"), _CASH: Decimal("1.1")}

    result = SimpleRiskPolicy().check_target_weights(weights, state)

    assert result.approved is False
    assert "target_weights_non_negative" in result.checked_rules


def test_check_target_weights_accepts_a_valid_all_cash_allocation() -> None:
    portfolio_id = uuid4()
    state = _state(portfolio_id, (), (), nav="1000", positions_value="0", cash_value="1000")

    result = SimpleRiskPolicy().check_target_weights({_CASH: Decimal("1")}, state)

    assert result.approved is True
    assert result.rejection_reasons == ()


# --- check_target_weights: per-asset / category weight caps ----------------


def test_check_target_weights_rejects_equity_weight_above_default_cap() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA)  # no override -> default 20%
    state = _state(portfolio_id, (member,), (), nav="1000", positions_value="0", cash_value="1000")
    weights = {_NVDA: Decimal("0.25"), _CASH: Decimal("0.75")}

    result = SimpleRiskPolicy().check_target_weights(weights, state)

    assert result.approved is False
    assert "max_single_asset_weight" in result.checked_rules
    assert _NVDA in result.context["max_single_asset_weight.violations"]


def test_check_target_weights_respects_a_member_specific_override_above_the_default() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA, target_weight_max=Decimal("0.4"))
    state = _state(portfolio_id, (member,), (), nav="1000", positions_value="0", cash_value="1000")
    weights = {_NVDA: Decimal("0.35"), _CASH: Decimal("0.65")}

    result = SimpleRiskPolicy().check_target_weights(weights, state)

    assert result.approved is True


def test_check_target_weights_rejects_total_crypto_exposure_above_budget() -> None:
    portfolio_id = uuid4()
    members = (_member(portfolio_id, _BTC, target_weight_max=Decimal("0.5")),)
    state = _state(portfolio_id, members, (), nav="1000", positions_value="0", cash_value="1000")
    # Single-asset cap overridden high enough that only the category budget trips.
    weights = {_BTC: Decimal("0.35"), _CASH: Decimal("0.65")}

    result = SimpleRiskPolicy().check_target_weights(weights, state)

    assert result.approved is False
    assert "max_crypto_category_weight" in result.checked_rules
    assert any("crypto category" in r for r in result.rejection_reasons)


def test_check_target_weights_allows_crypto_exposure_within_budget() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _BTC, target_weight_max=Decimal("0.5"))
    state = _state(portfolio_id, (member,), (), nav="1000", positions_value="0", cash_value="1000")
    weights = {_BTC: Decimal("0.30"), _CASH: Decimal("0.70")}  # exactly at the 30% budget

    result = SimpleRiskPolicy().check_target_weights(weights, state)

    assert result.approved is True


def test_check_target_weights_rejects_cash_weight_below_the_floor() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA, target_weight_max=Decimal("0.9"))
    state = _state(portfolio_id, (member,), (), nav="1000", positions_value="0", cash_value="1000")
    weights = {_NVDA: Decimal("0.85"), _CASH: Decimal("0.15")}

    result = SimpleRiskPolicy().check_target_weights(weights, state)

    assert result.approved is False
    assert "min_cash_weight" in result.checked_rules


# --- check_target_weights: max held positions -------------------------------


def test_check_target_weights_rejects_too_many_nonzero_positions() -> None:
    portfolio_id = uuid4()
    members = (
        _member(portfolio_id, _NVDA, target_weight_max=Decimal("0.5")),
        _member(portfolio_id, _QQQ, target_weight_max=Decimal("0.5")),
    )
    state = _state(portfolio_id, members, (), nav="1000", positions_value="0", cash_value="1000")
    weights = {_NVDA: Decimal("0.3"), _QQQ: Decimal("0.3"), _CASH: Decimal("0.4")}

    result = SimpleRiskPolicy(max_held_positions=1).check_target_weights(weights, state)

    assert result.approved is False
    assert "max_held_positions" in result.checked_rules


def test_check_target_weights_zero_weight_entries_do_not_count_toward_position_cap() -> None:
    portfolio_id = uuid4()
    members = (
        _member(portfolio_id, _NVDA, target_weight_max=Decimal("0.5")),
        _member(portfolio_id, _QQQ, target_weight_max=Decimal("0.5")),
    )
    state = _state(portfolio_id, members, (), nav="1000", positions_value="0", cash_value="1000")
    weights = {_NVDA: Decimal("0.3"), _QQQ: Decimal("0"), _CASH: Decimal("0.7")}

    result = SimpleRiskPolicy(max_held_positions=1).check_target_weights(weights, state)

    assert result.approved is True


# --- check_target_weights: restricted-member gate ---------------------------


def test_check_target_weights_rejects_a_brand_new_position_in_a_restricted_member() -> None:
    portfolio_id = uuid4()
    member = _member(
        portfolio_id, _NVDA, member_status="restricted", target_weight_max=Decimal("0.5")
    )
    state = _state(portfolio_id, (member,), (), nav="1000", positions_value="0", cash_value="1000")
    weights = {_NVDA: Decimal("0.1"), _CASH: Decimal("0.9")}

    result = SimpleRiskPolicy().check_target_weights(weights, state)

    assert result.approved is False
    assert "restricted_member_no_new_position" in result.checked_rules
    assert _NVDA in result.context["restricted_member_no_new_position.violations"]


def test_check_target_weights_allows_zero_weight_for_a_restricted_member_never_held() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA, member_status="restricted")
    state = _state(portfolio_id, (member,), (), nav="1000", positions_value="0", cash_value="1000")
    weights = {_CASH: Decimal("1")}

    result = SimpleRiskPolicy().check_target_weights(weights, state)

    assert result.approved is True


def test_check_target_weights_does_not_flag_an_already_held_restricted_member() -> None:
    # Documented gap: this narrow check only catches a *new* position from
    # zero quantity -- it cannot compare against a real current weight for
    # an already-held restricted asset without a per-asset price, which
    # check_target_weights's frozen inputs do not provide (see
    # SimpleRiskPolicy._restricted_member_no_new_position's docstring).
    portfolio_id = uuid4()
    member = _member(
        portfolio_id, _NVDA, member_status="restricted", target_weight_max=Decimal("0.9")
    )
    position = _position(portfolio_id, _NVDA, quantity="1", average_cost="90")
    state = _state(
        portfolio_id, (member,), (position,), nav="1000", positions_value="100", cash_value="900"
    )
    weights = {_NVDA: Decimal("0.5"), _CASH: Decimal("0.5")}  # a large proposed *increase*

    result = SimpleRiskPolicy().check_target_weights(weights, state)

    assert "restricted_member_no_new_position" in result.checked_rules
    assert result.context.get("restricted_member_no_new_position.violations") in (None, [])


# --- check_target_weights: drawdown stop-line -------------------------------


def test_check_target_weights_rejects_reduced_cash_when_drawdown_breaches_stop_line() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA, target_weight_max=Decimal("0.5"))
    state = _state(portfolio_id, (member,), (), nav="900", positions_value="0", cash_value="900")
    # Current actual cash weight is 900/900=100%; proposing less cash (90%)
    # is "new exposure" while in stop-line-breached drawdown.
    weights = {_NVDA: Decimal("0.1"), _CASH: Decimal("0.9")}

    policy = SimpleRiskPolicy(
        drawdown_stop_line=Decimal("0.08"),
        peak_net_asset_value_by_portfolio={portfolio_id: Decimal("1000")},
    )
    result = policy.check_target_weights(weights, state)

    assert result.approved is False
    assert "drawdown_stop_line_blocks_new_exposure" in result.checked_rules


def test_check_target_weights_allows_maintaining_or_raising_cash_during_a_stop_line_breach() -> (
    None
):
    portfolio_id = uuid4()
    state = _state(portfolio_id, (), (), nav="900", positions_value="0", cash_value="900")
    weights = {_CASH: Decimal("1")}  # cash weight maintained/raised, not reduced

    policy = SimpleRiskPolicy(
        drawdown_stop_line=Decimal("0.08"),
        peak_net_asset_value_by_portfolio={portfolio_id: Decimal("1000")},
    )
    result = policy.check_target_weights(weights, state)

    assert result.approved is True


# --- replaced_checks ---------------------------------------------------------


@pytest.mark.parametrize("environment", ["research", "backtest"])
def test_replaced_checks_lists_structurally_unavailable_checks_for_research_and_backtest(
    environment: Literal["research", "backtest"],
) -> None:
    policy = SimpleRiskPolicy(environment=environment)

    replaced = policy.replaced_checks()

    assert "trading_hours" in replaced
    assert "manual_confirmation" in replaced
    assert "duplicate_open_order_conflict" in replaced
    assert "emergency_stop_switch" in replaced


@pytest.mark.parametrize("environment", ["paper", "live"])
def test_replaced_checks_is_empty_for_paper_and_live(
    environment: Literal["paper", "live"],
) -> None:
    policy = SimpleRiskPolicy(environment=environment)

    assert policy.replaced_checks() == ()


# --- CONTRACT-003: same signature/behavior across environments -------------


def test_check_methods_behave_identically_across_environments_given_identical_config() -> None:
    """The only thing that may legitimately differ across environments is
    injected configuration (here: identical), never the method signature or
    an environment-conditional branch inside the check logic itself."""
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA, target_weight_max=Decimal("0.5"))
    state = _state(
        portfolio_id, (member,), (), nav="10000", positions_value="0", cash_value="10000"
    )
    intent = _order(portfolio_id, _NVDA, "buy", "1", "10")
    weights = {_NVDA: Decimal("0.3"), _CASH: Decimal("0.7")}
    environments: tuple[Literal["research", "backtest", "paper", "live"], ...] = (
        "research",
        "backtest",
        "paper",
        "live",
    )

    results_order = [
        SimpleRiskPolicy(environment=env).check_order(intent, state) for env in environments
    ]
    results_weights = [
        SimpleRiskPolicy(environment=env).check_target_weights(weights, state)
        for env in environments
    ]

    assert all(r == results_order[0] for r in results_order)
    assert all(r == results_weights[0] for r in results_weights)


# --- determinism / no hidden state ------------------------------------------


def test_check_order_is_deterministic_across_repeated_calls() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA, target_weight_max=Decimal("0.5"))
    state = _state(
        portfolio_id, (member,), (), nav="10000", positions_value="0", cash_value="10000"
    )
    intent = _order(portfolio_id, _NVDA, "buy", "1", "10")

    policy = SimpleRiskPolicy()
    first = policy.check_order(intent, state)
    second = policy.check_order(intent, state)

    assert first == second


def test_check_target_weights_is_deterministic_across_repeated_calls() -> None:
    portfolio_id = uuid4()
    member = _member(portfolio_id, _NVDA, target_weight_max=Decimal("0.5"))
    state = _state(
        portfolio_id, (member,), (), nav="10000", positions_value="0", cash_value="10000"
    )
    weights = {_NVDA: Decimal("0.3"), _CASH: Decimal("0.7")}

    policy = SimpleRiskPolicy()
    first = policy.check_target_weights(weights, state)
    second = policy.check_target_weights(weights, state)

    assert first == second
