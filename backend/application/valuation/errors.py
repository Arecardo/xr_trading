"""Error hierarchy for ``application.valuation`` (BE-003).

Mirrors the repo-wide convention (python-backend-standards.md §5,
``domain/strategies/errors.py``, ``adapters/market_data/errors.py``) of a
stable domain exception hierarchy rather than surfacing raw ``KeyError``/
``httpx``/asyncio failures to callers. Every one of these is a fail-closed
signal: ``ValuationService.generate_snapshot`` must not persist a snapshot
and must not fall back to a guessed value when one of these is raised (see
``domain.valuation.models.ValuationService.generate_snapshot``'s own
docstring).

``repository.errors.NotFoundError`` (portfolio not found, no snapshot ever
generated for ``current_state``) is allowed to propagate directly rather
than being re-wrapped here -- it is already a clear, specific error, and
re-wrapping it would only make callers catch two types for the same
condition.
"""

from __future__ import annotations


class ValuationError(Exception):
    """Base class for all ``application.valuation`` errors."""


class UnresolvedInstrumentError(ValuationError):
    """A position's (or the FX conversion's) asset has no known market-info-service Instrument.

    Raised when ``adapters.market_data.asset_instrument_resolver`` (or an
    equivalent injected resolver) has no mapping for the asset/currency pair
    in question. Fail-closed: the caller must not guess a price for an
    asset it cannot even identify upstream.
    """


class MissingPriceDataError(ValuationError):
    """No bar exists on or before the requested valuation date for a required instrument.

    Distinct from a *stale* price (a bar exists, just not dated the
    requested day -- that is a normal, expected outcome on non-trading days
    per DEC-002, and produces ``price_status="stale"``, not this error).
    This is raised only when the upstream `/bars` history has no data at
    all up to and including the requested date -- e.g. before the
    instrument's collection start, or a genuine data gap.
    """


class UnsupportedFxPairError(ValuationError):
    """A position's ``quote_currency`` differs from the portfolio's ``base_currency``,
    and there is no known FX conversion path between them.

    Fail-closed per DEC-006/python-backend-standards.md's "no false
    precision" rule: never assumes an unlisted currency pair is 1:1.
    """


class AsyncBridgingError(RuntimeError):
    """``SqlValuationService`` was called from a thread with an already-running event loop.

    ``ValuationService``'s Protocol methods are synchronous (frozen,
    CONTRACT-001) but internally drive the async ``MarketInfoClient`` via
    ``asyncio.run()`` (see ``application.valuation.service`` module
    docstring for the full sync/async bridging decision). ``asyncio.run()``
    itself raises a generic, easy-to-misdiagnose ``RuntimeError`` when
    called from inside a running loop; this subclass exists so that failure
    mode -- "you called a sync valuation method from async code without
    ``asyncio.to_thread``" -- is diagnosable at a glance instead of looking
    like an ``asyncio`` internals bug. Deliberately a ``RuntimeError``, not a
    ``ValuationError``: this is a caller-integration bug, not a valuation
    business-logic failure, and should not be caught by code that only means
    to handle "price data was unavailable."
    """


__all__ = [
    "AsyncBridgingError",
    "MissingPriceDataError",
    "UnresolvedInstrumentError",
    "UnsupportedFxPairError",
    "ValuationError",
]
