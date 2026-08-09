"""PostgreSQL-backed ``AssetRepository`` (CONTRACT-001 §0.1).

Translates between ``repository.schema.assets`` rows and the frozen
``domain.assets.models.Asset`` dataclass. Uses a synchronous SQLAlchemy
engine because ``AssetRepository``'s Protocol methods are synchronous (see
``repository/database.py`` module docstring for why).
"""

from __future__ import annotations

from collections.abc import Sequence

from sqlalchemy import select
from sqlalchemy.engine import Engine, RowMapping

from domain.assets.models import Asset
from repository.errors import NotFoundError
from repository.schema import assets as assets_table


def _row_to_asset(row: RowMapping) -> Asset:
    return Asset(
        asset_id=row["asset_id"],
        asset_type=row["asset_type"],
        symbol=row["symbol"],
        venue=row["venue"],
        quote_currency=row["quote_currency"],
        provider_symbols=dict(row["provider_symbols"] or {}),
        trading_status=row["trading_status"],
    )


class SqlAssetRepository:
    """``AssetRepository`` implementation backed by ``core.assets``."""

    def __init__(self, engine: Engine) -> None:
        self._engine = engine

    def get(self, asset_id: str) -> Asset:
        """Return the asset identified by ``asset_id``.

        Raises ``repository.errors.NotFoundError`` if no such asset exists.
        """
        with self._engine.connect() as conn:
            row = (
                conn.execute(select(assets_table).where(assets_table.c.asset_id == asset_id))
                .mappings()
                .first()
            )
        if row is None:
            raise NotFoundError(f"asset not found: {asset_id!r}")
        return _row_to_asset(row)

    def list_by_ids(self, asset_ids: Sequence[str]) -> list[Asset]:
        """Return the assets matching ``asset_ids``.

        Ids with no matching row are omitted rather than raising, per the
        Protocol docstring.
        """
        if not asset_ids:
            return []
        with self._engine.connect() as conn:
            rows = (
                conn.execute(select(assets_table).where(assets_table.c.asset_id.in_(asset_ids)))
                .mappings()
                .all()
            )
        return [_row_to_asset(row) for row in rows]


__all__ = ["SqlAssetRepository"]
