-- Migration 006: syslog_configs 加入 source_type / journal 支援
ALTER TABLE syslog_configs
    ADD COLUMN IF NOT EXISTS source_type    VARCHAR(20)  NOT NULL DEFAULT 'file',
    ADD COLUMN IF NOT EXISTS journal_units  TEXT[]       NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS journal_format VARCHAR(20)  NOT NULL DEFAULT 'short';
