-- migrations/001_init.sql
-- 專案主表
CREATE TABLE IF NOT EXISTS projects (
    id          SERIAL PRIMARY KEY,
    name        VARCHAR(100) UNIQUE NOT NULL,
    description TEXT,
    enabled     BOOLEAN DEFAULT true,
    nas_base    TEXT NOT NULL DEFAULT '/mnt/nas/backups',
    created_at  TIMESTAMPTZ DEFAULT NOW(),
    updated_at  TIMESTAMPTZ DEFAULT NOW()
);

-- 備份目標（每個專案可設多個）
CREATE TABLE IF NOT EXISTS backup_targets (
    id         SERIAL PRIMARY KEY,
    project_id INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    type       VARCHAR(20) NOT NULL,   -- 'files' | 'database' | 'system'
    label      VARCHAR(100),
    config     JSONB NOT NULL DEFAULT '{}',
    enabled    BOOLEAN DEFAULT true,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);

-- 排程表
CREATE TABLE IF NOT EXISTS schedules (
    id           SERIAL PRIMARY KEY,
    project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    label        VARCHAR(100),
    cron_expr    VARCHAR(100) NOT NULL,
    target_types TEXT[] NOT NULL DEFAULT '{all}',
    enabled      BOOLEAN DEFAULT true,
    last_run_at  TIMESTAMPTZ,
    next_run_at  TIMESTAMPTZ,
    created_at   TIMESTAMPTZ DEFAULT NOW(),
    updated_at   TIMESTAMPTZ DEFAULT NOW()
);

-- 保留政策
CREATE TABLE IF NOT EXISTS retention_policies (
    id           SERIAL PRIMARY KEY,
    project_id   INTEGER NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    target_type  VARCHAR(20) NOT NULL DEFAULT 'all',
    keep_daily   INTEGER DEFAULT 7,
    keep_weekly  INTEGER DEFAULT 4,
    keep_monthly INTEGER DEFAULT 12,
    UNIQUE(project_id, target_type)
);

-- 備份紀錄
CREATE TABLE IF NOT EXISTS backup_records (
    id             BIGSERIAL PRIMARY KEY,
    project_id     INTEGER REFERENCES projects(id) ON DELETE SET NULL,
    project_name   VARCHAR(100) NOT NULL,
    target_id      INTEGER REFERENCES backup_targets(id) ON DELETE SET NULL,
    schedule_id    INTEGER REFERENCES schedules(id) ON DELETE SET NULL,
    type           VARCHAR(20) NOT NULL,
    sub_type       VARCHAR(30),
    label          VARCHAR(100),
    filename       VARCHAR(500) NOT NULL,
    path           TEXT NOT NULL,
    size_bytes     BIGINT DEFAULT 0,
    checksum       CHAR(64),
    status         VARCHAR(20) NOT NULL DEFAULT 'running',
    duration_sec   NUMERIC(10,3),
    error_msg      TEXT,
    triggered_by   VARCHAR(20) DEFAULT 'schedule',
    retained_until TIMESTAMPTZ,
    created_at     TIMESTAMPTZ DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_records_project  ON backup_records(project_id);
CREATE INDEX IF NOT EXISTS idx_records_status   ON backup_records(status);
CREATE INDEX IF NOT EXISTS idx_records_created  ON backup_records(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_records_type     ON backup_records(type);
