"""Order/Fill entities and execution-service interface (CONTRACT-001 §0.1).

Responsibility (doc/technical/roadmap/02_backend_foundation.md BE-001):
``Order``/``Fill`` -- order generation, state machine, ``paper`` simulated
matching, ``live`` order-placement adapter (credential/environment
validation is delegated to ``accounts``).

Explicitly NOT this subpackage's job: generating signals, deciding
buy/sell, bypassing risk checks, or auto-enabling live trading
(python-backend-standards.md §7).
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime
from decimal import Decimal
from typing import Literal, Protocol
from uuid import UUID

from domain.risk.models import RiskCheckResult


@dataclass(frozen=True)
class Order:
    """An order, tracked through its full lifecycle state machine.

    ``status="unknown"`` covers API timeouts / unknown broker responses,
    which must never be resolved by blind retry (python-backend-standards.md
    §7).
    """

    order_id: UUID
    portfolio_id: UUID
    asset_id: str
    side: Literal["buy", "sell"]
    quantity: Decimal
    order_type: Literal["market", "limit"]
    limit_price: Decimal | None
    status: Literal[
        "pending_risk",
        "rejected",
        "submitted",
        "partially_filled",
        "filled",
        "cancelled",
        "unknown",
    ]


@dataclass(frozen=True)
class Fill:
    """A single execution (fill) against an order."""

    fill_id: UUID
    order_id: UUID
    quantity: Decimal
    price: Decimal
    commission: Decimal
    filled_at: datetime  # UTC


class ExecutionService(Protocol):
    """Submits orders, gated by a mandatory upstream risk-check result."""

    def submit(self, order: Order, risk_result: RiskCheckResult) -> Order:
        """Submit ``order`` for execution.

        When ``risk_result.approved`` is ``False``, this must reject the
        order and must not create it (python-backend-standards.md §7) --
        this constraint is not bypassable regardless of environment.
        """
        ...
