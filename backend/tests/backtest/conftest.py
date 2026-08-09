"""Shared pytest fixtures for backtest tests.

Async tests use the `anyio` pytest plugin (``@pytest.mark.anyio``) rather
than adding ``pytest-asyncio`` as a new dependency: ``anyio`` is already a
transitive dependency of ``httpx``/``fastapi``/``starlette`` and ships its
own auto-registered pytest plugin.
"""

from __future__ import annotations

import pytest


@pytest.fixture
def anyio_backend() -> str:
    # Restrict to the asyncio backend only -- trio is not a project
    # dependency and we don't need to test against it.
    return "asyncio"
