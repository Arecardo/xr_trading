"""Alembic migration environment for the XR-Trading core backend (BE-002).

Reads the migration DSN from ``BACKEND_MIGRATION_DATABASE_URL`` only --
never from ``alembic.ini`` -- and fails closed (raises) if it is unset,
per security-standards.md §4. This DSN connects as the ``xr_core_owner``
role (see repository/database.py's module docstring and
deploy/postgres/init/001_roles_and_schema.sql), which must already exist --
apply that bootstrap SQL first on a brand new Postgres instance that isn't
managed via backend/compose.yaml's docker-entrypoint-initdb.d mount.
"""

from __future__ import annotations

from logging.config import fileConfig

from alembic import context
from sqlalchemy import engine_from_config, pool

from repository.database import MIGRATION_DSN_ENV_VAR, require_env
from repository.schema import metadata

config = context.config

if config.config_file_name is not None:
    fileConfig(config.config_file_name)

target_metadata = metadata

# Fail closed: raises DatabaseConfigError if BACKEND_MIGRATION_DATABASE_URL
# is unset, rather than silently falling back to alembic.ini's (absent)
# sqlalchemy.url or some other default.
#
# `%` must be escaped as `%%` before going through set_main_option(): it
# stores the value in a configparser.ConfigParser with interpolation
# enabled, and our DSN's percent-encoded `options=-c%20role%3D...` query
# parameter is otherwise parsed as (invalid) interpolation syntax.
_migration_dsn = require_env(MIGRATION_DSN_ENV_VAR)
config.set_main_option("sqlalchemy.url", _migration_dsn.replace("%", "%%"))


def run_migrations_offline() -> None:
    """Emit SQL to stdout without a live DB connection (``alembic upgrade --sql``)."""
    url = config.get_main_option("sqlalchemy.url")
    context.configure(
        url=url,
        target_metadata=target_metadata,
        literal_binds=True,
        dialect_opts={"paramstyle": "named"},
        version_table_schema=target_metadata.schema,
        include_schemas=True,
    )
    with context.begin_transaction():
        context.run_migrations()


def run_migrations_online() -> None:
    """Run migrations against a live database connection."""
    connectable = engine_from_config(
        config.get_section(config.config_ini_section, {}),
        prefix="sqlalchemy.",
        poolclass=pool.NullPool,
    )

    with connectable.connect() as connection:
        context.configure(
            connection=connection,
            target_metadata=target_metadata,
            version_table_schema=target_metadata.schema,
            include_schemas=True,
        )

        with context.begin_transaction():
            context.run_migrations()


if context.is_offline_mode():
    run_migrations_offline()
else:
    run_migrations_online()
