export type TunnelData = {
  network: "tcp" | "udp";
  transport: string;
  bind: string;
  connect: string;
};

export type Proxy = {
  proxy_id: string;
  remark: string;
  kind: "tunnel";
  enabled: boolean;
  data: TunnelData;
};

export type ClientConfig = {
  client_id: string;
  client_token: string;
  remark: string;
  enabled: boolean;
  created_at: number;
  updated_at: number;
  proxy_list: Proxy[];
};

export type ClientFile = {
  revision: number;
  client_config: ClientConfig[];
};

export type ClientView = {
  client_id: string;
  remark: string;
  enabled: boolean;
  online: boolean;
  state: string;
  session_epoch: number;
  epoch: number;
  mux_sessions: number;
  active_streams: number;
  proxy_count: number;
  created_at: number;
  updated_at: number;
  proxies: Proxy[];
};

export type ClientList = {
  revision: number;
  items: ClientView[];
  clients: ClientView[];
};

export type AdminSession = {
  user_name: string;
  csrf_token: string;
  authenticated_at: number;
  expires_at: number;
  session_ttl: "10m" | "30m" | "1h" | "24h";
};

export type AuditChange = {
  id: string;
  entity_type: "client" | "proxy";
  entity_id: string;
  operation: "create" | "update" | "delete";
  before?: Record<string, unknown>;
  after?: Record<string, unknown>;
};

export type AuditEvent = {
  id: string;
  occurred_at: number;
  category: string;
  event_type: string;
  outcome: string;
  actor_type: string;
  actor_id: string;
  source_addr: string;
  client_id: string;
  proxy_id: string;
  connection_id: string;
  summary: string;
  reason_code: string;
  reason: string;
  details?: Record<string, unknown>;
  config_changes?: AuditChange[];
};

export type Page<T> = {
  items: T[];
  page: number;
  page_size: number;
  total: number;
  total_pages: number;
};

export type NetworkConnection = {
  activity_id: string;
  protocol: "tcp" | "udp";
  state: string;
  client_id: string;
  client_remark: string;
  proxy_id: string;
  proxy_remark: string;
  external_addr: string;
  server_bind: string;
  target_addr: string;
  transport: string;
  session_epoch: number;
  accepted_at: number;
  connected_at?: number;
  last_activity_at: number;
  closed_at?: number;
  up_bytes: number;
  down_bytes: number;
  up_packets: number;
  down_packets: number;
  close_reason_code: string;
  close_reason: string;
};

export type NetworkSample = {
  activity_id: string;
  bucket_at: number;
  up_bytes_delta: number;
  down_bytes_delta: number;
  up_packets_delta: number;
  down_packets_delta: number;
};

export type NetworkDetail = NetworkConnection & {
  samples: NetworkSample[];
  samples_expired: boolean;
};

export type NetworkClient = {
  client_id: string;
  client_remark: string;
  enabled: boolean;
  online: boolean;
  deleted: boolean;
  session_epoch: number;
  mux_sessions: number;
  active_connections: number;
  historical_connections: number;
  failures: number;
  up_bytes: number;
  down_bytes: number;
  last_seen_at: number;
};

export type NetworkPoint = {
  bucket_at: number;
  connections_started: number;
  connections_failed: number;
  peak_active: number;
  up_bytes: number;
  down_bytes: number;
};

export type NetworkSummary = {
  range: string;
  client_id: string;
  active_connections: number;
  active_clients: number;
  connections: number;
  failures: number;
  failure_rate: number;
  up_bytes: number;
  down_bytes: number;
  series: NetworkPoint[];
  clients: NetworkClient[];
};

export type Summary = {
  online_clients: number;
  pending_tunnels: number;
  active_tunnels: number;
  listeners: number;
  udp_channels: number;
  udp_pending_channels: number;
  udp_flows: number;
  degraded: boolean;
};
