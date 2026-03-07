-- migrations/008: 防止重複 syslog/gcp 設定，確保 rebuild 時不重複新增

-- 移除 syslog_configs 重複資料（保留最舊那筆）
DELETE FROM syslog_configs
WHERE id NOT IN (
    SELECT MIN(id) FROM syslog_configs GROUP BY name
);

-- 加入唯一索引，讓 ON CONFLICT (name) DO NOTHING 可正常運作
CREATE UNIQUE INDEX IF NOT EXISTS idx_syslog_configs_name ON syslog_configs(name);

-- 移除 gcp_configs 重複資料（保留最舊那筆）
DELETE FROM gcp_configs
WHERE id NOT IN (
    SELECT MIN(id) FROM gcp_configs GROUP BY name
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_gcp_configs_name ON gcp_configs(name);
