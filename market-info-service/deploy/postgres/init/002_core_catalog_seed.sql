-- Seeds the initial core catalog rows needed by BE-004a (CONTRACT-005 batch
-- precision query). This file follows 001_roles_and_core.sql: it only runs
-- automatically via docker-entrypoint-initdb.d on a FRESH PostgreSQL data
-- volume. Existing dev/staging databases must have this applied manually by
-- a role with core-schema write access (core.assets/core.instruments are not
-- writable by xr_market_data_owner or xr_market_data_runtime -- see
-- deploy/postgres/init/001_roles_and_core.sql, which grants those roles only
-- REFERENCES/SELECT on the core schema).
--
-- Precision sourcing (see BE-004a report for full citations):
--   * NVDA / QQQ (Longbridge, NASDAQ): price tick $0.01 for stocks >= $1,
--     minimum order = 1 whole share, fractional shares not supported.
--     Source: https://longbridge.com/sg/zh-CN/support/topics/Stocksandetfs/UStradingrules
--     ("美股最小交易单位是 1 股（暂不支持碎股交易）";
--      "股票价格最小变动单位：1美元以下为0.0001美元，1美元以上为0.01美元").
--   * BTC-USDT (Bybit spot): PLACEHOLDER -- unverified against the live
--     Bybit instruments-info endpoint. This environment has no outbound
--     network access to api.bybit.com/www.bybit.com (connection refused /
--     timed out on every attempt), and the only reachable secondary sources
--     (a web search summary and the generic Bybit API docs example page)
--     disagreed with each other (tickSize 0.01 vs 0.1 example; quotePrecision
--     1e-8 vs 1e-7 example), so neither is trustworthy as a verified current
--     value. DO NOT feed these four BTC-USDT fields into real order-quantity
--     rounding or risk checks until a maintainer confirms them against a live
--     GET https://api.bybit.com/v5/market/instruments-info?category=spot&symbol=BTCUSDT
--     response (see lotSizeFilter.basePrecision/quotePrecision/minOrderQty and
--     priceFilter.tickSize).

SET ROLE xr_core_owner;

INSERT INTO core.assets (id, code, asset_type, canonical_symbol, name, status)
VALUES
    ('019fdf90-a665-7962-addd-67d7884ea86d', 'asset.equity.us.nvda', 'STOCK', 'NVDA', 'NVIDIA Corporation', 'active'),
    ('019fdf90-a665-7c35-8bac-856fa41c788c', 'asset.etf.us.qqq', 'ETF', 'QQQ', 'Invesco QQQ Trust', 'active'),
    ('019fdf90-a665-7c96-9284-a007bbc41ffd', 'asset.crypto.btc', 'CRYPTO', 'BTC', 'Bitcoin', 'active')
ON CONFLICT (code) DO NOTHING;

INSERT INTO core.instruments (
    id, code, asset_id, venue, instrument_type, symbol, quote_currency, market_timezone,
    price_scale, quantity_scale, lot_size, min_quantity, status
)
VALUES
    (
        '019fdf90-a665-7cb9-8bfc-1231030f50f9', 'instrument.nasdaq.equity.nvda',
        '019fdf90-a665-7962-addd-67d7884ea86d', 'NASDAQ', 'EQUITY', 'NVDA', 'USD', 'America/New_York',
        2, 0, 1, 1, 'active'
    ),
    (
        '019fdf90-a665-7cd9-ba3b-0129f116abac', 'instrument.nasdaq.etf.qqq',
        '019fdf90-a665-7c35-8bac-856fa41c788c', 'NASDAQ', 'ETF', 'QQQ', 'USD', 'America/New_York',
        2, 0, 1, 1, 'active'
    ),
    (
        -- PLACEHOLDER: price_scale/quantity_scale/lot_size/min_quantity below
        -- are unverified against the live Bybit API. See the file header.
        '019fdf90-a665-7cf0-8ec4-c307741c6a9f', 'instrument.bybit.spot.btc-usdt',
        '019fdf90-a665-7c96-9284-a007bbc41ffd', 'BYBIT', 'SPOT', 'BTC-USDT', 'USDT', 'UTC',
        2, 6, 0.000001, 0.0001, 'active'
    )
ON CONFLICT (code) DO NOTHING;

RESET ROLE;
