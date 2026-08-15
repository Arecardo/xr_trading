"""Error hierarchy for the ``domain.strategies`` subpackage (BT-004).

Mirrors the repo-wide convention (python-backend-standards.md §5) of a
stable domain exception hierarchy rather than surfacing assertion failures
or raw ``KeyError``/``ZeroDivisionError`` to callers.
"""

from __future__ import annotations


class StrategyError(ValueError):
    """Base class for all ``domain.strategies`` errors.

    Subclasses ``ValueError`` per CONTRACT-002's own wording ("策略层直接拒绝
    生成 (``ValueError``/领域异常)") for the target-weight-sum constraint;
    reused as the shared base for this subpackage's other fail-closed input
    checks so callers can catch one type.
    """


class InvalidTargetWeightsError(StrategyError):
    """A computed ``target_weights`` mapping does not sum to exactly 1.

    CONTRACT-002 constraint (a): this must never be silently renormalized
    away -- it means a bug in the strategy's own weight construction, and
    the strategy layer must reject generation outright rather than defer to
    risk (risk performs a separate, independent output check).
    """


class MissingPriceDataError(StrategyError):
    """A currently-held position's asset has no ``AssetAnalysis`` entry.

    Computing that asset's current portfolio weight requires a real current
    price (``AssetAnalysis.close``); without one, the strategy fails closed
    rather than guessing from a stand-in such as cost basis
    (``Position.average_cost``), consistent with the project-wide rule
    against fabricating financial precision values.
    """


class InconsistentInputError(StrategyError):
    """``analysis`` and ``portfolio_state`` do not refer to the same portfolio.

    E.g. ``analysis.portfolio_id != portfolio_state.portfolio.portfolio_id``.
    Cheap defensive check that catches an integration bug at the boundary
    rather than silently producing a nonsensical ``StrategyOutput``.
    """
