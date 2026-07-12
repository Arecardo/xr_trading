ALTER SCHEMA market_data OWNER TO xr_market_data_owner;
REVOKE ALL ON SCHEMA market_data FROM PUBLIC;
GRANT USAGE ON SCHEMA market_data TO xr_market_data_runtime;
GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA market_data TO xr_market_data_runtime;
GRANT SELECT ON market_data.schema_migrations TO xr_market_data_runtime;

ALTER DEFAULT PRIVILEGES FOR ROLE xr_market_data_owner IN SCHEMA market_data
    GRANT SELECT, INSERT, UPDATE ON TABLES TO xr_market_data_runtime;
