"""``BacktestExecutionService`` (BT-003): the ``ExecutionService`` implementation
that gates every simulated fill through CONTRACT-001's non-bypassable rule.

`doc/technical/08_backtest_engine.md` §9 requires the backtest to reuse the
same risk-check machinery `paper`/`live` would use, and CONTRACT-001 (`Order`/
`ExecutionService`) requires ``risk_result.approved is False`` to always
produce a rejected order, never a fill (python-backend-standards.md §7 --
"不可绕过", not bypassable regardless of environment). This class is the
smallest possible ``ExecutionService`` implementation that structurally
enforces that: it holds no order book, no matching logic, and no I/O -- it
only ever flips ``Order.status`` between ``"submitted"`` (risk approved) and
``"rejected"`` (risk did not). The actual fill simulation (price/slippage/
commission/precision rounding) happens one level up, in
``application.backtest.engine.BacktestEngine``, only for orders this service
has already marked ``"submitted"`` -- this class is the gate, not the
matching engine.
"""

from __future__ import annotations

from dataclasses import replace

from domain.execution.models import Order
from domain.risk.models import RiskCheckResult


class BacktestExecutionService:
    """Deterministic, side-effect-free ``ExecutionService`` for backtest runs.

    No wall-clock time, no randomness, no mutable state: calling ``submit``
    twice with the same arguments always returns the same result (required
    for backtest reproducibility, python-backend-standards.md §9).
    """

    def submit(self, order: Order, risk_result: RiskCheckResult) -> Order:
        """Reject ``order`` outright when ``risk_result.approved`` is ``False``.

        Never raises for an ordinary rejection (that is a normal, expected
        outcome of the day loop, not an error) -- returns a copy of ``order``
        with ``status="rejected"``. Otherwise returns a copy with
        ``status="submitted"``, signalling the caller may proceed to simulate
        a fill for it.
        """
        if not risk_result.approved:
            return replace(order, status="rejected")
        return replace(order, status="submitted")


__all__ = ["BacktestExecutionService"]
