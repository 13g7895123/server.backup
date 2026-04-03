CREATE TABLE IF NOT EXISTS syslog_api_keys (
    id           SERIAL PRIMARY KEY,
    syslog_id    INTEGER NOT NULL REFERENCES syslog_configs(id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    key_hash     TEXT    NOT NULL UNIQUE,
    key_prefix   TEXT    NOT NULL,
    enabled      BOOLEAN NOT NULL DEFAULT true,
    last_used_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_syslog_api_keys_syslog_id ON syslog_api_keys(syslog_id);
CREATE INDEX IF NOT EXISTS idx_syslog_api_keys_key_hash  ON syslog_api_keys(key_hash);
