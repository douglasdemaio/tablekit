-- Runs once on an empty Postgres data directory (docker-entrypoint-initdb.d).
-- The migration role (POSTGRES_USER) owns the schema; the application role is
-- deliberately unprivileged and cannot bypass row-level security.

\getenv app_password TABLEKIT_APP_PASSWORD
\if :{?app_password}
\else
\set app_password 'change_me_dev_password'
\endif

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'tablekit_app') THEN
        CREATE ROLE tablekit_app LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS;
    END IF;
END $$;

ALTER ROLE tablekit_app WITH PASSWORD :'app_password';

GRANT CONNECT ON DATABASE tablekit TO tablekit_app;
GRANT USAGE ON SCHEMA public TO tablekit_app;
