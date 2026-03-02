package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"backup-manager/internal/backup"
	"backup-manager/internal/store"
)

type triggerHandler struct {
	store  *store.Store
	runner *backup.Runner
}

func RegisterTriggerRoute(mux *http.ServeMux, s *store.Store, r *backup.Runner) {
	h := &triggerHandler{store: s, runner: r}
	mux.HandleFunc("POST /api/backups/trigger", h.trigger)
}

type triggerRequest struct {
	ProjectID  int    `json:"project_id"`
	TargetType string `json:"target_type"` // "files" | "database" | "system" | "all"
}

func (h *triggerHandler) trigger(w http.ResponseWriter, r *http.Request) {
	var req triggerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	if req.ProjectID == 0 {
		writeError(w, http.StatusBadRequest, "project_id 不可為空")
		return
	}
	if req.TargetType == "" {
		req.TargetType = "all"
	}

	// 非同步執行，立即回應
	go h.runner.RunProject(r.Context(), req.ProjectID, []string{req.TargetType}, nil, "manual") //nolint

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":     "triggered",
		"project_id": req.ProjectID,
		"type":       req.TargetType,
		"message":    "備份已開始，可至 /api/projects/" + strconv.Itoa(req.ProjectID) + "/backups 查看進度",
	})
}
