package api

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"backup-manager/internal/store"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ── models ────────────────────────────────────────────────────────────────────

type SyslogConfig struct {
	ID            int        `json:"id"`
	Name          string     `json:"name"`
	LogType       string     `json:"log_type"`
	SourceType    string     `json:"source_type"` // "file" | "journal"
	LogFiles      []string   `json:"log_files"`
	JournalUnits  []string   `json:"journal_units"`  // systemd units for journalctl
	JournalFormat string     `json:"journal_format"` // short | json | export
	Dest          string     `json:"dest"`
	Compress      bool       `json:"compress"`
	Enabled       bool       `json:"enabled"`
	CronExpr      string     `json:"cron_expr"`
	LastRunAt     *time.Time `json:"last_run_at"`
	RunStatus     string     `json:"run_status"`
	RunMessage    string     `json:"run_message"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type GcpConfig struct {
	ID           int        `json:"id"`
	Name         string     `json:"name"`
	BackupDir    string     `json:"backup_dir"`
	BackupDbDir  string     `json:"backup_db_dir"`
	RemoteUser   string     `json:"remote_user"`
	RemoteHost   string     `json:"remote_host"`
	RemotePath   string     `json:"remote_path"`
	RemoteDbPath string     `json:"remote_db_path"`
	SshKey       string     `json:"ssh_key"`
	Enabled      bool       `json:"enabled"`
	CronExpr     string     `json:"cron_expr"`
	LastRunAt    *time.Time `json:"last_run_at"`
	RunStatus    string     `json:"run_status"`
	RunMessage   string     `json:"run_message"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// ── handler ───────────────────────────────────────────────────────────────────

type syslogHandler struct{ pool *pgxpool.Pool }
type gcpHandler struct{ pool *pgxpool.Pool }

func RegisterSyslogRoutes(mux *http.ServeMux, s *store.Store) {
	h := &syslogHandler{pool: s.Pool()}
	mux.HandleFunc("GET /api/syslogs", h.list)
	mux.HandleFunc("POST /api/syslogs", h.create)
	mux.HandleFunc("PUT /api/syslogs/{id}", h.update)
	mux.HandleFunc("DELETE /api/syslogs/{id}", h.delete)
	mux.HandleFunc("PATCH /api/syslogs/{id}/toggle", h.toggle)
	mux.HandleFunc("POST /api/syslogs/{id}/run", h.run)
	mux.HandleFunc("PATCH /api/syslogs/{id}/schedule", h.setSchedule)
}

func RegisterGcpRoutes(mux *http.ServeMux, s *store.Store) {
	h := &gcpHandler{pool: s.Pool()}
	mux.HandleFunc("GET /api/gcpconfigs", h.list)
	mux.HandleFunc("POST /api/gcpconfigs", h.create)
	mux.HandleFunc("PUT /api/gcpconfigs/{id}", h.update)
	mux.HandleFunc("DELETE /api/gcpconfigs/{id}", h.delete)
	mux.HandleFunc("PATCH /api/gcpconfigs/{id}/toggle", h.toggle)
	mux.HandleFunc("POST /api/gcpconfigs/{id}/run", h.run)
	mux.HandleFunc("PATCH /api/gcpconfigs/{id}/schedule", h.setSchedule)
}

// ── SyslogConfig CRUD ─────────────────────────────────────────────────────────

func (h *syslogHandler) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, name, log_type, source_type, log_files, journal_units, journal_format,
		       dest, compress, enabled,
		       cron_expr, last_run_at, run_status, run_message,
		       created_at, updated_at
		FROM syslog_configs ORDER BY id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	var items []SyslogConfig
	for rows.Next() {
		var c SyslogConfig
		if err := rows.Scan(&c.ID, &c.Name, &c.LogType, &c.SourceType, &c.LogFiles,
			&c.JournalUnits, &c.JournalFormat,
			&c.Dest, &c.Compress, &c.Enabled, &c.CronExpr, &c.LastRunAt, &c.RunStatus,
			&c.RunMessage, &c.CreatedAt, &c.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		items = append(items, c)
	}
	if items == nil {
		items = []SyslogConfig{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *syslogHandler) create(w http.ResponseWriter, r *http.Request) {
	var c SyslogConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	if c.Name == "" {
		writeError(w, http.StatusBadRequest, "name 不可為空")
		return
	}
	if c.LogFiles == nil {
		c.LogFiles = []string{}
	}
	if c.JournalUnits == nil {
		c.JournalUnits = []string{}
	}
	if c.SourceType == "" {
		c.SourceType = "file"
	}
	if c.JournalFormat == "" {
		c.JournalFormat = "short"
	}
	err := h.pool.QueryRow(r.Context(), `
		INSERT INTO syslog_configs
		  (name, log_type, source_type, log_files, journal_units, journal_format,
		   dest, compress, enabled, cron_expr)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id, created_at, updated_at`,
		c.Name, c.LogType, c.SourceType, c.LogFiles, c.JournalUnits, c.JournalFormat,
		c.Dest, c.Compress, c.Enabled, c.CronExpr).
		Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (h *syslogHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	var c SyslogConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	if c.LogFiles == nil {
		c.LogFiles = []string{}
	}
	if c.JournalUnits == nil {
		c.JournalUnits = []string{}
	}
	if c.SourceType == "" {
		c.SourceType = "file"
	}
	if c.JournalFormat == "" {
		c.JournalFormat = "short"
	}
	_, err = h.pool.Exec(r.Context(), `
		UPDATE syslog_configs SET name=$1, log_type=$2, source_type=$3,
		  log_files=$4, journal_units=$5, journal_format=$6,
		  dest=$7, compress=$8, enabled=$9, cron_expr=$10, updated_at=NOW()
		WHERE id=$11`,
		c.Name, c.LogType, c.SourceType,
		c.LogFiles, c.JournalUnits, c.JournalFormat,
		c.Dest, c.Compress, c.Enabled, c.CronExpr, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *syslogHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	_, err = h.pool.Exec(r.Context(), `DELETE FROM syslog_configs WHERE id=$1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *syslogHandler) toggle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON")
		return
	}
	_, err = h.pool.Exec(r.Context(),
		`UPDATE syslog_configs SET enabled=$1, updated_at=NOW() WHERE id=$2`, body.Enabled, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": body.Enabled})
}

// ── SyslogConfig run / schedule ───────────────────────────────────────────────

func (h *syslogHandler) setSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	var body struct {
		CronExpr string `json:"cron_expr"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON")
		return
	}
	_, err = h.pool.Exec(r.Context(),
		`UPDATE syslog_configs SET cron_expr=$1, updated_at=NOW() WHERE id=$2`, body.CronExpr, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"cron_expr": body.CronExpr})
}

func (h *syslogHandler) run(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	var c SyslogConfig
	err = h.pool.QueryRow(r.Context(), `
		SELECT id, name, log_type, source_type, log_files, journal_units, journal_format,
		       dest, compress, enabled,
		       cron_expr, last_run_at, run_status, run_message, created_at, updated_at
		FROM syslog_configs WHERE id=$1`, id).
		Scan(&c.ID, &c.Name, &c.LogType, &c.SourceType, &c.LogFiles,
			&c.JournalUnits, &c.JournalFormat,
			&c.Dest, &c.Compress, &c.Enabled, &c.CronExpr, &c.LastRunAt, &c.RunStatus,
			&c.RunMessage, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到設定")
		return
	}
	h.pool.Exec(r.Context(), //nolint
		`UPDATE syslog_configs SET run_status='running', run_message='', updated_at=NOW() WHERE id=$1`, id)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "triggered", "message": "日誌備份已開始"})
	go func() {
		msg, runErr := executeSyslogBackup(c)
		status := "success"
		if runErr != nil {
			status = "failed"
			msg = runErr.Error()
			log.Printf("[syslog-run] id=%d 失敗: %v", id, runErr)
		} else {
			log.Printf("[syslog-run] id=%d 完成: %s", id, msg)
		}
		now := time.Now()
		h.pool.Exec(context.Background(), //nolint
			`UPDATE syslog_configs SET run_status=$1, run_message=$2, last_run_at=$3, updated_at=NOW() WHERE id=$4`,
			status, msg, now, id)
	}()
}

func executeSyslogBackup(c SyslogConfig) (string, error) {
	date := time.Now().Format("2006-01-02")
	destDir := filepath.Join(c.Dest, date)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("建立目錄 %s 失敗: %w", destDir, err)
	}

	if c.SourceType == "journal" {
		return executeJournalBackup(c, destDir, date)
	}
	return executeFileBackup(c, destDir, date)
}

// executeJournalBackup 使用 journalctl 匯出日誌
func executeJournalBackup(c SyslogConfig, destDir, date string) (string, error) {
	outFmt := c.JournalFormat
	if outFmt == "" {
		outFmt = "short"
	}
	ext := ".log"
	if outFmt == "json" {
		ext = ".json"
	} else if outFmt == "export" {
		ext = ".journal"
	}

	// 組合 journalctl 參數
	args := []string{"--no-pager", "-o", outFmt, "--since", date + " 00:00:00", "--until", date + " 23:59:59"}
	for _, unit := range c.JournalUnits {
		if unit != "" {
			args = append(args, "-u", unit)
		}
	}

	unitTag := "all"
	if len(c.JournalUnits) > 0 {
		unitTag = c.JournalUnits[0]
	}
	destFile := filepath.Join(destDir, "journal_"+unitTag+"_"+date+ext)

	cmd := exec.Command("journalctl", args...) //nolint:gosec
	out, err := os.Create(destFile)            //nolint:gosec
	if err != nil {
		return "", fmt.Errorf("建立輸出檔案失敗: %w", err)
	}
	defer out.Close()
	cmd.Stdout = out
	if cmdErr := cmd.Run(); cmdErr != nil {
		return "", fmt.Errorf("journalctl 執行失敗: %w", cmdErr)
	}

	if c.Compress {
		if err := gzipFileBackup(destFile); err != nil {
			return "", fmt.Errorf("壓縮 journal 失敗: %w", err)
		}
		return fmt.Sprintf("journal 已備份至 %s.gz", destFile), nil
	}
	return fmt.Sprintf("journal 已備份至 %s", destFile), nil
}

// executeFileBackup 複製指定日誌檔案
func executeFileBackup(c SyslogConfig, destDir, date string) (string, error) {
	var copied []string
	for _, logFile := range c.LogFiles {
		if logFile == "" {
			continue
		}
		basename := filepath.Base(logFile)
		destFile := filepath.Join(destDir, basename+"_"+date)
		if err := copyFileBackup(logFile, destFile); err != nil {
			return "", fmt.Errorf("複製 %s 失敗: %w", logFile, err)
		}
		if c.Compress {
			if err := gzipFileBackup(destFile); err != nil {
				return "", fmt.Errorf("壓縮 %s 失敗: %w", destFile, err)
			}
			copied = append(copied, destFile+".gz")
		} else {
			copied = append(copied, destFile)
		}
	}
	return fmt.Sprintf("已備份 %d 個檔案至 %s", len(copied), destDir), nil
}

func copyFileBackup(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst) //nolint:gosec
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func gzipFileBackup(path string) error {
	in, err := os.Open(path) //nolint:gosec
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(path + ".gz") //nolint:gosec
	if err != nil {
		return err
	}
	defer out.Close()
	gz := gzip.NewWriter(out)
	if _, err := io.Copy(gz, in); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return os.Remove(path)
}

// ── GcpConfig CRUD ────────────────────────────────────────────────────────────

func (h *gcpHandler) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, name, backup_dir, backup_db_dir, remote_user, remote_host,
		       remote_path, remote_db_path, ssh_key, enabled,
		       cron_expr, last_run_at, run_status, run_message,
		       created_at, updated_at
		FROM gcp_configs ORDER BY id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	var items []GcpConfig
	for rows.Next() {
		var c GcpConfig
		if err := rows.Scan(&c.ID, &c.Name, &c.BackupDir, &c.BackupDbDir,
			&c.RemoteUser, &c.RemoteHost, &c.RemotePath, &c.RemoteDbPath,
			&c.SshKey, &c.Enabled, &c.CronExpr, &c.LastRunAt, &c.RunStatus,
			&c.RunMessage, &c.CreatedAt, &c.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		items = append(items, c)
	}
	if items == nil {
		items = []GcpConfig{}
	}
	writeJSON(w, http.StatusOK, items)
}

func (h *gcpHandler) create(w http.ResponseWriter, r *http.Request) {
	var c GcpConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	if c.Name == "" {
		writeError(w, http.StatusBadRequest, "name 不可為空")
		return
	}
	err := h.pool.QueryRow(r.Context(), `
		INSERT INTO gcp_configs
		  (name, backup_dir, backup_db_dir, remote_user, remote_host,
		   remote_path, remote_db_path, ssh_key, enabled, cron_expr)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		RETURNING id, created_at, updated_at`,
		c.Name, c.BackupDir, c.BackupDbDir, c.RemoteUser, c.RemoteHost,
		c.RemotePath, c.RemoteDbPath, c.SshKey, c.Enabled, c.CronExpr).
		Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (h *gcpHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	var c GcpConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	_, err = h.pool.Exec(r.Context(), `
		UPDATE gcp_configs SET
		  name=$1, backup_dir=$2, backup_db_dir=$3, remote_user=$4,
		  remote_host=$5, remote_path=$6, remote_db_path=$7, ssh_key=$8,
		  enabled=$9, cron_expr=$10, updated_at=NOW()
		WHERE id=$11`,
		c.Name, c.BackupDir, c.BackupDbDir, c.RemoteUser,
		c.RemoteHost, c.RemotePath, c.RemoteDbPath, c.SshKey, c.Enabled, c.CronExpr, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *gcpHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	_, err = h.pool.Exec(r.Context(), `DELETE FROM gcp_configs WHERE id=$1`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *gcpHandler) toggle(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON")
		return
	}
	_, err = h.pool.Exec(r.Context(),
		`UPDATE gcp_configs SET enabled=$1, updated_at=NOW() WHERE id=$2`, body.Enabled, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": body.Enabled})
}

// ── GcpConfig run / schedule ──────────────────────────────────────────────────

func (h *gcpHandler) setSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	var body struct {
		CronExpr string `json:"cron_expr"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON")
		return
	}
	_, err = h.pool.Exec(r.Context(),
		`UPDATE gcp_configs SET cron_expr=$1, updated_at=NOW() WHERE id=$2`, body.CronExpr, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"cron_expr": body.CronExpr})
}

func (h *gcpHandler) run(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	var c GcpConfig
	err = h.pool.QueryRow(r.Context(), `
		SELECT id, name, backup_dir, backup_db_dir, remote_user, remote_host,
		       remote_path, remote_db_path, ssh_key, enabled,
		       cron_expr, last_run_at, run_status, run_message, created_at, updated_at
		FROM gcp_configs WHERE id=$1`, id).
		Scan(&c.ID, &c.Name, &c.BackupDir, &c.BackupDbDir,
			&c.RemoteUser, &c.RemoteHost, &c.RemotePath, &c.RemoteDbPath,
			&c.SshKey, &c.Enabled, &c.CronExpr, &c.LastRunAt, &c.RunStatus,
			&c.RunMessage, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到設定")
		return
	}
	h.pool.Exec(r.Context(), //nolint
		`UPDATE gcp_configs SET run_status='running', run_message='', updated_at=NOW() WHERE id=$1`, id)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "triggered", "message": "GCP 備份已開始（rsync）"})
	go func() {
		msg, runErr := executeGcpBackup(c)
		status := "success"
		if runErr != nil {
			status = "failed"
			msg = runErr.Error()
			log.Printf("[gcp-run] id=%d 失敗: %v", id, runErr)
		} else {
			log.Printf("[gcp-run] id=%d 完成: %s", id, msg)
		}
		now := time.Now()
		h.pool.Exec(context.Background(), //nolint
			`UPDATE gcp_configs SET run_status=$1, run_message=$2, last_run_at=$3, updated_at=NOW() WHERE id=$4`,
			status, msg, now, id)
	}()
}

func executeGcpBackup(c GcpConfig) (string, error) {
	date := time.Now().Format("2006-01-02")
	sshOpt := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no", c.SshKey)

	srcDir := c.BackupDir + "/" + date + "/"
	dstDir := fmt.Sprintf("%s@%s:%s/%s", c.RemoteUser, c.RemoteHost, c.RemotePath, date)
	cmd1 := exec.Command("rsync", "-avz", "-e", sshOpt, srcDir, dstDir) //nolint:gosec
	out1, err1 := cmd1.CombinedOutput()
	if err1 != nil {
		return "", fmt.Errorf("rsync project 失敗: %w\n%s", err1, string(out1))
	}

	srcDbDir := c.BackupDbDir + "/" + date + "/"
	dstDbDir := fmt.Sprintf("%s@%s:%s/%s", c.RemoteUser, c.RemoteHost, c.RemoteDbPath, date)
	cmd2 := exec.Command("rsync", "-avz", "-e", sshOpt, srcDbDir, dstDbDir) //nolint:gosec
	out2, err2 := cmd2.CombinedOutput()
	if err2 != nil {
		return "", fmt.Errorf("rsync database 失敗: %w\n%s", err2, string(out2))
	}

	return fmt.Sprintf("rsync 完成 → %s@%s", c.RemoteUser, c.RemoteHost), nil
}
