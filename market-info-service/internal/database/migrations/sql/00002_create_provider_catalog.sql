-- +goose Up
CREATE TABLE market_data.providers (
    id uuid PRIMARY KEY,
    code varchar(64) NOT NULL,
    name varchar(128) NOT NULL,
    provider_type varchar(24) NOT NULL,
    status varchar(16) NOT NULL DEFAULT 'active',
    metadata jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_providers_code UNIQUE (code),
    CONSTRAINT ck_providers_code CHECK (code = lower(code)),
    CONSTRAINT ck_providers_type CHECK (provider_type IN ('BROKER', 'EXCHANGE', 'AGGREGATOR')),
    CONSTRAINT ck_providers_status CHECK (status IN ('active', 'disabled', 'degraded'))
);

CREATE TABLE market_data.provider_instruments (
    id uuid PRIMARY KEY,
    code varchar(192) NOT NULL,
    provider_id uuid NOT NULL REFERENCES market_data.providers(id),
    instrument_id uuid NOT NULL REFERENCES core.instruments(id),
    external_symbol varchar(128) NOT NULL,
    provider_market varchar(32) NOT NULL,
    capabilities jsonb NOT NULL DEFAULT '{}',
    priority smallint NOT NULL DEFAULT 100,
    is_default boolean NOT NULL DEFAULT false,
    enabled boolean NOT NULL DEFAULT true,
    valid_from timestamptz,
    valid_to timestamptz,
    metadata jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_provider_instruments_code UNIQUE (code),
    CONSTRAINT ck_provider_instruments_code CHECK (code = lower(code)),
    CONSTRAINT ck_provider_instruments_priority CHECK (priority >= 0),
    CONSTRAINT ck_provider_instruments_valid_range CHECK (
        valid_to IS NULL OR valid_from IS NULL OR valid_to > valid_from
    )
);

CREATE UNIQUE INDEX uq_active_provider_external_symbol
    ON market_data.provider_instruments (provider_id, provider_market, external_symbol)
    WHERE valid_to IS NULL;
CREATE INDEX idx_provider_instruments_instrument
    ON market_data.provider_instruments (instrument_id, enabled);
CREATE UNIQUE INDEX uq_enabled_default_provider_instrument
    ON market_data.provider_instruments (instrument_id)
    WHERE enabled = true AND is_default = true AND valid_to IS NULL;

CREATE TABLE market_data.collection_subscriptions (
    id uuid PRIMARY KEY,
    provider_instrument_id uuid NOT NULL REFERENCES market_data.provider_instruments(id),
    interval varchar(8) NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    priority smallint NOT NULL DEFAULT 100,
    close_delay_seconds integer NOT NULL DEFAULT 120,
    revision_delay_seconds integer,
    metadata jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT uq_collection_subscription UNIQUE (provider_instrument_id, interval),
    CONSTRAINT ck_collection_interval CHECK (interval IN ('1h', '1d')),
    CONSTRAINT ck_collection_priority CHECK (priority >= 0),
    CONSTRAINT ck_collection_close_delay CHECK (close_delay_seconds >= 0),
    CONSTRAINT ck_collection_revision_delay CHECK (
        revision_delay_seconds IS NULL OR revision_delay_seconds >= 0
    )
);

-- +goose Down
DROP TABLE IF EXISTS market_data.collection_subscriptions;
DROP TABLE IF EXISTS market_data.provider_instruments;
DROP TABLE IF EXISTS market_data.providers;
