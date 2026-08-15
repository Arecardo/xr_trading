"""``SimpleRiskPolicy`` (BT-005): the concrete ``RiskPolicy`` implementation.

The single portfolio-level and order-level risk gate reused unchanged across
``backtest``/``paper``/``live`` (CONTRACT-003): the same class, with the same
two check-method signatures, is constructed once per environment with
environment-appropriate configuration (thresholds, ``environment`` label,
injected drawdown history) and never branches its behaviour on anything
other than that fixed configuration + its two frozen-signature arguments.

Threshold defaults and their sourcing (see also the module-level constants
below and the BT-005 task report for the full reasoning):

- Single-asset weight cap (equity 20% / crypto 15%), crypto category weight
  cap (30%), and minimum cash weight (20%) come directly from
  ``doc/ai_quant_trading_system_requirements.md`` §4.3 "默认风险约束" --
  these are the document's own stated *initial suggested values*, explicitly
  overridable per portfolio/asset category (which ``target_weight_max`` on
  ``PortfolioMember`` already provides for the per-asset caps).
- §4.3 gives *ranges*, not single values, for the per-order risk budget
  (0.5%-1%) and the drawdown stop-line (8%-10%). Per this project's
  "risk-first" principle (requirements doc §2 "风险优先：先定义风险预算，再定义
  收益目标和目标仓位"), this module pins the conservative (tighter) end of
  each range as the default -- 0.5% per-order risk, 8% drawdown stop-line --
  rather than an arbitrary midpoint. Both are constructor parameters so a
  specific portfolio can widen them deliberately.
- Maximum held position count has **no** documented value anywhere in
  ``doc/``. The current first-batch asset universe (§4.4) has exactly three
  members (NVDA, QQQ, BTC-USDT), so any default above 3 is inherently
  untested against real constraint pressure today. This module picks 10 as
  a conservative headroom default (a genuine judgment call, flagged in the
  BT-005 report, not a documented figure) -- large enough not to trip over
  the current universe while still being a real, enforced ceiling once the
  universe grows.

Two of the checks a real implementation cannot fully answer from the frozen
``PortfolioState``/``OrderIntent``/``target_weights`` shapes alone are
handled with an explicit, documented simplification rather than a fabricated
number (mirrors ``domain.strategies.simple_rule_strategy``'s refusal to use
``Position.average_cost`` as a stand-in current price):

1. **Per-order and post-trade single-asset weight math** needs a current
   mark price. ``PortfolioState`` carries no per-asset current price (only
   ``Position.average_cost``, a cost basis) -- but ``OrderIntent`` itself
   *does* carry ``estimated_price`` for the one asset being traded, so
   ``check_order`` uses that (the caller's own best estimate for exactly
   that asset, not a fabricated stand-in for an unrelated asset).
   ``check_target_weights`` has no such per-asset price available at all
   (``target_weights`` is already weights, and no analysis/price channel is
   part of CONTRACT-003), so its restricted-member check below is
   deliberately narrower than ``check_order``'s (see
   ``_restricted_member_no_new_position`` for the exact, documented scope).
2. **Drawdown from a peak NAV** needs NAV *history*, which
   ``PortfolioState.latest_snapshot`` (a single snapshot, not a series) does
   not carry, and CONTRACT-003 forbids adding a new parameter/method to
   thread one through. This module accepts an optional
   ``peak_net_asset_value_by_portfolio`` mapping at construction time
   (injected configuration, matching CONTRACT-003's "only via
   portfolio_state/config" rule) and falls back to treating the current NAV
   as its own peak (drawdown = 0, i.e. the check is a no-op) when no entry
   is supplied for a portfolio. **This is a genuine, currently-open gap**:
   until BT-003's event-driven backtest loop exists, nothing in this
   codebase actually tracks a running peak NAV across days. See the BT-005
   report for how this should likely be threaded through once BT-003 lands
   (either the loop reconstructs/updates this mapping once per simulated
   day, or a future CONTRACT-003 revision extends ``PortfolioState`` with a
   short NAV history -- an out-of-scope decision for this task).
"""

from __future__ import annotations

from collections.abc import Mapping
from decimal import Decimal
from typing import Any, Literal
from uuid import UUID

from domain.portfolios.models import PortfolioMember
from domain.risk.errors import InconsistentInputError, InvalidRiskConfigurationError
from domain.risk.models import OrderIntent, RiskCheckResult
from domain.valuation.models import PortfolioState, Position

_ZERO = Decimal("0")
_ONE = Decimal("1")

# doc/ai_quant_trading_system_requirements.md §4.3 "默认风险约束" -- initial
# suggested values, explicitly documented there as overridable per
# portfolio/asset category.
DEFAULT_MAX_SINGLE_EQUITY_WEIGHT = Decimal("0.20")  # 单一美股资产最大权重：20%
DEFAULT_MAX_SINGLE_CRYPTO_WEIGHT = Decimal("0.15")  # 单一加密资产最大权重：15%
DEFAULT_MAX_CRYPTO_CATEGORY_WEIGHT = Decimal("0.30")  # 加密资产类别总权重：30%
DEFAULT_MIN_CASH_WEIGHT = Decimal("0.20")  # 最低现金比例：20%

# §4.3 gives ranges; this module pins the conservative end (see module
# docstring for the "risk-first" rationale) -- both overridable.
DEFAULT_MAX_ORDER_RISK_PCT_OF_NAV = Decimal("0.005")  # 单笔交易最大组合风险：0.5%-1%，取 0.5%
DEFAULT_DRAWDOWN_STOP_LINE = Decimal("0.08")  # 组合最大回撤停止线：8%-10%，取 8%

# Not documented anywhere in doc/ -- a BT-005 judgment call, see module docstring.
DEFAULT_MAX_HELD_POSITIONS = 10

_EQUITY_PREFIX = "equity:"
_CRYPTO_PREFIX = "crypto:"
_CASH_PREFIX = "cash:"

_ENVIRONMENTS: tuple[str, ...] = ("research", "backtest", "paper", "live")

# Checks named in doc/ai_quant_trading_system_requirements.md §6.5 that this
# module does not implement at all (no runtime check exists for them under
# ANY environment today -- see ``replaced_checks`` docstring for why an
# empty tuple for paper/live is not a claim that they are implemented).
_STRUCTURALLY_UNAVAILABLE_IN_RESEARCH_AND_BACKTEST: tuple[str, ...] = (
    "trading_hours",
    "manual_confirmation",
    "duplicate_open_order_conflict",
    "emergency_stop_switch",
)


def _asset_category(asset_id: str) -> Literal["equity", "crypto", "cash", "other"]:
    """Classify ``asset_id`` by its ``project-conventions.md`` §4 prefix.

    Uses the string prefix directly (``"equity:"``/``"crypto:"``/``"cash:"``)
    rather than importing ``domain.assets.Asset`` -- ``PortfolioState`` never
    carries ``Asset`` objects (see ``domain.strategies.models``'s own note on
    this), and the asset-id-prefix convention is itself an already-frozen,
    documented identifier format, not a new assumption introduced here.
    """
    if asset_id.startswith(_EQUITY_PREFIX):
        return "equity"
    if asset_id.startswith(_CRYPTO_PREFIX):
        return "crypto"
    if asset_id.startswith(_CASH_PREFIX):
        return "cash"
    return "other"


def _members_by_asset_id(portfolio_state: PortfolioState) -> dict[str, PortfolioMember]:
    return {m.asset_id: m for m in portfolio_state.members}


def _positions_by_asset_id(portfolio_state: PortfolioState) -> dict[str, Position]:
    return {p.asset_id: p for p in portfolio_state.positions}


def _current_drawdown(
    portfolio_state: PortfolioState,
    peak_net_asset_value_by_portfolio: Mapping[UUID, Decimal] | None,
) -> tuple[Decimal, Decimal]:
    """Return ``(drawdown_fraction, peak_net_asset_value_used)``.

    See the module docstring's point 2 for why ``peak_net_asset_value_by_portfolio``
    is an injected mapping rather than derived from ``portfolio_state`` itself,
    and why an unknown/non-positive peak degrades to "no drawdown" (fail-open
    on this specific sub-computation, not on the overall check -- there is no
    safe fabricated peak to fail-closed against).
    """
    nav = portfolio_state.latest_snapshot.net_asset_value
    peak = None
    if peak_net_asset_value_by_portfolio is not None:
        peak = peak_net_asset_value_by_portfolio.get(portfolio_state.portfolio.portfolio_id)
    if peak is None or peak <= _ZERO:
        peak = nav
    if peak <= _ZERO:
        return _ZERO, peak
    drawdown = (peak - nav) / peak
    if drawdown < _ZERO:
        drawdown = _ZERO  # a new NAV high is not a "negative drawdown"
    return drawdown, peak


class SimpleRiskPolicy:
    """The BT-005 concrete ``RiskPolicy`` implementation.

    Stateless per call and deterministic: both check methods are pure
    functions of their two arguments plus this instance's fixed,
    construction-time configuration (CONTRACT-003) -- no wall-clock time, no
    randomness, no mutable instance state read or written by either check
    method.
    """

    def __init__(
        self,
        *,
        environment: Literal["research", "backtest", "paper", "live"] = "backtest",
        max_single_equity_weight: Decimal = DEFAULT_MAX_SINGLE_EQUITY_WEIGHT,
        max_single_crypto_weight: Decimal = DEFAULT_MAX_SINGLE_CRYPTO_WEIGHT,
        max_crypto_category_weight: Decimal = DEFAULT_MAX_CRYPTO_CATEGORY_WEIGHT,
        min_cash_weight: Decimal = DEFAULT_MIN_CASH_WEIGHT,
        max_order_risk_pct_of_nav: Decimal = DEFAULT_MAX_ORDER_RISK_PCT_OF_NAV,
        max_held_positions: int = DEFAULT_MAX_HELD_POSITIONS,
        drawdown_stop_line: Decimal = DEFAULT_DRAWDOWN_STOP_LINE,
        peak_net_asset_value_by_portfolio: Mapping[UUID, Decimal] | None = None,
    ) -> None:
        """Construct a policy instance for one fixed environment + threshold set.

        ``environment`` only affects ``replaced_checks()``'s return value; it
        must never cause ``check_order``/``check_target_weights`` to branch
        (CONTRACT-003) -- both methods use only the threshold parameters
        below, which are equally valid configuration in any environment.

        Raises ``InvalidRiskConfigurationError`` for any out-of-range
        threshold (fail-closed at construction rather than silently never
        rejecting anything at check time).
        """
        if environment not in _ENVIRONMENTS:
            raise InvalidRiskConfigurationError(
                f"environment={environment!r} must be one of {_ENVIRONMENTS}"
            )
        for name, value in (
            ("max_single_equity_weight", max_single_equity_weight),
            ("max_single_crypto_weight", max_single_crypto_weight),
            ("max_crypto_category_weight", max_crypto_category_weight),
            ("min_cash_weight", min_cash_weight),
            ("max_order_risk_pct_of_nav", max_order_risk_pct_of_nav),
            ("drawdown_stop_line", drawdown_stop_line),
        ):
            if not (_ZERO <= value <= _ONE):
                raise InvalidRiskConfigurationError(f"{name}={value} must be within [0, 1]")
        if max_held_positions <= 0:
            raise InvalidRiskConfigurationError(
                f"max_held_positions={max_held_positions} must be positive"
            )
        if peak_net_asset_value_by_portfolio is not None:
            for portfolio_id, peak in peak_net_asset_value_by_portfolio.items():
                if peak < _ZERO:
                    raise InvalidRiskConfigurationError(
                        f"peak_net_asset_value_by_portfolio[{portfolio_id}]={peak} "
                        "must be non-negative"
                    )

        self._environment = environment
        self._max_single_equity_weight = max_single_equity_weight
        self._max_single_crypto_weight = max_single_crypto_weight
        self._max_crypto_category_weight = max_crypto_category_weight
        self._min_cash_weight = min_cash_weight
        self._max_order_risk_pct_of_nav = max_order_risk_pct_of_nav
        self._max_held_positions = max_held_positions
        self._drawdown_stop_line = drawdown_stop_line
        self._peak_net_asset_value_by_portfolio = peak_net_asset_value_by_portfolio

    # ------------------------------------------------------------------
    # check_order
    # ------------------------------------------------------------------

    def check_order(self, intent: OrderIntent, portfolio_state: PortfolioState) -> RiskCheckResult:
        """Order-level risk checks for a single proposed order.

        Every rule below is appended to ``checked_rules`` regardless of
        outcome (audit requirement); rules that only make sense for
        risk-increasing orders are scoped to ``side == "buy"`` and documented
        as such inline -- a "sell" order can never itself breach a weight
        cap, a cash floor, a per-order risk budget, a position-count ceiling,
        or a drawdown stop-line the way a "buy" can.
        """
        if intent.portfolio_id != portfolio_state.portfolio.portfolio_id:
            raise InconsistentInputError(
                f"intent.portfolio_id={intent.portfolio_id} != "
                f"portfolio_state.portfolio.portfolio_id="
                f"{portfolio_state.portfolio.portfolio_id}"
            )

        checked_rules: list[str] = []
        reasons: list[str] = []
        context: dict[str, Any] = {}

        members = _members_by_asset_id(portfolio_state)
        positions = _positions_by_asset_id(portfolio_state)
        member = members.get(intent.asset_id)
        current_quantity = (
            positions[intent.asset_id].quantity if intent.asset_id in positions else _ZERO
        )
        nav = portfolio_state.latest_snapshot.net_asset_value

        checked_rules.append("quantity_positive")
        if intent.quantity <= _ZERO:
            reasons.append(f"order quantity must be positive, got {intent.quantity}")
            context["quantity_positive.quantity"] = str(intent.quantity)

        checked_rules.append("estimated_price_positive")
        if intent.estimated_price <= _ZERO:
            reasons.append(f"estimated_price must be positive, got {intent.estimated_price}")
            context["estimated_price_positive.estimated_price"] = str(intent.estimated_price)

        valid_notional_inputs = intent.quantity > _ZERO and intent.estimated_price > _ZERO
        order_notional = (
            intent.quantity * intent.estimated_price if valid_notional_inputs else _ZERO
        )

        if intent.side == "buy":
            checked_rules.append("asset_must_be_portfolio_member")
            if member is None:
                reasons.append(
                    f"{intent.asset_id} is not a member of this portfolio; "
                    "cannot buy an asset outside the approved universe"
                )
                context["asset_must_be_portfolio_member.asset_id"] = intent.asset_id

            checked_rules.append("restricted_member_blocks_buy")
            if member is not None and member.member_status == "restricted":
                reasons.append(
                    f"{intent.asset_id} member_status is 'restricted': buy orders are blocked "
                    "(restricted members may only be held or reduced, per the same rule "
                    "domain.strategies.simple_rule_strategy enforces -- this is the "
                    "independent order-level gate on top of that strategy-level gate)"
                )
                context["restricted_member_blocks_buy.member_status"] = member.member_status

            checked_rules.append("max_order_risk_pct_of_nav")
            if nav > _ZERO:
                order_risk_pct = order_notional / nav
                if order_risk_pct > self._max_order_risk_pct_of_nav:
                    reasons.append(
                        f"order notional is {order_risk_pct:.4%} of NAV, exceeds the per-order "
                        f"risk budget of {self._max_order_risk_pct_of_nav:.4%}"
                    )
                    context["max_order_risk_pct_of_nav.actual"] = str(order_risk_pct)
                    context["max_order_risk_pct_of_nav.limit"] = str(
                        self._max_order_risk_pct_of_nav
                    )
            else:
                reasons.append(
                    "portfolio net_asset_value is 0 or unknown; cannot evaluate the per-order "
                    "risk budget, fail-closed"
                )
                context["max_order_risk_pct_of_nav.net_asset_value"] = str(nav)

        checked_rules.append("max_single_asset_weight_post_trade")
        category = _asset_category(intent.asset_id)
        cap = self._single_asset_cap(category, member)
        if cap is not None:
            if nav > _ZERO:
                signed_delta = intent.quantity if intent.side == "buy" else -intent.quantity
                post_trade_quantity = current_quantity + signed_delta
                post_trade_value = (
                    post_trade_quantity * intent.estimated_price
                    if post_trade_quantity > _ZERO
                    else _ZERO
                )
                post_trade_weight = post_trade_value / nav
                if post_trade_weight > cap:
                    reasons.append(
                        f"post-trade weight for {intent.asset_id} would be "
                        f"{post_trade_weight:.4%}, exceeds the single-asset cap of {cap:.4%}"
                    )
                    context["max_single_asset_weight_post_trade.post_trade_weight"] = str(
                        post_trade_weight
                    )
                    context["max_single_asset_weight_post_trade.cap"] = str(cap)
            else:
                reasons.append(
                    "portfolio net_asset_value is 0 or unknown; cannot evaluate the post-trade "
                    "single-asset weight cap, fail-closed"
                )
                context["max_single_asset_weight_post_trade.net_asset_value"] = str(nav)

        if intent.side == "buy":
            checked_rules.append("cash_floor_post_trade")
            current_cash = portfolio_state.latest_snapshot.cash_value
            if nav > _ZERO:
                post_trade_cash_weight = (current_cash - order_notional) / nav
                if post_trade_cash_weight < self._min_cash_weight:
                    reasons.append(
                        f"post-trade cash weight would be {post_trade_cash_weight:.4%}, below the "
                        f"minimum cash floor of {self._min_cash_weight:.4%}"
                    )
                    context["cash_floor_post_trade.post_trade_cash_weight"] = str(
                        post_trade_cash_weight
                    )
                    context["cash_floor_post_trade.min_cash_weight"] = str(self._min_cash_weight)
            else:
                reasons.append(
                    "portfolio net_asset_value is 0 or unknown; cannot evaluate the cash floor, "
                    "fail-closed"
                )
                context["cash_floor_post_trade.net_asset_value"] = str(nav)

            if current_quantity == _ZERO:
                checked_rules.append("max_held_positions_post_trade")
                held_count = sum(1 for p in portfolio_state.positions if p.quantity != _ZERO)
                if held_count >= self._max_held_positions:
                    reasons.append(
                        f"portfolio already holds {held_count} position(s), at the maximum of "
                        f"{self._max_held_positions}; cannot open a new position in "
                        f"{intent.asset_id}"
                    )
                    context["max_held_positions_post_trade.held_count"] = held_count
                    context["max_held_positions_post_trade.limit"] = self._max_held_positions

            checked_rules.append("drawdown_stop_line_blocks_buy")
            drawdown, peak = _current_drawdown(
                portfolio_state, self._peak_net_asset_value_by_portfolio
            )
            if drawdown >= self._drawdown_stop_line:
                reasons.append(
                    f"portfolio drawdown from peak NAV is {drawdown:.4%}, at or beyond the stop "
                    f"line of {self._drawdown_stop_line:.4%}; new/increased exposure is blocked"
                )
                context["drawdown_stop_line_blocks_buy.drawdown"] = str(drawdown)
                context["drawdown_stop_line_blocks_buy.stop_line"] = str(self._drawdown_stop_line)
                context["drawdown_stop_line_blocks_buy.peak_net_asset_value"] = str(peak)

        return RiskCheckResult(
            approved=not reasons,
            rejection_reasons=tuple(reasons),
            checked_rules=tuple(checked_rules),
            context=context,
        )

    def _single_asset_cap(
        self, category: Literal["equity", "crypto", "cash", "other"], member: PortfolioMember | None
    ) -> Decimal | None:
        """The applicable single-asset weight cap, or ``None`` if the category has none.

        ``PortfolioMember.target_weight_max`` overrides the category default
        when set, mirroring how ``domain.strategies.simple_rule_strategy``
        already treats it as the per-asset ceiling.
        """
        if member is not None and member.target_weight_max is not None:
            return member.target_weight_max
        if category == "equity":
            return self._max_single_equity_weight
        if category == "crypto":
            return self._max_single_crypto_weight
        return None

    # ------------------------------------------------------------------
    # check_target_weights
    # ------------------------------------------------------------------

    def check_target_weights(
        self, target_weights: dict[str, Decimal], portfolio_state: PortfolioState
    ) -> RiskCheckResult:
        """Portfolio-level risk checks for a proposed set of target weights.

        Note this is an *independent* check from the strategy layer's own
        output validation (``domain.strategies.simple_rule_strategy``
        enforces "sums to 1" on its own output before returning it) --
        CONTRACT-002's own commentary calls these "两道独立关卡" (two
        independent gates), so this method re-validates the sum-to-one and
        non-negativity constraints rather than trusting the caller.
        """
        checked_rules: list[str] = []
        reasons: list[str] = []
        context: dict[str, Any] = {}

        members = _members_by_asset_id(portfolio_state)
        positions = _positions_by_asset_id(portfolio_state)
        nav = portfolio_state.latest_snapshot.net_asset_value

        checked_rules.append("target_weights_non_negative")
        negative = {asset_id: w for asset_id, w in target_weights.items() if w < _ZERO}
        if negative:
            reasons.append(f"target_weights contains negative weight(s): {sorted(negative)}")
            context["target_weights_non_negative.negative_weights"] = {
                k: str(v) for k, v in negative.items()
            }

        checked_rules.append("target_weights_sum_to_one")
        total = sum(target_weights.values(), start=_ZERO)
        if total != _ONE:
            reasons.append(f"target_weights sum to {total}, expected exactly 1")
            context["target_weights_sum_to_one.total"] = str(total)

        checked_rules.append("max_single_asset_weight")
        weight_violations: dict[str, dict[str, str]] = {}
        for asset_id, weight in target_weights.items():
            category = _asset_category(asset_id)
            cap = self._single_asset_cap(category, members.get(asset_id))
            if cap is not None and weight > cap:
                weight_violations[asset_id] = {"weight": str(weight), "cap": str(cap)}
        if weight_violations:
            reasons.append(
                f"target weight exceeds the single-asset cap for: {sorted(weight_violations)}"
            )
            context["max_single_asset_weight.violations"] = weight_violations

        checked_rules.append("max_crypto_category_weight")
        crypto_total = sum(
            (w for asset_id, w in target_weights.items() if _asset_category(asset_id) == "crypto"),
            start=_ZERO,
        )
        if crypto_total > self._max_crypto_category_weight:
            reasons.append(
                f"total crypto category weight is {crypto_total:.4%}, exceeds the budget of "
                f"{self._max_crypto_category_weight:.4%}"
            )
            context["max_crypto_category_weight.actual"] = str(crypto_total)
            context["max_crypto_category_weight.limit"] = str(self._max_crypto_category_weight)

        checked_rules.append("min_cash_weight")
        cash_total = sum(
            (w for asset_id, w in target_weights.items() if _asset_category(asset_id) == "cash"),
            start=_ZERO,
        )
        if cash_total < self._min_cash_weight:
            reasons.append(
                f"total cash weight is {cash_total:.4%}, below the minimum cash floor of "
                f"{self._min_cash_weight:.4%}"
            )
            context["min_cash_weight.actual"] = str(cash_total)
            context["min_cash_weight.limit"] = str(self._min_cash_weight)

        checked_rules.append("max_held_positions")
        held_count = sum(
            1
            for asset_id, w in target_weights.items()
            if _asset_category(asset_id) != "cash" and w > _ZERO
        )
        if held_count > self._max_held_positions:
            reasons.append(
                f"target_weights would hold {held_count} position(s), exceeds the maximum of "
                f"{self._max_held_positions}"
            )
            context["max_held_positions.held_count"] = held_count
            context["max_held_positions.limit"] = self._max_held_positions

        checked_rules.append("restricted_member_no_new_position")
        restricted_violations = self._restricted_member_no_new_position(
            target_weights, portfolio_state, positions
        )
        if restricted_violations:
            reasons.append(
                "target_weights proposes a brand-new nonzero position in restricted "
                f"member(s) currently held at zero quantity: {sorted(restricted_violations)} "
                "(restricted members may only be held or reduced, never newly bought)"
            )
            context["restricted_member_no_new_position.violations"] = sorted(restricted_violations)

        checked_rules.append("drawdown_stop_line_blocks_new_exposure")
        drawdown, peak = _current_drawdown(portfolio_state, self._peak_net_asset_value_by_portfolio)
        if drawdown >= self._drawdown_stop_line:
            current_cash_weight = (
                portfolio_state.latest_snapshot.cash_value / nav if nav > _ZERO else _ONE
            )
            if cash_total < current_cash_weight:
                reasons.append(
                    f"portfolio drawdown from peak NAV is {drawdown:.4%}, at or beyond the stop "
                    f"line of {self._drawdown_stop_line:.4%}; target_weights' cash weight "
                    f"({cash_total:.4%}) is below the current actual cash weight "
                    f"({current_cash_weight:.4%}), i.e. it would add net risk exposure, which is "
                    "blocked"
                )
                context["drawdown_stop_line_blocks_new_exposure.drawdown"] = str(drawdown)
                context["drawdown_stop_line_blocks_new_exposure.stop_line"] = str(
                    self._drawdown_stop_line
                )
                context["drawdown_stop_line_blocks_new_exposure.proposed_cash_weight"] = str(
                    cash_total
                )
                context["drawdown_stop_line_blocks_new_exposure.current_cash_weight"] = str(
                    current_cash_weight
                )

        return RiskCheckResult(
            approved=not reasons,
            rejection_reasons=tuple(reasons),
            checked_rules=tuple(checked_rules),
            context=context,
        )

    @staticmethod
    def _restricted_member_no_new_position(
        target_weights: dict[str, Decimal],
        portfolio_state: PortfolioState,
        positions: dict[str, Position],
    ) -> list[str]:
        """The narrow, price-free subset of "restricted may only hold/reduce" this
        method can enforce without a per-asset current price.

        ``check_target_weights`` receives no per-asset price (see module
        docstring point 1): it cannot compute a restricted member's *current*
        weight to compare against its proposed weight in the general case.
        What it CAN determine exactly, with no price at all, is whether a
        restricted member currently held at zero quantity is being proposed
        a brand-new nonzero weight -- that is unambiguously "a new buy", the
        clearest violation, and is fail-closed here. Restricted members
        already held at nonzero quantity are NOT further checked by this
        rule (a real weight-delta comparison for those needs a price this
        method's frozen inputs do not provide) -- flagged as an open gap in
        the BT-005 report.
        """
        violations: list[str] = []
        for member in portfolio_state.members:
            if member.member_status != "restricted":
                continue
            proposed = target_weights.get(member.asset_id, _ZERO)
            current_quantity = (
                positions[member.asset_id].quantity if member.asset_id in positions else _ZERO
            )
            if proposed > _ZERO and current_quantity == _ZERO:
                violations.append(member.asset_id)
        return violations

    # ------------------------------------------------------------------
    # replaced_checks
    # ------------------------------------------------------------------

    def replaced_checks(self) -> tuple[str, ...]:
        """Checks substituted by an alternate implementation in this environment.

        Returns the four ``doc/ai_quant_trading_system_requirements.md``
        §6.5 checks this codebase cannot structurally run for real under
        ``research``/``backtest`` -- there is no live market clock to check
        real trading hours against, no human to ask for manual confirmation,
        no live broker order book to detect a duplicate open order against,
        and no live kill switch to wire an emergency stop to.

        For ``paper``/``live`` this returns an empty tuple. **That is not a
        claim these four checks are implemented** -- none of
        ``trading_hours``/``manual_confirmation``/``duplicate_open_order_conflict``/
        ``emergency_stop_switch`` has a real runtime check anywhere in this
        module today (out of BT-005's scope, which is the portfolio/order
        weight-and-exposure checks only). An empty tuple here means "unlike
        backtest, this environment cannot structurally excuse skipping them"
        -- building real implementations is a genuine prerequisite before
        ``paper``/``live`` should be trusted, tracked as an open gap in the
        BT-005 report, not asserted as done by this method's return value.
        """
        if self._environment in ("research", "backtest"):
            return _STRUCTURALLY_UNAVAILABLE_IN_RESEARCH_AND_BACKTEST
        return ()
