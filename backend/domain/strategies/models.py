"""Strategy entities and interface (CONTRACT-002, implementation_plan/01 §0.2).

Responsibility (doc/technical/roadmap/02_backend_foundation.md BE-001):
versioned ``Strategy`` rules, asset scoring, target-weight generation, and
rebalance suggestions.

Explicitly NOT this subpackage's job: submitting orders or bypassing risk
checks -- a ``Strategy`` only ever outputs suggestions (requirements doc
§2.3 "research and execution separation").

``AnalysisResult`` real shape (BT-004, replaces the BE-001 placeholder):
``Strategy.generate_targets``'s ``analysis: AnalysisResult`` parameter is
described by CONTRACT-002 as "BT-002 指标计算库输出" (BT-002's indicator
library output). BT-002 (``backtest.indicators``) is the only real analysis
data source in this repo today -- ``04_analysis_modules.md``'s news/
sentiment/fundamental/market-regime inputs (its §3-5) are not built and out
of scope for any currently dispatched task, so ``AnalysisResult`` is
deliberately *not* over-generalized to anticipate them; extend it when those
analyses actually exist.

``AnalysisResult`` bundles, per asset, more than a bare ``IndicatorBar``:

- ``indicators: IndicatorBar`` -- BT-002's eight raw technical indicators.
- ``close: Decimal`` and ``price_status`` -- threaded from BT-001's
  ``AlignedBar`` (not part of ``IndicatorBar``, which holds only *derived*
  indicators, never a raw price). A real current price is required to
  convert a held ``Position.quantity`` into a portfolio weight for
  CONTRACT-002 constraint (b) ("no new buy above current held weight");
  ``Position.average_cost`` is a cost basis, not a current price, and using
  it as a stand-in would silently misstate weights.
- ``trading_status`` -- threaded from ``domain.assets.Asset.trading_status``.
  CONTRACT-002 constraint (b) names ``Asset.trading_status == "blacklist"``
  as one of two independent triggers (the other being
  ``PortfolioMember.member_status == "restricted"``, already available via
  ``PortfolioState.members``), but nothing else
  ``Strategy.generate_targets`` receives carries it: ``PortfolioState``
  never includes ``Asset`` objects, and ``IndicatorBar`` has no such field.
  Since ``AnalysisResult``'s shape is this task's to define, it is the only
  available channel for this real, present-task requirement -- as opposed
  to hypothetical future inputs this module intentionally does not
  anticipate. This is a genuine cross-subpackage coupling worth flagging to
  the integration lead: it means whatever assembles a real ``AnalysisResult``
  (application layer, not built yet) must join ``Asset.trading_status`` in
  alongside the indicator computation.

Note for the integration lead: importing ``backtest.indicators.IndicatorBar``
here makes ``domain.strategies`` depend on the ``backtest`` package. That is
directed by the BT-004 task brief (BT-002's indicator library is "the ONLY
real analysis data source that exists in this repo right now") and passes
the existing ``test_no_infra_leakage`` check (``backtest`` is a first-party
package, not an infra dependency), but it is unusual layering: ``domain``
should not normally depend on the ``backtest`` engine, since CONTRACT-002's
whole premise is that the same ``Strategy`` code -- and by extension the
same ``AnalysisResult`` shape -- must be reusable from ``paper``/``live``,
which will need to produce compatible indicator values from live data
without going through the backtest engine. If/when a paper/live analysis
pipeline is built, consider relocating ``IndicatorBar`` (or an equivalent)
out of ``backtest`` into a neutral shared location so neither side owns the
other.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date
from decimal import Decimal
from typing import Literal, Protocol
from uuid import UUID

from backtest.indicators import IndicatorBar
from domain.valuation.models import PortfolioState


@dataclass(frozen=True)
class AssetAnalysis:
    """One asset's analysis inputs for a single evaluation date.

    See the module docstring for why this carries more than a bare
    ``IndicatorBar``.
    """

    indicators: IndicatorBar
    close: Decimal
    price_status: Literal["fresh", "stale"]
    trading_status: Literal["watch", "tradable", "paused", "blacklist"]


@dataclass(frozen=True)
class AnalysisResult:
    """The real ``AnalysisResult`` shape (replaces the BE-001 placeholder).

    ``assets`` keys are ``asset_id`` (mirrors ``domain.assets.Asset.asset_id``);
    an asset absent from this mapping means no analysis data was available
    for it as of ``as_of`` (e.g. a data gap, or an asset never requested) --
    strategies must treat that the same as "insufficient indicator history"
    (CONTRACT-002 constraint c), not as an error, unless the asset is a
    currently held position (see ``MissingPriceDataError``).
    """

    portfolio_id: UUID
    as_of: date
    assets: dict[str, AssetAnalysis]


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
