ALTER TABLE network_connections
    ADD COLUMN up_limit_bytes_per_second INTEGER NOT NULL DEFAULT 0;

ALTER TABLE network_connections
    ADD COLUMN down_limit_bytes_per_second INTEGER NOT NULL DEFAULT 0;

ALTER TABLE network_connections
    ADD COLUMN up_throttled_millis INTEGER NOT NULL DEFAULT 0;

ALTER TABLE network_connections
    ADD COLUMN down_throttled_millis INTEGER NOT NULL DEFAULT 0;

ALTER TABLE network_connections
    ADD COLUMN up_rate_limited_packets INTEGER NOT NULL DEFAULT 0;

ALTER TABLE network_connections
    ADD COLUMN down_rate_limited_packets INTEGER NOT NULL DEFAULT 0;

ALTER TABLE network_connections
    ADD COLUMN up_rate_limited_bytes INTEGER NOT NULL DEFAULT 0;

ALTER TABLE network_connections
    ADD COLUMN down_rate_limited_bytes INTEGER NOT NULL DEFAULT 0;

ALTER TABLE network_connections
    ADD COLUMN proxy_protocol_enabled INTEGER NOT NULL DEFAULT 0;

ALTER TABLE network_connections
    ADD COLUMN proxy_protocol_version INTEGER NOT NULL DEFAULT 0;

ALTER TABLE network_connections
    ADD COLUMN proxy_protocol_header_sent INTEGER NOT NULL DEFAULT 0;

CREATE TABLE client_agent_snapshots (
    client_id TEXT PRIMARY KEY,
    agent_status TEXT NOT NULL DEFAULT 'unavailable',
    agent_version TEXT NOT NULL DEFAULT '',
    agent_commit TEXT NOT NULL DEFAULT '',
    agent_build_time TEXT NOT NULL DEFAULT '',
    go_version TEXT NOT NULL DEFAULT '',
    os TEXT NOT NULL DEFAULT '',
    arch TEXT NOT NULL DEFAULT '',
    hostname TEXT NOT NULL DEFAULT '',
    process_started_at INTEGER NOT NULL DEFAULT 0,
    protocol_version INTEGER NOT NULL DEFAULT 0,
    capabilities_json TEXT NOT NULL DEFAULT '[]',
    source_addr TEXT NOT NULL DEFAULT '',
    source_ip TEXT NOT NULL DEFAULT '',
    first_seen_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    last_online_at INTEGER NOT NULL,
    last_offline_at INTEGER,
    last_offline_reason TEXT NOT NULL DEFAULT '',
    last_session_epoch INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX idx_client_agent_snapshots_last_seen
    ON client_agent_snapshots(last_seen_at DESC);
