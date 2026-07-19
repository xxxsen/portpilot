CREATE TABLE audit_events (
    id TEXT PRIMARY KEY,
    occurred_at INTEGER NOT NULL,
    category TEXT NOT NULL,
    event_type TEXT NOT NULL,
    outcome TEXT NOT NULL,
    actor_type TEXT NOT NULL,
    actor_id TEXT NOT NULL DEFAULT '',
    source_addr TEXT NOT NULL DEFAULT '',
    client_id TEXT NOT NULL DEFAULT '',
    proxy_id TEXT NOT NULL DEFAULT '',
    connection_id TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    reason_code TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    details_json TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE audit_config_changes (
    id TEXT PRIMARY KEY,
    event_id TEXT NOT NULL REFERENCES audit_events(id) ON DELETE CASCADE,
    entity_type TEXT NOT NULL,
    entity_id TEXT NOT NULL,
    operation TEXT NOT NULL,
    before_json TEXT,
    after_json TEXT
);

CREATE TABLE network_connections (
    activity_id TEXT PRIMARY KEY,
    protocol TEXT NOT NULL,
    state TEXT NOT NULL,
    client_id TEXT NOT NULL,
    client_remark TEXT NOT NULL DEFAULT '',
    proxy_id TEXT NOT NULL,
    proxy_remark TEXT NOT NULL DEFAULT '',
    external_addr TEXT NOT NULL DEFAULT '',
    server_bind TEXT NOT NULL DEFAULT '',
    target_addr TEXT NOT NULL DEFAULT '',
    transport TEXT NOT NULL DEFAULT '',
    session_epoch INTEGER NOT NULL DEFAULT 0,
    accepted_at INTEGER NOT NULL,
    connected_at INTEGER,
    last_activity_at INTEGER NOT NULL,
    closed_at INTEGER,
    up_bytes INTEGER NOT NULL DEFAULT 0,
    down_bytes INTEGER NOT NULL DEFAULT 0,
    up_packets INTEGER NOT NULL DEFAULT 0,
    down_packets INTEGER NOT NULL DEFAULT 0,
    close_reason_code TEXT NOT NULL DEFAULT '',
    close_reason TEXT NOT NULL DEFAULT ''
);

CREATE TABLE network_samples (
    activity_id TEXT NOT NULL REFERENCES network_connections(activity_id) ON DELETE CASCADE,
    bucket_at INTEGER NOT NULL,
    up_bytes_delta INTEGER NOT NULL DEFAULT 0,
    down_bytes_delta INTEGER NOT NULL DEFAULT 0,
    up_packets_delta INTEGER NOT NULL DEFAULT 0,
    down_packets_delta INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (activity_id, bucket_at)
);

CREATE TABLE network_hourly_rollups (
    bucket_at INTEGER NOT NULL,
    scope_client_id TEXT NOT NULL DEFAULT '',
    protocol TEXT NOT NULL,
    connections_started INTEGER NOT NULL DEFAULT 0,
    connections_failed INTEGER NOT NULL DEFAULT 0,
    peak_active INTEGER NOT NULL DEFAULT 0,
    up_bytes INTEGER NOT NULL DEFAULT 0,
    down_bytes INTEGER NOT NULL DEFAULT 0,
    up_packets INTEGER NOT NULL DEFAULT 0,
    down_packets INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (bucket_at, scope_client_id, protocol)
);

CREATE INDEX idx_audit_events_occurred
    ON audit_events(occurred_at DESC, id DESC);
CREATE INDEX idx_audit_events_type
    ON audit_events(event_type, occurred_at DESC);
CREATE INDEX idx_audit_events_category_outcome
    ON audit_events(category, outcome, occurred_at DESC);
CREATE INDEX idx_audit_events_client
    ON audit_events(client_id, occurred_at DESC);
CREATE INDEX idx_audit_events_connection
    ON audit_events(connection_id);
CREATE INDEX idx_audit_config_changes_event
    ON audit_config_changes(event_id);

CREATE INDEX idx_network_connections_accepted
    ON network_connections(accepted_at DESC, activity_id DESC);
CREATE INDEX idx_network_connections_client
    ON network_connections(client_id, accepted_at DESC);
CREATE INDEX idx_network_connections_proxy
    ON network_connections(proxy_id, accepted_at DESC);
CREATE INDEX idx_network_connections_state
    ON network_connections(state, accepted_at DESC);
CREATE INDEX idx_network_connections_protocol
    ON network_connections(protocol, accepted_at DESC);
CREATE INDEX idx_network_samples_bucket
    ON network_samples(bucket_at);
CREATE INDEX idx_network_hourly_client_bucket
    ON network_hourly_rollups(scope_client_id, bucket_at);
CREATE INDEX idx_network_hourly_bucket
    ON network_hourly_rollups(bucket_at);
