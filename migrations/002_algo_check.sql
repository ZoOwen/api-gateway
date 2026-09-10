-- Not idempotent (ALTER TABLE ADD CONSTRAINT fails if run twice) — this is
-- exactly why migrations now go through schema_migrations version
-- tracking (see internal/store/migrate.go). 001 stays a plain
-- CREATE TABLE IF NOT EXISTS; it doesn't need tracking to be safe to
-- re-run, but it's tracked too now for one uniform mechanism.
ALTER TABLE api_keys
    ADD CONSTRAINT api_keys_algo_check CHECK (algo IN ('token_bucket', 'sliding_window'));
