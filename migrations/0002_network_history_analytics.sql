ALTER TABLE network_connections
    ADD COLUMN external_ip TEXT NOT NULL DEFAULT '';

ALTER TABLE network_connections
    ADD COLUMN analytics_aggregated_at INTEGER;

CREATE TABLE network_history_ip_rollups (
    resolution_seconds INTEGER NOT NULL
        CHECK (resolution_seconds IN (300, 3600)),
    bucket_at INTEGER NOT NULL,
    client_id TEXT NOT NULL,
    proxy_id TEXT NOT NULL,
    protocol TEXT NOT NULL CHECK (protocol IN ('tcp', 'udp')),
    source_ip TEXT NOT NULL,
    geo_kind TEXT NOT NULL
        CHECK (geo_kind IN ('country', 'private', 'reserved', 'unknown', 'invalid')),
    country_code TEXT NOT NULL DEFAULT '',
    geo_database_fingerprint TEXT NOT NULL DEFAULT '',
    connections INTEGER NOT NULL DEFAULT 0,
    failures INTEGER NOT NULL DEFAULT 0,
    up_bytes INTEGER NOT NULL DEFAULT 0,
    down_bytes INTEGER NOT NULL DEFAULT 0,
    up_packets INTEGER NOT NULL DEFAULT 0,
    down_packets INTEGER NOT NULL DEFAULT 0,
    estimated_up_bytes INTEGER NOT NULL DEFAULT 0,
    estimated_down_bytes INTEGER NOT NULL DEFAULT 0,
    first_seen_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    PRIMARY KEY (
        resolution_seconds,
        bucket_at,
        client_id,
        proxy_id,
        protocol,
        source_ip,
        geo_kind,
        country_code,
        geo_database_fingerprint
    )
) WITHOUT ROWID;

CREATE INDEX idx_history_ip_rollup_client_bucket
    ON network_history_ip_rollups (
        resolution_seconds, client_id, bucket_at, proxy_id
    );

CREATE INDEX idx_history_ip_rollup_ip_bucket
    ON network_history_ip_rollups (
        resolution_seconds, source_ip, bucket_at
    );

CREATE INDEX idx_history_ip_rollup_bucket
    ON network_history_ip_rollups (
        resolution_seconds, bucket_at
    );

CREATE TABLE network_history_proxy_dimensions (
    client_id TEXT NOT NULL,
    proxy_id TEXT NOT NULL,
    client_remark TEXT NOT NULL DEFAULT '',
    proxy_remark TEXT NOT NULL DEFAULT '',
    first_seen_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    PRIMARY KEY (client_id, proxy_id)
) WITHOUT ROWID;

CREATE TABLE network_analytics_meta (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    status TEXT NOT NULL
        CHECK (status IN ('pending', 'backfilling', 'ready', 'degraded')),
    complete_from INTEGER,
    processed_connections INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL
);

CREATE INDEX idx_network_connections_analytics_pending
    ON network_connections (analytics_aggregated_at, accepted_at DESC)
    WHERE state IN ('closed', 'failed', 'interrupted');
