package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"backup-manager/internal/store"
)

// DashboardClient 代替 store.Store，讓 host agent 透過 HTTP API 讀寫資料
// 所有方法簽名與 store.Store 一致，方便 runner / scheduler 切換
type DashboardClient struct {
	base  string // e.g. http://127.0.0.1:8105
	token string // optional: shared secret header
	http  *http.Client
}

func New(baseURL, token string) *DashboardClient {
	return &DashboardClient{
		base:  baseURL,
		token: token,
		http: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func (c *DashboardClient) do(ctx context.Context, method, path string, body any, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("X-Agent-Token", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API %s %s → %d: %s", method, path, resp.StatusCode, string(b))
	}
	if out != nil && resp.StatusCode != 204 {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// ── Projects ───────────────────────────────────────────────────────────────

func (c *DashboardClient) ListProjects(ctx context.Context) ([]store.Project, error) {
	var out []store.Project
	return out, c.do(ctx, "GET", "/api/projects", nil, &out)
}

func (c *DashboardClient) GetProject(ctx context.Context, id int) (*store.Project, error) {
	var out store.Project
	return &out, c.do(ctx, "GET", fmt.Sprintf("/api/projects/%d", id), nil, &out)
}

// ── Targets ────────────────────────────────────────────────────────────────

func (c *DashboardClient) ListTargets(ctx context.Context, projectID int) ([]store.BackupTarget, error) {
	var out []store.BackupTarget
	return out, c.do(ctx, "GET", fmt.Sprintf("/api/projects/%d/targets", projectID), nil, &out)
}

// ── Schedules ──────────────────────────────────────────────────────────────

func (c *DashboardClient) ListEnabledSchedules(ctx context.Context) ([]store.Schedule, error) {
	var out []store.Schedule
	return out, c.do(ctx, "GET", "/api/agent/schedules/enabled", nil, &out)
}

func (c *DashboardClient) GetSchedule(ctx context.Context, id int) (*store.Schedule, error) {
	var out store.Schedule
	return &out, c.do(ctx, "GET", fmt.Sprintf("/api/agent/schedules/%d", id), nil, &out)
}

func (c *DashboardClient) UpdateScheduleRunTime(ctx context.Context, id int, lastRun, nextRun time.Time) error {
	return c.do(ctx, "POST", fmt.Sprintf("/api/agent/schedules/%d/runtime", id), map[string]any{
		"last_run_at": lastRun,
		"next_run_at": nextRun,
	}, nil)
}

// ── Retention ──────────────────────────────────────────────────────────────

func (c *DashboardClient) ListRetention(ctx context.Context, projectID int) ([]store.RetentionPolicy, error) {
	var out []store.RetentionPolicy
	return out, c.do(ctx, "GET", fmt.Sprintf("/api/projects/%d/retention", projectID), nil, &out)
}

// ── Records ────────────────────────────────────────────────────────────────

func (c *DashboardClient) CreateRecord(ctx context.Context, r *store.BackupRecord) (int64, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	if err := c.do(ctx, "POST", "/api/agent/records", r, &out); err != nil {
		return 0, err
	}
	return out.ID, nil
}

func (c *DashboardClient) UpdateRecord(ctx context.Context, r *store.BackupRecord) error {
	return c.do(ctx, "PUT", fmt.Sprintf("/api/agent/records/%d", r.ID), r, nil)
}

func (c *DashboardClient) ListRecords(ctx context.Context, f store.ListRecordsFilter) ([]store.BackupRecord, int64, error) {
	var out struct {
		Records []store.BackupRecord `json:"records"`
		Total   int64                `json:"total"`
	}
	path := fmt.Sprintf("/api/backups?project_id=%d&status=%s&limit=%d&offset=%d",
		f.ProjectID, f.Status, f.Limit, f.Offset)
	return out.Records, out.Total, c.do(ctx, "GET", path, nil, &out)
}

func (c *DashboardClient) DeleteRecord(ctx context.Context, id int64) (string, error) {
	var out struct {
		Path string `json:"path"`
	}
	return out.Path, c.do(ctx, "DELETE", fmt.Sprintf("/api/backups/%d", id), nil, &out)
}

// Close is a no-op (implements same iface as store.Store for future use)
func (c *DashboardClient) Close() {}
