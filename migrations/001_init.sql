CREATE TABLE IF NOT EXISTS api_keys (
    id           uuid primary key default gen_random_uuid(),
    name         text not null,
    key_hash     text not null unique,
    key_prefix   text not null,
    limit_count  int not null,
    window_ms    int not null,
    burst        int not null default 0,
    algo         text not null default 'token_bucket',
    upstream_url text not null,
    active       bool not null default true,
    created_at   timestamptz not null default now()
);

-- No separate "index di key_hash" statement: the UNIQUE constraint above
-- already creates one (a unique constraint is implemented as a unique
-- index in Postgres). A second CREATE INDEX on the same column would just
-- be a redundant duplicate that costs writes for nothing.
