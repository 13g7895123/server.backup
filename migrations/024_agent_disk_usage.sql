CREATE TABLE IF NOT EXISTS agent_disk_usage (
    agent_id      INTEGER PRIMARY KEY REFERENCES agents(id) ON DELETE CASCADE,
    collected_at  TIMESTAMPTZ NOT NULL,
    partitions    JSONB NOT NULL DEFAULT '[]'::jsonb,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_agent_disk_usage_collected_at
    ON agent_disk_usage(collected_at DESC);
