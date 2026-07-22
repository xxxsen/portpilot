export type TunnelData = {
  network: "tcp" | "udp";
  transport: string;
  bind: string;
  connect: string;
  enable_proxy_protocol?: boolean;
  access_policy?: {
    allow_cidrs: string[];
    deny_cidrs: string[];
  };
  rate_limit?: {
    up_bytes_per_second: number;
    down_bytes_per_second: number;
  };
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
  agent: ClientAgent | null;
  agent_status: "available" | "legacy" | "invalid" | "unavailable";
  session: ClientSessionView | null;
};

export type ClientAgent = {
  version: string;
  commit: string;
  build_time: string;
  go_version: string;
  os: string;
  arch: string;
  hostname: string;
  started_at: number;
  protocol_version: number;
  capabilities: string[];
};

export type ClientSessionView = {
  source_addr: string;
  source_ip: string;
  connected_at: number;
  last_seen_at: number;
  control_rtt_millis: number;
  last_offline_at?: number;
  last_offline_reason: string;
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
  up_limit_bytes_per_second: number;
  down_limit_bytes_per_second: number;
  up_throttled_millis: number;
  down_throttled_millis: number;
  up_rate_limited_packets: number;
  down_rate_limited_packets: number;
  up_rate_limited_bytes: number;
  down_rate_limited_bytes: number;
  proxy_protocol_enabled: boolean;
  proxy_protocol_version: number;
  proxy_protocol_header_sent: boolean;
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

export type ProxyListenerStatus = {
  state: "stopped" | "listening" | "error";
  bound_at: number;
  last_accept_at: number;
  failed_at: number;
  last_error_code: string;
  last_error: string;
};

export type ProxySessionStatus = {
  current: boolean;
  state: string;
  last_seen_at: number;
  control_rtt_millis: number;
  mux_sessions: number;
  active_streams: number;
};

export type ProxyRuntime = {
  client_id: string;
  client_remark: string;
  proxy_id: string;
  proxy_remark: string;
  enabled: boolean;
  protocol: "tcp" | "udp";
  transport: string;
  effective_transport: string;
  bind: string;
  connect: string;
  availability: "disabled" | "waiting_client" | "degraded" | "ready" | "error";
  reason_code: string;
  listener: ProxyListenerStatus;
  session: ProxySessionStatus;
  realtime: {
    active_connections: number;
    pending_connections: number;
    up_bytes_per_second: number;
    down_bytes_per_second: number;
  };
  history: {
    status: "ready" | "unavailable";
    range: "24h";
    connections: number | null;
    failures: number | null;
    failure_rate: number | null;
    up_bytes: number | null;
    down_bytes: number | null;
    last_activity_at: number | null;
    last_failure_at: number | null;
    last_failure_reason_code: string;
  };
};

export type ProxyRuntimePage = Page<ProxyRuntime> & {
  revision: number;
  snapshot_at: number;
};

export type NavigationDestination = {
  view: "overview" | "clients" | "proxies" | "network" | "audit";
  params: Record<string, string>;
};

export type AttentionItem = {
  id: string;
  severity: "critical" | "warning" | "info";
  kind: string;
  title: string;
  summary: string;
  first_observed_at: number;
  updated_at: number;
  client_id: string;
  proxy_id: string;
  count: number;
  destination: NavigationDestination;
};

export type AttentionResponse = {
  snapshot_at: number;
  items: AttentionItem[];
  total: number;
  counts: {critical: number; warning: number; info: number};
};

export type SearchItem = {
  type: "client" | "proxy" | "source_ip" | "activity";
  id: string;
  title: string;
  subtitle: string;
  score: number;
  destination: NavigationDestination;
};

export type SearchResponse = {
  query: string;
  items: SearchItem[];
  partial: boolean;
  unavailable_sources: string[];
};

export type MetricChange = {
  current: number;
  previous: number;
  absolute_change: number;
  relative_change: number | null;
  state: "unchanged" | "new" | "inactive" | "increased" | "decreased";
  estimated: boolean;
};

export type ComparisonMetrics = {
  connections: MetricChange;
  failures: MetricChange;
  failure_rate: MetricChange;
  unique_ips: MetricChange;
  up_bytes: MetricChange;
  down_bytes: MetricChange;
  total_bytes: MetricChange;
};

export type AlignedAnalyticsPoint = AnalyticsPoint & {
  aligned_bucket_at: number;
};

export type RankChange = {
  source_ip: string;
  geo_kind: string;
  country_code: string;
  current_rank: number | null;
  previous_rank: number | null;
  rank_change: number | null;
  status: "new" | "up" | "down" | "same" | "departed";
};

export type ProxyChangeSignal = {
  client_id: string;
  proxy_id: string;
  protocol: string;
  signals: string[];
  current_connections: number;
  previous_connections: number;
  current_failure_rate: number;
  previous_failure_rate: number;
  current_traffic: number;
  previous_traffic: number;
};

export type ComparisonSource = AnalyticsTopIP;

export type AnalyticsComparison = {
  current: AnalyticsMetadata;
  previous: AnalyticsMetadata;
  comparable: boolean;
  reason: string;
  metrics: ComparisonMetrics;
  current_series: AnalyticsPoint[];
  previous_series: AlignedAnalyticsPoint[];
  new_source_count: number;
  new_sources: ComparisonSource[];
  new_countries: string[];
  connections_rank_changes: RankChange[];
  traffic_rank_changes: RankChange[];
  proxy_changes: ProxyChangeSignal[];
};
