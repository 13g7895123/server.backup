package api

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// ── types ─────────────────────────────────────────────────────────────────────

// DiskPartition 代表一個磁碟分割區的使用狀況
type DiskPartition struct {
	Filesystem  string  `json:"filesystem"`
	MountPoint  string  `json:"mount_point"`
	TotalBytes  int64   `json:"total_bytes"`
	UsedBytes   int64   `json:"used_bytes"`
	FreeBytes   int64   `json:"free_bytes"`
	UsedPercent float64 `json:"used_percent"`
}

// DiskUsageResponse 是 /api/disk-usage 的回應結構
type DiskUsageResponse struct {
	CollectedAt time.Time       `json:"collected_at"`
	Partitions  []DiskPartition `json:"partitions"`
}

// ── handlers ──────────────────────────────────────────────────────────────────

func RegisterDiskUsageRoute(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/disk-usage", handleDiskUsage)
	// 外部 API：無需 API key（系統資訊，僅供內部監控使用）
	mux.HandleFunc("GET /api/v1/system/disk", handleDiskUsage)
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
	partitions, err := collectDiskUsage()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "讀取磁碟狀況失敗: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, DiskUsageResponse{
		CollectedAt: time.Now(),
		Partitions:  partitions,
	})
}

// collectDiskUsage 執行 df 並解析輸出
func collectDiskUsage() ([]DiskPartition, error) {
	// -P：POSIX 格式；-B1：以 byte 為單位
	out, err := exec.Command("df", "-PB1", "--output=source,target,size,used,avail,pcent").Output() //nolint:gosec
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
