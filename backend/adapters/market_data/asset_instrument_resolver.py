"""Static ``AssetInstrumentResolver`` for the fixed DEC-001 asset universe (BE-003).

Resolves BE-004's flagged gap: ``subscriptions.AssetInstrumentResolver`` was
deliberately left a bare ``Protocol`` ("this module does not guess at that
mapping" -- see that module's docstring) because deriving a `market-info-
service` ``instrument_code``/``provider`` from a `backend` ``asset_id`` is
not a formula, it is a fact that has to be looked up somewhere. This module
is that lookup, for the first three portfolio members.

Why a static mapping is the honest choice here, not a shortcut: DEC-001
freezes the first-batch asset universe to exactly three tradable members
(``equity:nasdaq:NVDA``, ``equity:nasdaq:QQQ``, ``crypto:bybit:BTC-USDT``)
plus one FX reference instrument (DEC-006, BE-003a:
``fx:coingecko:usdt-usd``). A dynamic resolver (e.g. calling `market-info-
service`'s ``GET /instruments?asset_code=...`` at runtime and picking the
default/highest-priority provider) would be *more* general but would also
silently paper over the fact that "which provider is authoritative for this
asset" is a decision this project already made once (DEC-001's venue/provider
choices) and does not need to re-derive per call. The values below are
transcribed verbatim from `market-info-service/deploy/postgres/init/
002_core_catalog_seed.sql`, `004_fx_catalog_seed.sql`, and
`internal/database/migrations/sql/00006_seed_provider_catalog.sql`/
`00007_seed_coingecko_fx_provider.sql` (already-seeded ground truth, not
invented here).

**This module must be revisited if the asset universe becomes dynamic**
(candidate assets added/removed at runtime rather than via a DEC-001-style
frozen decision) -- at that point a catalog-lookup-backed resolver (the
"query ``/instruments?asset_code=``" alternative this module's docstring
flags but does not implement) becomes the honest choice instead, because a
static Python dict cannot track a mapping that changes without a code
deploy.
"""

from __future__ import annotations

from collections.abc import Sequence
from typing import Final

from .models import InstrumentRef

_DAILY_INTERVAL: Final = "1d"

# asset_id -> (instrument_code, provider). Transcribed from the
# market-info-service seed data referenced in the module docstring; do not
# add an entry here without a corresponding, already-seeded Instrument.
_ASSET_INSTRUMENT_MAPPING: Final[dict[str, tuple[str, str]]] = {
    "equity:nasdaq:NVDA": ("instrument.nasdaq.equity.nvda", "longbridge"),
    "equity:nasdaq:QQQ": ("instrument.nasdaq.etf.qqq", "longbridge"),
    "crypto:bybit:BTC-USDT": ("instrument.bybit.spot.btc-usdt", "bybit"),
}

# (from_currency, to_currency) -> (instrument_code, provider). Only
# USDT->USD is wired today (DEC-006): it is the only non-base-currency quote
# currency anywhere in the current fixed universe (NVDA/QQQ quote in USD,
# every portfolio's base_currency is USD, BTC-USDT quotes in USDT). Adding a
# new asset that quotes in a currency other than USD/USDT must add its FX
# pair here explicitly -- callers must never assume an unlisted pair is 1:1.
_FX_INSTRUMENT_MAPPING: Final[dict[tuple[str, str], tuple[str, str]]] = {
    ("USDT", "USD"): ("instrument.coingecko.fx.usdt-usd", "coingecko"),
}


class StaticAssetInstrumentResolver:
    """``subscriptions.AssetInstrumentResolver`` implementation for the DEC-001 universe.

    ``intervals`` controls which ``InstrumentRef``s are returned per asset
    (default: daily only, ``("1d",)``) -- BE-004's ``compute_desired_instruments``
    and BE-003's ``ValuationService`` both only need daily bars today; a
    future caller wanting e.g. hourly collection too would pass
    ``intervals=("1d", "1h")``.
    """

    def __init__(self, intervals: Sequence[str] = (_DAILY_INTERVAL,)) -> None:
        if not intervals:
            raise ValueError("intervals must not be empty")
        self._intervals = tuple(intervals)

    def resolve(self, asset_id: str) -> Sequence[InstrumentRef]:
        """Return the ``InstrumentRef``s for ``asset_id``, one per configured interval.

        Returns an empty sequence for an ``asset_id`` outside the static
        mapping (e.g. ``cash:USD``, which has no market-info-service
        Instrument at all) -- per the ``AssetInstrumentResolver`` Protocol's
        own docstring, an unmapped asset is this resolver's choice to treat
        as "nothing to collect", not an error.
        """
        mapping = _ASSET_INSTRUMENT_MAPPING.get(asset_id)
        if mapping is None:
            return ()
        instrument_code, provider = mapping
        return tuple(
            InstrumentRef(provider=provider, instrument_code=instrument_code, interval=interval)
            for interval in self._intervals
        )


def resolve_fx_instrument(from_currency: str, to_currency: str) -> InstrumentRef | None:
    """Resolve a currency-pair conversion to its market-info-service FX Instrument (DEC-006).

    Returns ``None`` for any pair other than ``("USDT", "USD")`` -- callers
    (``application.valuation.service.SqlValuationService``) must treat a
    ``None`` result as "this backend does not know how to convert this
    currency pair" and fail closed, never assume a 1:1 rate.
    """
    mapping = _FX_INSTRUMENT_MAPPING.get((from_currency, to_currency))
    if mapping is None:
        return None
    instrument_code, provider = mapping
    return InstrumentRef(
        provider=provider, instrument_code=instrument_code, interval=_DAILY_INTERVAL
    )


__all__ = ["StaticAssetInstrumentResolver", "resolve_fx_instrument"]
