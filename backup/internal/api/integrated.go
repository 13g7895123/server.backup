package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"backup-manager/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ── types ─────────────────────────────────────────────────────────────────────

// IntegratedItem 代表整合備份清單中的一個備份項目（project / syslog / gcp）
type IntegratedItem struct {
	Type       string     `json:"type"`        // "project" | "syslog" | "gcp"
	ID         int        `json:"id"`
	Name       string     `json:"name"`
	Enabled    bool       `json:"enabled"`
	CronExpr   string     `json:"cron_expr"`
	LastRunAt  *time.Time `json:"last_run_at"`
	RunStatus  string     `json:"run_status"`
	RunMessage string     `json:"run_message"`
}

// ── handler ───────────────────────────────────────────────────────────────────

type integratedHandler struct {
	pool    *pgxpool.Pool
	syslogH *syslogHandler
	gcpH    *gcpHandler
}

func RegisterIntegratedRoutes(mux *http.ServeMux, s *store.Store) {
	h := &integratedHandler{
		pool:    s.Pool(),
		syslogH: &syslogHandler{pool: s.Pool()},
		gcpH:    &gcpHandler{pool: s.Pool()},
	}
	mux.HandleFunc("GET /api/integrated", h.listAll)
	mux.HandleFunc("POST /api/integrated/run-all", h.runAll)
	mux.HandleFunc("POST /api/integrated/batch-schedule", h.batchSchedule)
	mux.HandleFunc("POST /api/integrated/batch-run", h.batchRun)
}

// listAll 回傳所有項目（projects + syslogs + gcp）
func (h *integratedHandler) listAll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var items []IntegratedItem

	// --- projects ---
	projRows, err := h.pool.Query(ctx, `
		SELECT p.id, p.name, COALESCE(s.cron_expression,'') AS cron_expr,
		       p.enabled
		FROM projects p
		LEFT JOIN schedules s ON s.project_id = p.id AND s.type = 'all'
		ORDER BY p.id`)
	if err == nil {
		defer projRows.Close()
		for projRows.Next() {
			var it IntegratedItem
			var enabled *bool
			if err := projRows.Scan(&it.ID, &it.Name, &it.CronExpr, &enabled); err == nil {
				it.Type = "project"
				if enabled != nil {
					it.Enabled = *enabled
				}
				items = append(items, it)
			}
		}
	}

	// --- syslogs ---
	slRows, err := h.pool.Query(ctx, `
		SELECT id, name, enabled, cron_expr, last_run_at, run_status, run_message
		FROM syslog_configs ORDER BY id`)
	if err == nil {
		defer slRows.Close()
		for slRows.Next() {
			var it IntegratedItem
			if err := slRows.Scan(&it.ID, &it.Name, &it.Enabled, &it.CronExpr,
				&it.LastRunAt, &it.RunStatus, &it.RunMessage); err == nil {
				it.Type = "syslog"
				items = append(items, it)
			}
		}
	}

	// --- gcp ---
	gcpRows, err := h.pool.Query(ctx, `
		SELECT id, name, enabled, cron_expr, last_run_at, run_status, run_message
		FROM gcp_configs ORDER BY id`)
	if err == nil {
		defer gcpRows.Close()
		for gcpRows.Next() {
			var it IntegratedItem
			if err := gcpRows.Scan(&it.ID, &it.Name, &it.Enabled, &it.CronExpr,
				&it.LastRunAt, &it.RunStatus, &it.RunMessage); err == nil {
				it.Type = "gcp"
				items = append(items, it)
			}
		}
	}

	if items == nil {
		items = []IntegratedItem{}
	}
	writeJSON(w, http.StatusOK, items)
}

// runAll 觸發所有 enabled 的項目（syslog + gcp；project 使用現有 trigger）
func (h *integratedHandler) runAll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// 觸發 syslog
	slRows, err := h.pool.Query(ctx,
		`SELECT id, name, log_type, log_files, dest, compress, enabled,
		        cron_expr, last_run_at, run_status, run_message, created_at, updated_at
		 FROM syslog_configs WHERE enabled=true`)
	if err == nil {
		defer slRows.Close()
		for slRows.Next() {
			var c SyslogConfig
			if err := slRows.Scan(&c.ID, &c.Name, &c.LogType, &c.LogFiles, &c.Dest,
				&c.Compress, &c.Enabled, &c.CronExpr, &c.LastRunAt, &c.RunStatus,
				&c.RunMessage, &c.CreatedAt, &c.UpdatedAt); err == nil {
				id := c.ID
				h.pool.Exec(ctx, //nolint
					`UPDATE syslog_configs SET run_status='running', run_message='', updated_at=NOW() WHERE id=$1`, id)
				go func(cfg SyslogConfig) {
					msg, runErr := executeSyslogBackup(cfg)
					status := "success"
					if runErr != nil {
						status = "failed"
						msg = runErr.Error()
						log.Printf("[run-all/syslog] id=%d 失敗: %v", cfg.ID, runErr)
					}
					now := time.Now()
					h.pool.Exec(context.Background(), //nolint
						`UPDATE syslog_configs SET run_status=$1, run_message=$2, last_run_at=$3, updated_at=NOW() WHERE id=$4`,
						status, msg, now, cfg.ID)
				}(c)
			}
		}
	}

	// 觸發 gcp
	gcpRows, err := h.pool.Query(ctx,
		`SELECT id, name, backup_dir, backup_db_dir, remote_user, remote_host,
		        remote_path, remote_db_path, ssh_key, enabled,
		        cron_expr, last_run_at, run_status, run_message, created_at, updated_at
		 FROM gcp_configs WHERE enabled=true`)
	if err == nil {
		defer gcpRows.Close()
		for gcpRows.Next() {
			var c GcpConfig
			if err := gcpRows.Scan(&c.ID, &c.Name, &c.BackupDir, &c.BackupDbDir,
				&c.RemoteUser, &c.RemoteHost, &c.RemotePath, &c.RemoteDbPath,
				&c.SshKey, &c.Enabled, &c.CronExpr, &c.LastRunAt, &c.RunStatus,
				&c.RunMessage, &c.CreatedAt, &c.UpdatedAt); err == nil {
				id := c.ID
				h.pool.Exec(ctx, //nolint
					`UPDATE gcp_configs SET run_status='running', run_message='', updated_at=NOW() WHERE id=$1`, id)
				go func(cfg GcpConfig) {
					msg, runErr := executeGcpBackup(cfg)
					status := "success"
					if runErr != nil {
						status = "failed"
						msg = runErr.Error()
						log.Printf("[run-all/gcp] id=%d 失敗: %v", cfg.ID, runErr)
					}
					now := time.Now()
					h.pool.Exec(context.Background(), //nolint
						`UPDATE gcp_configs SET run_status=$1, run_message=$2, last_run_at=$3, updated_at=NOW() WHERE id=$4`,
						status, msg, now, cfg.ID)
				}(c)
			}
		}
	}

	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "triggered",
		"message": "已觸發全部已啟用的備份任務",
	})
}

// batchRun 觸發指定清單的備份項目
func (h *integratedHandler) batchRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []struct {
			Type string `json:"type"`
			ID   int    `json:"id"`
		} `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	ctx := r.Context()
	for _, item := range body.Items {
		switch item.Type {
		case "syslog":
			var c SyslogConfig
			err := h.pool.QueryRow(ctx,
				`SELECT id, name, log_type, log_files, dest, compress, enabled,
				        cron_expr, last_run_at, run_status, run_message, created_at, updated_at
				 FROM syslog_configs WHERE id=$1`, item.ID).
				Scan(&c.ID, &c.Name, &c.LogType, &c.LogFiles, &c.Dest,
					&c.Compress, &c.Enabled, &c.CronExpr, &c.LastRunAt,
					&c.RunStatus, &c.RunMessage, &c.CreatedAt, &c.UpdatedAt)
			if err != nil {
				continue
			}
			h.pool.Exec(ctx, //nolint
				`UPDATE syslog_configs SET run_status='running', run_message='', updated_at=NOW() WHERE id=$1`, c.ID)
			go func(cfg SyslogConfig) {
				msg, runErr := executeSyslogBackup(cfg)
				status := "success"
				if runErr != nil {
					status = "failed"
					msg = runErr.Error()
				}
				now := time.Now()
				h.pool.Exec(context.Background(), //nolint
					`UPDATE syslog_configs SET run_status=$1, run_message=$2, last_run_at=$3, updated_at=NOW() WHERE id=$4`,
					status, msg, now, cfg.ID)
			}(c)

		case "gcp":
			var c GcpConfig
			err := h.pool.QueryRow(ctx,
				`SELECT id, name, backup_dir, backup_db_dir, remote_user, remote_host,
				        remote_path, remote_db_path, ssh_key, enabled,
				        cron_expr, last_run_at, run_status, run_message, created_at, updated_at
				 FROM gcp_configs WHERE id=$1`, item.ID).
				Scan(&c.ID, &c.Name, &c.BackupDir, &c.BackupDbDir,
					&c.RemoteUser, &c.RemoteHost, &c.RemotePath, &c.RemoteDbPath,
					&c.SshKey, &c.Enabled, &c.CronExpr, &c.LastRunAt,
					&c.RunStatus, &c.RunMessage, &c.CreatedAt, &c.UpdatedAt)
			if err != nil {
				continue
			}
			h.pool.Exec(ctx, //nolint
				`UPDATE gcp_configs SET run_status='running', run_message='', updated_at=NOW() WHERE id=$1`, c.ID)
			go func(cfg GcpConfig) {
				msg, runErr := executeGcpBackup(cfg)
				status := "success"
				if runErr != nil {
					status = "failed"
					msg = runErr.Error()
				}
				now := time.Now()
				h.pool.Exec(context.Background(), //nolint
					`UPDATE gcp_configs SET run_status=$1, run_message=$2, last_run_at=$3, updated_at=NOW() WHERE id=$4`,
					status, msg, now, cfg.ID)
			}(c)
		}
		// "project" 類型：使用現有 /api/backups/trigger 端點，前端自行呼叫
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"status":  "triggered",
		"message": "已觸發選取的備份任務",
	})
}

// batchSchedule 批次設定排程頻率（cron_expr）
func (h *integratedHandler) batchSchedule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Items []struct {
			Type string `json:"type"`
			ID   int    `json:"id"`
		} `json:"items"`
		CronExpr string `json:"cron_expr"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	if body.CronExpr == "" {
		writeError(w, http.StatusBadRequest, "cron_expr 不可為空")
		return
	}
	ctx := r.Context()
	updated := 0
	for _, item := range body.Items {
		switch item.Type {
		case "syslog":
			tag, err := h.pool.Exec(ctx,
				`UPDATE syslog_configs SET cron_expr=$1, updated_at=NOW() WHERE id=$2`,
				body.CronExpr, item.ID)
			if err == nil && tag.RowsAffected() > 0 {
				updated++
			}
		case "gcp":
			tag, err := h.pool.Exec(ctx,
				`UPDATE gcp_configs SET cron_expr=$1, updated_at=NOW() WHERE id=$2`,
				body.CronExpr, item.ID)
			if err == nil && tag.RowsAffected() > 0 {
				updated++
			}
		case "project":
			// 更新或插入排程（type='all'）
			tag, err := h.pool.Exec(ctx, `
				UPDATE schedules SET cron_expression=$1, updated_at=NOW()
				WHERE project_id=$2 AND type='all'`,
				body.CronExpr, item.ID)
			if err == nil {
				if tag.RowsAffected() == 0 {
					// 沒有既有排程，INSERT
					h.pool.Exec(ctx, //nolint
						`INSERT INTO schedules (project_id, type, cron_expression)
						 VALUES ($1, 'all', $2)
						 ON CONFLICT DO NOTHING`,
						item.ID, body.CronExpr)
				}
				updated++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"updated":   updated,
		"cron_expr": body.CronExpr,
	})
}
