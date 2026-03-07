package api

import (
	"bytes"
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
	"strings"
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
	ProjectIDs   []int      `json:"project_ids"`   // 關聯專案，用各專案 nas_base 做 rsync 來源
	BackupDir    string     `json:"backup_dir"`    // fallback：未設定 project_ids 時使用
	BackupDbDir  string     `json:"backup_db_dir"` // fallback：未設定 project_ids 時使用
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

type syslogHandler struct {
	pool  *pgxpool.Pool
	store *store.Store
}
type gcpHandler struct{ pool *pgxpool.Pool }

// testCheck 用於 test endpoint 回傳每個檢查項目的結果
type testCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func RegisterSyslogRoutes(mux *http.ServeMux, s *store.Store) {
	h := &syslogHandler{pool: s.Pool(), store: s}
	mux.HandleFunc("GET /api/syslogs", h.list)
	mux.HandleFunc("POST /api/syslogs", h.create)
	mux.HandleFunc("PUT /api/syslogs/{id}", h.update)
	mux.HandleFunc("DELETE /api/syslogs/{id}", h.delete)
	mux.HandleFunc("PATCH /api/syslogs/{id}/toggle", h.toggle)
	mux.HandleFunc("POST /api/syslogs/{id}/run", h.run)
	mux.HandleFunc("PATCH /api/syslogs/{id}/schedule", h.setSchedule)
	mux.HandleFunc("GET /api/syslogs/{id}/records", h.listRecords)
	mux.HandleFunc("GET /api/syslogs/{id}/export", h.exportConfig)
	mux.HandleFunc("POST /api/syslogs/{id}/test", h.test)
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
	mux.HandleFunc("POST /api/gcpconfigs/{id}/test", h.test)
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

	// 建立備份紀錄
	rec := &store.BackupRecord{
		ProjectName: c.Name,
		Type:        "syslog",
		SubType:     strconv.Itoa(c.ID),
		Label:       c.Name,
		Filename:    time.Now().Format("2006-01-02") + "/",
		Path:        c.Dest,
		TriggeredBy: "manual",
	}
	recID, _ := h.store.CreateRecord(r.Context(), rec)
	start := time.Now()

	writeJSON(w, http.StatusAccepted, map[string]string{"status": "triggered", "message": "日誌備份已開始"})
	go func() {
		var msg string
		var runErr error

		// journal 類型且有 AGENT_URL → 委派給 agent（agent 在 host 有 journalctl）
		if c.SourceType == "journal" {
			if agentURL := os.Getenv("AGENT_URL"); agentURL != "" {
				msg, runErr = proxySyslogRun(agentURL, c)
			} else {
				msg, runErr = executeSyslogBackup(c)
			}
		} else {
			msg, runErr = executeSyslogBackup(c)
		}
		status := "success"
		errStr := ""
		if runErr != nil {
			status = "failed"
			msg = runErr.Error()
			errStr = msg
			log.Printf("[syslog-run] id=%d 失敗: %v", id, runErr)
		} else {
			log.Printf("[syslog-run] id=%d 完成: %s", id, msg)
		}
		now := time.Now()
		h.pool.Exec(context.Background(), //nolint
			`UPDATE syslog_configs SET run_status=$1, run_message=$2, last_run_at=$3, updated_at=NOW() WHERE id=$4`,
			status, msg, now, id)
		// 更新備份紀錄
		if recID > 0 {
			rec.ID = recID
			rec.Status = status
			rec.DurationSec = time.Since(start).Seconds()
			rec.ErrorMsg = errStr
			h.store.UpdateRecord(context.Background(), rec) //nolint
		}
	}()
}

// listRecords 查詢此 syslog 設定的備份執行紀錄
func (h *syslogHandler) listRecords(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	limit := 30
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, e := strconv.Atoi(q); e == nil && n > 0 {
			limit = n
		}
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, status, COALESCE(error_msg,''), COALESCE(duration_sec,0), filename, created_at
		FROM backup_records
		WHERE type='syslog' AND sub_type=$1
		ORDER BY created_at DESC
		LIMIT $2`, strconv.Itoa(id), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	type RunRecord struct {
		ID          int64     `json:"id"`
		Status      string    `json:"status"`
		ErrorMsg    string    `json:"error_msg"`
		DurationSec float64   `json:"duration_sec"`
		Filename    string    `json:"filename"`
		CreatedAt   time.Time `json:"created_at"`
	}
	var records []RunRecord
	for rows.Next() {
		var rec RunRecord
		if err := rows.Scan(&rec.ID, &rec.Status, &rec.ErrorMsg, &rec.DurationSec, &rec.Filename, &rec.CreatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		records = append(records, rec)
	}
	if records == nil {
		records = []RunRecord{}
	}
	writeJSON(w, http.StatusOK, records)
}

// exportConfig 匯出此 syslog 設定為 JSON
func (h *syslogHandler) exportConfig(w http.ResponseWriter, r *http.Request) {
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
			&c.Dest, &c.Compress, &c.Enabled, &c.CronExpr, &c.LastRunAt,
			&c.RunStatus, &c.RunMessage, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到設定")
		return
	}
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="syslog_%s_%s.json"`, c.Name, time.Now().Format("2006-01-02")))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(c) //nolint
}

// test 執行備份前的預檢診斷
func (h *syslogHandler) test(w http.ResponseWriter, r *http.Request) {
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
			&c.Dest, &c.Compress, &c.Enabled, &c.CronExpr, &c.LastRunAt,
			&c.RunStatus, &c.RunMessage, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到設定")
		return
	}

	// journal 類型且有 AGENT_URL → 委派給 agent（agent 在 host 有 journalctl）
	if c.SourceType == "journal" {
		if agentURL := os.Getenv("AGENT_URL"); agentURL != "" {
			checks, allOK, proxyErr := proxySyslogTest(agentURL, c)
			if proxyErr != nil {
				writeJSON(w, http.StatusOK, map[string]any{
					"ok":     false,
					"checks": []testCheck{{"連線 agent", false, proxyErr.Error()}},
				})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": allOK, "checks": checks})
			return
		}
	}

	checks, allOK := runSyslogTestChecks(c)
	writeJSON(w, http.StatusOK, map[string]any{"ok": allOK, "checks": checks})
}

// ── Agent proxy helpers ───────────────────────────────────────────────────────

// proxySyslogRun 將 journal 備份委派給 agent 執行（agent 在 host 上有 journalctl）
func proxySyslogRun(agentURL string, c SyslogConfig) (string, error) {
	body, _ := json.Marshal(c)
	agentToken := os.Getenv("AGENT_TOKEN")
	req, err := http.NewRequest("POST", agentURL+"/syslogs/run", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("建立 agent 請求失敗: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if agentToken != "" {
		req.Header.Set("X-Agent-Token", agentToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("無法連線 agent: %w", err)
	}
	defer resp.Body.Close()
	var result struct {
		OK  bool   `json:"ok"`
		Msg string `json:"msg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("agent 回應解析失敗: %w", err)
	}
	if !result.OK {
		return "", fmt.Errorf("%s", result.Msg)
	}
	return result.Msg, nil
}

// proxySyslogTest 將 journal 診斷測試委派給 agent 執行
func proxySyslogTest(agentURL string, c SyslogConfig) ([]testCheck, bool, error) {
	body, _ := json.Marshal(c)
	agentToken := os.Getenv("AGENT_TOKEN")
	req, err := http.NewRequest("POST", agentURL+"/syslogs/test", bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("建立 agent 請求失敗: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if agentToken != "" {
		req.Header.Set("X-Agent-Token", agentToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false, fmt.Errorf("無法連線 agent: %w", err)
	}
	defer resp.Body.Close()
	var result struct {
		OK     bool        `json:"ok"`
		Checks []testCheck `json:"checks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, false, fmt.Errorf("agent 回應解析失敗: %w", err)
	}
	return result.Checks, result.OK, nil
}

// runSyslogTestChecks 執行 syslog 備份前診斷（可在 agent 端或 dashboard 端執行）
func runSyslogTestChecks(c SyslogConfig) ([]testCheck, bool) {
	var checks []testCheck
	allOK := true

	if c.SourceType == "journal" {
		out, e := exec.Command("journalctl", "--version").Output() //nolint:gosec
		if e != nil {
			checks = append(checks, testCheck{"journalctl 可用", false, e.Error()})
			allOK = false
		} else {
			ver := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
			checks = append(checks, testCheck{"journalctl 可用", true, ver})
		}
		for _, unit := range c.JournalUnits {
			if unit == "" {
				continue
			}
			ou, eu := exec.Command("journalctl", "-u", unit, "-n", "1", "--no-pager").CombinedOutput() //nolint:gosec
			if eu != nil {
				detail := strings.TrimSpace(string(ou))
				if detail == "" {
					detail = eu.Error()
				}
				checks = append(checks, testCheck{"Unit: " + unit, false, detail})
				allOK = false
			} else {
				checks = append(checks, testCheck{"Unit: " + unit, true, "可正常查詢"})
			}
		}
	} else {
		for _, f := range c.LogFiles {
			if f == "" {
				continue
			}
			info, e := os.Stat(f)
			if e != nil {
				checks = append(checks, testCheck{"來源：" + f, false, e.Error()})
				allOK = false
			} else {
				checks = append(checks, testCheck{"來源：" + f, true,
					fmt.Sprintf("存在，大小 %d bytes", info.Size())})
			}
		}
	}

	// 檢查目的地父層目錄
	if _, e := os.Stat(c.Dest); e != nil {
		if e2 := os.MkdirAll(c.Dest, 0o755); e2 != nil {
			checks = append(checks, testCheck{"目的地目錄", false, e2.Error()})
			allOK = false
		} else {
			checks = append(checks, testCheck{"目的地目錄（已建立）", true, c.Dest})
		}
	} else {
		checks = append(checks, testCheck{"目的地目錄", true, c.Dest})
	}

	return checks, allOK
}

// HandleSyslogRunDirect 供 agent POST /syslogs/run 路由使用（在 host 上執行備份）
func HandleSyslogRunDirect(w http.ResponseWriter, r *http.Request) {
	var c SyslogConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	msg, err := executeSyslogBackup(c)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "msg": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "msg": msg})
}

// HandleSyslogTestDirect 供 agent POST /syslogs/test 路由使用（在 host 上執行診斷）
func HandleSyslogTestDirect(w http.ResponseWriter, r *http.Request) {
	var c SyslogConfig
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	checks, allOK := runSyslogTestChecks(c)
	writeJSON(w, http.StatusOK, map[string]any{"ok": allOK, "checks": checks})
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
		SELECT id, name, project_ids, backup_dir, backup_db_dir, remote_user, remote_host,
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
		if err := rows.Scan(&c.ID, &c.Name, &c.ProjectIDs, &c.BackupDir, &c.BackupDbDir,
			&c.RemoteUser, &c.RemoteHost, &c.RemotePath, &c.RemoteDbPath,
			&c.SshKey, &c.Enabled, &c.CronExpr, &c.LastRunAt, &c.RunStatus,
			&c.RunMessage, &c.CreatedAt, &c.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if c.ProjectIDs == nil {
			c.ProjectIDs = []int{}
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
	if c.ProjectIDs == nil {
		c.ProjectIDs = []int{}
	}
	err := h.pool.QueryRow(r.Context(), `
		INSERT INTO gcp_configs
		  (name, project_ids, backup_dir, backup_db_dir, remote_user, remote_host,
		   remote_path, remote_db_path, ssh_key, enabled, cron_expr)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		RETURNING id, created_at, updated_at`,
		c.Name, c.ProjectIDs, c.BackupDir, c.BackupDbDir, c.RemoteUser, c.RemoteHost,
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
	if c.ProjectIDs == nil {
		c.ProjectIDs = []int{}
	}
	_, err = h.pool.Exec(r.Context(), `
		UPDATE gcp_configs SET
		  name=$1, project_ids=$2, backup_dir=$3, backup_db_dir=$4, remote_user=$5,
		  remote_host=$6, remote_path=$7, remote_db_path=$8, ssh_key=$9,
		  enabled=$10, cron_expr=$11, updated_at=NOW()
		WHERE id=$12`,
		c.Name, c.ProjectIDs, c.BackupDir, c.BackupDbDir, c.RemoteUser,
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
		SELECT id, name, project_ids, backup_dir, backup_db_dir, remote_user, remote_host,
		       remote_path, remote_db_path, ssh_key, enabled,
		       cron_expr, last_run_at, run_status, run_message, created_at, updated_at
		FROM gcp_configs WHERE id=$1`, id).
		Scan(&c.ID, &c.Name, &c.ProjectIDs, &c.BackupDir, &c.BackupDbDir,
			&c.RemoteUser, &c.RemoteHost, &c.RemotePath, &c.RemoteDbPath,
			&c.SshKey, &c.Enabled, &c.CronExpr, &c.LastRunAt, &c.RunStatus,
			&c.RunMessage, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到設定")
		return
	}
	if c.ProjectIDs == nil {
		c.ProjectIDs = []int{}
	}
	h.pool.Exec(r.Context(), //nolint
		`UPDATE gcp_configs SET run_status='running', run_message='', updated_at=NOW() WHERE id=$1`, id)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "triggered", "message": "GCP 備份已開始（rsync）"})
	go func() {
		msg, runErr := executeGcpBackup(r.Context(), h.pool, c)
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

// gcpProjectInfo 查詢結果
type gcpProjectInfo struct {
	ID      int
	Name    string
	NasBase string
}

// test 執行 GCP 備份前的預檢診斷
func (h *gcpHandler) test(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	var c GcpConfig
	err = h.pool.QueryRow(r.Context(), `
		SELECT id, name, project_ids, backup_dir, backup_db_dir, remote_user, remote_host,
		       remote_path, remote_db_path, ssh_key, enabled,
		       cron_expr, last_run_at, run_status, run_message, created_at, updated_at
		FROM gcp_configs WHERE id=$1`, id).
		Scan(&c.ID, &c.Name, &c.ProjectIDs, &c.BackupDir, &c.BackupDbDir,
			&c.RemoteUser, &c.RemoteHost, &c.RemotePath, &c.RemoteDbPath,
			&c.SshKey, &c.Enabled, &c.CronExpr, &c.LastRunAt, &c.RunStatus,
			&c.RunMessage, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到設定")
		return
	}
	if c.ProjectIDs == nil {
		c.ProjectIDs = []int{}
	}

	var checks []testCheck
	allOK := true

	// 檢查 SSH Key 檔案
	if _, e := os.Stat(c.SshKey); e != nil {
		checks = append(checks, testCheck{"SSH Key 存在", false, e.Error()})
		allOK = false
	} else {
		checks = append(checks, testCheck{"SSH Key 存在", true, c.SshKey})
	}

	// 檢查 rsync
	out, e := exec.Command("rsync", "--version").Output() //nolint:gosec
	if e != nil {
		checks = append(checks, testCheck{"rsync 可用", false, e.Error()})
		allOK = false
	} else {
		ver := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)[0]
		checks = append(checks, testCheck{"rsync 可用", true, ver})
	}

	// 測試 SSH 連線（timeout 8s）
	sshOut, sshErr := exec.Command("ssh", //nolint:gosec
		"-i", c.SshKey,
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=8",
		"-o", "StrictHostKeyChecking=no",
		c.RemoteUser+"@"+c.RemoteHost, "echo ok").CombinedOutput()
	if sshErr != nil {
		detail := strings.TrimSpace(string(sshOut))
		if detail == "" {
			detail = sshErr.Error()
		}
		checks = append(checks, testCheck{
			fmt.Sprintf("SSH %s@%s", c.RemoteUser, c.RemoteHost), false, detail})
		allOK = false
	} else {
		checks = append(checks, testCheck{
			fmt.Sprintf("SSH %s@%s", c.RemoteUser, c.RemoteHost), true, "連線成功"})
	}

	// 若有關聯專案，檢查各 nas_base 目錄
	if len(c.ProjectIDs) > 0 {
		prows, qErr := h.pool.Query(r.Context(),
			`SELECT name, nas_base FROM projects WHERE id = ANY($1)`, c.ProjectIDs)
		if qErr == nil {
			defer prows.Close()
			for prows.Next() {
				var pname, nasBase string
				prows.Scan(&pname, &nasBase) //nolint
				if _, e := os.Stat(nasBase); e != nil {
					checks = append(checks, testCheck{"專案 " + pname + " nas_base", false, e.Error()})
					allOK = false
				} else {
					checks = append(checks, testCheck{"專案 " + pname + " nas_base", true, nasBase})
				}
			}
		}
	} else {
		// fallback 模式：檢查 backup_dir
		if _, e := os.Stat(c.BackupDir); e != nil {
			checks = append(checks, testCheck{"本地備份目錄", false, e.Error()})
			allOK = false
		} else {
			checks = append(checks, testCheck{"本地備份目錄", true, c.BackupDir})
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": allOK, "checks": checks})
}

func executeGcpBackup(ctx context.Context, pool *pgxpool.Pool, c GcpConfig) (string, error) {
	date := time.Now().Format("2006-01-02")
	sshOpt := fmt.Sprintf("ssh -i %s -o StrictHostKeyChecking=no", c.SshKey)

	// 若有指定 project_ids，對每個專案的 nas_base 做 rsync
	if len(c.ProjectIDs) > 0 {
		rows, err := pool.Query(ctx,
			`SELECT id, name, nas_base FROM projects WHERE id = ANY($1) AND enabled = true ORDER BY id`,
			c.ProjectIDs)
		if err != nil {
			return "", fmt.Errorf("查詢專案失敗: %w", err)
		}
		defer rows.Close()
		var projects []gcpProjectInfo
		for rows.Next() {
			var p gcpProjectInfo
			if err := rows.Scan(&p.ID, &p.Name, &p.NasBase); err != nil {
				return "", fmt.Errorf("掃描專案失敗: %w", err)
			}
			projects = append(projects, p)
		}
		if len(projects) == 0 {
			return "", fmt.Errorf("找不到已啟用的專案（ids: %v）", c.ProjectIDs)
		}
		var synced []string
		for _, p := range projects {
			if p.NasBase == "" {
				log.Printf("[gcp-run] 專案 %s (id=%d) nas_base 為空，略過", p.Name, p.ID)
				continue
			}
			srcDir := p.NasBase + "/" + date + "/"
			// 遠端路徑加上專案名稱子目錄
			dstDir := fmt.Sprintf("%s@%s:%s/%s/%s", c.RemoteUser, c.RemoteHost, c.RemotePath, p.Name, date)
			cmd := exec.Command("rsync", "-avz", "-e", sshOpt, srcDir, dstDir) //nolint:gosec
			out, cmdErr := cmd.CombinedOutput()
			if cmdErr != nil {
				return "", fmt.Errorf("rsync 專案 %s 失敗: %w\n%s", p.Name, cmdErr, string(out))
			}
			synced = append(synced, p.Name)
			log.Printf("[gcp-run] 專案 %s rsync 完成: %s → %s", p.Name, srcDir, dstDir)
		}
		return fmt.Sprintf("rsync 完成 %d 個專案 (%s) → %s@%s", len(synced), join(synced, ", "), c.RemoteUser, c.RemoteHost), nil
	}

	// fallback：使用固定的 backup_dir / backup_db_dir
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

func join(ss []string, sep string) string {
	result := ""
	for i, s := range ss {
		if i > 0 {
			result += sep
		}
		result += s
	}
	return result
}
