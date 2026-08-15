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


@dataclass(frozen=True)
class PerformanceSnapshot:
    """A point-in-time performance/risk summary for a portfolio (BE-003).

    Not part of the original CONTRACT-001 freeze (which only froze
    ``Position``/``CashBalance``/``ValuationSnapshot``/``PortfolioState``/
    ``ValuationService``) -- CONTRACT-004's DDL always listed the
    ``performance_snapshots`` table and its columns, but no dataclass existed
    for it yet. Defined now, by BE-003, per the roadmap's explicit
    instruction; this is a new addition to the frozen set, not a change to
    an existing signature.

    All metric fields are ``Decimal | None`` because CONTRACT-004 leaves the
    ``performance_snapshots`` columns nullable on purpose: a metric that is
    undefined given the available history (e.g. ``sharpe_ratio`` with only
    one data point, or ``benchmark_return_pct`` with no benchmark configured)
    must be recorded as absent, never fabricated as ``0`` or an arbitrary
    placeholder (python-backend-standards.md's "no false precision" rule,
    already applied to ``ValuationSnapshot.price_status``).

    Percentage/ratio fields are fractions, not already-multiplied-by-100
    percentages -- e.g. ``total_return_pct=Decimal("0.05")`` means +5%. This
    mirrors ``domain.portfolios.models.PortfolioMember.target_weight_min``'s
    existing fraction convention (0..1 range) rather than inventing a
    different scale for this one dataclass. ``max_drawdown_pct`` is a
    non-positive fraction (0 or negative; e.g. ``Decimal("-0.12")`` for a
    12% peak-to-trough decline).

    See ``domain.valuation.performance.compute_performance_snapshot`` for
    the (deliberately minimal) BE-003 computation -- ``sharpe_ratio``/
    ``sortino_ratio``/``benchmark_return_pct`` are always ``None`` from that
    function today; full performance-report computation is BT-006/BT-007's
    job, not BE-003's (see that module's docstring for exactly what is and
    is not computed).
    """

    portfolio_id: UUID
    as_of: date
    total_return_pct: Decimal | None
    max_drawdown_pct: Decimal | None
    annualized_volatility: Decimal | None
    sharpe_ratio: Decimal | None
    sortino_ratio: Decimal | None
    benchmark_return_pct: Decimal | None


# --- repository Protocols (BE-003) -----------------------------------------
#
# CONTRACT-001 §0.1 froze the five dataclasses/Protocol above but did not
# freeze per-entity repository interfaces for this subpackage (unlike
# ``assets``/``portfolios``/``accounts``, each of which already had its
# ``*Repository`` Protocol frozen alongside its entity). BE-002's report
# flagged this explicitly: "positions/cash_balances/valuation_snapshots/
# performance_snapshots ... migration-only for now, by design -- CONTRACT-001
# does not freeze a ValuationService implementation for this task to
# satisfy." These four Protocols fill that gap now, following the same
# shape as ``domain.assets.models.AssetRepository`` et al. (structural typing
# only, zero infra imports -- concrete implementations live in
# ``repository/valuation.py``, BE-003).


class PositionRepository(Protocol):
    """Read/write access to a portfolio's positions.

    Implementations belong in ``repository/`` (BE-003); this subpackage
    declares the interface only.
    """

    def list_by_portfolio(self, portfolio_id: UUID) -> list[Position]:
        """Return every position held by ``portfolio_id`` (including zero-quantity rows)."""
        ...

    def upsert(self, position: Position) -> None:
        """Insert or replace the ``(portfolio_id, asset_id)`` row.

        Idempotent: calling this twice with the same ``(portfolio_id,
        asset_id)`` overwrites ``quantity``/``average_cost`` rather than
        raising a uniqueness violation.
        """
        ...


class CashBalanceRepository(Protocol):
    """Read/write access to a portfolio's cash balances.

    Implementations belong in ``repository/`` (BE-003); this subpackage
    declares the interface only.
    """

    def list_by_portfolio(self, portfolio_id: UUID) -> list[CashBalance]:
        """Return every currency balance held by ``portfolio_id``."""
        ...

    def upsert(self, cash_balance: CashBalance) -> None:
        """Insert or replace the ``(portfolio_id, currency)`` row.

        Idempotent, same semantics as ``PositionRepository.upsert``.
        """
        ...


class ValuationSnapshotRepository(Protocol):
    """Read/write access to a portfolio's valuation snapshot history.

    Implementations belong in ``repository/`` (BE-003); this subpackage
    declares the interface only.
    """

    def get_latest(self, portfolio_id: UUID) -> ValuationSnapshot:
        """Return the most recent (by ``valuation_date``) snapshot for ``portfolio_id``.

        Failure path: implementations raise a domain-specific not-found
        error when no snapshot has ever been generated for this portfolio --
        callers must not substitute a synthetic zero-value snapshot.
        """
        ...

    def get_by_date(self, portfolio_id: UUID, valuation_date: date) -> ValuationSnapshot | None:
        """Return the snapshot for the exact ``(portfolio_id, valuation_date)``, or ``None``.

        ``None`` (not an exception) signals "not generated yet for this
        date" -- used by callers deciding whether a re-run is a fresh
        insert or an idempotent overwrite.
        """
        ...

    def upsert(self, snapshot: ValuationSnapshot) -> None:
        """Insert or replace the ``(portfolio_id, valuation_date)`` row.

        Idempotent: re-running ``ValuationService.generate_snapshot`` for a
        date that already has a snapshot overwrites it in place (the
        snapshot's own identity/UUID is implementation-internal and not part
        of this frozen dataclass) rather than raising a uniqueness
        violation or accumulating duplicate rows.
        """
        ...


class PerformanceSnapshotRepository(Protocol):
    """Read/write access to a portfolio's performance snapshot history.

    Implementations belong in ``repository/`` (BE-003); this subpackage
    declares the interface only.
    """

    def list_by_portfolio(self, portfolio_id: UUID) -> list[PerformanceSnapshot]:
        """Return ``portfolio_id``'s performance snapshots, ordered ascending by ``as_of``."""
        ...

    def upsert(self, snapshot: PerformanceSnapshot) -> None:
        """Insert or replace the ``(portfolio_id, as_of)`` row. Idempotent, same as above."""
        ...


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
