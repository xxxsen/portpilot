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
  external_ip: string;
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
  up_bytes_per_second: number;
  down_bytes_per_second: number;
  rate_sampled_at: number;
  rate_window_millis: number;
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

export type AnalyticsRange = {
  requested: "1h" | "24h" | "7d" | "30d" | "90d" | "custom";
  effective_from: number;
  effective_to: number;
  resolution_seconds: number;
  timezone: "UTC";
};

export type AnalyticsScope = {
  client_id: string;
  proxy_id: string;
  protocol: string;
};

export type AnalyticsCompleteness = {
  status: "pending" | "backfilling" | "ready" | "degraded" | "unavailable";
  complete_from: number;
  statistics_available_from: number;
  statistics_retention_seconds: number;
  details_available_from: number;
  estimated_bytes: number;
  estimated_ratio: number;
  geoip_status: "disabled" | "ready" | "degraded";
  geoip_database_fingerprint: string;
};

export type AnalyticsMetadata = {
  range: AnalyticsRange;
  scope: AnalyticsScope;
  completeness: AnalyticsCompleteness;
};

export type AnalyticsPoint = {
  bucket_at: number;
  connections: number;
  failures: number;
  up_bytes: number;
  down_bytes: number;
  estimated_bytes: number;
};

export type AnalyticsCountry = {
  geo_kind: string;
  country_code: string;
  connections: number;
  unique_ips: number;
  up_bytes: number;
  down_bytes: number;
  traffic_share: number;
};

export type AnalyticsOverview = AnalyticsMetadata & {
  connections: number;
  failures: number;
  failure_rate: number;
  unique_ips: number;
  countries: number;
  up_bytes: number;
  down_bytes: number;
  estimated_bytes: number;
  series: AnalyticsPoint[];
  country_distribution: AnalyticsCountry[];
};

export type AnalyticsDimension = {
  client_id: string;
  client_remark: string;
  client_deleted: boolean;
  proxy_id: string;
  proxy_remark: string;
  proxy_deleted: boolean;
  protocol: "tcp" | "udp";
  connections: number;
  failures: number;
  unique_ips: number;
  countries: number;
  up_bytes: number;
  down_bytes: number;
  top_country_code: string;
  first_seen_at: number;
  last_seen_at: number;
};

export type AnalyticsTopIP = {
  rank: number;
  source_ip: string;
  geo_kind: string;
  country_code: string;
  connections: number;
  failures: number;
  up_bytes: number;
  down_bytes: number;
  connection_share: number;
  traffic_share: number;
  first_seen_at: number;
  last_seen_at: number;
};

export type AnalyticsTopIPs = AnalyticsMetadata & {
  by_connections: AnalyticsTopIP[];
  by_traffic: AnalyticsTopIP[];
};

export type AnalyticsIPSeries = AnalyticsMetadata & {
  source_ip: string;
  geo_kind: string;
  country_code: string;
  connections: number;
  failures: number;
  up_bytes: number;
  down_bytes: number;
  first_seen_at: number;
  last_seen_at: number;
  series: AnalyticsPoint[];
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
