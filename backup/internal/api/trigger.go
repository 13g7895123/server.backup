package api

import (
	"context"
	"encoding/json"
	"log"
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

	// 非同步執行，立即回應（使用 context.Background() 避免 HTTP context 取消後中斷備份）
	go func() {
		if err := h.runner.RunProject(context.Background(), req.ProjectID, []string{req.TargetType}, nil, "manual"); err != nil {
			log.Printf("[trigger] project_id=%d type=%s 備份失敗: %v", req.ProjectID, req.TargetType, err)
		} else {
			log.Printf("[trigger] project_id=%d type=%s 備份完成", req.ProjectID, req.TargetType)
		}
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":     "triggered",
		"project_id": req.ProjectID,
		"type":       req.TargetType,
		"message":    "備份已開始，可至 /api/projects/" + strconv.Itoa(req.ProjectID) + "/backups 查看進度",
	})
}
