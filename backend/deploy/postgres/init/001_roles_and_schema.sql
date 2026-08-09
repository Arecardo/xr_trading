-- Bootstrap roles and schema for the XR-Trading core backend database
-- (BE-002, CONTRACT-004). Mirrors the role-separation pattern established
-- by market-info-service/deploy/postgres/init/001_roles_and_core.sql:
--
--   * xr_core_owner   (NOLOGIN) -- owns the `core` schema, runs Alembic
--                                  migrations (DDL) via `SET ROLE` after
--                                  connecting as a superuser/admin login.
--   * xr_core_runtime (NOLOGIN) -- read/write DML only on `core` tables,
--                                  used by the application's connection
--                                  pool at runtime. No DDL rights.
--
-- This file only runs automatically via docker-entrypoint-initdb.d on a
-- FRESH PostgreSQL data volume (see backend/compose.yaml). It does not
-- create any tables -- table DDL is owned exclusively by the versioned
-- Alembic migrations under backend/migrations/versions/ (project-
-- conventions.md §1: "数据库真相：版本化 migration"). Existing/external
-- Postgres instances that don't go through docker-entrypoint-initdb.d must
-- have this applied manually by a superuser before running `alembic
-- upgrade head`.

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'xr_core_owner') THEN
        CREATE ROLE xr_core_owner NOLOGIN;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'xr_core_runtime') THEN
        CREATE ROLE xr_core_runtime NOLOGIN;
    END IF;
END
$$;

DO $$
BEGIN
    EXECUTE format(
        'GRANT CREATE ON DATABASE %I TO xr_core_owner',
        current_database()
    );
END
$$;

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA IF NOT EXISTS core AUTHORIZATION xr_core_owner;
REVOKE ALL ON SCHEMA core FROM PUBLIC;

GRANT USAGE ON SCHEMA core TO xr_core_owner, xr_core_runtime;

-- Any table xr_core_owner creates from here on (i.e. every Alembic
-- migration) automatically grants DML to xr_core_runtime, so new
-- migrations don't need to remember to re-grant.
ALTER DEFAULT PRIVILEGES FOR ROLE xr_core_owner IN SCHEMA core
    GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO xr_core_runtime;
