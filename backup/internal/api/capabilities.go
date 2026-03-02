package api

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type capabilityInfo struct {
	Version      string     `json:"version"`
	BuildTime    string     `json:"build_time"`
	Capabilities capDetails `json:"capabilities"`
}

type capDetails struct {
	BackupTypes   []backupTypeCap `json:"backup_types"`
	Scheduler     schedulerCap    `json:"scheduler"`
	Storage       storageCap      `json:"storage"`
	Retention     retentionCap    `json:"retention"`
	Notifications notifyCap       `json:"notifications"`
	API           apiCap          `json:"api"`
	System        systemCap       `json:"system"`
}

type backupTypeCap struct {
	Type         string      `json:"type"`
	Description  string      `json:"description"`
	Available    bool        `json:"available"`
	Supported    []dbSupport `json:"supported,omitempty"`
	ConfigFields []string    `json:"config_fields"`
}

type dbSupport struct {
	DBType      string `json:"db_type"`
	Description string `json:"description"`
	Available   bool   `json:"available"`
	Version     string `json:"version,omitempty"`
}

type schedulerCap struct {
	Description string        `json:"description"`
	CronFormat  string        `json:"cron_format"`
	Examples    []cronExample `json:"examples"`
}

type cronExample struct {
	Expr string `json:"expr"`
	Desc string `json:"desc"`
}

type storageCap struct {
	CompressFormats []string `json:"compress_formats"`
	Checksum        string   `json:"checksum"`
	NasMount        string   `json:"nas_mount"`
}

type retentionCap struct {
	Description string `json:"description"`
}

type notifyCap struct {
	Supported []string `json:"supported"`
	Triggers  []string `json:"triggers"`
}

type apiCap struct {
	Description  string   `json:"description"`
	TriggerModes []string `json:"trigger_modes"`
	Routes       []route  `json:"routes"`
}

type route struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Desc   string `json:"desc"`
}

type systemCap struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

func HandleCapabilities(w http.ResponseWriter, r *http.Request) {
	pgVersion := getCmdVersion("pg_dump", "--version")
	mysqlVersion := getCmdVersion("mysqldump", "--version")
	tarVersion := getCmdVersion("tar", "--version")

	info := capabilityInfo{
		Version:   "1.0.0",
		BuildTime: time.Now().Format(time.RFC3339),
		Capabilities: capDetails{
			BackupTypes: []backupTypeCap{
				{
					Type:         "files",
					Description:  "備份伺服器本地目錄（支援 gzip 壓縮、排除規則）",
					Available:    tarVersion != "",
					ConfigFields: []string{"source", "compress", "exclude"},
				},
				{
					Type:        "database",
					Description: "備份關聯式資料庫",
					Available:   pgVersion != "" || mysqlVersion != "",
					Supported: []dbSupport{
						{
							DBType:      "postgres",
							Description: "PostgreSQL 備份（使用 pg_dump）",
							Available:   pgVersion != "",
							Version:     pgVersion,
						},
						{
							DBType:      "mysql",
							Description: "MySQL / MariaDB 備份（使用 mysqldump）",
							Available:   mysqlVersion != "",
							Version:     mysqlVersion,
						},
					},
					ConfigFields: []string{"db_type", "host", "port", "name", "user", "password_env"},
				},
				{
					Type:         "system",
					Description:  "Debian 系統整體備份（目錄打包 + 套件清單 + 服務清單）",
					Available:    isDebianHost(),
					ConfigFields: []string{"include", "exclude", "backup_packages", "backup_services"},
				},
			},
			Scheduler: schedulerCap{
				Description: "標準 5 欄位 cron 格式，動態更新不需重啟",
				CronFormat:  "分 時 日 月 星期",
				Examples: []cronExample{
					{"0 2 * * *", "每天凌晨 2:00"},
					{"0 3 * * 1", "每週一凌晨 3:00"},
					{"0 1 1 * *", "每月 1 日凌晨 1:00"},
					{"0 */6 * * *", "每 6 小時"},
					{"30 23 * * 5", "每週五 23:30"},
				},
			},
			Storage: storageCap{
				CompressFormats: []string{"gzip"},
				Checksum:        "SHA256",
				NasMount:        getEnvOr("NAS_BASE", "/mnt/nas/backups"),
			},
			Retention: retentionCap{
				Description: "支援每日/每週/每月保留政策，可依專案及備份類型分別設定",
			},
			Notifications: notifyCap{
				Supported: []string{"slack"},
				Triggers:  []string{"on_failure"},
			},
			API: apiCap{
				Description:  "RESTful API，支援完整專案 CRUD / 排程設定 / 手動觸發",
				TriggerModes: []string{"schedule", "manual", "api"},
				Routes: []route{
					{"GET", "/api/projects", "列出所有專案"},
					{"POST", "/api/projects", "新增專案"},
					{"GET", "/api/projects/{id}", "取得專案詳情"},
					{"PUT", "/api/projects/{id}", "更新專案"},
					{"DELETE", "/api/projects/{id}", "刪除專案"},
					{"PATCH", "/api/projects/{id}/toggle", "啟用/停用專案"},
					{"GET", "/api/projects/{id}/targets", "列出備份目標"},
					{"POST", "/api/projects/{id}/targets", "新增備份目標"},
					{"PUT", "/api/projects/{id}/targets/{tid}", "更新備份目標"},
					{"DELETE", "/api/projects/{id}/targets/{tid}", "刪除備份目標"},
					{"GET", "/api/projects/{id}/schedules", "列出排程"},
					{"POST", "/api/projects/{id}/schedules", "新增排程"},
					{"PUT", "/api/projects/{id}/schedules/{sid}", "更新排程"},
					{"DELETE", "/api/projects/{id}/schedules/{sid}", "刪除排程"},
					{"PATCH", "/api/projects/{id}/schedules/{sid}/toggle", "啟用/停用排程"},
					{"GET", "/api/projects/{id}/retention", "取得保留政策"},
					{"PUT", "/api/projects/{id}/retention", "更新保留政策"},
					{"GET", "/api/backups", "列出備份紀錄（支援篩選/分頁）"},
					{"GET", "/api/projects/{id}/backups", "特定專案備份紀錄"},
					{"DELETE", "/api/backups/{bid}", "刪除備份紀錄與實體檔案"},
					{"POST", "/api/backups/trigger", "手動觸發備份"},
					{"GET", "/api/summary", "全系統統計摘要"},
					{"GET", "/api/capabilities", "系統支援功能清單"},
				},
			},
			System: systemCap{
				OS:   runtime.GOOS,
				Arch: runtime.GOARCH,
			},
		},
	}

	writeJSON(w, http.StatusOK, info)
}

func getCmdVersion(cmd string, arg string) string {
	if _, err := exec.LookPath(cmd); err != nil {
		return ""
	}
	out, err := exec.Command(cmd, arg).Output()
	if err != nil {
		return "available"
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[0])
	}
	return "available"
}

func isDebianHost() bool {
	hostPrefix := os.Getenv("HOST_PREFIX")
	_, err := os.Stat(hostPrefix + "/var/lib/dpkg/status")
	return err == nil
}

func getEnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
