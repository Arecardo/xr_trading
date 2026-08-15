"""Error hierarchy for the ``domain.risk`` subpackage (BT-005).

Mirrors ``domain.strategies.errors`` (python-backend-standards.md §5): a
stable domain exception hierarchy for genuine programmer/wiring bugs, kept
separate from ordinary business rejections.

A crucial distinction from ``domain.strategies``: an *ordinary* risk
rejection (a target-weight breach, an over-budget order, a restricted asset,
...) is never raised as an exception here -- it is returned as a populated
``RiskCheckResult(approved=False, ...)``, per ``domain.risk.models``'s own
docstring ("risk only ever approves or rejects, it never proposes") and the
project-wide rule that risk must never itself decide to bypass a rejection
or silently swallow one. Exceptions in this module are reserved for inputs
that are not a legitimate business question for risk to answer at all (e.g.
mismatched portfolio identifiers, or a ``RiskPolicy`` constructed with an
invalid threshold) -- the same "structural wiring bug vs. business outcome"
split ``domain.strategies.errors.InconsistentInputError`` already draws.
"""

from __future__ import annotations


class RiskPolicyError(ValueError):
    """Base class for all ``domain.risk`` errors.

    Subclasses ``ValueError``, mirroring ``domain.strategies.errors.StrategyError``,
    so callers can catch one type for this subpackage's fail-closed input checks.
    """


class InconsistentInputError(RiskPolicyError):
    """``intent``/``target_weights`` and ``portfolio_state`` do not agree.

    E.g. ``intent.portfolio_id != portfolio_state.portfolio.portfolio_id``.
    Cheap defensive check that catches an integration bug at the boundary
    rather than silently evaluating risk against the wrong portfolio's state.
    """


class InvalidRiskConfigurationError(RiskPolicyError):
    """A ``SimpleRiskPolicy`` was constructed with an invalid threshold.

    Configuration is validated eagerly at construction time (fail-closed)
    rather than left to produce nonsensical ``RiskCheckResult`` values later,
    e.g. a negative weight cap that could never reject anything.
    """
