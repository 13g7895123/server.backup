package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"backup-manager/internal/scheduler"
	"backup-manager/internal/store"
)

type scheduleHandler struct {
	store     *store.Store
	scheduler *scheduler.DynamicScheduler
}

func RegisterScheduleRoutes(mux *http.ServeMux, s *store.Store, sc *scheduler.DynamicScheduler) {
	h := &scheduleHandler{store: s, scheduler: sc}
	mux.HandleFunc("GET /api/projects/{id}/schedules", h.list)
	mux.HandleFunc("POST /api/projects/{id}/schedules", h.create)
	mux.HandleFunc("PUT /api/projects/{id}/schedules/{sid}", h.update)
	mux.HandleFunc("DELETE /api/projects/{id}/schedules/{sid}", h.delete)
	mux.HandleFunc("PATCH /api/projects/{id}/schedules/{sid}/toggle", h.toggle)
	mux.HandleFunc("GET /api/schedules/all", h.listAll)
}

func (h *scheduleHandler) list(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 project id")
		return
	}
	schedules, err := h.store.ListSchedules(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, schedules)
}

func (h *scheduleHandler) create(w http.ResponseWriter, r *http.Request) {
	projectID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 project id")
		return
	}
	var sch store.Schedule
	if err := json.NewDecoder(r.Body).Decode(&sch); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	if sch.CronExpr == "" {
		writeError(w, http.StatusBadRequest, "cron_expr 不可為空")
		return
	}
	if len(sch.TargetTypes) == 0 {
		sch.TargetTypes = []string{"all"}
	}
	sch.ProjectID = projectID
	sch.Enabled = true

	result, err := h.store.CreateSchedule(r.Context(), &sch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 立即載入排程
	if err := h.scheduler.Reload(r.Context(), result.ID); err != nil {
		// 排程格式有誤，回傳 400 並刪除
		h.store.DeleteSchedule(r.Context(), result.ID) //nolint
		writeError(w, http.StatusBadRequest, "排程格式無效: "+err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, result)
}

func (h *scheduleHandler) update(w http.ResponseWriter, r *http.Request) {
	sid, err := strconv.Atoi(r.PathValue("sid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 schedule id")
		return
	}
	var sch store.Schedule
	if err := json.NewDecoder(r.Body).Decode(&sch); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	sch.ID = sid
	if err := h.store.UpdateSchedule(r.Context(), &sch); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// 重新載入排程
	h.scheduler.Reload(r.Context(), sid) //nolint
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *scheduleHandler) delete(w http.ResponseWriter, r *http.Request) {
	sid, err := strconv.Atoi(r.PathValue("sid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 schedule id")
		return
	}
	h.scheduler.Remove(sid)
	if err := h.store.DeleteSchedule(r.Context(), sid); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *scheduleHandler) toggle(w http.ResponseWriter, r *http.Request) {
	sid, err := strconv.Atoi(r.PathValue("sid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 schedule id")
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON")
		return
	}
	if err := h.store.ToggleSchedule(r.Context(), sid, body.Enabled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.scheduler.Reload(r.Context(), sid) //nolint
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": body.Enabled})
}

// listAll 回傳所有排程（跨所有專案），包含所屬專案名稱
func (h *scheduleHandler) listAll(w http.ResponseWriter, r *http.Request) {
	pool := h.store.Pool()
	rows, err := pool.Query(r.Context(), `
		SELECT s.id, s.project_id, p.name AS project_name, s.label, s.cron_expr,
		       s.target_types, s.enabled, s.last_run_at, s.next_run_at,
		       s.created_at, s.updated_at
		FROM schedules s
		JOIN projects p ON p.id = s.project_id
		ORDER BY p.name, s.id`)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()

	type ScheduleRow struct {
		store.Schedule
		ProjectName string `json:"project_name"`
	}
	var result []ScheduleRow
	for rows.Next() {
		var row ScheduleRow
		if err := rows.Scan(&row.ID, &row.ProjectID, &row.ProjectName, &row.Label, &row.CronExpr,
			&row.TargetTypes, &row.Enabled, &row.LastRunAt, &row.NextRunAt,
			&row.CreatedAt, &row.UpdatedAt); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		result = append(result, row)
	}
	if result == nil {
		result = []ScheduleRow{}
	}
	writeJSON(w, http.StatusOK, result)
}
