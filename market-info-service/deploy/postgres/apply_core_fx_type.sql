-- Manual counterpart of deploy/postgres/init/003_add_fx_asset_instrument_type.sql
-- for a database that already ran 001_roles_and_core.sql / 002_core_catalog_seed.sql
-- on a previous fresh-volume bootstrap (docker-entrypoint-initdb.d only runs
-- init/ scripts once, on first container start). Apply this with a role that
-- has core-schema write access (e.g. the cluster superuser, or a role
-- granted membership in xr_core_owner) before applying
-- 004_fx_catalog_seed.sql's INSERT statements by hand.

SET ROLE xr_core_owner;

DO $$
DECLARE
    existing_constraint text;
BEGIN
    SELECT conname INTO existing_constraint
    FROM pg_constraint
    WHERE conrelid = 'core.assets'::regclass
      AND contype = 'c'
      AND pg_get_constraintdef(oid) ILIKE '%asset_type%';
    IF existing_constraint IS NOT NULL THEN
        EXECUTE format('ALTER TABLE core.assets DROP CONSTRAINT %I', existing_constraint);
    END IF;
END
$$;

ALTER TABLE core.assets
    ADD CONSTRAINT ck_assets_asset_type
    CHECK (asset_type IN ('STOCK', 'ETF', 'CRYPTO', 'CASH', 'FX'));

DO $$
DECLARE
    existing_constraint text;
BEGIN
    SELECT conname INTO existing_constraint
    FROM pg_constraint
    WHERE conrelid = 'core.instruments'::regclass
      AND contype = 'c'
      AND pg_get_constraintdef(oid) ILIKE '%instrument_type%';
    IF existing_constraint IS NOT NULL THEN
        EXECUTE format('ALTER TABLE core.instruments DROP CONSTRAINT %I', existing_constraint);
    END IF;
END
$$;

ALTER TABLE core.instruments
    ADD CONSTRAINT ck_instruments_instrument_type
    CHECK (instrument_type IN ('EQUITY', 'ETF', 'SPOT', 'FX'));

RESET ROLE;
