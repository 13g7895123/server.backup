package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"backup-manager/internal/store"
)

func TestFetchAgentDiskUsageUsesAgentCredentials(t *testing.T) {
	collectedAt := time.Date(2026, 7, 30, 12, 0, 0, 0, time.FixedZone("Asia/Taipei", 8*60*60))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/disk-usage" {
			t.Errorf("path = %q, want /disk-usage", r.URL.Path)
		}
		if got := r.Header.Get("X-Agent-Code"); got != "agent-1" {
			t.Errorf("X-Agent-Code = %q, want agent-1", got)
		}
		if got := r.Header.Get("X-Agent-Token"); got != "secret" {
			t.Errorf("X-Agent-Token = %q, want secret", got)
		}
		_ = json.NewEncoder(w).Encode(store.DiskUsageSnapshot{
			CollectedAt: collectedAt,
			Partitions: []store.DiskPartition{{
				Filesystem:  "/dev/sda1",
				MountPoint:  "/",
				TotalBytes:  100,
				UsedBytes:   60,
				FreeBytes:   40,
				UsedPercent: 60,
			}},
		})
	}))
	defer server.Close()

	snapshot, err := fetchAgentDiskUsage(context.Background(), &store.Agent{
		BaseURL:   server.URL + "/",
		Code:      "agent-1",
		TokenHash: "secret",
	})
	if err != nil {
		t.Fatalf("fetchAgentDiskUsage: %v", err)
	}
	if len(snapshot.Partitions) != 1 || snapshot.Partitions[0].MountPoint != "/" {
		t.Fatalf("partitions = %#v", snapshot.Partitions)
	}
	if !snapshot.CollectedAt.Equal(collectedAt) {
		t.Fatalf("collected_at = %v, want %v", snapshot.CollectedAt, collectedAt)
	}
}
