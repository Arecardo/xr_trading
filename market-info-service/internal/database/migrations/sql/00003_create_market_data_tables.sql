-- +goose Up
CREATE TABLE market_data.market_bars (
    instrument_id uuid NOT NULL REFERENCES core.instruments(id),
    provider_instrument_id uuid NOT NULL REFERENCES market_data.provider_instruments(id),
    interval varchar(8) NOT NULL,
    open_time timestamptz NOT NULL,
    revision integer NOT NULL DEFAULT 1,
    close_time timestamptz NOT NULL,
    open_price numeric(38,18) NOT NULL,
    high_price numeric(38,18) NOT NULL,
    low_price numeric(38,18) NOT NULL,
    close_price numeric(38,18) NOT NULL,
    base_volume numeric(38,18),
    quote_volume numeric(38,18),
    trade_count bigint,
    is_closed boolean NOT NULL,
    is_current boolean NOT NULL DEFAULT true,
    quality_status varchar(16) NOT NULL DEFAULT 'unchecked',
    provider_updated_at timestamptz,
    collected_at timestamptz NOT NULL DEFAULT now(),
    raw_hash varchar(64),
    metadata jsonb NOT NULL DEFAULT '{}',
    PRIMARY KEY (instrument_id, provider_instrument_id, interval, open_time, revision),
    CONSTRAINT ck_market_bars_interval CHECK (interval IN ('1h', '1d')),
    CONSTRAINT ck_market_bars_revision CHECK (revision > 0),
    CONSTRAINT ck_market_bars_time CHECK (close_time > open_time),
    CONSTRAINT ck_market_bars_ohlc CHECK (
        low_price <= open_price AND low_price <= close_price
        AND high_price >= open_price AND high_price >= close_price
        AND high_price >= low_price
    ),
    CONSTRAINT ck_market_bars_volume CHECK (
        (base_volume IS NULL OR base_volume >= 0)
        AND (quote_volume IS NULL OR quote_volume >= 0)
    ),
    CONSTRAINT ck_market_bars_quality CHECK (
        quality_status IN ('unchecked', 'valid', 'warning', 'invalid')
    )
);
CREATE UNIQUE INDEX uq_market_bars_current
    ON market_data.market_bars (instrument_id, provider_instrument_id, interval, open_time)
    WHERE is_current = true;
CREATE INDEX idx_market_bars_query
    ON market_data.market_bars (instrument_id, interval, open_time DESC)
    WHERE is_current = true AND quality_status IN ('valid', 'warning');

CREATE TABLE market_data.latest_quotes (
    instrument_id uuid NOT NULL REFERENCES core.instruments(id),
    provider_instrument_id uuid NOT NULL REFERENCES market_data.provider_instruments(id),
    market_time timestamptz NOT NULL,
    last_price numeric(38,18) NOT NULL,
    bid_price numeric(38,18),
    bid_size numeric(38,18),
    ask_price numeric(38,18),
    ask_size numeric(38,18),
    open_24h numeric(38,18),
    high_24h numeric(38,18),
    low_24h numeric(38,18),
    base_volume_24h numeric(38,18),
    quote_volume_24h numeric(38,18),
    quality_status varchar(16) NOT NULL DEFAULT 'unchecked',
    collected_at timestamptz NOT NULL DEFAULT now(),
    metadata jsonb NOT NULL DEFAULT '{}',
    PRIMARY KEY (instrument_id, provider_instrument_id),
    CONSTRAINT ck_latest_quotes_last_price CHECK (last_price >= 0),
    CONSTRAINT ck_latest_quotes_bid_price CHECK (bid_price IS NULL OR bid_price >= 0),
    CONSTRAINT ck_latest_quotes_ask_price CHECK (ask_price IS NULL OR ask_price >= 0),
    CONSTRAINT ck_latest_quotes_quality CHECK (
        quality_status IN ('unchecked', 'valid', 'warning', 'invalid')
    )
);

-- +goose Down
DROP TABLE IF EXISTS market_data.latest_quotes;
DROP TABLE IF EXISTS market_data.market_bars;
