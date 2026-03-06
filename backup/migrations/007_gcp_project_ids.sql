-- 007: GCP 備份設定關聯專案
-- gcp_configs 加入 project_ids 欄位，指定要同步哪些專案的 nas_base
-- 同時保留 backup_dir / backup_db_dir 作為 fallback（向後相容）

ALTER TABLE gcp_configs
  ADD COLUMN IF NOT EXISTS project_ids INT[] NOT NULL DEFAULT '{}';
