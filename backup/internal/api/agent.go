package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"time"

	"backup-manager/internal/store"
)

// agentMiddleware 驗證 X-Agent-Token（若設定了 AGENT_TOKEN 環境變數）
func agentMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := os.Getenv("AGENT_TOKEN")
		if token != "" && r.Header.Get("X-Agent-Token") != token {
			writeError(w, http.StatusUnauthorized, "invalid agent token")
			return
		}
		next(w, r)
	}
}

type agentHandler struct{ store *store.Store }

// RegisterAgentRoutes 註冊 host agent 專用的內部 API
func RegisterAgentRoutes(mux *http.ServeMux, s *store.Store) {
	h := &agentHandler{store: s}

	// 排程管理
	mux.HandleFunc("GET /api/agent/schedules/enabled", agentMiddleware(h.listEnabledSchedules))
	mux.HandleFunc("GET /api/agent/schedules/{id}", agentMiddleware(h.getSchedule))
	mux.HandleFunc("POST /api/agent/schedules/{id}/runtime", agentMiddleware(h.updateRuntime))

	// 備份紀錄 CRUD（agent 建立 + 更新）
	mux.HandleFunc("POST /api/agent/records", agentMiddleware(h.createRecord))
	mux.HandleFunc("PUT /api/agent/records/{id}", agentMiddleware(h.updateRecord))
}

// GET /api/agent/schedules/enabled
func (h *agentHandler) listEnabledSchedules(w http.ResponseWriter, r *http.Request) {
	schedules, err := h.store.ListEnabledSchedules(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, schedules)
}

// GET /api/agent/schedules/{id}
func (h *agentHandler) getSchedule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	sch, err := h.store.GetSchedule(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到排程")
		return
	}
	writeJSON(w, http.StatusOK, sch)
}

// POST /api/agent/schedules/{id}/runtime
func (h *agentHandler) updateRuntime(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	var body struct {
		LastRunAt time.Time `json:"last_run_at"`
		NextRunAt time.Time `json:"next_run_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON")
		return
	}
	if err := h.store.UpdateScheduleRunTime(r.Context(), id, body.LastRunAt, body.NextRunAt); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// POST /api/agent/records
func (h *agentHandler) createRecord(w http.ResponseWriter, r *http.Request) {
	var rec store.BackupRecord
	if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	id, err := h.store.CreateRecord(r.Context(), &rec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

// PUT /api/agent/records/{id}
func (h *agentHandler) updateRecord(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	var rec store.BackupRecord
	if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	rec.ID = id
	if err := h.store.UpdateRecord(r.Context(), &rec); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
