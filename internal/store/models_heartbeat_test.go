package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAgentHeartbeatAcceptsLegacyPayloadWithoutDiskUsage(t *testing.T) {
	var heartbeat AgentHeartbeat
	err := json.Unmarshal([]byte(`{
		"host_name":"legacy-agent",
		"ip_address":"10.0.0.10",
		"version":"1.0.0",
		"last_error":""
	}`), &heartbeat)
	if err != nil {
		t.Fatalf("unmarshal legacy heartbeat: %v", err)
	}
	if heartbeat.DiskUsage != nil {
		t.Fatalf("legacy heartbeat disk_usage = %#v, want nil", heartbeat.DiskUsage)
	}
	if heartbeat.HostName != "legacy-agent" {
		t.Fatalf("host_name = %q, want legacy-agent", heartbeat.HostName)
	}
}

func TestNewAgentHeartbeatRemainsReadableByLegacyDashboard(t *testing.T) {
	type legacyHeartbeat struct {
		HostName  string `json:"host_name"`
		IPAddress string `json:"ip_address"`
		Version   string `json:"version"`
		LastError string `json:"last_error"`
	}

	payload, err := json.Marshal(AgentHeartbeat{
		HostName: "new-agent",
		Version:  "2.0.0",
		DiskUsage: &DiskUsageSnapshot{
			CollectedAt: time.Now(),
			Partitions:  []DiskPartition{{Filesystem: "/dev/sda1", MountPoint: "/"}},
		},
	})
	if err != nil {
		t.Fatalf("marshal new heartbeat: %v", err)
	}

	var legacy legacyHeartbeat
	if err := json.Unmarshal(payload, &legacy); err != nil {
		t.Fatalf("legacy dashboard decode new heartbeat: %v", err)
	}
	if legacy.HostName != "new-agent" || legacy.Version != "2.0.0" {
		t.Fatalf("legacy heartbeat = %#v", legacy)
	}
}
