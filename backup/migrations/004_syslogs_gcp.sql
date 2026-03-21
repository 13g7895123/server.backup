-- migrations/004_syslogs_gcp.sql

-- 系統日誌備份設定（auth_logs / system_logs）
CREATE TABLE IF NOT EXISTS syslog_configs (
    id          SERIAL PRIMARY KEY,
    name        VARCHAR(100) NOT NULL,        -- 顯示名稱，如 "Auth 日誌"
    log_type    VARCHAR(20)  NOT NULL DEFAULT 'auth',  -- 'auth' | 'system'
    log_files   TEXT[]       NOT NULL DEFAULT '{}',    -- 來源日誌路徑陣列
    dest        TEXT         NOT NULL DEFAULT '/mnt/nas/backup/os/auth',  -- 目的地
    compress    BOOLEAN      NOT NULL DEFAULT false,
    enabled     BOOLEAN      NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- GCP 備份設定
CREATE TABLE IF NOT EXISTS gcp_configs (
    id              SERIAL PRIMARY KEY,
    name            VARCHAR(100) NOT NULL,
    backup_dir      TEXT NOT NULL DEFAULT '/mnt/nas/backup/project/professional',
    backup_db_dir   TEXT NOT NULL DEFAULT '/mnt/nas/backup/database/professional',
    remote_user     TEXT NOT NULL DEFAULT 'backupuser',
    remote_host     TEXT NOT NULL DEFAULT '104.199.148.199',
    remote_path     TEXT NOT NULL DEFAULT '/home/backupuser/backup/project',
    remote_db_path  TEXT NOT NULL DEFAULT '/home/backupuser/backup/database',
    ssh_key         TEXT NOT NULL DEFAULT '/home/chinchungtu/.ssh/id_rsa_backup_gcp',
    enabled         BOOLEAN NOT NULL DEFAULT true,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- 預設一筆（來自 backup_gcp.sh）
INSERT INTO gcp_configs (name, backup_dir, backup_db_dir, remote_user, remote_host, remote_path, remote_db_path, ssh_key, enabled)
VALUES (
  'Professional GCP 備份',
  '/mnt/nas/backup/project/professional',
  '/mnt/nas/backup/database/professional',
  'backupuser',
  '104.199.148.199',
  '/home/backupuser/backup/project',
  '/home/backupuser/backup/database',
  '/home/chinchungtu/.ssh/id_rsa_backup_gcp',
  true
) ON CONFLICT DO NOTHING;
