-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'xr_market_data_owner')
       OR NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'xr_market_data_runtime') THEN
        RAISE EXCEPTION 'market information database roles are missing; run the database bootstrap first';
    END IF;
END
$$;
-- +goose StatementEnd

ALTER SCHEMA market_data OWNER TO xr_market_data_owner;
REVOKE ALL ON SCHEMA market_data FROM PUBLIC;
GRANT USAGE ON SCHEMA market_data TO xr_market_data_runtime;
GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA market_data TO xr_market_data_runtime;

ALTER DEFAULT PRIVILEGES FOR ROLE xr_market_data_owner IN SCHEMA market_data
    GRANT SELECT, INSERT, UPDATE ON TABLES TO xr_market_data_runtime;

-- +goose Down
ALTER DEFAULT PRIVILEGES FOR ROLE xr_market_data_owner IN SCHEMA market_data
    REVOKE SELECT, INSERT, UPDATE ON TABLES FROM xr_market_data_runtime;
REVOKE SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA market_data FROM xr_market_data_runtime;
REVOKE USAGE ON SCHEMA market_data FROM xr_market_data_runtime;
