"""Error hierarchy for ``application.backtest`` (BT-003).

Mirrors the repo-wide convention (python-backend-standards.md §5,
``application/valuation/errors.py``, ``domain/strategies/errors.py``) of a
stable domain exception hierarchy rather than surfacing raw ``KeyError``/
config-typo failures to callers. Every one of these means "the backtest
cannot proceed at all" (a setup/wiring problem) -- as opposed to an ordinary,
expected per-order outcome during the day loop (a risk rejection, a
below-``min_quantity`` restricted-mode rounding, a last-day-with-no-next-day
skip), which are never exceptions here: they are recorded as a
``application.backtest.models.TradeRecord`` with a non-``"filled"`` status,
per the task brief's "never silently dropped" instruction.
"""

from __future__ import annotations


class BacktestEngineError(Exception):
    """Base class for all ``application.backtest`` errors."""


class InvalidBacktestConfigError(BacktestEngineError):
    """A ``BacktestConfig`` was constructed with an invalid value.

    Fail-closed at construction (mirrors
    ``domain.risk.simple_risk_policy.SimpleRiskPolicy``'s eager threshold
    validation) rather than producing a confusing failure partway through a
    multi-day run.
    """


class UnresolvedInstrumentError(BacktestEngineError):
    """A portfolio member (or benchmark) asset has no known market-info-service
    Instrument to load history for.

    Mirrors ``application.valuation.errors.UnresolvedInstrumentError`` --
    raised when ``adapters.market_data.asset_instrument_resolver`` has no
    mapping for the ``asset_id`` in question at setup time (before the day
    loop starts), never guessed.
    """


class UnsupportedFxPairError(BacktestEngineError):
    """A tradable asset's ``quote_currency`` differs from the portfolio's
    ``base_currency``, and there is no known FX conversion path between them.

    Mirrors ``application.valuation.errors.UnsupportedFxPairError`` (DEC-006):
    fail-closed, never assumes an unlisted currency pair is 1:1. Raised once
    at setup time, not per simulated day -- the currency pairs a portfolio's
    tradable universe needs are fixed for the whole run.
    """


__all__ = [
    "BacktestEngineError",
    "InvalidBacktestConfigError",
    "UnresolvedInstrumentError",
    "UnsupportedFxPairError",
]
