package api

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"backup-manager/internal/store"
)

// ── 中介層：API Key 驗證 ─────────────────────────────────────────────────────

// apiKeyMiddleware 從 Authorization: Bearer <key> 或 ?api_key=<key> 取得 key，
// 驗證後將 project_id 注入 context，並確保只能存取對應的專案。
func apiKeyAuth(s *store.Store, next func(w http.ResponseWriter, r *http.Request, projectID int)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := ""
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			raw = strings.TrimPrefix(auth, "Bearer ")
		} else {
			raw = r.URL.Query().Get("api_key")
		}
		if raw == "" {
			writeError(w, http.StatusUnauthorized, "缺少 API Key（請使用 Authorization: Bearer <key> 或 ?api_key=<key>）")
			return
		}
		hash := hashKey(raw)
		projectID, err := s.ValidateAPIKey(r.Context(), hash)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "API Key 無效或已失效")
			return
		}
		next(w, r, projectID)
	}
}

// ── Key 管理 Handler（需 AGENT_TOKEN 或僅內部使用）────────────────────────────

type apiKeyHandler struct{ store *store.Store }

func RegisterAPIKeyRoutes(mux *http.ServeMux, s *store.Store) {
	h := &apiKeyHandler{store: s}

	// 管理介面用（專案 key）
	mux.HandleFunc("GET /api/admin/projects/{id}/api-keys", h.list)
	mux.HandleFunc("POST /api/admin/projects/{id}/api-keys", h.create)
	mux.HandleFunc("DELETE /api/admin/api-keys/{kid}", h.delete)
	mux.HandleFunc("PATCH /api/admin/api-keys/{kid}/revoke", h.revoke)

	// 管理介面用（syslog key）
	mux.HandleFunc("GET /api/admin/syslogs/{id}/api-keys", h.syslogList)
	mux.HandleFunc("POST /api/admin/syslogs/{id}/api-keys", h.syslogCreate)
	mux.HandleFunc("DELETE /api/admin/syslog-api-keys/{kid}", h.syslogDelete)
	mux.HandleFunc("PATCH /api/admin/syslog-api-keys/{kid}/revoke", h.syslogRevoke)

	// 管理介面用（system key）
	mux.HandleFunc("GET /api/admin/system-api-keys", h.systemList)
	mux.HandleFunc("POST /api/admin/system-api-keys", h.systemCreate)
	mux.HandleFunc("DELETE /api/admin/system-api-keys/{kid}", h.systemDelete)
	mux.HandleFunc("PATCH /api/admin/system-api-keys/{kid}/revoke", h.systemRevoke)

	// 對外資料 API（專案 key）
	mux.HandleFunc("GET /api/v1/project/overview", apiKeyAuth(s, projectOverview(s)))
	mux.HandleFunc("GET /api/v1/project/targets", apiKeyAuth(s, projectTargets(s)))
	mux.HandleFunc("GET /api/v1/project/schedules", apiKeyAuth(s, projectSchedules(s)))
	mux.HandleFunc("GET /api/v1/project/retention", apiKeyAuth(s, projectRetention(s)))
	mux.HandleFunc("GET /api/v1/project/backups", apiKeyAuth(s, projectBackups(s)))

	// 對外資料 API（syslog key）
	mux.HandleFunc("GET /api/v1/syslog/info", syslogKeyAuth(s, syslogInfo(s)))
	mux.HandleFunc("GET /api/v1/syslog/records", syslogKeyAuth(s, syslogRecords(s)))
}

// GET /api/admin/projects/{id}/api-keys
func (h *apiKeyHandler) list(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 project id")
		return
	}
	keys, err := h.store.ListAPIKeys(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if keys == nil {
		keys = []store.APIKey{}
	}
	writeJSON(w, http.StatusOK, keys)
}

// POST /api/admin/projects/{id}/api-keys
func (h *apiKeyHandler) create(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 project id")
		return
	}
	// 確認專案存在
	if _, err := h.store.GetProject(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, "找不到專案")
		return
	}

	var body struct {
		Name      string  `json:"name"`
		ExpiresIn *string `json:"expires_in"` // "30d" | "90d" | "365d" | null
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "name 不可為空")
		return
	}

	// 產生隨機 key：bak_ + 48 隨機 hex bytes = 100 chars total
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "key 生成失敗")
		return
	}
	rawKey := "bak_" + hex.EncodeToString(rawBytes)
	keyHash := hashKey(rawKey)
	keyPrefix := rawKey[:8]

	var expiresAt *time.Time
	if body.ExpiresIn != nil && *body.ExpiresIn != "" {
		d, err := parseDuration(*body.ExpiresIn)
		if err != nil {
			writeError(w, http.StatusBadRequest, "無效的 expires_in（支援 30d / 90d / 365d）")
			return
		}
		t := time.Now().Add(d)
		expiresAt = &t
	}

	k, err := h.store.CreateAPIKey(r.Context(), id, body.Name, keyHash, keyPrefix, expiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 只在建立時回傳完整 key，之後不再顯示
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         k.ID,
		"project_id": k.ProjectID,
		"name":       k.Name,
		"key":        rawKey, // 僅此次回傳
		"key_prefix": k.KeyPrefix,
		"enabled":    k.Enabled,
		"expires_at": k.ExpiresAt,
		"created_at": k.CreatedAt,
	})
}

// PATCH /api/admin/api-keys/{kid}/revoke
func (h *apiKeyHandler) revoke(w http.ResponseWriter, r *http.Request) {
	kid, err := strconv.Atoi(r.PathValue("kid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 key id")
		return
	}
	if err := h.store.RevokeAPIKey(r.Context(), kid); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// DELETE /api/admin/api-keys/{kid}
func (h *apiKeyHandler) delete(w http.ResponseWriter, r *http.Request) {
	kid, err := strconv.Atoi(r.PathValue("kid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 key id")
		return
	}
	if err := h.store.DeleteAPIKey(r.Context(), kid); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── 對外資料 API ──────────────────────────────────────────────────────────────

// projectOverview 回傳專案基本資訊 + 統計摘要
func projectOverview(s *store.Store) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, projectID int) {
		proj, err := s.GetProject(r.Context(), projectID)
		if err != nil {
			writeError(w, http.StatusNotFound, "找不到專案")
			return
		}
		targets, _ := s.ListTargets(r.Context(), projectID)
		schedules, _ := s.ListSchedules(r.Context(), projectID)
		retention, _ := s.ListRetention(r.Context(), projectID)

		f := store.ListRecordsFilter{ProjectID: &projectID, Limit: 5}
		recentRecords, total, _ := s.ListRecords(r.Context(), f)

		writeJSON(w, http.StatusOK, map[string]any{
			"project":        proj,
			"targets":        nullSlice(targets),
			"schedules":      nullSlice(schedules),
			"retention":      nullSlice(retention),
			"recent_backups": nullSlice(recentRecords),
			"total_backups":  total,
		})
	}
}

// projectTargets 回傳備份目標列表
func projectTargets(s *store.Store) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, projectID int) {
		targets, err := s.ListTargets(r.Context(), projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, nullSlice(targets))
	}
}

// projectSchedules 回傳排程列表
func projectSchedules(s *store.Store) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, projectID int) {
		schedules, err := s.ListSchedules(r.Context(), projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, nullSlice(schedules))
	}
}

// projectRetention 回傳保留政策
func projectRetention(s *store.Store) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, projectID int) {
		policies, err := s.ListRetention(r.Context(), projectID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, nullSlice(policies))
	}
}

// projectBackups 回傳備份紀錄（支援 limit / offset / type / status 篩選）
func projectBackups(s *store.Store) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, projectID int) {
		f := parseFilter(r)
		f.ProjectID = &projectID
		records, total, err := s.ListRecords(r.Context(), f)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"records": nullSlice(records),
			"total":   total,
			"limit":   f.Limit,
			"offset":  f.Offset,
		})
	}
}

// ── Syslog Key 中介層 ─────────────────────────────────────────────────────────

func syslogKeyAuth(s *store.Store, next func(http.ResponseWriter, *http.Request, int)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := ""
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			raw = strings.TrimPrefix(auth, "Bearer ")
		} else {
			raw = r.URL.Query().Get("api_key")
		}
		if raw == "" {
			writeError(w, http.StatusUnauthorized, "缺少 API Key（請使用 Authorization: Bearer <key> 或 ?api_key=<key>）")
			return
		}
		hash := hashKey(raw)
		syslogID, err := s.ValidateSyslogAPIKey(r.Context(), hash)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "API Key 無效或已失效")
			return
		}
		next(w, r, syslogID)
	}
}

// ── Syslog Key 管理 Handler ───────────────────────────────────────────────────

// GET /api/admin/syslogs/{id}/api-keys
func (h *apiKeyHandler) syslogList(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 syslog id")
		return
	}
	keys, err := h.store.ListSyslogAPIKeys(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if keys == nil {
		keys = []store.SyslogAPIKey{}
	}
	writeJSON(w, http.StatusOK, keys)
}

// POST /api/admin/syslogs/{id}/api-keys
func (h *apiKeyHandler) syslogCreate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 syslog id")
		return
	}
	var body struct {
		Name      string  `json:"name"`
		ExpiresIn *string `json:"expires_in"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "name 不可為空")
		return
	}
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "key 生成失敗")
		return
	}
	rawKey := "bsl_" + hex.EncodeToString(rawBytes)
	keyHash := hashKey(rawKey)
	keyPrefix := rawKey[:8]
	var expiresAt *time.Time
	if body.ExpiresIn != nil && *body.ExpiresIn != "" {
		d, err := parseDuration(*body.ExpiresIn)
		if err != nil {
			writeError(w, http.StatusBadRequest, "無效的 expires_in")
			return
		}
		t := time.Now().Add(d)
		expiresAt = &t
	}
	k, err := h.store.CreateSyslogAPIKey(r.Context(), id, body.Name, keyHash, keyPrefix, expiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         k.ID,
		"syslog_id":  k.SyslogID,
		"name":       k.Name,
		"key":        rawKey,
		"key_prefix": k.KeyPrefix,
		"enabled":    k.Enabled,
		"expires_at": k.ExpiresAt,
		"created_at": k.CreatedAt,
	})
}

// PATCH /api/admin/syslog-api-keys/{kid}/revoke
func (h *apiKeyHandler) syslogRevoke(w http.ResponseWriter, r *http.Request) {
	kid, err := strconv.Atoi(r.PathValue("kid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 key id")
		return
	}
	if err := h.store.RevokeSyslogAPIKey(r.Context(), kid); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// DELETE /api/admin/syslog-api-keys/{kid}
func (h *apiKeyHandler) syslogDelete(w http.ResponseWriter, r *http.Request) {
	kid, err := strconv.Atoi(r.PathValue("kid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 key id")
		return
	}
	if err := h.store.DeleteSyslogAPIKey(r.Context(), kid); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Syslog 對外資料 API ───────────────────────────────────────────────────────

// syslogInfo 回傳 syslog 設定基本資訊
func syslogInfo(s *store.Store) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, syslogID int) {
		var cfg struct {
			ID         int        `json:"id"`
			Name       string     `json:"name"`
			LogType    string     `json:"log_type"`
			SourceType string     `json:"source_type"`
			Dest       string     `json:"dest"`
			Compress   bool       `json:"compress"`
			Enabled    bool       `json:"enabled"`
			CronExpr   string     `json:"cron_expr"`
			LastRunAt  *time.Time `json:"last_run_at"`
			RunStatus  string     `json:"run_status"`
			RunMessage string     `json:"run_message"`
			CreatedAt  time.Time  `json:"created_at"`
		}
		err := s.Pool().QueryRow(r.Context(), `
			SELECT id, name, log_type, source_type, dest, compress, enabled,
			       COALESCE(cron_expr,''), last_run_at, COALESCE(run_status,''),
			       COALESCE(run_message,''), created_at
			FROM syslog_configs WHERE id=$1`, syslogID).
			Scan(&cfg.ID, &cfg.Name, &cfg.LogType, &cfg.SourceType, &cfg.Dest, &cfg.Compress,
				&cfg.Enabled, &cfg.CronExpr, &cfg.LastRunAt, &cfg.RunStatus, &cfg.RunMessage, &cfg.CreatedAt)
		if err != nil {
			writeError(w, http.StatusNotFound, "找不到系統日誌設定")
			return
		}
		writeJSON(w, http.StatusOK, cfg)
	}
}

// syslogRecords 回傳 syslog 備份紀錄（支援 limit / offset）
func syslogRecords(s *store.Store) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, syslogID int) {
		q := r.URL.Query()
		limit := 50
		offset := 0
		if v := q.Get("limit"); v != "" {
			if n, e := strconv.Atoi(v); e == nil && n > 0 {
				limit = n
			}
		}
		if v := q.Get("offset"); v != "" {
			if n, e := strconv.Atoi(v); e == nil && n >= 0 {
				offset = n
			}
		}
		rows, err := s.Pool().Query(r.Context(), `
			SELECT id, status, COALESCE(error_msg,''), COALESCE(duration_sec,0), filename, size_bytes, created_at
			FROM backup_records
			WHERE type='syslog' AND sub_type=$1
			ORDER BY created_at DESC
			LIMIT $2 OFFSET $3`, strconv.Itoa(syslogID), limit, offset)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		type rec struct {
			ID          int64     `json:"id"`
			Status      string    `json:"status"`
			ErrorMsg    string    `json:"error_msg"`
			DurationSec float64   `json:"duration_sec"`
			Filename    string    `json:"filename"`
			SizeBytes   int64     `json:"size_bytes"`
			CreatedAt   time.Time `json:"created_at"`
		}
		var records []rec
		for rows.Next() {
			var r rec
			if err := rows.Scan(&r.ID, &r.Status, &r.ErrorMsg, &r.DurationSec, &r.Filename, &r.SizeBytes, &r.CreatedAt); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			records = append(records, r)
		}
		if records == nil {
			records = []rec{}
		}
		var total int64
		s.Pool().QueryRow(r.Context(), `SELECT COUNT(*) FROM backup_records WHERE type='syslog' AND sub_type=$1`, strconv.Itoa(syslogID)).Scan(&total) //nolint
		writeJSON(w, http.StatusOK, map[string]any{
			"records": records,
			"total":   total,
			"limit":   limit,
			"offset":  offset,
		})
	}
}

// ── System Key 中介層 ─────────────────────────────────────────────────────────

// systemKeyAuth 驗證 sys_ 前綴的系統 API key，適用於 /api/v1/system/* 路由
func systemKeyAuth(s *store.Store, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw := ""
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			raw = strings.TrimPrefix(auth, "Bearer ")
		} else {
			raw = r.URL.Query().Get("api_key")
		}
		if raw == "" {
			writeError(w, http.StatusUnauthorized, "缺少 API Key（請使用 Authorization: Bearer <key> 或 ?api_key=<key>）")
			return
		}
		hash := hashKey(raw)
		if err := s.ValidateSystemAPIKey(r.Context(), hash); err != nil {
			writeError(w, http.StatusUnauthorized, "API Key 無效或已失效")
			return
		}
		next(w, r)
	}
}

// ── System Key 管理 Handler ───────────────────────────────────────────────────

// GET /api/admin/system-api-keys
func (h *apiKeyHandler) systemList(w http.ResponseWriter, r *http.Request) {
	keys, err := h.store.ListSystemAPIKeys(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if keys == nil {
		keys = []store.SystemAPIKey{}
	}
	writeJSON(w, http.StatusOK, keys)
}

// POST /api/admin/system-api-keys
func (h *apiKeyHandler) systemCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name      string  `json:"name"`
		ExpiresIn *string `json:"expires_in"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON")
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusBadRequest, "name 不可為空")
		return
	}
	rawBytes := make([]byte, 32)
	if _, err := rand.Read(rawBytes); err != nil {
		writeError(w, http.StatusInternalServerError, "key 生成失敗")
		return
	}
	rawKey := "sys_" + hex.EncodeToString(rawBytes)
	keyHash := hashKey(rawKey)
	keyPrefix := rawKey[:8]
	var expiresAt *time.Time
	if body.ExpiresIn != nil && *body.ExpiresIn != "" {
		d, err := parseDuration(*body.ExpiresIn)
		if err != nil {
			writeError(w, http.StatusBadRequest, "無效的 expires_in（支援 30d / 90d / 365d）")
			return
		}
		t := time.Now().Add(d)
		expiresAt = &t
	}
	k, err := h.store.CreateSystemAPIKey(r.Context(), body.Name, keyHash, keyPrefix, expiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":         k.ID,
		"name":       k.Name,
		"key":        rawKey,
		"key_prefix": k.KeyPrefix,
		"enabled":    k.Enabled,
		"expires_at": k.ExpiresAt,
		"created_at": k.CreatedAt,
	})
}

// PATCH /api/admin/system-api-keys/{kid}/revoke
func (h *apiKeyHandler) systemRevoke(w http.ResponseWriter, r *http.Request) {
	kid, err := strconv.Atoi(r.PathValue("kid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 key id")
		return
	}
	if err := h.store.RevokeSystemAPIKey(r.Context(), kid); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}

// DELETE /api/admin/system-api-keys/{kid}
func (h *apiKeyHandler) systemDelete(w http.ResponseWriter, r *http.Request) {
	kid, err := strconv.Atoi(r.PathValue("kid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 key id")
		return
	}
	if err := h.store.DeleteSystemAPIKey(r.Context(), kid); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── 工具函式 ──────────────────────────────────────────────────────────────────

func hashKey(raw string) string {
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

func parseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return 0, fmt.Errorf("格式錯誤")
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("格式錯誤")
	}
	switch s[len(s)-1] {
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'h':
		return time.Duration(n) * time.Hour, nil
	default:
		return 0, fmt.Errorf("只支援 d（天）或 h（小時）")
	}
}

// nullSlice 確保 nil slice 序列化為 [] 而非 null
func nullSlice[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
