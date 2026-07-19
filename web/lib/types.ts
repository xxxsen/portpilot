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
  enabled: boolean;
  proxy_list: Proxy[];
};

export type ClientFile = {
  revision: number;
  client_config: ClientConfig[];
};

export type ClientView = {
  client_id: string;
  enabled: boolean;
  state: string;
  session_id?: string;
  epoch: number;
  mux_sessions: number;
  active_streams: number;
  proxies: Proxy[];
};

export type EventItem = {
  created_at: string;
  kind: string;
  client_id?: string;
  proxy_id?: string;
  connection_id?: string;
  up_bytes?: number;
  down_bytes?: number;
  reason?: string;
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

