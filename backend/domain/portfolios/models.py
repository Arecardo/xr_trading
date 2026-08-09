"""Portfolio and PortfolioMember entities and repository interface (CONTRACT-001 §0.1).

Responsibility (doc/technical/roadmap/02_backend_foundation.md BE-001):
``Portfolio`` (base currency / benchmark / risk level / execution mode /
status state machine), ``PortfolioMember`` (candidate/approved/held/
restricted state machine, target weight band), and rebalance-preview
domain rules.

Explicitly NOT this subpackage's job: strategy scoring / target-weight
computation -- that lives in ``domain.strategies``.
"""

from __future__ import annotations

from dataclasses import dataclass
from decimal import Decimal
from typing import Literal, Protocol
from uuid import UUID


@dataclass(frozen=True)
class Portfolio:
    """A managed portfolio (research/backtest/paper/live)."""

    portfolio_id: UUID
    name: str
    base_currency: str
    benchmark_asset_id: str | None
    risk_level: str
    execution_mode: Literal["research", "backtest", "paper", "live"]
    status: Literal["draft", "active", "paused", "archived"]


@dataclass(frozen=True)
class PortfolioMember:
    """The membership relationship between a portfolio and an asset.

    ``asset_id`` references ``domain.assets.Asset.asset_id`` by value (no
    object reference) to keep ``portfolios`` free of a hard import
    dependency on ``assets``.
    """

    portfolio_id: UUID
    asset_id: str
    member_status: Literal["candidate", "approved", "held", "restricted"]
    target_weight_min: Decimal | None
    target_weight_max: Decimal | None


class PortfolioRepository(Protocol):
    """Read/query access to portfolios and their members.

    Implementations belong in ``repository/`` (BE-002); this subpackage
    declares the interface only.
    """

    def get(self, portfolio_id: UUID) -> Portfolio:
        """Return the portfolio identified by ``portfolio_id``.

        Failure path: implementations raise a domain-specific not-found
        error when no portfolio with this id exists.
        """
        ...

    def list_members(self, portfolio_id: UUID, status: str | None = None) -> list[PortfolioMember]:
        """Return members of ``portfolio_id``, optionally filtered by ``status``.

        ``status`` of ``None`` returns members in all states.
        """
        ...
