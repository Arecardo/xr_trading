"""Static ``instrument_code`` -> ``instrument_id`` mapping for BT-003 precision lookups.

**Design choice (BT-003), explicitly documented per the task brief's
instruction not to leave this unaddressed:** ``CONTRACT-005``'s batch
precision endpoint (wrapped by
``adapters.market_data.precision_cache.PrecisionCache``) is keyed by
``instrument_id: UUID``, but ``adapters.market_data.models.InstrumentRef``
(and ``adapters.market_data.asset_instrument_resolver``, which BT-003 reuses
to resolve an ``asset_id`` to an ``instrument_code``/``provider`` pair) only
carries the string ``instrument_code``/``provider``, never the UUID. Turning
one into the other needs an actual lookup somewhere.

Two options were on the table:

1. A live call, e.g. ``MarketInfoClient.get_instrument_options`` per
   backtest run, to resolve ``instrument_code -> instrument_id``.
2. A small static mapping, mirroring
   ``adapters.market_data.asset_instrument_resolver.StaticAssetInstrumentResolver``'s
   own rationale for the DEC-001 fixed asset universe (three tradable
   members plus one FX reference instrument, none of which change without a
   deliberate DEC-001-style decision).

**Option 2 is chosen here**, for two reasons:

- It keeps BT-003's per-simulated-day loop network-free (the task brief's
  own instruction: "fast, no network calls per simulated day" -- the whole
  point of loading history into memory via ``HistoricalDataLoader`` up
  front). A live instrument-id lookup would only need to happen once per
  backtest run, not once per day, so this is not strictly required by that
  constraint alone -- but avoiding it entirely is simpler and consistent
  with how ``StaticAssetInstrumentResolver`` already made the same call for
  the same reason.
- This module intentionally does **not** modify
  ``adapters/market_data/asset_instrument_resolver.py`` itself (a BE-003
  file this task does not own, per the single-owner scoping instructions).
  A separate, narrowly-scoped, BT-003-owned static mapping keeps a clean
  single-writer boundary per module (`.claude/standards/
  development-workflow.md` §5: "每个目录设唯一写入者") rather than reaching into
  another task's frozen file to bolt on a second, unrelated concern
  (instrument_id lookup) that BE-003 never needed.

The four UUIDs below are transcribed verbatim from the task brief's "known
concrete facts" section, itself sourced from
``market-info-service/deploy/postgres/init/002_core_catalog_seed.sql`` and
``004_fx_catalog_seed.sql`` -- already-seeded ground truth, not invented
here, and not claimed to be independently re-verified by this task.

**Must be revisited alongside ``asset_instrument_resolver.py``** if the
asset universe becomes dynamic -- see that module's own docstring for the
matching caveat.
"""

from __future__ import annotations

from typing import Final
from uuid import UUID

INSTRUMENT_ID_BY_CODE: Final[dict[str, UUID]] = {
    "instrument.nasdaq.equity.nvda": UUID("019fdf90-a665-7cb9-8bfc-1231030f50f9"),
    "instrument.nasdaq.etf.qqq": UUID("019fdf90-a665-7cd9-ba3b-0129f116abac"),
    "instrument.bybit.spot.btc-usdt": UUID("019fdf90-a665-7cf0-8ec4-c307741c6a9f"),
    "instrument.coingecko.fx.usdt-usd": UUID("01a004b4-765d-78a6-8861-f0e6087f2ec3"),
}

__all__ = ["INSTRUMENT_ID_BY_CODE"]
