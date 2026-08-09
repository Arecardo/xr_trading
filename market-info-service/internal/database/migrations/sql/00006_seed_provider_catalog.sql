-- +goose Up
-- Seeds the Provider and ProviderInstrument rows BE-004a needs for the three
-- instruments the new batch precision endpoint must serve (NVDA, QQQ,
-- BTC-USDT). instrument_id values below are hardcoded rather than resolved
-- with a `SELECT ... FROM core.instruments` subquery because this migration
-- runs as xr_market_data_owner, which only has REFERENCES (not SELECT) on
-- the core schema (see 00001_verify_database_prerequisites.sql and
-- deploy/postgres/init/001_roles_and_core.sql). They must stay in sync with
-- deploy/postgres/init/002_core_catalog_seed.sql, which creates the
-- corresponding core.instruments rows on a fresh database. The foreign key
-- itself is still enforced by PostgreSQL at insert time.
INSERT INTO market_data.providers (id, code, name, provider_type, status)
VALUES
    ('019fdf90-a665-7d0b-ad8b-9890e82de4ea', 'longbridge', 'Longbridge', 'BROKER', 'active'),
    ('019fdf90-a665-7d23-be32-20783f124c67', 'bybit', 'Bybit', 'EXCHANGE', 'active')
ON CONFLICT (code) DO NOTHING;

INSERT INTO market_data.provider_instruments (
    id, code, provider_id, instrument_id, external_symbol, provider_market,
    capabilities, priority, is_default, enabled
)
VALUES
    (
        '019fdf90-a665-7d3a-b5b0-99dd839fd464', 'provider.longbridge.us.nvda-us',
        '019fdf90-a665-7d0b-ad8b-9890e82de4ea', -- provider.longbridge
        '019fdf90-a665-7cb9-8bfc-1231030f50f9', -- instrument.nasdaq.equity.nvda
        'NVDA.US', 'US', '{"quote": true, "historical": true, "intervals": ["1h", "1d"]}'::jsonb,
        100, true, true
    ),
    (
        '019fdf90-a665-7d56-9508-1e0f9d3984c3', 'provider.longbridge.us.qqq-us',
        '019fdf90-a665-7d0b-ad8b-9890e82de4ea', -- provider.longbridge
        '019fdf90-a665-7cd9-ba3b-0129f116abac', -- instrument.nasdaq.etf.qqq
        'QQQ.US', 'US', '{"quote": true, "historical": true, "intervals": ["1h", "1d"]}'::jsonb,
        100, true, true
    ),
    (
        '019fdf90-a665-7d6d-b777-e9034145eaf3', 'provider.bybit.spot.btcusdt',
        '019fdf90-a665-7d23-be32-20783f124c67', -- provider.bybit
        '019fdf90-a665-7cf0-8ec4-c307741c6a9f', -- instrument.bybit.spot.btc-usdt
        'BTCUSDT', 'spot', '{"quote": true, "historical": true, "intervals": ["1h", "1d"]}'::jsonb,
        100, true, true
    )
ON CONFLICT (code) DO NOTHING;

-- +goose Down
DELETE FROM market_data.provider_instruments WHERE code IN (
    'provider.longbridge.us.nvda-us', 'provider.longbridge.us.qqq-us', 'provider.bybit.spot.btcusdt'
);
DELETE FROM market_data.providers WHERE code IN ('longbridge', 'bybit');
