-- Migration 009: 更新 GCP 預設備份路徑，並新增 system journal 日誌備份設定

-- 更新 GCP 備份的本地來源路徑（backup_dir）為實際 NAS 掛載的路徑
-- 執行時 backup_dir + "/" + 日期 即為 rsync 來源目錄
INSERT INTO gcp_configs
    (name, project_ids, backup_dir, backup_db_dir, remote_user, remote_host,
     remote_path, remote_db_path, ssh_key, enabled)
VALUES (
    'Professional GCP 備份',
    '{}',
    '/volume1/debian/backup-new/projects/01_rootadviser/files',
    '/volume1/debian/backup-new/projects/01_rootadviser/db',
    'backupuser',
    '104.199.148.199',
    '/home/backupuser/backup/project',
    '/home/backupuser/backup/database',
    '/home/chinchungtu/.ssh/id_rsa_backup_gcp',
    true
) ON CONFLICT (name) DO UPDATE
    SET backup_dir    = EXCLUDED.backup_dir,
        backup_db_dir = EXCLUDED.backup_db_dir;

-- 新增 system journal 日誌備份（來源為 systemd journal，需透過 agent 執行）
-- journal_units 留空 = 匯出全部 journal；可在 UI 編輯指定特定 unit（如 ssh.service）
INSERT INTO syslog_configs
    (name, log_type, source_type, log_files, journal_units, journal_format, dest, compress, enabled)
VALUES (
    'System 日誌 (Journal)',
    'system',
    'journal',
    '{}',
    '{}',
    'short',
    '/mnt/nas/backup/os/system-journal',
    true,
    true
) ON CONFLICT (name) DO NOTHING;
