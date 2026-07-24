import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { OverviewPage } from "@/components/overview/overview-page";
import type { AuditEvent, ClientView, Summary } from "@/lib/types";

afterEach(cleanup);

const heartbeatAt = new Date("2026-07-21T01:02:03Z").getTime();
const client: ClientView = {
  client_id: "branch-a",
  remark: "Branch A",
  enabled: true,
  online: true,
  state: "ACTIVE",
  session_epoch: 4,
  epoch: 4,
  mux_sessions: 2,
  active_streams: 3,
  proxy_count: 5,
  created_at: heartbeatAt - 10000,
  updated_at: heartbeatAt - 5000,
  proxies: [],
  agent_status: "available",
  agent: {
    version: "v1.2.3", commit: "abc123", build_time: "2026-07-20T00:00:00Z",
    go_version: "go1.26.0", os: "linux", arch: "amd64", hostname: "branch-host",
    started_at: heartbeatAt - 60000, protocol_version: 1, capabilities: ["tcp-smux"],
  },
  session: {
    source_addr: "192.0.2.10:55000", source_ip: "192.0.2.10",
    connected_at: heartbeatAt - 30000, last_seen_at: heartbeatAt,
    control_rtt_millis: 12, last_offline_reason: "",
  },
};

const recentEvent: AuditEvent = {
  id: "audit-1",
  occurred_at: heartbeatAt,
  category: "auth",
  event_type: "admin_login",
  outcome: "success",
  actor_type: "admin",
  actor_id: "operator",
  source_addr: "203.0.113.8",
  client_id: "",
  proxy_id: "",
  connection_id: "",
  summary: "管理员登录成功",
  reason_code: "",
  reason: "",
  details: {},
  config_changes: [],
};

const summary: Summary = {
  server_version: "v1.2.3",
  online_clients: 1,
  pending_tunnels: 0,
  active_tunnels: 0,
  listeners: 5,
  udp_channels: 0,
  udp_pending_channels: 0,
  udp_flows: 0,
  degraded: false,
};

describe("OverviewPage", () => {
  it("shows the last heartbeat and opens the shared client detail drawer", () => {
    render(<OverviewPage summary={null} clients={[client]} events={[]} onNavigate={vi.fn()} onUnauthorized={vi.fn()} />);
    expect(screen.getByText(`心跳 ${new Date(heartbeatAt).toLocaleString()}`)).toBeInTheDocument();

    fireEvent.click(screen.getByText("Branch A"));
    const dialog = screen.getByRole("dialog", {name: "Branch A"});
    expect(within(dialog).getByRole("heading", {name: "Agent 环境"})).toBeInTheDocument();
    expect(within(dialog).getByText("branch-host")).toBeInTheDocument();
    expect(within(dialog).getByText("最后心跳")).toBeInTheDocument();
  });

  it("shows client and server versions with match status", () => {
    const mismatched = {
      ...client,
      client_id: "branch-b",
      remark: "Branch B",
      agent: client.agent ? {...client.agent, version: "v1.2.2"} : null,
    };

    render(<OverviewPage summary={summary} clients={[client, mismatched]} events={[]} onNavigate={vi.fn()} onUnauthorized={vi.fn()} />);

    expect(screen.getByLabelText("客户端版本 v1.2.3，服务端版本 v1.2.3")).toBeInTheDocument();
    expect(screen.getByTitle("客户端版本：v1.2.3")).toHaveClass("version-match");
    expect(screen.getAllByTitle("服务端版本：v1.2.3")).toHaveLength(2);
    for (const serverVersion of screen.getAllByTitle("服务端版本：v1.2.3")) {
      expect(serverVersion).toHaveClass("version-match");
    }
    expect(screen.getByLabelText("客户端版本 v1.2.2，服务端版本 v1.2.3")).toBeInTheDocument();
    expect(screen.getByTitle("客户端版本：v1.2.2")).toHaveClass("version-mismatch");
  });

  it("opens network activity for the selected client without opening details", () => {
    const onNavigate = vi.fn();
    render(<OverviewPage summary={null} clients={[client]} events={[]} onNavigate={onNavigate} onUnauthorized={vi.fn()} />);

    fireEvent.click(screen.getByRole("button", {name: "查看 branch-a 网络活动"}));
    expect(onNavigate).toHaveBeenCalledWith({view: "network", params: {client_id: "branch-a", network_view: "client"}});
    expect(screen.queryByRole("dialog", {name: "Branch A"})).not.toBeInTheDocument();
  });

  it("shows useful recent event context on the overview", () => {
    render(<OverviewPage summary={null} clients={[client]} events={[recentEvent]} onNavigate={vi.fn()} onUnauthorized={vi.fn()} />);

    expect(screen.getByText("管理员登录")).toBeInTheDocument();
    expect(screen.getByText("管理员登录成功")).toBeInTheDocument();
    expect(screen.getByText("203.0.113.8")).toBeInTheDocument();
    expect(screen.getByText("operator")).toBeInTheDocument();
    expect(screen.getByText("成功")).toBeInTheDocument();
  });

  it("uses the shared user-facing name for knock events", () => {
    const knockEvent = {
      ...recentEvent,
      id: "audit-knock",
      category: "security",
      event_type: "knock_grant_created",
      summary: "临时访问授权已创建",
    };

    render(<OverviewPage summary={null} clients={[client]} events={[knockEvent]} onNavigate={vi.fn()} onUnauthorized={vi.fn()} />);

    expect(screen.getByText("创建临时访问授权")).toBeInTheDocument();
    expect(screen.queryByText("knock_grant_created")).not.toBeInTheDocument();
  });
});
