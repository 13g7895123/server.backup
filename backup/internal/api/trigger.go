package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

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

	// 若有設定 AGENT_URL，將 trigger 轉發給 host agent（agent 有 docker 環境）
	agentURL := os.Getenv("AGENT_URL") // e.g. http://127.0.0.1:9090
	agentToken := os.Getenv("AGENT_TOKEN")

	if agentURL != "" {
		if err := forwardToAgent(agentURL, agentToken, req.ProjectID, req.TargetType); err != nil {
			log.Printf("[trigger] 轉發 agent 失敗，改用本地執行: %v", err)
			h.runLocal(req)
		} else {
			log.Printf("[trigger] 已轉發至 agent: project_id=%d type=%s", req.ProjectID, req.TargetType)
		}
	} else {
		h.runLocal(req)
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"status":     "triggered",
		"project_id": req.ProjectID,
		"type":       req.TargetType,
		"message":    "備份已開始，可至 /api/projects/" + strconv.Itoa(req.ProjectID) + "/backups 查看進度",
	})
}

func (h *triggerHandler) runLocal(req triggerRequest) {
	go func() {
		if err := h.runner.RunProject(context.Background(), req.ProjectID, []string{req.TargetType}, nil, "manual"); err != nil {
			log.Printf("[trigger] project_id=%d type=%s 備份失敗: %v", req.ProjectID, req.TargetType, err)
		} else {
			log.Printf("[trigger] project_id=%d type=%s 備份完成", req.ProjectID, req.TargetType)
		}
	}()
}

// forwardToAgent 將 trigger 請求轉發給 host agent 的 HTTP server
func forwardToAgent(agentURL, token string, projectID int, targetType string) error {
	body, _ := json.Marshal(map[string]any{
		"project_id":  projectID,
		"target_type": targetType,
	})
	req, err := http.NewRequestWithContext(context.Background(), "POST",
		fmt.Sprintf("%s/trigger", agentURL), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Agent-Token", token)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("連線 agent 失敗: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("agent 回應 %d: %s", resp.StatusCode, string(b))
	}
	return nil
}
