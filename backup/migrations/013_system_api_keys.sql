CREATE TABLE IF NOT EXISTS system_api_keys (
    id           SERIAL PRIMARY KEY,
    name         TEXT    NOT NULL,          -- 描述性名稱（例：監控系統、Grafana）
    key_hash     TEXT    NOT NULL UNIQUE,   -- SHA-256(key) hex，不存明文
    key_prefix   TEXT    NOT NULL,          -- 明文前 8 碼，顯示用
    enabled      BOOLEAN NOT NULL DEFAULT true,
    last_used_at TIMESTAMPTZ,
    expires_at   TIMESTAMPTZ,               -- NULL = 永不過期
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_system_api_keys_key_hash ON system_api_keys(key_hash);
