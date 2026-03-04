-- migrations/003_add_db_password.sql
-- 為 projects 表新增 db_password 欄位，支援直接填入資料庫密碼（取代純 env var）
ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS db_password TEXT NOT NULL DEFAULT '';
