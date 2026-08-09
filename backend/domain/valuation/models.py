"""Valuation entities and service interface (CONTRACT-001 §0.1).

Responsibility (doc/technical/roadmap/02_backend_foundation.md BE-001):
``Position``, ``CashBalance``, ``ValuationSnapshot`` (day-cutover per
DEC-002), ``PerformanceSnapshot`` (return/drawdown/volatility/benchmark
contribution -- not yet part of the frozen CONTRACT-001 dataclasses),
and FX conversion. Also owns ``PortfolioState``, the read-only projection
of a portfolio's current state consumed by ``strategies`` and ``risk``.

Explicitly NOT this subpackage's job: strategy or risk decisions.

Dependency direction (frozen): ``valuation`` -> ``portfolios`` (for
``Portfolio``/``PortfolioMember`` in ``PortfolioState``); ``valuation`` ->
``assets``/``accounts`` for currency and account-dimension concerns (no
concrete field references yet in the frozen dataclasses below -- see open
question in the BE-001 implementation report).
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date
from decimal import Decimal
from typing import Literal, Protocol
from uuid import UUID

from domain.portfolios.models import Portfolio, PortfolioMember


@dataclass(frozen=True)
class Position:
    """A portfolio's holding of a single asset."""

    portfolio_id: UUID
    asset_id: str
    quantity: Decimal
    average_cost: Decimal


@dataclass(frozen=True)
class CashBalance:
    """A portfolio's cash balance in a single currency."""

    portfolio_id: UUID
    currency: str
    amount: Decimal


@dataclass(frozen=True)
class ValuationSnapshot:
    """A point-in-time net asset value snapshot for a portfolio.

    ``valuation_date`` is a UTC calendar day per DEC-002 (365 days/year,
    not a trading-day calendar); non-trading days carry forward the prior
    close and are marked ``price_status="stale"``.
    """

    portfolio_id: UUID
    valuation_date: date
    positions_value: Decimal
    cash_value: Decimal
    net_asset_value: Decimal
    base_currency: str
    price_status: Literal["fresh", "stale"]


@dataclass(frozen=True)
class PortfolioState:
    """Read-only projection of a portfolio's current state.

    Consumed by ``strategies`` and ``risk``; produced by ``valuation``.
    """

    portfolio: Portfolio
    members: tuple[PortfolioMember, ...]
    positions: tuple[Position, ...]
    cash: tuple[CashBalance, ...]
    latest_snapshot: ValuationSnapshot


class ValuationService(Protocol):
    """Generates valuation snapshots and the current portfolio-state projection.

    Implementations belong in ``application``/``adapters`` layers built on
    top of this domain subpackage (BE-003); this subpackage declares the
    interface only.
    """

    def generate_snapshot(self, portfolio_id: UUID, as_of: date) -> ValuationSnapshot:
        """Generate (or recompute) the valuation snapshot for ``as_of``.

        Failure path: implementations must not produce a spuriously precise
        net asset value when required FX rates are missing (§9 of the
        Python backend standards) -- they should raise a domain-specific
        error instead.
        """
        ...

    def current_state(self, portfolio_id: UUID) -> PortfolioState:
        """Return the current read-only state projection for ``portfolio_id``."""
        ...
