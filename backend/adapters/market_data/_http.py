"""Shared HTTP request/response plumbing for this package's clients.

Internal module (leading underscore): not part of the public
``adapters.market_data`` surface. Both ``client.py`` (public query API) and
``subscriptions.py`` (admin collection-subscription API) send requests to
the same `market-info-service` and must translate transport failures and
the `07_api_and_admin_ui.md` §8 error envelope the same way, so the logic
lives here once instead of being duplicated per client (contrast with the
intentional backtest/adapters duplication noted in
`backend/backtest/market_data_client.py` -- that is duplication *across*
packages with different owners/scopes; this is duplication *within* one
package, which the same standards call for consolidating).
"""

from __future__ import annotations

from typing import Any

import httpx

from .errors import MarketDataResponseError, MarketDataUnavailableError


async def send_json_request(
    client: httpx.AsyncClient, method: str, path: str, **kwargs: Any
) -> dict[str, Any]:
    """Send a request and return its decoded JSON object body.

    Raises ``MarketDataUnavailableError`` if the request could not complete
    at all (connection failure, DNS error, timeout -- any
    ``httpx.RequestError``). Raises ``MarketDataResponseError`` if the
    service responded but with a non-2xx status, a non-JSON body, or a body
    that is not a JSON object.
    """
    try:
        response = await client.request(method, path, **kwargs)
    except httpx.TimeoutException as exc:
        raise MarketDataUnavailableError(
            f"market-info-service request to {method} {path} timed out"
        ) from exc
    except httpx.RequestError as exc:
        raise MarketDataUnavailableError(
            f"market-info-service request to {method} {path} failed: {exc}"
        ) from exc

    try:
        payload = response.json()
    except ValueError as exc:
        raise MarketDataResponseError(
            f"non-JSON response from {method} {path} "
            f"(status {response.status_code}): {response.text!r}"
        ) from exc

    if response.is_error:
        error = payload.get("error") if isinstance(payload, dict) else None
        code = error.get("code") if isinstance(error, dict) else None
        message = error.get("message") if isinstance(error, dict) else None
        raise MarketDataResponseError(
            f"market-info-service returned {response.status_code} for {method} {path} "
            f"(code={code!r}, message={message!r})"
        )

    if not isinstance(payload, dict):
        raise MarketDataResponseError(
            f"expected a JSON object response from {method} {path}, got {type(payload).__name__}"
        )
    return payload


__all__ = ["send_json_request"]
