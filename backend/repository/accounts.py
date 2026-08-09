"""PostgreSQL-backed ``AccountBindingRepository`` (CONTRACT-001 §0.1).

Translates between ``repository.schema.account_bindings`` rows and the
frozen ``domain.accounts.models.AccountBinding`` dataclass. Synchronous,
matching the frozen Protocol (see ``repository/database.py`` module
docstring).
"""

from __future__ import annotations

from uuid import UUID

from sqlalchemy import select
from sqlalchemy.engine import Engine, RowMapping

from domain.accounts.models import AccountBinding
from repository.errors import AmbiguousBindingError, CredentialReuseError, NotFoundError
from repository.schema import account_bindings as account_bindings_table


def _row_to_binding(row: RowMapping) -> AccountBinding:
    return AccountBinding(
        account_binding_id=row["account_binding_id"],
        portfolio_id=row["portfolio_id"],
        broker_code=row["broker_code"],
        environment=row["environment"],
        credential_ref=row["credential_ref"],
    )


class SqlAccountBindingRepository:
    """``AccountBindingRepository`` implementation backed by ``core.account_bindings``."""

    def __init__(self, engine: Engine) -> None:
        self._engine = engine

    def get_for_portfolio(
        self, portfolio_id: UUID, environment: str, broker_code: str
    ) -> AccountBinding:
        """Return the binding for ``portfolio_id`` in ``environment`` on ``broker_code``.

        Raises ``repository.errors.NotFoundError`` if no binding exists for
        this exact (portfolio_id, environment, broker_code) combination.
        """
        with self._engine.connect() as conn:
            rows = (
                conn.execute(
                    select(account_bindings_table).where(
                        account_bindings_table.c.portfolio_id == portfolio_id,
                        account_bindings_table.c.environment == environment,
                        account_bindings_table.c.broker_code == broker_code,
                    )
                )
                .mappings()
                .all()
            )
        if not rows:
            raise NotFoundError(
                f"account binding not found: portfolio_id={portfolio_id} "
                f"environment={environment!r} broker_code={broker_code!r}"
            )
        # (portfolio_id, broker_code, environment) is UNIQUE at the schema
        # level (CONTRACT-004), so more than one row here would indicate a
        # constraint violation elsewhere, not a normal runtime outcome.
        if len(rows) > 1:
            raise AmbiguousBindingError(
                f"schema invariant violated: multiple account bindings for "
                f"portfolio_id={portfolio_id} environment={environment!r} "
                f"broker_code={broker_code!r}, expected at most one"
            )
        return _row_to_binding(rows[0])

    def assert_no_credential_reuse(self, paper_ref: str, live_ref: str) -> None:
        """Raise ``CredentialReuseError`` if ``paper_ref`` and ``live_ref`` collide.

        Returns ``None`` when the references are distinct.
        """
        if paper_ref == live_ref:
            raise CredentialReuseError(
                "paper and live credential references must not be reused "
                "(project-conventions.md §6, python-backend-standards.md §7)"
            )


__all__ = ["SqlAccountBindingRepository"]
