"""Integration tests for SqlAccountBindingRepository against a real PostgreSQL instance.

Requires BACKEND_TEST_ADMIN_DATABASE_URL to be set and reachable -- see
conftest.py.
"""

from __future__ import annotations

from uuid import UUID, uuid4

import pytest
from sqlalchemy import insert
from sqlalchemy.engine import Engine

from repository.accounts import SqlAccountBindingRepository
from repository.errors import CredentialReuseError, NotFoundError
from repository.schema import account_bindings as account_bindings_table
from repository.schema import portfolios as portfolios_table


def _insert_portfolio(engine: Engine, portfolio_id: UUID) -> None:
    with engine.begin() as conn:
        conn.execute(
            insert(portfolios_table).values(
                portfolio_id=portfolio_id,
                name="Test Portfolio",
                base_currency="USD",
                benchmark_asset_id=None,
                risk_level="moderate",
                execution_mode="paper",
                status="active",
            )
        )


def _insert_binding(
    engine: Engine,
    portfolio_id: UUID,
    *,
    broker_code: str = "longbridge",
    environment: str = "paper",
    credential_ref: str = "vault://paper/longbridge/portfolio-1",
) -> UUID:
    account_binding_id = uuid4()
    with engine.begin() as conn:
        conn.execute(
            insert(account_bindings_table).values(
                account_binding_id=account_binding_id,
                portfolio_id=portfolio_id,
                broker_code=broker_code,
                environment=environment,
                credential_ref=credential_ref,
            )
        )
    return account_binding_id


class TestGetForPortfolio:
    def test_returns_matching_binding(self, owner_engine: Engine, runtime_engine: Engine) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        _insert_binding(owner_engine, portfolio_id, environment="paper")
        repo = SqlAccountBindingRepository(runtime_engine)

        binding = repo.get_for_portfolio(portfolio_id, "paper", "longbridge")

        assert binding.portfolio_id == portfolio_id
        assert binding.environment == "paper"
        assert binding.broker_code == "longbridge"
        assert binding.credential_ref == "vault://paper/longbridge/portfolio-1"

    def test_raises_not_found_when_no_binding(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        repo = SqlAccountBindingRepository(runtime_engine)

        with pytest.raises(NotFoundError):
            repo.get_for_portfolio(portfolio_id, "live", "longbridge")

    def test_disambiguates_multiple_brokers_by_broker_code(
        self, owner_engine: Engine, runtime_engine: Engine
    ) -> None:
        # CONTRACT-004's account_bindings uniqueness is on (portfolio_id,
        # broker_code, environment): a single portfolio legitimately holds
        # bindings to more than one broker in the same environment at once
        # (the actual first-batch universe needs both -- NVDA/QQQ via
        # longbridge, BTC-USDT via bybit). broker_code was added to
        # get_for_portfolio (2026-08-05, CONTRACT-001 amendment) so this is
        # resolved by an explicit parameter, not a guess.
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        _insert_binding(
            owner_engine,
            portfolio_id,
            broker_code="longbridge",
            environment="paper",
            credential_ref="vault://paper/longbridge/portfolio-1",
        )
        _insert_binding(
            owner_engine,
            portfolio_id,
            broker_code="bybit",
            environment="paper",
            credential_ref="vault://paper/bybit/portfolio-1",
        )
        repo = SqlAccountBindingRepository(runtime_engine)

        longbridge_binding = repo.get_for_portfolio(portfolio_id, "paper", "longbridge")
        bybit_binding = repo.get_for_portfolio(portfolio_id, "paper", "bybit")

        assert longbridge_binding.credential_ref == "vault://paper/longbridge/portfolio-1"
        assert bybit_binding.credential_ref == "vault://paper/bybit/portfolio-1"

    def test_environment_isolation(self, owner_engine: Engine, runtime_engine: Engine) -> None:
        portfolio_id = uuid4()
        _insert_portfolio(owner_engine, portfolio_id)
        _insert_binding(
            owner_engine,
            portfolio_id,
            environment="paper",
            credential_ref="vault://paper/longbridge/portfolio-1",
        )
        _insert_binding(
            owner_engine,
            portfolio_id,
            environment="live",
            credential_ref="vault://live/longbridge/portfolio-1",
        )
        repo = SqlAccountBindingRepository(runtime_engine)

        paper_binding = repo.get_for_portfolio(portfolio_id, "paper", "longbridge")
        live_binding = repo.get_for_portfolio(portfolio_id, "live", "longbridge")

        assert paper_binding.credential_ref != live_binding.credential_ref


class TestAssertNoCredentialReuse:
    def test_distinct_refs_do_not_raise(self, runtime_engine: Engine) -> None:
        repo = SqlAccountBindingRepository(runtime_engine)

        repo.assert_no_credential_reuse(
            "vault://paper/longbridge/portfolio-1", "vault://live/longbridge/portfolio-1"
        )

    def test_identical_refs_raise_credential_reuse_error(self, runtime_engine: Engine) -> None:
        repo = SqlAccountBindingRepository(runtime_engine)

        with pytest.raises(CredentialReuseError):
            repo.assert_no_credential_reuse(
                "vault://shared/longbridge/portfolio-1", "vault://shared/longbridge/portfolio-1"
            )
