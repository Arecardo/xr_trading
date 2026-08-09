"""Strategy entities and interface (CONTRACT-002, implementation_plan/01 §0.2).

Responsibility (doc/technical/roadmap/02_backend_foundation.md BE-001):
versioned ``Strategy`` rules, asset scoring, target-weight generation, and
rebalance suggestions.

Explicitly NOT this subpackage's job: submitting orders or bypassing risk
checks -- a ``Strategy`` only ever outputs suggestions (requirements doc
§2.3 "research and execution separation").

Open question (see BE-001 implementation report): ``Strategy.generate_targets``
is frozen to take an ``analysis: AnalysisResult`` parameter, described as
"BT-002 指标计算库输出" (the BT-002 indicator-calculation library's output).
BT-002 has not been implemented or its contract frozen yet, so
``AnalysisResult`` does not exist anywhere in the codebase. This module
defines a minimal placeholder so the frozen ``Strategy`` signature can be
implemented verbatim; BT-002 should replace it with the real contract
without changing the ``Strategy.generate_targets`` signature itself.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date
from decimal import Decimal
from typing import Protocol
from uuid import UUID

from domain.valuation.models import PortfolioState


@dataclass(frozen=True)
class AnalysisResult:
    """Placeholder for the BT-002 indicator-calculation library's output.

    Not part of CONTRACT-001/002/003. BT-002 owns the real contract; this
    stub only unblocks typing ``Strategy.generate_targets`` per CONTRACT-002
    until BT-002 lands. ``asset_id`` keys mirror ``domain.assets.Asset.asset_id``.
    """

    portfolio_id: UUID
    as_of: date
    indicators_by_asset: dict[str, dict[str, Decimal]]


@dataclass(frozen=True)
class AssetScore:
    """A strategy's score for one asset, with the reasons that produced it."""

    asset_id: str
    score: Decimal  # unit/scale defined per-strategy; the interface assumes no range
    reasons: tuple[str, ...]


@dataclass(frozen=True)
class StrategyOutput:
    """A strategy run's output: scores, target weights, and rebalance notes.

    ``target_weights`` maps ``asset_id`` (including cash, e.g. ``"cash:USD"``)
    to weight; the values must sum to 1.
    """

    portfolio_id: UUID
    as_of: date
    asset_scores: tuple[AssetScore, ...]
    target_weights: dict[str, Decimal]
    rebalance_notes: tuple[str, ...]


class Strategy(Protocol):
    """A versioned strategy: analysis + portfolio state -> target weights.

    Constraints (CONTRACT-002):
    - ``target_weights`` must sum to 1; if not, the strategy layer must
      reject generation outright (``ValueError``/domain error), not defer
      to risk -- strategy input validation and risk output validation are
      two independent gates.
    - Assets with ``member_status == "restricted"`` or
      ``trading_status == "blacklist"`` must not receive a target weight
      above their current holding weight (hold or reduce only, never a new
      buy).
    - When an asset's key technical indicators are missing from
      ``analysis``, no new buy signal may be generated for that asset.
    - Calls must be side-effect free and deterministic: the same
      ``analysis`` + ``portfolio_state`` inputs must always produce the same
      ``StrategyOutput`` (required for backtest reproducibility and for
      reusing the same strategy code across backtest/paper).
    """

    strategy_id: str
    version: str

    def generate_targets(
        self,
        analysis: AnalysisResult,
        portfolio_state: PortfolioState,
    ) -> StrategyOutput:
        """Produce scores/target weights/rebalance notes only.

        Must never submit orders or perform risk checks itself.
        """
        ...
