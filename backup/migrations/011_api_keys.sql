CREATE TABLE IF NOT EXISTS api_keys (
    id          SERIAL PRIMARY KEY,
    project_id  INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    name        TEXT    NOT NULL,          -- 描述性名稱（例：監控系統、CI/CD）
    key_hash    TEXT    NOT NULL UNIQUE,   -- SHA-256(key) hex，不存明文
    key_prefix  TEXT    NOT NULL,          -- 明文前 8 碼，顯示用
    enabled     BOOLEAN NOT NULL DEFAULT true,
    last_used_at TIMESTAMPTZ,
    expires_at  TIMESTAMPTZ,               -- NULL = 永不過期
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_api_keys_project_id ON api_keys(project_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_key_hash   ON api_keys(key_hash);
