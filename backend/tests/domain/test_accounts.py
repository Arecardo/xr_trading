"""Unit tests for domain.accounts: instantiation and expected field types."""

from __future__ import annotations

from typing import Literal
from uuid import UUID, uuid4

import pytest

from domain.accounts import AccountBinding


@pytest.mark.parametrize("environment", ["research", "backtest", "paper", "live"])
def test_account_binding_accepts_every_environment(
    environment: Literal["research", "backtest", "paper", "live"],
) -> None:
    binding = AccountBinding(
        account_binding_id=uuid4(),
        portfolio_id=uuid4(),
        broker_code="longbridge",
        environment=environment,
        credential_ref="secretref:longbridge:paper:001",
    )

    assert isinstance(binding.account_binding_id, UUID)
    assert isinstance(binding.portfolio_id, UUID)
    assert binding.broker_code == "longbridge"
    assert binding.environment == environment
    assert binding.credential_ref == "secretref:longbridge:paper:001"
