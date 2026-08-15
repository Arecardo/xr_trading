"""``SimpleRuleStrategy`` (BT-004): the concrete rule-based ``Strategy``.

A single, deliberately simple rule strategy that scores each portfolio
member from BT-002's raw indicators and turns scores into target weights via
fixed thresholds -- the roadmap's "如分资产类型加权评分 + 阈值决策" suggestion,
minus the "按资产类型加权" part (this strategy scores every asset with the
same rule regardless of ``asset_type``; per-type weighting is a reasonable
future extension, not implemented here to keep the first version simple).

Scoring rule (plain language -- also see the per-component docstrings below):

For each portfolio member with enough indicator history (``ma20`` and
``price_position_20d`` both available, i.e. at least 20 calendar days of
data), sum six signed components derived directly from BT-002's raw numbers:

1. MA5-vs-MA20 direction (``ma_trend``): +1 if "up", -1 if "down", 0 if
   "flat" (mirrors 04_analysis_modules.md's "5 日均线高于 20 日均线，加分").
2. Close vs MA20: +1 above, -1 below, 0 if equal or MA20 unavailable
   (mirrors "收盘价高于 20 日... 均线，加分").
3. Close vs MA50: +1 above, -1 below, 0 if equal or MA50 unavailable
   (mirrors the same rule's "...和 50 日均线" half; MA50 needs 50 days of
   history so is frequently ``None`` even when MA20 is not -- that is not
   treated as insufficient history for the strategy as a whole, only this
   one component drops out).
4. RSI overbought: -1 if RSI14 >= 70, else 0 (mirrors "RSI 过高，降低追涨评分").
   Deliberately one-sided: the source doc only calls out *high* RSI; this
   strategy does not also penalize low RSI, to avoid inventing an untethered
   rule the source table does not state.
5. 20-day breakdown: -2 if ``price_position_20d`` <= 0.05 (today's close is
   at or within 5% of the trailing-20-day low), else 0 (mirrors "跌破 20 日
   低点，大幅扣分" -- the larger magnitude here matches "大幅"/"significant").
6. Relative strength vs benchmark: +1 if ``relative_strength_20d`` > 0, -1 if
   < 0, 0 if exactly 0 or unavailable (mirrors "强于对应资产类别基准，加分").

Two indicators from 04_analysis_modules.md's suggestion table are
deliberately NOT used, and are called out here rather than left implicit:

- ATR ("ATR 显著升高，降低仓位建议") is skipped. "显著升高" (a *significant
  increase*) requires comparing today's ATR to a historical baseline/average
  ATR, which BT-002's ``IndicatorBar`` does not expose (only the current
  ``atr14`` value, no rolling history of it) -- and this strategy's
  ``AnalysisResult`` is deliberately scoped to one date at a time (see
  ``domain.strategies.models``), so there is no baseline available to
  compare against without inventing an arbitrary threshold. Position
  sizing from volatility is also arguably a risk-policy concern (BT-005),
  not a scoring concern.
- ``trend_state``/``momentum_state``/``overheated``/``breakdown`` (the
  qualitative classification fields from 04_analysis_modules.md's JSON
  example) do not exist -- BT-002 explicitly scoped those out as a later
  task's job. This strategy computes its breakdown/momentum logic directly
  from the raw numbers instead (components 1, 3, 5 above).

Score -> bucket -> desired weight (before the constraint clamps below):

- score >= 2 ("buy"): desired weight = the member's ``target_weight_max`` if
  set on ``PortfolioMember``, else a default single-asset ceiling of 20%.
- -2 < score < 2 ("hold"): desired weight = the asset's current weight
  (maintain, no active decision).
- score <= -2 ("sell"): desired weight = 0 (full exit).

An asset lacking sufficient indicator history never goes through the bucket
logic above at all -- its desired weight is fixed at its current weight
(maintain-only), which trivially satisfies "no new buy signal"
(CONTRACT-002 constraint c) whether or not it is currently held (current
weight is 0 for an unheld asset, so "maintain" means "stays at 0").

Constraint clamps (applied per-asset, after the bucket desired weight, in
this order):

- (b) restricted/blacklisted: ``final = min(desired, current_weight)`` --
  the desired weight from the bucket logic above is capped at the current
  holding weight, so a "buy" bucket can never push a restricted/blacklisted
  asset's weight up, while "hold"/"sell" outcomes (which are already
  <= current weight) pass through unchanged.

Then, globally: if the sum of all per-asset final weights exceeds 1 (can
happen once enough assets land in the "buy" bucket simultaneously), every
non-cash weight is scaled down proportionally by ``1 / raw_total`` so the
total is exactly 1; cash absorbs the (non-negative) remainder in both cases.
Cash is always the residual -- ``target_weights[cash_id] = 1 -
sum(other weights)`` -- which is how constraint (a) (sum must be exactly 1)
holds by construction, not by rounding, plus an explicit runtime check
before returning (see ``_build_target_weights``).
"""

from __future__ import annotations

from dataclasses import dataclass
from decimal import Decimal

from domain.strategies.errors import (
    InconsistentInputError,
    InvalidTargetWeightsError,
    MissingPriceDataError,
)
from domain.strategies.models import AnalysisResult, AssetAnalysis, AssetScore, StrategyOutput
from domain.valuation.models import PortfolioState, Position

_BUY_THRESHOLD = Decimal("2")
_SELL_THRESHOLD = Decimal("-2")
_RSI_OVERBOUGHT = Decimal("70")
_BREAKDOWN_THRESHOLD = Decimal("0.05")
_DEFAULT_MAX_ASSET_WEIGHT = Decimal("0.20")
_ZERO = Decimal("0")
_ONE = Decimal("1")


@dataclass(frozen=True)
class _AssetEvaluation:
    """Internal scratch result for one portfolio member, before global scaling."""

    score: Decimal
    reasons: tuple[str, ...]
    desired_weight: Decimal  # after the restricted/blacklist clamp, before scaling
    has_sufficient_history: bool


def _current_weights(
    portfolio_state: PortfolioState, analysis: AnalysisResult
) -> dict[str, Decimal]:
    """Each held asset's current weight (position market value / NAV).

    Requires a real price from ``analysis.assets`` for every position with
    nonzero quantity -- raises ``MissingPriceDataError`` rather than
    falling back to ``Position.average_cost`` (a cost basis, not a current
    price; see ``domain.strategies.errors.MissingPriceDataError``).
    """
    nav = portfolio_state.latest_snapshot.net_asset_value
    positions: tuple[Position, ...] = portfolio_state.positions
    nonzero_positions = [p for p in positions if p.quantity != _ZERO]

    if nav == _ZERO:
        if nonzero_positions:
            raise MissingPriceDataError(
                "portfolio_state.latest_snapshot.net_asset_value is 0 but "
                f"{len(nonzero_positions)} position(s) have nonzero quantity; "
                "cannot compute current weights"
            )
        return {}

    weights: dict[str, Decimal] = {}
    for position in nonzero_positions:
        asset_analysis = analysis.assets.get(position.asset_id)
        if asset_analysis is None:
            raise MissingPriceDataError(
                f"held position {position.asset_id!r} (quantity={position.quantity}) has no "
                "AssetAnalysis entry in analysis.assets; cannot compute its current weight "
                "without a real current price"
            )
        weights[position.asset_id] = (position.quantity * asset_analysis.close) / nav
    return weights


def _score_asset(asset_analysis: AssetAnalysis) -> tuple[Decimal, tuple[str, ...]]:
    """Sum the six score components described in the module docstring."""
    ind = asset_analysis.indicators
    score = _ZERO
    reasons: list[str] = []

    if ind.ma_trend == "up":
        score += 1
        reasons.append("ma5>ma20 (ma_trend=up): +1")
    elif ind.ma_trend == "down":
        score -= 1
        reasons.append("ma5<ma20 (ma_trend=down): -1")

    # close-vs-ma20 / close-vs-ma50 need the raw close price, which lives on
    # AssetAnalysis (not IndicatorBar -- see domain.strategies.models).
    close = asset_analysis.close
    if ind.ma20 is not None:
        if close > ind.ma20:
            score += 1
            reasons.append("close>ma20: +1")
        elif close < ind.ma20:
            score -= 1
            reasons.append("close<ma20: -1")
    if ind.ma50 is not None:
        if close > ind.ma50:
            score += 1
            reasons.append("close>ma50: +1")
        elif close < ind.ma50:
            score -= 1
            reasons.append("close<ma50: -1")

    if ind.rsi14 is not None and ind.rsi14 >= _RSI_OVERBOUGHT:
        score -= 1
        reasons.append(f"rsi14={ind.rsi14}>=70 (overbought): -1")

    if ind.price_position_20d is not None and ind.price_position_20d <= _BREAKDOWN_THRESHOLD:
        score -= 2
        reasons.append(f"price_position_20d={ind.price_position_20d}<=0.05 (near 20d low): -2")

    if ind.relative_strength_20d is not None:
        if ind.relative_strength_20d > _ZERO:
            score += 1
            reasons.append(f"relative_strength_20d={ind.relative_strength_20d}>0: +1")
        elif ind.relative_strength_20d < _ZERO:
            score -= 1
            reasons.append(f"relative_strength_20d={ind.relative_strength_20d}<0: -1")

    return score, tuple(reasons)


def _bucket_desired_weight(
    score: Decimal, current_weight: Decimal, target_weight_max: Decimal | None
) -> tuple[Decimal, str]:
    """Score -> bucket -> desired weight, before any constraint clamp."""
    if score >= _BUY_THRESHOLD:
        desired = target_weight_max if target_weight_max is not None else _DEFAULT_MAX_ASSET_WEIGHT
        return desired, f"score={score}>=2 (buy): desired weight {desired}"
    if score <= _SELL_THRESHOLD:
        return _ZERO, f"score={score}<=-2 (sell): desired weight 0"
    return current_weight, f"score={score} (hold): maintain current weight {current_weight}"


class SimpleRuleStrategy:
    """The BT-004 simple rule-based ``Strategy`` implementation.

    Stateless and deterministic: ``generate_targets`` is a pure function of
    its two arguments (CONTRACT-002 constraint d) -- no wall-clock time, no
    randomness, no mutable instance state read or written.
    """

    strategy_id: str
    version: str

    def __init__(self, strategy_id: str = "simple_rule_v1", version: str = "1.0.0") -> None:
        self.strategy_id = strategy_id
        self.version = version

    def generate_targets(
        self,
        analysis: AnalysisResult,
        portfolio_state: PortfolioState,
    ) -> StrategyOutput:
        if analysis.portfolio_id != portfolio_state.portfolio.portfolio_id:
            raise InconsistentInputError(
                f"analysis.portfolio_id={analysis.portfolio_id} != "
                f"portfolio_state.portfolio.portfolio_id="
                f"{portfolio_state.portfolio.portfolio_id}"
            )

        current_weights = _current_weights(portfolio_state, analysis)

        evaluations: dict[str, _AssetEvaluation] = {}
        for member in portfolio_state.members:
            asset_id = member.asset_id
            current_weight = current_weights.get(asset_id, _ZERO)
            asset_analysis = analysis.assets.get(asset_id)

            has_sufficient_history = (
                asset_analysis is not None
                and asset_analysis.indicators.ma20 is not None
                and asset_analysis.indicators.price_position_20d is not None
            )
            is_restricted = member.member_status == "restricted"
            is_blacklisted = (
                asset_analysis is not None and asset_analysis.trading_status == "blacklist"
            )

            reasons: tuple[str, ...]
            if not has_sufficient_history:
                score = _ZERO
                reasons = (
                    "insufficient indicator history (ma20/price_position_20d unavailable): "
                    "no new-buy signal, holding at current weight",
                )
                desired = current_weight
                bucket_reason = "maintain current weight (no score computed)"
            else:
                assert asset_analysis is not None  # narrowed by has_sufficient_history
                score, score_reasons = _score_asset(asset_analysis)
                desired, bucket_reason = _bucket_desired_weight(
                    score, current_weight, member.target_weight_max
                )
                reasons = score_reasons

            final_reasons: tuple[str, ...] = reasons + (bucket_reason,)
            if (is_restricted or is_blacklisted) and desired > current_weight:
                trading_status = (
                    asset_analysis.trading_status if asset_analysis is not None else None
                )
                final_reasons = final_reasons + (
                    f"capped at current weight {current_weight}: "
                    f"member_status={member.member_status!r}, trading_status={trading_status!r} "
                    "(restricted/blacklisted assets may only hold or reduce)",
                )
                desired = min(desired, current_weight)

            evaluations[asset_id] = _AssetEvaluation(
                score=score,
                reasons=final_reasons,
                desired_weight=desired,
                has_sufficient_history=has_sufficient_history,
            )

        target_weights, rebalance_notes = _build_target_weights(
            evaluations, portfolio_state.portfolio.base_currency
        )

        asset_scores = tuple(
            AssetScore(asset_id=asset_id, score=ev.score, reasons=ev.reasons)
            for asset_id, ev in evaluations.items()
        )

        return StrategyOutput(
            portfolio_id=portfolio_state.portfolio.portfolio_id,
            as_of=analysis.as_of,
            asset_scores=asset_scores,
            target_weights=target_weights,
            rebalance_notes=rebalance_notes,
        )


def _ensure_weights_sum_to_one(target_weights: dict[str, Decimal]) -> None:
    """Runtime enforcement of CONTRACT-002 constraint (a).

    Cash is constructed as the exact residual (see ``_build_target_weights``),
    so this should always hold by construction; kept as an explicit,
    independently testable check rather than relying on that invariant
    silently, per the task brief's "enforce as actual runtime checks, not
    just docstring prose."
    """
    total = sum(target_weights.values(), start=_ZERO)
    if total != _ONE:
        raise InvalidTargetWeightsError(
            f"target_weights sum to {total}, expected exactly 1: {target_weights}"
        )


def _build_target_weights(
    evaluations: dict[str, _AssetEvaluation], base_currency: str
) -> tuple[dict[str, Decimal], tuple[str, ...]]:
    """Turn per-asset desired weights into a ``target_weights`` mapping summing to 1.

    Scales all non-cash desired weights down proportionally if their raw sum
    exceeds 1; cash is always the exact residual, which is what guarantees
    CONTRACT-002 constraint (a) by construction (see module docstring).
    """
    notes: list[str] = []
    raw_total = sum((ev.desired_weight for ev in evaluations.values()), start=_ZERO)

    if raw_total > _ONE:
        scale_factor = _ONE / raw_total
        scaled = {
            asset_id: ev.desired_weight * scale_factor for asset_id, ev in evaluations.items()
        }
        notes.append(
            f"raw desired weights summed to {raw_total} (> 1); scaled all non-cash weights "
            f"down by a factor of {scale_factor} so cash weight is non-negative"
        )
    else:
        scaled = {asset_id: ev.desired_weight for asset_id, ev in evaluations.items()}

    cash_asset_id = f"cash:{base_currency}"
    cash_weight = _ONE - sum(scaled.values(), start=_ZERO)

    target_weights: dict[str, Decimal] = {
        asset_id: weight for asset_id, weight in scaled.items() if weight != _ZERO
    }
    target_weights[cash_asset_id] = cash_weight

    _ensure_weights_sum_to_one(target_weights)

    insufficient = [
        asset_id for asset_id, ev in evaluations.items() if not ev.has_sufficient_history
    ]
    if insufficient:
        notes.append(
            f"{len(insufficient)} asset(s) held at current weight due to insufficient "
            f"indicator history: {insufficient}"
        )

    return target_weights, tuple(notes)
