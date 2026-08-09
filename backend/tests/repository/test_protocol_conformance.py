"""Static conformance check: the concrete repositories satisfy CONTRACT-001's Protocols.

No DB access -- this only needs mypy (via CI's `mypy .` gate) and Python's
import machinery to succeed. The assignments below are meaningless at
runtime (mypy erases them under ``if TYPE_CHECKING``), but if a
``SqlAssetRepository`` ever drifted from ``AssetRepository``'s frozen
method signatures, ``mypy .`` would fail here with an
``incompatible type`` error -- catching a contract violation mechanically
instead of relying on someone noticing during review, matching the spirit
of ``tests/domain/test_no_infra_leakage.py``.
"""

from __future__ import annotations

from typing import TYPE_CHECKING

from repository.accounts import SqlAccountBindingRepository
from repository.assets import SqlAssetRepository
from repository.portfolios import SqlPortfolioRepository

if TYPE_CHECKING:
    from sqlalchemy.engine import Engine

    from domain.accounts.models import AccountBindingRepository
    from domain.assets.models import AssetRepository
    from domain.portfolios.models import PortfolioRepository

    def _asset_repo_conforms(engine: Engine) -> AssetRepository:
        return SqlAssetRepository(engine)

    def _portfolio_repo_conforms(engine: Engine) -> PortfolioRepository:
        return SqlPortfolioRepository(engine)

    def _account_binding_repo_conforms(engine: Engine) -> AccountBindingRepository:
        return SqlAccountBindingRepository(engine)


def test_module_imports_cleanly() -> None:
    # The real assertion is mypy's static check above (TYPE_CHECKING block);
    # this test just proves the module is collected and importable so the
    # static check actually runs as part of `mypy .`.
    assert SqlAssetRepository is not None
    assert SqlPortfolioRepository is not None
    assert SqlAccountBindingRepository is not None
