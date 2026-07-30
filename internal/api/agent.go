package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"backup-manager/internal/store"
)

type agentContextKey string

const currentAgentKey agentContextKey = "current-agent"

func currentAgentFromContext(ctx context.Context) (*store.Agent, bool) {
	agent, ok := ctx.Value(currentAgentKey).(*store.Agent)
	return agent, ok
}

func agentMiddleware(s *store.Store, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code := strings.TrimSpace(r.Header.Get("X-Agent-Code"))
		token := r.Header.Get("X-Agent-Token")
		if code == "" || token == "" {
			writeError(w, http.StatusUnauthorized, "missing agent credentials")
			return
		}

		agent, err := s.GetAgentByCode(r.Context(), code)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid agent code")
			return
		}
		if !agent.Enabled {
			writeError(w, http.StatusForbidden, "agent disabled")
			return
		}
		if token != agent.TokenHash {
			writeError(w, http.StatusUnauthorized, "invalid agent token")
			return
		}

		ctx := context.WithValue(r.Context(), currentAgentKey, agent)
		next(w, r.WithContext(ctx))
	}
}

type agentHandler struct{ store *store.Store }

func RegisterAgentRoutes(mux *http.ServeMux, s *store.Store) {
	h := &agentHandler{store: s}

	mux.HandleFunc("POST /api/agent/heartbeat", agentMiddleware(s, h.heartbeat))
	mux.HandleFunc("POST /api/agent/commands/{id}/finish", agentMiddleware(s, h.finishCommand))
	mux.HandleFunc("GET /api/agent/schedules/enabled", agentMiddleware(s, h.listEnabledSchedules))
	mux.HandleFunc("GET /api/agent/schedules/{id}", agentMiddleware(s, h.getSchedule))
	mux.HandleFunc("POST /api/agent/schedules/{id}/runtime", agentMiddleware(s, h.updateRuntime))
	mux.HandleFunc("POST /api/agent/schedules/{id}/status", agentMiddleware(s, h.updateStatus))
	mux.HandleFunc("GET /api/agent/projects/{id}", agentMiddleware(s, h.getProject))
	mux.HandleFunc("GET /api/agent/projects/{id}/targets", agentMiddleware(s, h.listTargets))
	mux.HandleFunc("GET /api/agent/projects/{id}/retention", agentMiddleware(s, h.listRetention))
	mux.HandleFunc("POST /api/agent/records", agentMiddleware(s, h.createRecord))
	mux.HandleFunc("GET /api/agent/records/{id}", agentMiddleware(s, h.getRecord))
	mux.HandleFunc("PUT /api/agent/records/{id}", agentMiddleware(s, h.updateRecord))
	mux.HandleFunc("POST /api/agent/records/{id}/upload", agentMiddleware(s, h.uploadRecord))
	mux.HandleFunc("GET /api/agent/records/{id}/download", agentMiddleware(s, h.downloadRecord))
}

func (h *agentHandler) requireAgent(w http.ResponseWriter, r *http.Request) (*store.Agent, bool) {
	agent, ok := currentAgentFromContext(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "missing agent context")
		return nil, false
	}
	return agent, true
}

func (h *agentHandler) heartbeat(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	var body store.AgentHeartbeat
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON")
		return
	}
	if err := h.store.TouchAgentHeartbeat(r.Context(), agent.ID, body); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.DiskUsage != nil {
		if err := h.store.UpsertAgentDiskUsage(r.Context(), agent.ID, body.DiskUsage); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	updated, err := h.store.GetAgent(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	commands, err := h.store.ClaimPendingAgentCommands(r.Context(), agent.ID, 10)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, agentHeartbeatResponse{
		Agent:    updated,
		Commands: commands,
	})
}

func (h *agentHandler) finishCommand(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 command id")
		return
	}
	cmd, err := h.store.GetAgentCommand(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到 command")
		return
	}
	if cmd.AgentID != agent.ID {
		writeError(w, http.StatusForbidden, "command not assigned to agent")
		return
	}
	var body agentCommandFinishRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON")
		return
	}
	cmd.Status = body.Status
	cmd.Result = body.Result
	cmd.LogOutput = body.LogOutput
	cmd.LogRef = body.LogRef
	cmd.ErrorMsg = body.ErrorMsg
	if cmd.Status == "" {
		cmd.Status = store.AgentCommandStatusSuccess
	}
	if err := h.store.FinishAgentCommand(r.Context(), cmd); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cmd.RestoreRecordID != nil {
		snapshotPath := ""
		if len(cmd.Result) > 0 {
			var result map[string]any
			if err := json.Unmarshal(cmd.Result, &result); err == nil {
				if v, _ := result["snapshot_path"].(string); v != "" {
					snapshotPath = v
				}
			}
		}
		restoreStatus := "success"
		if cmd.Status != store.AgentCommandStatusSuccess {
			restoreStatus = "failed"
		}
		if err := h.store.FinishRestoreRecord(r.Context(), *cmd.RestoreRecordID, restoreStatus, snapshotPath, cmd.ErrorMsg); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *agentHandler) listEnabledSchedules(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	schedules, err := h.store.ListEnabledSchedulesForAgent(r.Context(), agent.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, schedules)
}

func (h *agentHandler) getSchedule(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	owns, err := h.store.AgentOwnsSchedule(r.Context(), agent.ID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !owns {
		writeError(w, http.StatusForbidden, "schedule not assigned to agent")
		return
	}
	sch, err := h.store.GetSchedule(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到排程")
		return
	}
	writeJSON(w, http.StatusOK, sch)
}

func (h *agentHandler) updateRuntime(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	owns, err := h.store.AgentOwnsSchedule(r.Context(), agent.ID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !owns {
		writeError(w, http.StatusForbidden, "schedule not assigned to agent")
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

func (h *agentHandler) updateStatus(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	owns, err := h.store.AgentOwnsSchedule(r.Context(), agent.ID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !owns {
		writeError(w, http.StatusForbidden, "schedule not assigned to agent")
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON")
		return
	}
	if err := h.store.UpdateScheduleStatus(r.Context(), id, body.Status); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *agentHandler) getProject(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	projectID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 project id")
		return
	}
	owns, err := h.store.AgentOwnsProject(r.Context(), agent.ID, projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !owns {
		writeError(w, http.StatusForbidden, "project not assigned to agent")
		return
	}
	project, err := h.store.GetProject(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到專案")
		return
	}
	if project.TransferMode != "upload" {
		if err := h.store.ResolveProjectNASForAgent(r.Context(), project, agent.ID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, project)
}

func (h *agentHandler) listTargets(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	projectID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 project id")
		return
	}
	owns, err := h.store.AgentOwnsProject(r.Context(), agent.ID, projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !owns {
		writeError(w, http.StatusForbidden, "project not assigned to agent")
		return
	}
	targets, err := h.store.ListTargets(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, targets)
}

func (h *agentHandler) listRetention(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	projectID, err := pathID(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 project id")
		return
	}
	owns, err := h.store.AgentOwnsProject(r.Context(), agent.ID, projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !owns {
		writeError(w, http.StatusForbidden, "project not assigned to agent")
		return
	}
	policies, err := h.store.ListRetention(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, policies)
}

func (h *agentHandler) createRecord(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	var rec store.BackupRecord
	if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	if rec.ProjectID == nil {
		writeError(w, http.StatusBadRequest, "project_id required")
		return
	}
	owns, err := h.store.AgentOwnsProject(r.Context(), agent.ID, *rec.ProjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !owns {
		writeError(w, http.StatusForbidden, "project not assigned to agent")
		return
	}
	rec.AgentID = &agent.ID
	rec.AgentName = agent.Name
	rec.RunHost = agent.HostName
	id, err := h.store.CreateRecord(r.Context(), &rec)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (h *agentHandler) getRecord(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	owns, err := h.store.AgentOwnsRecord(r.Context(), agent.ID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !owns {
		writeError(w, http.StatusForbidden, "record not assigned to agent")
		return
	}
	rec, err := h.store.GetRecord(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到備份紀錄")
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (h *agentHandler) updateRecord(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	owns, err := h.store.AgentOwnsRecord(r.Context(), agent.ID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !owns {
		writeError(w, http.StatusForbidden, "record not assigned to agent")
		return
	}
	var rec store.BackupRecord
	if err := json.NewDecoder(r.Body).Decode(&rec); err != nil {
		writeError(w, http.StatusBadRequest, "無效的 JSON: "+err.Error())
		return
	}
	rec.ID = id
	rec.AgentID = &agent.ID
	rec.AgentName = agent.Name
	rec.RunHost = agent.HostName
	if err := h.store.UpdateRecord(r.Context(), &rec); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *agentHandler) uploadRecord(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	owns, err := h.store.AgentOwnsRecord(r.Context(), agent.ID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !owns {
		writeError(w, http.StatusForbidden, "record not assigned to agent")
		return
	}
	rec, err := h.store.GetRecord(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到備份紀錄")
		return
	}
	if rec.ProjectID == nil {
		writeError(w, http.StatusBadRequest, "record missing project")
		return
	}
	proj, err := h.store.GetProject(r.Context(), *rec.ProjectID)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到專案")
		return
	}
	projectRoot := filepath.Clean(projectNASRoot(proj))

	filename := filepath.Base(strings.TrimSpace(r.Header.Get("X-Backup-Filename")))
	if filename == "." || filename == string(filepath.Separator) || filename == "" {
		filename = filepath.Base(rec.Filename)
	}
	expected := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Backup-Sha256")))
	if expected == "" {
		writeError(w, http.StatusBadRequest, "X-Backup-Sha256 required")
		return
	}

	destPath := filepath.Clean(rec.Path)
	if destPath == "." || destPath == "" {
		destPath = filepath.Join(projectRoot, rec.Type, filename)
	} else {
		destPath = filepath.Join(filepath.Dir(destPath), filename)
	}
	if !pathWithinRoot(destPath, projectRoot) {
		writeError(w, http.StatusBadRequest, "record path escapes project directory")
		return
	}
	destDir := filepath.Dir(destPath)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("建立目標目錄失敗: %v", err))
		return
	}
	tmpPath := destPath + ".uploading"
	out, err := os.Create(tmpPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("建立上傳檔案失敗: %v", err))
		return
	}
	hash := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(out, hash), r.Body)
	closeErr := out.Close()
	actual := hex.EncodeToString(hash.Sum(nil))
	if copyErr != nil || closeErr != nil {
		os.Remove(tmpPath) //nolint
		if copyErr != nil {
			writeError(w, http.StatusInternalServerError, "寫入上傳檔案失敗: "+copyErr.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "關閉上傳檔案失敗: "+closeErr.Error())
		return
	}
	if actual != expected {
		os.Remove(tmpPath) //nolint
		writeError(w, http.StatusBadRequest, "checksum mismatch")
		return
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath) //nolint
		writeError(w, http.StatusInternalServerError, "移動上傳檔案失敗: "+err.Error())
		return
	}
	if err := h.store.UpdateRecordPath(r.Context(), id, filename, destPath, size, actual); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":       destPath,
		"filename":   filename,
		"size_bytes": size,
		"checksum":   actual,
	})
}

func (h *agentHandler) downloadRecord(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.requireAgent(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "無效的 id")
		return
	}
	owns, err := h.store.AgentOwnsRecord(r.Context(), agent.ID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !owns {
		writeError(w, http.StatusForbidden, "record not assigned to agent")
		return
	}
	rec, err := h.store.GetRecord(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到備份紀錄")
		return
	}
	if rec.ProjectID == nil {
		writeError(w, http.StatusBadRequest, "record missing project")
		return
	}
	proj, err := h.store.GetProject(r.Context(), *rec.ProjectID)
	if err != nil {
		writeError(w, http.StatusNotFound, "找不到專案")
		return
	}
	if rec.Status != "success" {
		writeError(w, http.StatusBadRequest, "record is not successful")
		return
	}
	projectRoot := filepath.Clean(projectNASRoot(proj))
	if !pathWithinRoot(rec.Path, projectRoot) {
		writeError(w, http.StatusBadRequest, "record path escapes project directory")
		return
	}
	f, err := os.Open(rec.Path)
	if err != nil {
		writeError(w, http.StatusNotFound, "備份檔不存在")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Disposition", `attachment; filename="`+strings.ReplaceAll(filepath.Base(rec.Filename), `"`, "")+`"`)
	w.Header().Set("X-Backup-Sha256", rec.Checksum)
	http.ServeContent(w, r, filepath.Base(rec.Filename), rec.CreatedAt, f)
}

func projectNASRoot(proj *store.Project) string {
	if proj.NasSubpath == "" {
		return proj.NasBase
	}
	return filepath.Join(proj.NasBase, proj.NasSubpath)
}

func pathWithinRoot(path, root string) bool {
	cleanPath := filepath.Clean(path)
	cleanRoot := filepath.Clean(root)
	return cleanPath == cleanRoot || strings.HasPrefix(cleanPath, cleanRoot+string(filepath.Separator))
}
