"""Data access implementations for the repository Protocols declared in ``domain``.

BE-002 (CONTRACT-004) landed:

* ``schema.py`` -- SQLAlchemy Core ``Table``/``MetaData`` definitions for
  all ten frozen tables (``assets``, ``portfolios``, ``portfolio_members``,
  ``account_bindings``, ``positions``, ``cash_balances``,
  ``valuation_snapshots``, ``performance_snapshots``, ``orders``, ``fills``).
* ``database.py`` -- environment-variable-driven, fail-closed engine/pool
  construction (sync ``psycopg`` for the repositories below and for
  Alembic; async ``asyncpg`` as general-purpose infrastructure for future
  consumers).
* ``errors.py`` -- ``NotFoundError``/``CredentialReuseError``/
  ``AmbiguousBindingError`` raised by the implementations below.
* ``assets.py`` / ``portfolios.py`` / ``accounts.py`` -- concrete
  implementations of ``AssetRepository``, ``PortfolioRepository``, and
  ``AccountBindingRepository`` (CONTRACT-001 §0.1). These three Protocols'
  methods are synchronous, so the implementations run on the sync engine.

Migration coverage vs. repository coverage: all ten CONTRACT-004 tables
exist via the Alembic migration in ``backend/migrations/versions/``.
BE-003 added ``valuation.py`` -- implementations of
``domain.valuation.models.PositionRepository``/``CashBalanceRepository``/
``ValuationSnapshotRepository``/``PerformanceSnapshotRepository`` (four
Protocols BE-003 itself defines; CONTRACT-001 never froze per-entity
repository interfaces for the ``valuation`` subpackage the way it did for
``assets``/``portfolios``/``accounts``). ``orders``/``fills`` (a future
execution task) remain migration-only for now, by design -- CONTRACT-001
does not freeze an ``ExecutionService`` implementation for this task to
satisfy.
"""
