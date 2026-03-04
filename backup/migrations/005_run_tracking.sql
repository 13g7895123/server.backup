-- migrations/005_run_tracking.sql
-- 為 syslog_configs 和 gcp_configs 新增排程頻率與執行狀態追蹤

ALTER TABLE syslog_configs
    ADD COLUMN IF NOT EXISTS cron_expr   TEXT         NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_run_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS run_status  VARCHAR(20)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS run_message TEXT         NOT NULL DEFAULT '';

ALTER TABLE gcp_configs
    ADD COLUMN IF NOT EXISTS cron_expr   TEXT         NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS last_run_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS run_status  VARCHAR(20)  NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS run_message TEXT         NOT NULL DEFAULT '';
