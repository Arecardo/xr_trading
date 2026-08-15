"""PostgreSQL-backed ``PositionRepository``/``CashBalanceRepository``/
``ValuationSnapshotRepository``/``PerformanceSnapshotRepository`` (BE-003).

Translates between the ``core.positions``/``core.cash_balances``/
``core.valuation_snapshots``/``core.performance_snapshots`` rows (BE-002,
CONTRACT-004) and the ``domain.valuation.models`` dataclasses. Synchronous,
matching the Protocols these classes implement (see
``repository/database.py``'s module docstring for why the whole
``repository`` package is sync-only for now).

Idempotency (BE-003 design decision -- see the task's "decide and document
idempotency/upsert behavior" instruction): every ``upsert`` here is a real
Postgres upsert (``INSERT ... ON CONFLICT ... DO UPDATE``) keyed on each
table's natural uniqueness constraint --
``(portfolio_id, asset_id)``/``(portfolio_id, currency)`` (composite primary
keys, no synthetic id) for positions/cash, and
``(portfolio_id, valuation_date)``/``(portfolio_id, as_of)`` (the
``UNIQUE`` constraint, CONTRACT-004) for the two snapshot tables, which do
have their own synthetic UUID primary key. Re-running
``ValuationService.generate_snapshot`` for a date that already has a
snapshot therefore recomputes and overwrites that row in place, keeping its
original ``valuation_snapshot_id`` -- not "reject if exists" and not
"always insert a new row" (which would violate the ``UNIQUE`` constraint on
a second call for the same date). This is the simplest behavior consistent
with a snapshot being a deterministic function of ``(portfolio_id, as_of)``
plus whatever positions/cash/prices exist at generation time: if any of
those inputs changed since the last run for that date (e.g. a late-arriving
price revision), re-running is expected to correct the stored snapshot
rather than leave a stale one in place next to a rejected write.
"""

from __future__ import annotations

from collections.abc import Callable
from datetime import date
from uuid import UUID, uuid4

from sqlalchemy import select
from sqlalchemy.dialects.postgresql import insert as pg_insert
from sqlalchemy.engine import Engine, RowMapping

from domain.valuation.models import CashBalance, PerformanceSnapshot, Position, ValuationSnapshot
from repository.errors import NotFoundError
from repository.schema import cash_balances as cash_balances_table
from repository.schema import performance_snapshots as performance_snapshots_table
from repository.schema import positions as positions_table
from repository.schema import valuation_snapshots as valuation_snapshots_table

# --- positions --------------------------------------------------------------


def _row_to_position(row: RowMapping) -> Position:
    return Position(
        portfolio_id=row["portfolio_id"],
        asset_id=row["asset_id"],
        quantity=row["quantity"],
        average_cost=row["average_cost"],
    )


class SqlPositionRepository:
    """``PositionRepository`` implementation backed by ``core.positions``."""

    def __init__(self, engine: Engine) -> None:
        self._engine = engine

    def list_by_portfolio(self, portfolio_id: UUID) -> list[Position]:
        """Return every position held by ``portfolio_id`` (including zero-quantity rows)."""
        with self._engine.connect() as conn:
            rows = (
                conn.execute(
                    select(positions_table).where(positions_table.c.portfolio_id == portfolio_id)
                )
                .mappings()
                .all()
            )
        return [_row_to_position(row) for row in rows]

    def upsert(self, position: Position) -> None:
        """Insert or replace the ``(portfolio_id, asset_id)`` row (see module docstring)."""
        stmt = pg_insert(positions_table).values(
            portfolio_id=position.portfolio_id,
            asset_id=position.asset_id,
            quantity=position.quantity,
            average_cost=position.average_cost,
        )
        stmt = stmt.on_conflict_do_update(
            index_elements=[positions_table.c.portfolio_id, positions_table.c.asset_id],
            set_={
                "quantity": stmt.excluded.quantity,
                "average_cost": stmt.excluded.average_cost,
            },
        )
        with self._engine.begin() as conn:
            conn.execute(stmt)


# --- cash_balances ------------------------------------------------------------


def _row_to_cash_balance(row: RowMapping) -> CashBalance:
    return CashBalance(
        portfolio_id=row["portfolio_id"],
        currency=row["currency"],
        amount=row["amount"],
    )


class SqlCashBalanceRepository:
    """``CashBalanceRepository`` implementation backed by ``core.cash_balances``."""

    def __init__(self, engine: Engine) -> None:
        self._engine = engine

    def list_by_portfolio(self, portfolio_id: UUID) -> list[CashBalance]:
        """Return every currency balance held by ``portfolio_id``."""
        with self._engine.connect() as conn:
            rows = (
                conn.execute(
                    select(cash_balances_table).where(
                        cash_balances_table.c.portfolio_id == portfolio_id
                    )
                )
                .mappings()
                .all()
            )
        return [_row_to_cash_balance(row) for row in rows]

    def upsert(self, cash_balance: CashBalance) -> None:
        """Insert or replace the ``(portfolio_id, currency)`` row (see module docstring)."""
        stmt = pg_insert(cash_balances_table).values(
            portfolio_id=cash_balance.portfolio_id,
            currency=cash_balance.currency,
            amount=cash_balance.amount,
        )
        stmt = stmt.on_conflict_do_update(
            index_elements=[cash_balances_table.c.portfolio_id, cash_balances_table.c.currency],
            set_={"amount": stmt.excluded.amount},
        )
        with self._engine.begin() as conn:
            conn.execute(stmt)


# --- valuation_snapshots --------------------------------------------------


def _row_to_valuation_snapshot(row: RowMapping) -> ValuationSnapshot:
    return ValuationSnapshot(
        portfolio_id=row["portfolio_id"],
        valuation_date=row["valuation_date"],
        positions_value=row["positions_value"],
        cash_value=row["cash_value"],
        net_asset_value=row["net_asset_value"],
        base_currency=row["base_currency"],
        price_status=row["price_status"],
    )


class SqlValuationSnapshotRepository:
    """``ValuationSnapshotRepository`` implementation backed by ``core.valuation_snapshots``.

    ``id_factory`` generates the synthetic ``valuation_snapshot_id`` for a
    brand-new row (never used on an idempotent overwrite, which keeps the
    existing row's id -- see module docstring); it is injectable rather than
    hardcoded to ``uuid4`` so tests can supply a deterministic sequence
    (python-backend-standards.md §2: "非确定性能力...通过接口/参数注入").
    """

    def __init__(self, engine: Engine, *, id_factory: Callable[[], UUID] = uuid4) -> None:
        self._engine = engine
        self._id_factory = id_factory

    def get_latest(self, portfolio_id: UUID) -> ValuationSnapshot:
        """Return the most recent (by ``valuation_date``) snapshot for ``portfolio_id``.

        Raises ``repository.errors.NotFoundError`` if no snapshot has ever
        been generated for this portfolio.
        """
        with self._engine.connect() as conn:
            row = (
                conn.execute(
                    select(valuation_snapshots_table)
                    .where(valuation_snapshots_table.c.portfolio_id == portfolio_id)
                    .order_by(valuation_snapshots_table.c.valuation_date.desc())
                    .limit(1)
                )
                .mappings()
                .first()
            )
        if row is None:
            raise NotFoundError(f"no valuation snapshot exists for portfolio_id={portfolio_id}")
        return _row_to_valuation_snapshot(row)

    def get_by_date(self, portfolio_id: UUID, valuation_date: date) -> ValuationSnapshot | None:
        """Return the snapshot for the exact ``(portfolio_id, valuation_date)``, or ``None``."""
        with self._engine.connect() as conn:
            row = (
                conn.execute(
                    select(valuation_snapshots_table).where(
                        valuation_snapshots_table.c.portfolio_id == portfolio_id,
                        valuation_snapshots_table.c.valuation_date == valuation_date,
                    )
                )
                .mappings()
                .first()
            )
        return None if row is None else _row_to_valuation_snapshot(row)

    def upsert(self, snapshot: ValuationSnapshot) -> None:
        """Insert or replace the ``(portfolio_id, valuation_date)`` row (see module docstring)."""
        stmt = pg_insert(valuation_snapshots_table).values(
            valuation_snapshot_id=self._id_factory(),
            portfolio_id=snapshot.portfolio_id,
            valuation_date=snapshot.valuation_date,
            positions_value=snapshot.positions_value,
            cash_value=snapshot.cash_value,
            net_asset_value=snapshot.net_asset_value,
            base_currency=snapshot.base_currency,
            price_status=snapshot.price_status,
        )
        stmt = stmt.on_conflict_do_update(
            index_elements=[
                valuation_snapshots_table.c.portfolio_id,
                valuation_snapshots_table.c.valuation_date,
            ],
            set_={
                "positions_value": stmt.excluded.positions_value,
                "cash_value": stmt.excluded.cash_value,
                "net_asset_value": stmt.excluded.net_asset_value,
                "base_currency": stmt.excluded.base_currency,
                "price_status": stmt.excluded.price_status,
            },
        )
        with self._engine.begin() as conn:
            conn.execute(stmt)


# --- performance_snapshots --------------------------------------------------


def _row_to_performance_snapshot(row: RowMapping) -> PerformanceSnapshot:
    return PerformanceSnapshot(
        portfolio_id=row["portfolio_id"],
        as_of=row["as_of"],
        total_return_pct=row["total_return_pct"],
        max_drawdown_pct=row["max_drawdown_pct"],
        annualized_volatility=row["annualized_volatility"],
        sharpe_ratio=row["sharpe_ratio"],
        sortino_ratio=row["sortino_ratio"],
        benchmark_return_pct=row["benchmark_return_pct"],
    )


class SqlPerformanceSnapshotRepository:
    """``PerformanceSnapshotRepository`` implementation backed by ``core.performance_snapshots``.

    Same injectable-``id_factory`` rationale as ``SqlValuationSnapshotRepository``.
    """

    def __init__(self, engine: Engine, *, id_factory: Callable[[], UUID] = uuid4) -> None:
        self._engine = engine
        self._id_factory = id_factory

    def list_by_portfolio(self, portfolio_id: UUID) -> list[PerformanceSnapshot]:
        """Return ``portfolio_id``'s performance snapshots, ordered ascending by ``as_of``."""
        with self._engine.connect() as conn:
            rows = (
                conn.execute(
                    select(performance_snapshots_table)
                    .where(performance_snapshots_table.c.portfolio_id == portfolio_id)
                    .order_by(performance_snapshots_table.c.as_of.asc())
                )
                .mappings()
                .all()
            )
        return [_row_to_performance_snapshot(row) for row in rows]

    def upsert(self, snapshot: PerformanceSnapshot) -> None:
        """Insert or replace the ``(portfolio_id, as_of)`` row (see module docstring)."""
        stmt = pg_insert(performance_snapshots_table).values(
            performance_snapshot_id=self._id_factory(),
            portfolio_id=snapshot.portfolio_id,
            as_of=snapshot.as_of,
            total_return_pct=snapshot.total_return_pct,
            max_drawdown_pct=snapshot.max_drawdown_pct,
            annualized_volatility=snapshot.annualized_volatility,
            sharpe_ratio=snapshot.sharpe_ratio,
            sortino_ratio=snapshot.sortino_ratio,
            benchmark_return_pct=snapshot.benchmark_return_pct,
        )
        stmt = stmt.on_conflict_do_update(
            index_elements=[
                performance_snapshots_table.c.portfolio_id,
                performance_snapshots_table.c.as_of,
            ],
            set_={
                "total_return_pct": stmt.excluded.total_return_pct,
                "max_drawdown_pct": stmt.excluded.max_drawdown_pct,
                "annualized_volatility": stmt.excluded.annualized_volatility,
                "sharpe_ratio": stmt.excluded.sharpe_ratio,
                "sortino_ratio": stmt.excluded.sortino_ratio,
                "benchmark_return_pct": stmt.excluded.benchmark_return_pct,
            },
        )
        with self._engine.begin() as conn:
            conn.execute(stmt)


__all__ = [
    "SqlCashBalanceRepository",
    "SqlPerformanceSnapshotRepository",
    "SqlPositionRepository",
    "SqlValuationSnapshotRepository",
]
