package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// UpsertAgentDiskUsage 儲存 agent 最近一次主動上報的磁碟資訊。
func (s *Store) UpsertAgentDiskUsage(ctx context.Context, agentID int, snapshot *DiskUsageSnapshot) error {
	if snapshot == nil {
		return nil
	}
	if snapshot.CollectedAt.IsZero() {
		snapshot.CollectedAt = time.Now()
	}
	partitions, err := json.Marshal(snapshot.Partitions)
	if err != nil {
		return fmt.Errorf("marshal disk partitions: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO agent_disk_usage (agent_id, collected_at, partitions, updated_at)
		VALUES ($1, $2, $3, NOW())
		ON CONFLICT (agent_id) DO UPDATE
		SET collected_at = EXCLUDED.collected_at,
		    partitions = EXCLUDED.partitions,
		    updated_at = NOW()`,
		agentID, snapshot.CollectedAt, partitions)
	return err
}

// GetAgentDiskUsage 取得 agent 最近一次上報的磁碟資訊。
func (s *Store) GetAgentDiskUsage(ctx context.Context, agentID int) (*DiskUsageSnapshot, error) {
	var snapshot DiskUsageSnapshot
	var partitions []byte
	err := s.pool.QueryRow(ctx, `
		SELECT collected_at, partitions
		FROM agent_disk_usage
		WHERE agent_id = $1`, agentID).
		Scan(&snapshot.CollectedAt, &partitions)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(partitions, &snapshot.Partitions); err != nil {
		return nil, fmt.Errorf("unmarshal disk partitions: %w", err)
	}
	return &snapshot, nil
}
