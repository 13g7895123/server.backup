-- migrations/002_project_details.sql
-- 為 projects 表新增：專案路徑、備份目錄清單、資料庫連線資訊
ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS project_path         TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS backup_dirs          TEXT[] NOT NULL DEFAULT '{}',
    ADD COLUMN IF NOT EXISTS db_type              VARCHAR(20) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS db_host              TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS db_port              INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS db_name              TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS db_user              TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS db_password_env      TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS docker_db_container  TEXT NOT NULL DEFAULT '';
