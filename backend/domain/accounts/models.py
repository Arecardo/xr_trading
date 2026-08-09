"""AccountBinding entity and repository interface (CONTRACT-001 §0.1).

Responsibility (doc/technical/roadmap/02_backend_foundation.md BE-001): the
binding between a portfolio and a broker/exchange account, credential
references (never the credential itself), and environment
(research/backtest/paper/live) x credential matching validation (paper and
live credentials must never be reused for each other).

Explicitly NOT this subpackage's job: placing orders -- only the binding
relationship and environment validation live here.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Literal, Protocol
from uuid import UUID


@dataclass(frozen=True)
class AccountBinding:
    """Binding of a portfolio to a broker/exchange account in one environment."""

    account_binding_id: UUID
    portfolio_id: UUID
    broker_code: str  # "longbridge" | "bybit"
    environment: Literal["research", "backtest", "paper", "live"]
    credential_ref: str  # reference only, never the credential itself


class AccountBindingRepository(Protocol):
    """Read access and cross-environment credential-reuse validation.

    Implementations belong in ``repository/`` (BE-002); this subpackage
    declares the interface only.
    """

    def get_for_portfolio(
        self, portfolio_id: UUID, environment: str, broker_code: str
    ) -> AccountBinding:
        """Return the binding for ``portfolio_id`` in ``environment`` on ``broker_code``.

        ``broker_code`` is required, not optional: a single portfolio
        legitimately holds multiple simultaneous bindings in the same
        environment (e.g. NVDA/QQQ via ``longbridge`` and BTC-USDT via
        ``bybit`` in the same ``paper`` portfolio -- this is the actual
        first-batch asset universe, not a hypothetical edge case). An
        earlier draft of this signature omitted ``broker_code`` and was
        caught by BE-002's repository implementation raising on the
        resulting ambiguity; amended 2026-08-05 rather than left as a
        "pick one" guess.

        Failure path: implementations raise a domain-specific not-found
        error when no binding exists for this portfolio/environment/broker
        combination.
        """
        ...

    def assert_no_credential_reuse(self, paper_ref: str, live_ref: str) -> None:
        """Raise a domain-specific error if ``paper_ref`` and ``live_ref`` collide.

        Enforces the simulated/real credential isolation rule
        (project-conventions.md §6, python-backend-standards.md §7). Returns
        ``None`` (no exception) when the references are distinct.
        """
        ...
