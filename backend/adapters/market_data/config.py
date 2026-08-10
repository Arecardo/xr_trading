"""Environment-variable configuration for the `market-info-service` client.

Fail-closed posture (security-standards.md §4, mirroring
`repository/database.py`'s ``require_env``): every value is read from an
environment variable with no built-in default; a missing/blank variable
raises ``MarketDataConfigError`` at the point it is needed, rather than
silently falling back to a predictable local URL or credential.

Two separate configs, because the two halves of this client have different
auth postures (07_api_and_admin_ui.md §2.5, §7):

- Public query routes (`/quotes/latest`, `/bars`, `/instruments`,
  `/instruments/precision:batch`) do not go through the admin permission
  middleware at all -- only a base URL is required.
- The collection-subscription admin API (§4) requires a bearer token
  scoped to ``subscriptions.manage``. Naming
  (``MARKET_INFO_MANAGE_BEARER_TOKEN``) matches the variable the existing
  `backend/app.py` BFF prototype already reads for the same upstream
  credential (§7) -- this is the same secret for the same purpose, not a
  new one; from this package's own perspective (`adapters/market_data` has
  no config today) it is nonetheless a newly-required variable, and reading
  it fails closed exactly like every other credential in this repo.
"""

from __future__ import annotations

import os
from dataclasses import dataclass

from .errors import MarketDataConfigError

SERVICE_URL_ENV_VAR = "MARKET_INFO_SERVICE_URL"
ADMIN_BEARER_TOKEN_ENV_VAR = "MARKET_INFO_MANAGE_BEARER_TOKEN"


def _require_env(name: str, *, purpose: str) -> str:
    value = os.environ.get(name)
    if not value:
        raise MarketDataConfigError(
            f"{name} is not set; refusing to {purpose} with an implicit/default value "
            "(security-standards.md §4: fail closed on missing configuration)."
        )
    return value


@dataclass(frozen=True)
class MarketDataClientConfig:
    """Base URL for `market-info-service`'s public query API."""

    base_url: str


def load_client_config() -> MarketDataClientConfig:
    """Read ``MARKET_INFO_SERVICE_URL``.

    Raises ``MarketDataConfigError`` if unset/blank. There is no localhost
    default: an unconfigured process must not silently start talking to
    whatever happens to be listening on a guessed port.
    """
    base_url = _require_env(SERVICE_URL_ENV_VAR, purpose="construct a market-info-service client")
    return MarketDataClientConfig(base_url=base_url)


@dataclass(frozen=True)
class SubscriptionSyncConfig:
    """Admin bearer token for the collection-subscription API."""

    admin_bearer_token: str


def load_subscription_sync_config() -> SubscriptionSyncConfig:
    """Read ``MARKET_INFO_MANAGE_BEARER_TOKEN``.

    Raises ``MarketDataConfigError`` if unset/blank. Only called when
    collection-subscription sync is actually invoked -- a process that never
    exercises subscription sync (e.g. one that only reads quotes/bars/
    precision) never needs this credential and never fails on its absence.
    """
    token = _require_env(
        ADMIN_BEARER_TOKEN_ENV_VAR, purpose="sync collection-subscriptions (admin write access)"
    )
    return SubscriptionSyncConfig(admin_bearer_token=token)


__all__ = [
    "ADMIN_BEARER_TOKEN_ENV_VAR",
    "SERVICE_URL_ENV_VAR",
    "MarketDataClientConfig",
    "SubscriptionSyncConfig",
    "load_client_config",
    "load_subscription_sync_config",
]
