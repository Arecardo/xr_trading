"""SQLAlchemy Core table definitions for CONTRACT-004 (BE-002).

These are plain ``sqlalchemy.Table`` objects on a shared ``MetaData`` --
deliberately *not* ORM model classes bound to the frozen domain dataclasses
in ``domain/*/models.py``. The domain layer must stay pure Python with zero
SQLAlchemy/DB-driver imports (python-backend-standards.md §2: "领域层不依赖
HTTP、DB 驱动或 Provider SDK"); the repository implementations in this
package (``assets.py``, ``portfolios.py``, ``accounts.py``) are the only
code that translates between a ``Row`` here and a domain dataclass.

Table shapes transcribe doc/technical/implementation_plan/
01_contracts_and_foundation.md §0.4 (CONTRACT-004, frozen) column-for-column.
All ten frozen tables are declared here so downstream tasks (BE-003
valuation, a future execution task) are never blocked waiting on a table to
exist, even though this task only ships repository implementations for
three of them (assets/portfolios+portfolio_members/account_bindings) --
see the module docstring in ``repository/__init__.py`` for the full
migration-vs-repository coverage matrix.

Numeric precision follows CONTRACT-004's closing note: amount/quantity
fields use ``numeric(38,18)``; weight fields (0..1 range) use
``numeric(9,6)``. All such columns must round-trip through Python
``decimal.Decimal`` -- never ``float`` (python-backend-standards.md §1, §6).

Tables live in a dedicated ``core`` schema (mirroring market-info-service's
``core`` schema pattern in a *separate* database instance -- no table
sharing, project-conventions.md §2) so schema-level privileges can be
granted narrowly: ``xr_core_owner`` owns the schema and runs DDL,
``xr_core_runtime`` only gets DML (see
deploy/postgres/init/001_roles_and_schema.sql).
"""

from __future__ import annotations

from sqlalchemy import (
    CheckConstraint,
    Column,
    Date,
    DateTime,
    ForeignKey,
    MetaData,
    Numeric,
    PrimaryKeyConstraint,
    String,
    Table,
    UniqueConstraint,
    func,
)
from sqlalchemy.dialects.postgresql import JSONB, UUID

SCHEMA = "core"

metadata = MetaData(schema=SCHEMA)

AMOUNT = Numeric(38, 18)
WEIGHT = Numeric(9, 6)

# --- assets -----------------------------------------------------------
#
# asset_id format: CONTRACT-004 specifies "type:venue:symbol" (e.g.
# "equity:nasdaq:NVDA", "crypto:bybit:BTC-USDT"), but CONTRACT-001 §0.2
# (target_weights dict keys) also uses the *two*-segment form "cash:USD" as
# an example asset_id. These two frozen documents disagree on whether CASH
# assets have a venue segment. Modeling this permissively (2 or 3 colon-
# separated segments) rather than picking one interpretation and silently
# discarding the other example -- flagged as an open question in the BE-002
# report for the integration lead to resolve via CONTRACT-004 amendment.
assets = Table(
    "assets",
    metadata,
    Column("asset_id", String, primary_key=True),
    Column("asset_type", String(16), nullable=False),
    Column("symbol", String, nullable=False),
    Column("venue", String, nullable=False),
    Column("quote_currency", String(16), nullable=False),
    Column("provider_symbols", JSONB, nullable=False, server_default="{}"),
    Column("trading_status", String(16), nullable=False),
    Column("created_at", DateTime(timezone=True), nullable=False, server_default=func.now()),
    Column(
        "updated_at",
        DateTime(timezone=True),
        nullable=False,
        server_default=func.now(),
        onupdate=func.now(),
    ),
    CheckConstraint(
        "asset_type IN ('STOCK', 'ETF', 'CRYPTO', 'CASH')",
        name="ck_assets_asset_type",
    ),
    CheckConstraint(
        "trading_status IN ('watch', 'tradable', 'paused', 'blacklist')",
        name="ck_assets_trading_status",
    ),
    CheckConstraint(
        r"asset_id ~ '^[a-z][a-z0-9_]*(:[A-Za-z0-9_.\-]+){1,2}$'",
        name="ck_assets_asset_id_format",
    ),
)

# --- portfolios ---------------------------------------------------------

portfolios = Table(
    "portfolios",
    metadata,
    Column("portfolio_id", UUID(as_uuid=True), primary_key=True),
    Column("name", String, nullable=False),
    Column("base_currency", String(16), nullable=False),
    Column(
        "benchmark_asset_id",
        String,
        ForeignKey(f"{SCHEMA}.assets.asset_id"),
        nullable=True,
    ),
    Column("risk_level", String, nullable=False),
    Column("execution_mode", String(16), nullable=False),
    Column("status", String(16), nullable=False),
    Column("created_at", DateTime(timezone=True), nullable=False, server_default=func.now()),
    Column(
        "updated_at",
        DateTime(timezone=True),
        nullable=False,
        server_default=func.now(),
        onupdate=func.now(),
    ),
    CheckConstraint(
        "execution_mode IN ('research', 'backtest', 'paper', 'live')",
        name="ck_portfolios_execution_mode",
    ),
    CheckConstraint(
        "status IN ('draft', 'active', 'paused', 'archived')",
        name="ck_portfolios_status",
    ),
)

# --- portfolio_members ---------------------------------------------------

portfolio_members = Table(
    "portfolio_members",
    metadata,
    Column(
        "portfolio_id",
        UUID(as_uuid=True),
        ForeignKey(f"{SCHEMA}.portfolios.portfolio_id"),
        nullable=False,
    ),
    Column("asset_id", String, ForeignKey(f"{SCHEMA}.assets.asset_id"), nullable=False),
    Column("member_status", String(16), nullable=False),
    Column("target_weight_min", WEIGHT, nullable=True),
    Column("target_weight_max", WEIGHT, nullable=True),
    Column("added_at", DateTime(timezone=True), nullable=False, server_default=func.now()),
    PrimaryKeyConstraint("portfolio_id", "asset_id", name="pk_portfolio_members"),
    CheckConstraint(
        "member_status IN ('candidate', 'approved', 'held', 'restricted')",
        name="ck_portfolio_members_status",
    ),
)

# --- account_bindings -----------------------------------------------------

account_bindings = Table(
    "account_bindings",
    metadata,
    Column("account_binding_id", UUID(as_uuid=True), primary_key=True),
    Column(
        "portfolio_id",
        UUID(as_uuid=True),
        ForeignKey(f"{SCHEMA}.portfolios.portfolio_id"),
        nullable=False,
    ),
    Column("broker_code", String, nullable=False),
    Column("environment", String(16), nullable=False),
    # Reference only -- never the credential itself (security-standards.md §2).
    Column("credential_ref", String, nullable=False),
    Column("created_at", DateTime(timezone=True), nullable=False, server_default=func.now()),
    UniqueConstraint(
        "portfolio_id",
        "broker_code",
        "environment",
        name="uq_account_bindings_portfolio_broker_env",
    ),
    CheckConstraint(
        "environment IN ('research', 'backtest', 'paper', 'live')",
        name="ck_account_bindings_environment",
    ),
)

# --- positions -----------------------------------------------------------

positions = Table(
    "positions",
    metadata,
    Column(
        "portfolio_id",
        UUID(as_uuid=True),
        ForeignKey(f"{SCHEMA}.portfolios.portfolio_id"),
        nullable=False,
    ),
    Column("asset_id", String, ForeignKey(f"{SCHEMA}.assets.asset_id"), nullable=False),
    Column("quantity", AMOUNT, nullable=False),
    Column("average_cost", AMOUNT, nullable=False),
    Column(
        "updated_at",
        DateTime(timezone=True),
        nullable=False,
        server_default=func.now(),
        onupdate=func.now(),
    ),
    PrimaryKeyConstraint("portfolio_id", "asset_id", name="pk_positions"),
    CheckConstraint("quantity >= 0", name="ck_positions_quantity_non_negative"),
)

# --- cash_balances ---------------------------------------------------------

cash_balances = Table(
    "cash_balances",
    metadata,
    Column(
        "portfolio_id",
        UUID(as_uuid=True),
        ForeignKey(f"{SCHEMA}.portfolios.portfolio_id"),
        nullable=False,
    ),
    Column("currency", String(16), nullable=False),
    Column("amount", AMOUNT, nullable=False),
    Column(
        "updated_at",
        DateTime(timezone=True),
        nullable=False,
        server_default=func.now(),
        onupdate=func.now(),
    ),
    PrimaryKeyConstraint("portfolio_id", "currency", name="pk_cash_balances"),
)

# --- valuation_snapshots ----------------------------------------------------

valuation_snapshots = Table(
    "valuation_snapshots",
    metadata,
    Column("valuation_snapshot_id", UUID(as_uuid=True), primary_key=True),
    Column(
        "portfolio_id",
        UUID(as_uuid=True),
        ForeignKey(f"{SCHEMA}.portfolios.portfolio_id"),
        nullable=False,
    ),
    Column("valuation_date", Date, nullable=False),
    Column("positions_value", AMOUNT, nullable=False),
    Column("cash_value", AMOUNT, nullable=False),
    Column("net_asset_value", AMOUNT, nullable=False),
    Column("base_currency", String(16), nullable=False),
    Column("price_status", String(16), nullable=False),
    Column("created_at", DateTime(timezone=True), nullable=False, server_default=func.now()),
    UniqueConstraint(
        "portfolio_id", "valuation_date", name="uq_valuation_snapshots_portfolio_date"
    ),
    CheckConstraint(
        "price_status IN ('fresh', 'stale')",
        name="ck_valuation_snapshots_price_status",
    ),
)

# --- performance_snapshots --------------------------------------------------
#
# Metric columns are nullable: CONTRACT-004 lists them but does not mark
# NOT NULL, and some (e.g. sharpe_ratio) are undefined with insufficient
# history -- a snapshot must still be recordable in that case rather than
# forced to fabricate a value (python-backend-standards.md's "no false
# precision" spirit, echoed in BE-003's ValuationSnapshot price_status
# design).

performance_snapshots = Table(
    "performance_snapshots",
    metadata,
    Column("performance_snapshot_id", UUID(as_uuid=True), primary_key=True),
    Column(
        "portfolio_id",
        UUID(as_uuid=True),
        ForeignKey(f"{SCHEMA}.portfolios.portfolio_id"),
        nullable=False,
    ),
    Column("as_of", Date, nullable=False),
    Column("total_return_pct", AMOUNT, nullable=True),
    Column("max_drawdown_pct", AMOUNT, nullable=True),
    Column("annualized_volatility", AMOUNT, nullable=True),
    Column("sharpe_ratio", AMOUNT, nullable=True),
    Column("sortino_ratio", AMOUNT, nullable=True),
    Column("benchmark_return_pct", AMOUNT, nullable=True),
    Column("created_at", DateTime(timezone=True), nullable=False, server_default=func.now()),
    UniqueConstraint("portfolio_id", "as_of", name="uq_performance_snapshots_portfolio_as_of"),
)

# --- orders -----------------------------------------------------------------

orders = Table(
    "orders",
    metadata,
    Column("order_id", UUID(as_uuid=True), primary_key=True),
    Column(
        "portfolio_id",
        UUID(as_uuid=True),
        ForeignKey(f"{SCHEMA}.portfolios.portfolio_id"),
        nullable=False,
    ),
    Column("asset_id", String, ForeignKey(f"{SCHEMA}.assets.asset_id"), nullable=False),
    Column("side", String(8), nullable=False),
    Column("quantity", AMOUNT, nullable=False),
    Column("order_type", String(16), nullable=False),
    Column("limit_price", AMOUNT, nullable=True),
    Column("status", String(24), nullable=False),
    Column("risk_check_result", JSONB, nullable=False, server_default="{}"),
    Column("created_at", DateTime(timezone=True), nullable=False, server_default=func.now()),
    Column(
        "updated_at",
        DateTime(timezone=True),
        nullable=False,
        server_default=func.now(),
        onupdate=func.now(),
    ),
    CheckConstraint("side IN ('buy', 'sell')", name="ck_orders_side"),
    CheckConstraint("order_type IN ('market', 'limit')", name="ck_orders_order_type"),
    CheckConstraint(
        "status IN ('pending_risk', 'rejected', 'submitted', 'partially_filled', "
        "'filled', 'cancelled', 'unknown')",
        name="ck_orders_status",
    ),
    CheckConstraint("quantity > 0", name="ck_orders_quantity_positive"),
)

# --- fills --------------------------------------------------------------

fills = Table(
    "fills",
    metadata,
    Column("fill_id", UUID(as_uuid=True), primary_key=True),
    Column("order_id", UUID(as_uuid=True), ForeignKey(f"{SCHEMA}.orders.order_id"), nullable=False),
    Column("quantity", AMOUNT, nullable=False),
    Column("price", AMOUNT, nullable=False),
    Column("commission", AMOUNT, nullable=False),
    Column("filled_at", DateTime(timezone=True), nullable=False),
    CheckConstraint("quantity > 0", name="ck_fills_quantity_positive"),
)

__all__ = [
    "SCHEMA",
    "metadata",
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
]
