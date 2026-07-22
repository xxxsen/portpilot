CREATE INDEX idx_network_connections_external_ip_accepted
    ON network_connections(external_ip, accepted_at DESC);

CREATE INDEX idx_network_connections_client_proxy_terminal
    ON network_connections(client_id, proxy_id, closed_at DESC)
    WHERE state IN ('closed', 'failed', 'interrupted');
