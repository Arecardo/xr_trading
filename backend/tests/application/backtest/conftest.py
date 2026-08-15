"""Shared pytest fixtures for application.backtest tests.

Mirrors ``tests/backtest/conftest.py``: async tests use the `anyio` pytest
plugin rather than adding ``pytest-asyncio`` as a new dependency.
"""

from __future__ import annotations

import pytest


@pytest.fixture
def anyio_backend() -> str:
    return "asyncio"
