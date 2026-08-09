"""Domain-agnostic errors raised by the PostgreSQL-backed repository implementations.

CONTRACT-001's repository Protocols deliberately do not prescribe an
exception type for "not found" (see e.g. ``AssetRepository.get``'s
docstring: "this Protocol does not prescribe the exception type"). These
are the concrete types this package's implementations raise.
"""

from __future__ import annotations


class NotFoundError(LookupError):
    """Raised when a requested entity does not exist."""


class CredentialReuseError(ValueError):
    """Raised when a paper credential ref and a live credential ref collide.

    Enforces the simulated/real credential isolation rule
    (project-conventions.md §6, python-backend-standards.md §7).
    """


class AmbiguousBindingError(LookupError):
    """Raised if a (portfolio_id, environment, broker_code) lookup ever returns
    more than one row.

    This should not happen in normal operation: CONTRACT-004's
    ``account_bindings`` table enforces uniqueness on exactly
    (portfolio_id, broker_code, environment), and
    ``AccountBindingRepository.get_for_portfolio`` takes all three as
    parameters (``broker_code`` was added 2026-08-05 -- see CONTRACT-001
    §0.1 for why: a single portfolio legitimately holds bindings to more
    than one broker in the same environment, e.g. longbridge for equities
    and bybit for crypto in the same paper portfolio). If this is ever
    raised, it indicates the schema's uniqueness constraint was bypassed
    or violated, not a normal ambiguous-lookup case.
    """
