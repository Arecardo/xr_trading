"""Unit tests for domain.strategies: instantiation and target-weight shape."""

from __future__ import annotations

from datetime import date
from decimal import Decimal
from uuid import uuid4

from domain.strategies import AssetScore, StrategyOutput


def test_asset_score_instantiates_with_expected_field_types() -> None:
    score = AssetScore(
        asset_id="equity:nasdaq:NVDA",
        score=Decimal("0.82"),
        reasons=("momentum_20d_positive", "above_200d_ma"),
    )

    assert score.asset_id == "equity:nasdaq:NVDA"
    assert isinstance(score.score, Decimal)
    assert score.reasons == ("momentum_20d_positive", "above_200d_ma")


def test_strategy_output_target_weights_sum_to_one_in_a_valid_case() -> None:
    portfolio_id = uuid4()
    output = StrategyOutput(
        portfolio_id=portfolio_id,
        as_of=date(2026, 8, 8),
        asset_scores=(AssetScore(asset_id="equity:nasdaq:NVDA", score=Decimal("0.8"), reasons=()),),
        target_weights={
            "equity:nasdaq:NVDA": Decimal("0.6"),
            "cash:USD": Decimal("0.4"),
        },
        rebalance_notes=("increase NVDA to target",),
    )

    assert output.portfolio_id == portfolio_id
    assert sum(output.target_weights.values()) == Decimal("1")
