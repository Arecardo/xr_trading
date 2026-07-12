DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'xr_core_owner') THEN
        CREATE ROLE xr_core_owner NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'xr_market_data_owner') THEN
        CREATE ROLE xr_market_data_owner NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'xr_market_data_runtime') THEN
        CREATE ROLE xr_market_data_runtime NOLOGIN;
    END IF;
END
$$;

DO $$
BEGIN
    EXECUTE format(
        'GRANT CREATE ON DATABASE %I TO xr_market_data_owner',
        current_database()
    );
END
$$;

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA IF NOT EXISTS core AUTHORIZATION xr_core_owner;
REVOKE ALL ON SCHEMA core FROM PUBLIC;

SET ROLE xr_core_owner;

CREATE TABLE core.assets (
    id uuid PRIMARY KEY,
    code varchar(128) NOT NULL UNIQUE CHECK (code = lower(code)),
    asset_type varchar(16) NOT NULL CHECK (asset_type IN ('STOCK', 'ETF', 'CRYPTO', 'CASH')),
    canonical_symbol varchar(64) NOT NULL,
    name varchar(255) NOT NULL,
    status varchar(16) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'delisted')),
    metadata jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_assets_symbol ON core.assets (canonical_symbol);
CREATE INDEX idx_assets_type_status ON core.assets (asset_type, status);

CREATE TABLE core.instruments (
    id uuid PRIMARY KEY,
    code varchar(160) NOT NULL UNIQUE CHECK (code = lower(code)),
    asset_id uuid NOT NULL REFERENCES core.assets(id),
    venue varchar(32) NOT NULL,
    instrument_type varchar(24) NOT NULL CHECK (instrument_type IN ('EQUITY', 'ETF', 'SPOT')),
    symbol varchar(64) NOT NULL,
    quote_asset_id uuid REFERENCES core.assets(id),
    quote_currency varchar(16) NOT NULL,
    market_timezone varchar(64) NOT NULL,
    price_scale smallint CHECK (price_scale IS NULL OR price_scale BETWEEN 0 AND 18),
    quantity_scale smallint CHECK (quantity_scale IS NULL OR quantity_scale BETWEEN 0 AND 18),
    lot_size numeric(38,18) CHECK (lot_size IS NULL OR lot_size > 0),
    min_quantity numeric(38,18) CHECK (min_quantity IS NULL OR min_quantity > 0),
    status varchar(16) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'delisted')),
    valid_from timestamptz,
    valid_to timestamptz,
    metadata jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_instruments_valid_range CHECK (
        valid_to IS NULL OR valid_from IS NULL OR valid_to > valid_from
    )
);
CREATE UNIQUE INDEX uq_active_instrument_market_symbol
    ON core.instruments (venue, instrument_type, symbol) WHERE valid_to IS NULL;
CREATE INDEX idx_instruments_asset ON core.instruments (asset_id, status);

CREATE TABLE core.asset_aliases (
    id uuid PRIMARY KEY,
    asset_id uuid NOT NULL REFERENCES core.assets(id),
    alias_code varchar(128) NOT NULL UNIQUE CHECK (alias_code = lower(alias_code)),
    valid_from timestamptz,
    valid_to timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_asset_aliases_valid_range CHECK (
        valid_to IS NULL OR valid_from IS NULL OR valid_to > valid_from
    )
);

CREATE TABLE core.instrument_aliases (
    id uuid PRIMARY KEY,
    instrument_id uuid NOT NULL REFERENCES core.instruments(id),
    alias_code varchar(160) NOT NULL UNIQUE CHECK (alias_code = lower(alias_code)),
    valid_from timestamptz,
    valid_to timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT ck_instrument_aliases_valid_range CHECK (
        valid_to IS NULL OR valid_from IS NULL OR valid_to > valid_from
    )
);

RESET ROLE;
GRANT USAGE ON SCHEMA core TO xr_market_data_owner, xr_market_data_runtime;
GRANT REFERENCES ON core.assets, core.instruments TO xr_market_data_owner;
GRANT SELECT ON ALL TABLES IN SCHEMA core TO xr_market_data_runtime;
