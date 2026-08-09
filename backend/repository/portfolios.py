"""PostgreSQL-backed ``PortfolioRepository`` (CONTRACT-001 §0.1).

Translates between ``repository.schema.portfolios``/``portfolio_members``
rows and the frozen ``domain.portfolios.models.Portfolio``/
``PortfolioMember`` dataclasses. Synchronous, matching the frozen Protocol
(see ``repository/database.py`` module docstring).
"""

from __future__ import annotations

from uuid import UUID

from sqlalchemy import select
from sqlalchemy.engine import Engine, RowMapping

from domain.portfolios.models import Portfolio, PortfolioMember
from repository.errors import NotFoundError
from repository.schema import portfolio_members as portfolio_members_table
from repository.schema import portfolios as portfolios_table


def _row_to_portfolio(row: RowMapping) -> Portfolio:
    return Portfolio(
        portfolio_id=row["portfolio_id"],
        name=row["name"],
        base_currency=row["base_currency"],
        benchmark_asset_id=row["benchmark_asset_id"],
        risk_level=row["risk_level"],
        execution_mode=row["execution_mode"],
        status=row["status"],
    )


def _row_to_member(row: RowMapping) -> PortfolioMember:
    return PortfolioMember(
        portfolio_id=row["portfolio_id"],
        asset_id=row["asset_id"],
        member_status=row["member_status"],
        target_weight_min=row["target_weight_min"],
        target_weight_max=row["target_weight_max"],
    )


class SqlPortfolioRepository:
    """``PortfolioRepository`` implementation.

    Backed by the ``core.portfolios`` and ``core.portfolio_members`` tables.
    """

    def __init__(self, engine: Engine) -> None:
        self._engine = engine

    def get(self, portfolio_id: UUID) -> Portfolio:
        """Return the portfolio identified by ``portfolio_id``.

        Raises ``repository.errors.NotFoundError`` if no such portfolio exists.
        """
        with self._engine.connect() as conn:
            row = (
                conn.execute(
                    select(portfolios_table).where(portfolios_table.c.portfolio_id == portfolio_id)
                )
                .mappings()
                .first()
            )
        if row is None:
            raise NotFoundError(f"portfolio not found: {portfolio_id}")
        return _row_to_portfolio(row)

    def list_members(self, portfolio_id: UUID, status: str | None = None) -> list[PortfolioMember]:
        """Return members of ``portfolio_id``, optionally filtered by ``status``.

        ``status`` of ``None`` returns members in all states.
        """
        stmt = select(portfolio_members_table).where(
            portfolio_members_table.c.portfolio_id == portfolio_id
        )
        if status is not None:
            stmt = stmt.where(portfolio_members_table.c.member_status == status)
        with self._engine.connect() as conn:
            rows = conn.execute(stmt).mappings().all()
        return [_row_to_member(row) for row in rows]


__all__ = ["SqlPortfolioRepository"]
