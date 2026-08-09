"""Unit tests for repository.schema (BE-002, CONTRACT-004).

Pure introspection of the SQLAlchemy Core ``Table``/``MetaData`` objects --
no DB connection required. Confirms all ten CONTRACT-004 tables are present
with the frozen primary keys / uniqueness constraints, and that
amount/quantity columns use ``Numeric`` (never a float type), matching
python-backend-standards.md §1/§6.
"""

from __future__ import annotations

from decimal import Decimal

from sqlalchemy import Numeric

from repository import schema

CONTRACT_004_TABLES = {
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
}


def test_all_ten_contract_004_tables_are_declared() -> None:
    declared = set(schema.metadata.tables.keys())
    # Table keys are schema-qualified ("core.assets"); strip the prefix.
    names = {name.split(".", 1)[1] for name in declared}
    assert names == CONTRACT_004_TABLES


def test_all_tables_live_in_the_core_schema() -> None:
    for table in schema.metadata.tables.values():
        assert table.schema == schema.SCHEMA


def test_amount_columns_use_numeric_38_18_not_float() -> None:
    amount_columns = [
        (schema.positions, "quantity"),
        (schema.positions, "average_cost"),
        (schema.cash_balances, "amount"),
        (schema.valuation_snapshots, "positions_value"),
        (schema.valuation_snapshots, "cash_value"),
        (schema.valuation_snapshots, "net_asset_value"),
        (schema.orders, "quantity"),
        (schema.orders, "limit_price"),
        (schema.fills, "quantity"),
        (schema.fills, "price"),
        (schema.fills, "commission"),
    ]
    for table, column_name in amount_columns:
        column = table.c[column_name]
        assert isinstance(column.type, Numeric), f"{table.name}.{column_name} must be Numeric"
        assert (column.type.precision, column.type.scale) == (38, 18)
        assert column.type.asdecimal is not False


def test_weight_columns_use_numeric_9_6() -> None:
    for column_name in ("target_weight_min", "target_weight_max"):
        column = schema.portfolio_members.c[column_name]
        assert isinstance(column.type, Numeric)
        assert (column.type.precision, column.type.scale) == (9, 6)


def test_primary_keys_match_contract_004() -> None:
    assert [c.name for c in schema.assets.primary_key] == ["asset_id"]
    assert [c.name for c in schema.portfolios.primary_key] == ["portfolio_id"]
    assert {c.name for c in schema.portfolio_members.primary_key} == {"portfolio_id", "asset_id"}
    assert [c.name for c in schema.account_bindings.primary_key] == ["account_binding_id"]
    assert {c.name for c in schema.positions.primary_key} == {"portfolio_id", "asset_id"}
    assert {c.name for c in schema.cash_balances.primary_key} == {"portfolio_id", "currency"}
    assert [c.name for c in schema.valuation_snapshots.primary_key] == ["valuation_snapshot_id"]
    assert [c.name for c in schema.performance_snapshots.primary_key] == ["performance_snapshot_id"]
    assert [c.name for c in schema.orders.primary_key] == ["order_id"]
    assert [c.name for c in schema.fills.primary_key] == ["fill_id"]


def test_unique_constraints_match_contract_004() -> None:
    def unique_column_sets(table: object) -> set[frozenset[str]]:
        return {
            frozenset(col.name for col in c.columns)
            for c in table.constraints  # type: ignore[attr-defined]
            if c.__class__.__name__ == "UniqueConstraint"
        }

    assert frozenset({"portfolio_id", "broker_code", "environment"}) in unique_column_sets(
        schema.account_bindings
    )
    assert frozenset({"portfolio_id", "valuation_date"}) in unique_column_sets(
        schema.valuation_snapshots
    )
    assert frozenset({"portfolio_id", "as_of"}) in unique_column_sets(schema.performance_snapshots)


def test_decimal_values_are_representable_without_float() -> None:
    # Sanity check the intent, not the DB: a Decimal must be usable directly
    # with these column types without ever routing through float().
    value = Decimal("123.456789012345678901")
    assert isinstance(value, Decimal)
    assert float(value) != value  # demonstrates why float() must never be used for this data
