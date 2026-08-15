-- +goose Up
-- Seeds the CoinGecko Provider, its ProviderInstrument mapping for the
-- fx:coingecko:usdt-usd reference rate, and an always-enabled
-- collection_subscription for it (BE-003a / RM0 DEC-006).
--
-- instrument_id below is hardcoded rather than resolved with a
-- `SELECT ... FROM core.instruments` subquery for the same reason
-- 00006_seed_provider_catalog.sql hardcodes NVDA/QQQ/BTC-USDT: this
-- migration runs as xr_market_data_owner, which only has REFERENCES (not
-- SELECT) on the core schema (see 00001_verify_database_prerequisites.sql
-- and deploy/postgres/init/001_roles_and_core.sql). It must stay in sync
-- with deploy/postgres/init/004_fx_catalog_seed.sql, which creates the
-- corresponding core.assets/core.instruments rows on a fresh database. The
-- foreign key itself is still enforced by PostgreSQL at insert time.
--
-- Unlike every other Instrument's collection, this subscription is NOT
-- driven by portfolio membership: FX reference-rate collection scope is
-- "does the asset universe contain a non-base-currency holding", not
-- portfolio membership (doc/technical/03_universe_data.md §7, RM0 DEC-006).
-- Since market-info-service itself has no portfolio-membership concept --
-- that lives in `backend` and only ever calls this service's subscription
-- API for instruments it resolves through Asset/PortfolioMember, and FX
-- instruments are never a PortfolioMember candidate -- the always-enabled
-- row seeded here is the simplest correct implementation: nothing in the
-- existing subscription-sync flow can ever see or disable it.
INSERT INTO market_data.providers (id, code, name, provider_type, status)
VALUES
    ('01a004b4-765d-78b6-b219-de97f180314a', 'coingecko', 'CoinGecko', 'AGGREGATOR', 'active')
ON CONFLICT (code) DO NOTHING;

INSERT INTO market_data.provider_instruments (
    id, code, provider_id, instrument_id, external_symbol, provider_market,
    capabilities, priority, is_default, enabled
)
VALUES
    (
        '01a004b4-765d-78c2-9035-3885b36d2de0', 'provider.coingecko.fx.usdt-usd',
        '01a004b4-765d-78b6-b219-de97f180314a', -- provider.coingecko
        '01a004b4-765d-78a6-8861-f0e6087f2ec3', -- instrument.coingecko.fx.usdt-usd
        'tether', 'fx', '{"quote": true, "historical": true, "intervals": ["1d"]}'::jsonb,
        100, true, true
    )
ON CONFLICT (code) DO NOTHING;

INSERT INTO market_data.collection_subscriptions (
    id, provider_instrument_id, interval, enabled, priority, close_delay_seconds,
    revision_delay_seconds, metadata
)
VALUES
    (
        '01a004b4-765d-78cd-8d62-37cd50c1f3db',
        '01a004b4-765d-78c2-9035-3885b36d2de0', -- provider.coingecko.fx.usdt-usd
        '1d', true, 100, 300, NULL,
        '{"reason": "RM0 DEC-006: always-on, not portfolio-membership-driven"}'::jsonb
    )
ON CONFLICT (provider_instrument_id, interval) DO NOTHING;

-- +goose Down
DELETE FROM market_data.collection_subscriptions WHERE id = '01a004b4-765d-78cd-8d62-37cd50c1f3db';
DELETE FROM market_data.provider_instruments WHERE code = 'provider.coingecko.fx.usdt-usd';
DELETE FROM market_data.providers WHERE code = 'coingecko';
