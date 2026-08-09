"""Risk-policy entities and interface (CONTRACT-003, implementation_plan/01 §0.3).

Responsibility (doc/technical/roadmap/02_backend_foundation.md BE-001):
portfolio-level and order-level risk rules, risk budgets, weight-cap
checks, and producing auditable risk-check results.

Explicitly NOT this subpackage's job: generating signals or deciding
buy/sell -- ``risk`` only ever approves or rejects, it never proposes.
"""

from __future__ import annotations

from dataclasses import dataclass
from decimal import Decimal
from typing import Any, Literal, Protocol
from uuid import UUID

from domain.valuation.models import PortfolioState


@dataclass(frozen=True)
class RiskCheckResult:
    """The auditable outcome of a risk check.

    ``context`` carries the concrete values that triggered a threshold and
    must never contain sensitive information (credentials, tokens, etc.).
    """

    approved: bool
    rejection_reasons: tuple[str, ...]
    checked_rules: tuple[str, ...]
    context: dict[str, Any]


@dataclass(frozen=True)
class OrderIntent:
    """A proposed order, prior to risk approval."""

    portfolio_id: UUID
    asset_id: str
    side: Literal["buy", "sell"]
    quantity: Decimal
    estimated_price: Decimal


class RiskPolicy(Protocol):
    """Portfolio-level and order-level risk checks.

    Constraints (CONTRACT-003):
    - ``check_order``/``check_target_weights`` must have the exact same
      signature across ``backtest``/``paper``/``live``; only
      ``portfolio_state``/configuration may vary environment-specific
      thresholds -- no environment-specific branches or extra parameters.
    - When ``RiskCheckResult.approved`` is ``False``,
      ``execution.ExecutionService.submit`` must refuse to create an order;
      this is a hard, non-bypassable constraint
      (python-backend-standards.md §7).
    """

    def check_order(self, intent: OrderIntent, portfolio_state: PortfolioState) -> RiskCheckResult:
        """Run order-level risk checks for a single proposed order."""
        ...

    def check_target_weights(
        self, target_weights: dict[str, Decimal], portfolio_state: PortfolioState
    ) -> RiskCheckResult:
        """Run portfolio-level risk checks for a proposed set of target weights."""
        ...

    def replaced_checks(self) -> tuple[str, ...]:
        """List checks substituted by an alternate implementation in this environment.

        E.g. trading-hours checks or manual confirmation, when running under
        ``research``/``backtest``. Must be surfaced in reports so the
        substitution is never silently assumed.
        """
        ...
