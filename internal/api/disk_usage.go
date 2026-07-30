package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"backup-manager/internal/store"

	"github.com/jackc/pgx/v5"
)

// ── types ─────────────────────────────────────────────────────────────────────

// DiskPartition 代表一個磁碟分割區的使用狀況
type DiskPartition = store.DiskPartition

// DiskUsageResponse 是 /api/disk-usage 的回應結構
type DiskUsageResponse = store.DiskUsageSnapshot

type projectDiskUsageResponse struct {
	store.DiskUsageSnapshot
	ExecutorType string `json:"executor_type"`
	AgentID      *int   `json:"agent_id,omitempty"`
	AgentName    string `json:"agent_name,omitempty"`
	Source       string `json:"source"`
}

// ── handlers ──────────────────────────────────────────────────────────────────

func RegisterDiskUsageRoute(mux *http.ServeMux, s *store.Store) {
	mux.HandleFunc("GET /api/disk-usage", handleDiskUsage)
	mux.HandleFunc("GET /api/projects/{id}/disk-usage", projectDiskUsageHandler(s))
	mux.HandleFunc("GET /api/agents/{id}/disk-usage", agentDiskUsageHandler(s))
	// 外部 API：需要 sys_ 前綴的系統 API Key
	mux.HandleFunc("GET /api/v1/system/disk", systemKeyAuth(s, handleDiskUsage))
}

// HandleDiskUsageDirect 供 agent GET /disk-usage 路由使用（在 host 上執行）
func HandleDiskUsageDirect(w http.ResponseWriter, r *http.Request) {
	diskUsageCore(w, r)
}

// handleDiskUsage：若有 AGENT_URL 則 proxy 給 agent，否則本機執行
func handleDiskUsage(w http.ResponseWriter, r *http.Request) {
	if agentURL := os.Getenv("AGENT_URL"); agentURL != "" {
		proxyDiskUsageToAgent(w, r, agentURL)
		return
	}
	diskUsageCore(w, r)
}

func proxyDiskUsageToAgent(w http.ResponseWriter, r *http.Request, agentURL string) {
	target := agentURL + "/disk-usage"
	req, err := http.NewRequestWithContext(r.Context(), "GET", target, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "建立 proxy 請求失敗: "+err.Error())
		return
	}
	if token := os.Getenv("AGENT_TOKEN"); token != "" {
		req.Header.Set("X-Agent-Token", token)
	}
	if code := os.Getenv("AGENT_CODE"); code != "" {
		req.Header.Set("X-Agent-Code", code)
	}
	cli := &http.Client{Timeout: 10 * time.Second}
	resp, err := cli.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "agent 無回應: "+err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body) //nolint:errcheck
}

func diskUsageCore(w http.ResponseWriter, r *http.Request) {
	snapshot, err := CollectDiskUsageSnapshot()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "讀取磁碟狀況失敗: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

// CollectDiskUsageSnapshot 收集 host 磁碟資訊，供 agent heartbeat 與 HTTP handler 共用。
func CollectDiskUsageSnapshot() (*store.DiskUsageSnapshot, error) {
	partitions, err := collectDiskUsage()
	if err != nil {
		return nil, err
	}
	return &store.DiskUsageSnapshot{
		CollectedAt: time.Now(),
		Partitions:  partitions,
	}, nil
}

func projectDiskUsageHandler(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		projectID, err := pathID(r, "id")
		if err != nil {
			writeError(w, http.StatusBadRequest, "無效的 project id")
			return
		}
		project, err := s.GetProject(r.Context(), projectID)
		if err != nil {
			writeError(w, http.StatusNotFound, "找不到專案")
			return
		}
		if project.ExecutorType != "agent" {
			snapshot, err := CollectDiskUsageSnapshot()
			if err != nil {
				writeError(w, http.StatusInternalServerError, "讀取磁碟狀況失敗: "+err.Error())
				return
			}
			writeJSON(w, http.StatusOK, projectDiskUsageResponse{
				DiskUsageSnapshot: *snapshot,
				ExecutorType:      "local",
				Source:            "dashboard",
			})
			return
		}
		if project.ExecutorAgentID == nil {
			writeError(w, http.StatusConflict, "專案未指派 agent")
			return
		}
		agent, err := s.GetAgent(r.Context(), *project.ExecutorAgentID)
		if err != nil {
			writeError(w, http.StatusNotFound, "找不到專案指派的 agent")
			return
		}
		writeAgentDiskUsage(w, r, s, agent)
	}
}

func agentDiskUsageHandler(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		agentID, err := pathID(r, "id")
		if err != nil {
			writeError(w, http.StatusBadRequest, "無效的 agent id")
			return
		}
		agent, err := s.GetAgent(r.Context(), agentID)
		if err != nil {
			writeError(w, http.StatusNotFound, "找不到 agent")
			return
		}
		writeAgentDiskUsage(w, r, s, agent)
	}
}

func writeAgentDiskUsage(w http.ResponseWriter, r *http.Request, s *store.Store, agent *store.Agent) {
	snapshot, err := s.GetAgentDiskUsage(r.Context(), agent.ID)
	if err == nil {
		writeJSON(w, http.StatusOK, projectDiskUsageResponse{
			DiskUsageSnapshot: *snapshot,
			ExecutorType:      "agent",
			AgentID:           &agent.ID,
			AgentName:         agent.Name,
			Source:            "heartbeat",
		})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// 舊 agent 不會在 heartbeat 上報磁碟資訊；若 dashboard 可直接連入，
	// 使用既有 /disk-usage endpoint 作為向後相容 fallback。
	snapshot, err = fetchAgentDiskUsage(r.Context(), agent)
	if err != nil {
		writeError(w, http.StatusBadGateway,
			"尚未收到 agent 磁碟資訊，且無法直接連線；請升級 agent 或確認 base_url: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, projectDiskUsageResponse{
		DiskUsageSnapshot: *snapshot,
		ExecutorType:      "agent",
		AgentID:           &agent.ID,
		AgentName:         agent.Name,
		Source:            "agent-direct",
	})
}

func fetchAgentDiskUsage(ctx context.Context, agent *store.Agent) (*store.DiskUsageSnapshot, error) {
	if agent == nil || strings.TrimSpace(agent.BaseURL) == "" {
		return nil, fmt.Errorf("agent base_url 未設定")
	}
	target := strings.TrimRight(agent.BaseURL, "/") + "/disk-usage"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if agent.Code != "" {
		req.Header.Set("X-Agent-Code", agent.Code)
	}
	if agent.TokenHash != "" {
		req.Header.Set("X-Agent-Token", agent.TokenHash)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("agent 回應 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var snapshot store.DiskUsageSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("解析 agent 回應失敗: %w", err)
	}
	return &snapshot, nil
}

// collectDiskUsage 執行 df 並解析輸出
func collectDiskUsage() ([]DiskPartition, error) {
	// --output：固定欄位順序；-B1：以 byte 為單位。
	// GNU df 的 -P 與 --output 互斥，因此只在 fallback 使用 -P。
	out, err := exec.Command("df", "-B1", "--output=source,target,size,used,avail,pcent").Output() //nolint:gosec
	if err != nil {
		// fallback：某些舊版 df 不支援 --output
		out, err = exec.Command("df", "-PB1").Output() //nolint:gosec
		if err != nil {
			return nil, fmt.Errorf("df 執行失敗: %w", err)
		}
		return parseDfClassic(out), nil
	}
	return parseDfOutput(out), nil
}

// parseDfOutput 解析帶 --output= 的 df 輸出
func parseDfOutput(raw []byte) []DiskPartition {
	var result []DiskPartition
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for i, line := range lines {
		if i == 0 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		fs := fields[0]
		mount := fields[1]
		if shouldSkipFS(fs, mount) {
			continue
		}
		total, _ := strconv.ParseInt(fields[2], 10, 64)
		used, _ := strconv.ParseInt(fields[3], 10, 64)
		free, _ := strconv.ParseInt(fields[4], 10, 64)
		pct, _ := strconv.ParseFloat(strings.TrimSuffix(fields[5], "%"), 64)
		result = append(result, DiskPartition{
			Filesystem:  fs,
			MountPoint:  mount,
			TotalBytes:  total,
			UsedBytes:   used,
			FreeBytes:   free,
			UsedPercent: pct,
		})
	}
	return result
}

// parseDfClassic 解析標準 df -P 輸出（無 --output）
// 欄位順序：Filesystem 1K-blocks Used Available Use% Mounted
func parseDfClassic(raw []byte) []DiskPartition {
	var result []DiskPartition
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	for i, line := range lines {
		if i == 0 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		fs := fields[0]
		mount := fields[5]
		if shouldSkipFS(fs, mount) {
			continue
		}
		total, _ := strconv.ParseInt(fields[1], 10, 64)
		used, _ := strconv.ParseInt(fields[2], 10, 64)
		free, _ := strconv.ParseInt(fields[3], 10, 64)
		pct, _ := strconv.ParseFloat(strings.TrimSuffix(fields[4], "%"), 64)
		result = append(result, DiskPartition{
			Filesystem:  fs,
			MountPoint:  mount,
			TotalBytes:  total,
			UsedBytes:   used,
			FreeBytes:   free,
			UsedPercent: pct,
		})
	}
	return result
}

func shouldSkipFS(fs, mount string) bool {
	skipFS := []string{"tmpfs", "devtmpfs", "overlay", "shm", "cgroup", "proc", "sysfs", "udev"}
	fsl := strings.ToLower(fs)
	for _, s := range skipFS {
		if strings.HasPrefix(fsl, s) {
			return true
		}
	}
	if strings.HasPrefix(mount, "/proc") || strings.HasPrefix(mount, "/sys") ||
		strings.HasPrefix(mount, "/dev") || strings.HasPrefix(mount, "/run/user") {
		return true
	}
	return false
}
