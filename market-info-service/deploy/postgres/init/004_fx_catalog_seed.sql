-- Seeds the fx:coingecko:usdt-usd Asset + Instrument BE-003a needs (RM0
-- DEC-006, USDT->USD reference rate). This file follows
-- 003_add_fx_asset_instrument_type.sql, which must run first so the 'FX'
-- CHECK values already exist. Like 002_core_catalog_seed.sql, it only runs
-- automatically via docker-entrypoint-initdb.d on a FRESH PostgreSQL data
-- volume; existing dev/staging databases must have this applied manually by
-- a role with core-schema write access (see that file's header for why).
--
-- `venue` is 'COINGECKO': FX reference data has no real trading venue, so
-- (like BTC-USDT's venue=BYBIT, where provider and venue coincide for a
-- single-source crypto pair) the sourcing provider is the closest available
-- meaning for this required, non-nullable field.
--
-- `quote_currency` is 'USD': the Instrument represents "1 USDT priced in
-- USD", matching the direction ValuationSnapshot needs to convert a
-- USDT-denominated holding into the portfolio's USD base currency.
--
-- price_scale=6 gives headroom for a stablecoin rate that normally prints
-- close to 1.000000 with small deviations; quantity_scale/lot_size/
-- min_quantity are left NULL because this Instrument is never traded or
-- sized -- those fields describe order-sizing rules that do not apply here.
SET ROLE xr_core_owner;

INSERT INTO core.assets (id, code, asset_type, canonical_symbol, name, status)
VALUES
    ('01a004b4-765d-77c8-93f7-509f31cfafc8', 'asset.fx.usdt-usd', 'FX', 'USDT-USD', 'USDT to USD reference rate', 'active')
ON CONFLICT (code) DO NOTHING;

INSERT INTO core.instruments (
    id, code, asset_id, venue, instrument_type, symbol, quote_currency, market_timezone,
    price_scale, quantity_scale, lot_size, min_quantity, status
)
VALUES
    (
        '01a004b4-765d-78a6-8861-f0e6087f2ec3', 'instrument.coingecko.fx.usdt-usd',
        '01a004b4-765d-77c8-93f7-509f31cfafc8', 'COINGECKO', 'FX', 'USDT-USD', 'USD', 'UTC',
        6, NULL, NULL, NULL, 'active'
    )
ON CONFLICT (code) DO NOTHING;

RESET ROLE;
