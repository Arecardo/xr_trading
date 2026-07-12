-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF current_setting('server_version_num')::integer < 150000 THEN
        RAISE EXCEPTION 'market-info-service requires PostgreSQL 15 or newer';
    END IF;

    IF to_regclass('core.assets') IS NULL
       OR to_regclass('core.instruments') IS NULL
       OR to_regclass('core.asset_aliases') IS NULL
       OR to_regclass('core.instrument_aliases') IS NULL THEN
        RAISE EXCEPTION 'core catalog prerequisite is missing; run the core bootstrap/migrations first';
    END IF;
END
$$;
-- +goose StatementEnd

-- +goose Down
SELECT 1;
