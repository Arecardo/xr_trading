"""Initial schema: all ten CONTRACT-004 tables.

Revision ID: 0001
Revises:
Create Date: 2026-08-09

Creates the ten tables frozen by CONTRACT-004
(doc/technical/implementation_plan/01_contracts_and_foundation.md §0.4) in
the ``core`` schema: assets, portfolios, portfolio_members,
account_bindings, positions, cash_balances, valuation_snapshots,
performance_snapshots, orders, fills.

This migration assumes the ``core`` schema and the ``xr_core_owner`` /
``xr_core_runtime`` roles already exist (bootstrapped by
deploy/postgres/init/001_roles_and_schema.sql on a fresh Postgres data
volume via backend/compose.yaml's docker-entrypoint-initdb.d mount, or
applied manually against an external instance). Column/constraint shapes
are transcribed by hand from repository/schema.py rather than generated
from the live ``MetaData`` object, per standard Alembic practice: a
migration is an immutable historical record and must not silently drift if
``schema.py`` changes later -- future schema changes belong in new
migrations, never edits to this file.

Explicit GRANTs at the end are a belt-and-suspenders duplicate of
deploy/postgres/init/001_roles_and_schema.sql's ``ALTER DEFAULT PRIVILEGES``
(which should already cover tables xr_core_owner creates), covering the case
where a database was provisioned without that init script having run.
"""

from __future__ import annotations

from collections.abc import Sequence

import sqlalchemy as sa
from alembic import op
from sqlalchemy.dialects.postgresql import JSONB, UUID

# revision identifiers, used by Alembic.
revision: str = "0001"
down_revision: str | None = None
branch_labels: Sequence[str] | str | None = None
depends_on: Sequence[str] | str | None = None

SCHEMA = "core"
AMOUNT = sa.Numeric(38, 18)
WEIGHT = sa.Numeric(9, 6)

# Tables granted DML access for the runtime role once created.
_TABLE_NAMES = (
    "assets",
    "portfolios",
    "portfolio_members",
    "account_bindings",
    "positions",
    "cash_balances",
    "valuation_snapshots",
    "performance_snapshots",
    "orders",
    "fills",
)


def upgrade() -> None:
    op.create_table(
        "assets",
        sa.Column("asset_id", sa.String(), primary_key=True),
        sa.Column("asset_type", sa.String(16), nullable=False),
        sa.Column("symbol", sa.String(), nullable=False),
        sa.Column("venue", sa.String(), nullable=False),
        sa.Column("quote_currency", sa.String(16), nullable=False),
        sa.Column("provider_symbols", JSONB, nullable=False, server_default="{}"),
        sa.Column("trading_status", sa.String(16), nullable=False),
        sa.Column(
            "created_at", sa.DateTime(timezone=True), nullable=False, server_default=sa.func.now()
        ),
        sa.Column(
            "updated_at", sa.DateTime(timezone=True), nullable=False, server_default=sa.func.now()
        ),
        sa.CheckConstraint(
            "asset_type IN ('STOCK', 'ETF', 'CRYPTO', 'CASH')", name="ck_assets_asset_type"
        ),
        sa.CheckConstraint(
            "trading_status IN ('watch', 'tradable', 'paused', 'blacklist')",
            name="ck_assets_trading_status",
        ),
        sa.CheckConstraint(
            r"asset_id ~ '^[a-z][a-z0-9_]*(:[A-Za-z0-9_.\-]+){1,2}$'",
            name="ck_assets_asset_id_format",
        ),
        schema=SCHEMA,
    )

    op.create_table(
        "portfolios",
        sa.Column("portfolio_id", UUID(as_uuid=True), primary_key=True),
        sa.Column("name", sa.String(), nullable=False),
        sa.Column("base_currency", sa.String(16), nullable=False),
        sa.Column(
            "benchmark_asset_id",
            sa.String(),
            sa.ForeignKey(f"{SCHEMA}.assets.asset_id"),
            nullable=True,
        ),
        sa.Column("risk_level", sa.String(), nullable=False),
        sa.Column("execution_mode", sa.String(16), nullable=False),
        sa.Column("status", sa.String(16), nullable=False),
        sa.Column(
            "created_at", sa.DateTime(timezone=True), nullable=False, server_default=sa.func.now()
        ),
        sa.Column(
            "updated_at", sa.DateTime(timezone=True), nullable=False, server_default=sa.func.now()
        ),
        sa.CheckConstraint(
            "execution_mode IN ('research', 'backtest', 'paper', 'live')",
            name="ck_portfolios_execution_mode",
        ),
        sa.CheckConstraint(
            "status IN ('draft', 'active', 'paused', 'archived')",
            name="ck_portfolios_status",
        ),
        schema=SCHEMA,
    )

    op.create_table(
        "portfolio_members",
        sa.Column(
            "portfolio_id",
            UUID(as_uuid=True),
            sa.ForeignKey(f"{SCHEMA}.portfolios.portfolio_id"),
            nullable=False,
        ),
        sa.Column(
            "asset_id", sa.String(), sa.ForeignKey(f"{SCHEMA}.assets.asset_id"), nullable=False
        ),
        sa.Column("member_status", sa.String(16), nullable=False),
        sa.Column("target_weight_min", WEIGHT, nullable=True),
        sa.Column("target_weight_max", WEIGHT, nullable=True),
        sa.Column(
            "added_at", sa.DateTime(timezone=True), nullable=False, server_default=sa.func.now()
        ),
        sa.PrimaryKeyConstraint("portfolio_id", "asset_id", name="pk_portfolio_members"),
        sa.CheckConstraint(
            "member_status IN ('candidate', 'approved', 'held', 'restricted')",
            name="ck_portfolio_members_status",
        ),
        schema=SCHEMA,
    )

    op.create_table(
        "account_bindings",
        sa.Column("account_binding_id", UUID(as_uuid=True), primary_key=True),
        sa.Column(
            "portfolio_id",
            UUID(as_uuid=True),
            sa.ForeignKey(f"{SCHEMA}.portfolios.portfolio_id"),
            nullable=False,
        ),
        sa.Column("broker_code", sa.String(), nullable=False),
        sa.Column("environment", sa.String(16), nullable=False),
        sa.Column("credential_ref", sa.String(), nullable=False),
        sa.Column(
            "created_at", sa.DateTime(timezone=True), nullable=False, server_default=sa.func.now()
        ),
        sa.UniqueConstraint(
            "portfolio_id",
            "broker_code",
            "environment",
            name="uq_account_bindings_portfolio_broker_env",
        ),
        sa.CheckConstraint(
            "environment IN ('research', 'backtest', 'paper', 'live')",
            name="ck_account_bindings_environment",
        ),
        schema=SCHEMA,
    )

    op.create_table(
        "positions",
        sa.Column(
            "portfolio_id",
            UUID(as_uuid=True),
            sa.ForeignKey(f"{SCHEMA}.portfolios.portfolio_id"),
            nullable=False,
        ),
        sa.Column(
            "asset_id", sa.String(), sa.ForeignKey(f"{SCHEMA}.assets.asset_id"), nullable=False
        ),
        sa.Column("quantity", AMOUNT, nullable=False),
        sa.Column("average_cost", AMOUNT, nullable=False),
        sa.Column(
            "updated_at", sa.DateTime(timezone=True), nullable=False, server_default=sa.func.now()
        ),
        sa.PrimaryKeyConstraint("portfolio_id", "asset_id", name="pk_positions"),
        sa.CheckConstraint("quantity >= 0", name="ck_positions_quantity_non_negative"),
        schema=SCHEMA,
    )

    op.create_table(
        "cash_balances",
        sa.Column(
            "portfolio_id",
            UUID(as_uuid=True),
            sa.ForeignKey(f"{SCHEMA}.portfolios.portfolio_id"),
            nullable=False,
        ),
        sa.Column("currency", sa.String(16), nullable=False),
        sa.Column("amount", AMOUNT, nullable=False),
        sa.Column(
            "updated_at", sa.DateTime(timezone=True), nullable=False, server_default=sa.func.now()
        ),
        sa.PrimaryKeyConstraint("portfolio_id", "currency", name="pk_cash_balances"),
        schema=SCHEMA,
    )

    op.create_table(
        "valuation_snapshots",
        sa.Column("valuation_snapshot_id", UUID(as_uuid=True), primary_key=True),
        sa.Column(
            "portfolio_id",
            UUID(as_uuid=True),
            sa.ForeignKey(f"{SCHEMA}.portfolios.portfolio_id"),
            nullable=False,
        ),
        sa.Column("valuation_date", sa.Date(), nullable=False),
        sa.Column("positions_value", AMOUNT, nullable=False),
        sa.Column("cash_value", AMOUNT, nullable=False),
        sa.Column("net_asset_value", AMOUNT, nullable=False),
        sa.Column("base_currency", sa.String(16), nullable=False),
        sa.Column("price_status", sa.String(16), nullable=False),
        sa.Column(
            "created_at", sa.DateTime(timezone=True), nullable=False, server_default=sa.func.now()
        ),
        sa.UniqueConstraint(
            "portfolio_id", "valuation_date", name="uq_valuation_snapshots_portfolio_date"
        ),
        sa.CheckConstraint(
            "price_status IN ('fresh', 'stale')",
            name="ck_valuation_snapshots_price_status",
        ),
        schema=SCHEMA,
    )

    op.create_table(
        "performance_snapshots",
        sa.Column("performance_snapshot_id", UUID(as_uuid=True), primary_key=True),
        sa.Column(
            "portfolio_id",
            UUID(as_uuid=True),
            sa.ForeignKey(f"{SCHEMA}.portfolios.portfolio_id"),
            nullable=False,
        ),
        sa.Column("as_of", sa.Date(), nullable=False),
        sa.Column("total_return_pct", AMOUNT, nullable=True),
        sa.Column("max_drawdown_pct", AMOUNT, nullable=True),
        sa.Column("annualized_volatility", AMOUNT, nullable=True),
        sa.Column("sharpe_ratio", AMOUNT, nullable=True),
        sa.Column("sortino_ratio", AMOUNT, nullable=True),
        sa.Column("benchmark_return_pct", AMOUNT, nullable=True),
        sa.Column(
            "created_at", sa.DateTime(timezone=True), nullable=False, server_default=sa.func.now()
        ),
        sa.UniqueConstraint(
            "portfolio_id", "as_of", name="uq_performance_snapshots_portfolio_as_of"
        ),
        schema=SCHEMA,
    )

    op.create_table(
        "orders",
        sa.Column("order_id", UUID(as_uuid=True), primary_key=True),
        sa.Column(
            "portfolio_id",
            UUID(as_uuid=True),
            sa.ForeignKey(f"{SCHEMA}.portfolios.portfolio_id"),
            nullable=False,
        ),
        sa.Column(
            "asset_id", sa.String(), sa.ForeignKey(f"{SCHEMA}.assets.asset_id"), nullable=False
        ),
        sa.Column("side", sa.String(8), nullable=False),
        sa.Column("quantity", AMOUNT, nullable=False),
        sa.Column("order_type", sa.String(16), nullable=False),
        sa.Column("limit_price", AMOUNT, nullable=True),
        sa.Column("status", sa.String(24), nullable=False),
        sa.Column("risk_check_result", JSONB, nullable=False, server_default="{}"),
        sa.Column(
            "created_at", sa.DateTime(timezone=True), nullable=False, server_default=sa.func.now()
        ),
        sa.Column(
            "updated_at", sa.DateTime(timezone=True), nullable=False, server_default=sa.func.now()
        ),
        sa.CheckConstraint("side IN ('buy', 'sell')", name="ck_orders_side"),
        sa.CheckConstraint("order_type IN ('market', 'limit')", name="ck_orders_order_type"),
        sa.CheckConstraint(
            "status IN ('pending_risk', 'rejected', 'submitted', 'partially_filled', "
            "'filled', 'cancelled', 'unknown')",
            name="ck_orders_status",
        ),
        sa.CheckConstraint("quantity > 0", name="ck_orders_quantity_positive"),
        schema=SCHEMA,
    )

    op.create_table(
        "fills",
        sa.Column("fill_id", UUID(as_uuid=True), primary_key=True),
        sa.Column(
            "order_id",
            UUID(as_uuid=True),
            sa.ForeignKey(f"{SCHEMA}.orders.order_id"),
            nullable=False,
        ),
        sa.Column("quantity", AMOUNT, nullable=False),
        sa.Column("price", AMOUNT, nullable=False),
        sa.Column("commission", AMOUNT, nullable=False),
        sa.Column("filled_at", sa.DateTime(timezone=True), nullable=False),
        sa.CheckConstraint("quantity > 0", name="ck_fills_quantity_positive"),
        schema=SCHEMA,
    )

    # Belt-and-suspenders: deploy/postgres/init/001_roles_and_schema.sql's
    # ALTER DEFAULT PRIVILEGES should already cover this, but grant
    # explicitly too in case that bootstrap script was not applied (e.g. a
    # hand-provisioned external Postgres instance).
    for table_name in _TABLE_NAMES:
        op.execute(
            f"GRANT SELECT, INSERT, UPDATE, DELETE ON {SCHEMA}.{table_name} TO xr_core_runtime"
        )


def downgrade() -> None:
    # Drop in reverse dependency order.
    for table_name in reversed(_TABLE_NAMES):
        op.drop_table(table_name, schema=SCHEMA)
