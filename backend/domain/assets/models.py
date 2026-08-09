"""Asset entity and repository interface (CONTRACT-001 §0.1).

Responsibility (doc/technical/roadmap/02_backend_foundation.md BE-001):
the ``Asset`` entity (``asset_id``/``asset_type``/``symbol``/``venue``/
``quote_currency``/``provider_symbols``/``trading_status``) and its
uniqueness rules.

Explicitly NOT this subpackage's job: the relationship between an asset and
a portfolio (candidate/held/target weight, etc.) -- that lives in
``domain.portfolios``.
"""

from __future__ import annotations

from collections.abc import Sequence
from dataclasses import dataclass
from typing import Literal, Protocol


@dataclass(frozen=True)
class Asset:
    """A tradable or watchable asset.

    Identity is ``asset_id`` (e.g. ``"equity:nasdaq:NVDA"``,
    ``"crypto:bybit:BTC-USDT"``, ``"cash:USD"``), never ``symbol`` --
    ``symbol`` is a human-readable, provider-facing label only
    (project-conventions.md §4).
    """

    asset_id: str
    asset_type: Literal["STOCK", "ETF", "CRYPTO", "CASH"]
    symbol: str
    venue: str
    quote_currency: str
    provider_symbols: dict[str, str]
    trading_status: Literal["watch", "tradable", "paused", "blacklist"]


class AssetRepository(Protocol):
    """Read access to the asset catalog.

    Implementations belong in ``repository/`` (BE-002); this subpackage
    declares the interface only.
    """

    def get(self, asset_id: str) -> Asset:
        """Return the asset identified by ``asset_id``.

        Failure path: implementations raise a domain-specific not-found
        error when no asset with this id exists; this Protocol does not
        prescribe the exception type.
        """
        ...

    def list_by_ids(self, asset_ids: Sequence[str]) -> list[Asset]:
        """Return the assets matching ``asset_ids``.

        Ids with no matching asset are omitted from the result rather than
        raising; callers that require strict validation must check the
        returned list length against the input.
        """
        ...
