package backup

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// DatabaseConfig 對應 backup_targets.config (type=database)
type DatabaseConfig struct {
	DBType        string `json:"db_type"` // "postgres" | "mysql"
	Host          string `json:"host"`
	Port          int    `json:"port"`
	Name          string `json:"name"`
	User          string `json:"user"`
	Password      string `json:"password"`       // 直接填入密碼（優先）
	PasswordEnv   string `json:"password_env"`   // 環境變數名稱（次要，向下相容）
	ContainerName string `json:"container_name"` // docker container 名稱（設定則優先使用 docker exec）
}

func ParseDatabaseConfig(raw json.RawMessage) (*DatabaseConfig, error) {
	var cfg DatabaseConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.Port == 0 {
		if cfg.DBType == "postgres" {
			cfg.Port = 5432
		} else {
			cfg.Port = 3306
		}
	}
	return &cfg, nil
}

// BackupDatabase 執行資料庫備份，寫入 destPath(.sql.gz)
// 回傳 sha256、大小、錯誤
func BackupDatabase(cfg *DatabaseConfig, destPath string) (checksum string, size int64, err error) {
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return "", 0, fmt.Errorf("建立目標目錄失敗: %w", err)
	}

	// 密碼優先順序：直接填入 > 環境變數
	password := cfg.Password
	if password == "" && cfg.PasswordEnv != "" {
		password = os.Getenv(cfg.PasswordEnv)
	}

	outFile, err := os.Create(destPath)
	if err != nil {
		return "", 0, err
	}
	defer outFile.Close()

	hash := sha256.New()
	mw := io.MultiWriter(outFile, hash)
	gw, _ := gzip.NewWriterLevel(mw, gzip.BestSpeed)

	switch {
	case cfg.ContainerName != "":
		// 優先透過 docker exec 備份，不需 pg_dump/mysqldump 在 agent 裡
		err = dumpViaDockerExec(cfg.ContainerName, cfg.DBType, cfg, password, gw)
	case cfg.DBType == "postgres":
		err = dumpPostgres(cfg, password, gw)
	case cfg.DBType == "mysql":
		err = dumpMySQL(cfg, password, gw)
	default:
		err = fmt.Errorf("不支援的資料庫類型: %s", cfg.DBType)
	}

	gw.Close()

	if err != nil {
		os.Remove(destPath)
		return "", 0, err
	}

	stat, _ := outFile.Stat()
	return hex.EncodeToString(hash.Sum(nil)), stat.Size(), nil
}
