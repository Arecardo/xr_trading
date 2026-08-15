"""Unit tests for backtest.instrument_ids.INSTRUMENT_ID_BY_CODE."""

from __future__ import annotations

from uuid import UUID

from backtest.instrument_ids import INSTRUMENT_ID_BY_CODE


def test_mapping_covers_the_fixed_dec_001_universe_plus_fx() -> None:
    assert set(INSTRUMENT_ID_BY_CODE) == {
        "instrument.nasdaq.equity.nvda",
        "instrument.nasdaq.etf.qqq",
        "instrument.bybit.spot.btc-usdt",
        "instrument.coingecko.fx.usdt-usd",
    }


def test_every_value_is_a_uuid() -> None:
    for instrument_code, instrument_id in INSTRUMENT_ID_BY_CODE.items():
        assert isinstance(instrument_id, UUID), instrument_code


def test_all_uuids_are_distinct() -> None:
    values = list(INSTRUMENT_ID_BY_CODE.values())
    assert len(values) == len(set(values))
