"use client";

import {useState} from "react";
import {Activity, Cable, Network, Server, Users} from "lucide-react";
import type {
  AuditEvent,
  ClientView,
  NavigationDestination,
  Summary,
} from "@/lib/types";
import {
  ClientDetailDialog,
  ClientStatus,
  formatClientTime,
} from "@/components/clients/client-detail-dialog";
import {AttentionList} from "@/components/overview/attention-list";

export function OverviewPage({
  summary,
  clients,
  events,
  onNavigate,
  onUnauthorized,
}: {
  summary: Summary | null;
  clients: ClientView[];
  events: AuditEvent[];
  onNavigate: (destination: NavigationDestination) => void;
  onUnauthorized: () => void;
}) {
  const [detailClientID, setDetailClientID] = useState(() => {
    if (typeof window === "undefined") return "";
    return new URLSearchParams(window.location.search).get("client_id") ?? "";
  });
  const detail = clients.find((client) => client.client_id === detailClientID) ?? null;
  const stats = [
    {label: "在线客户端", value: summary?.online_clients ?? 0, icon: Users},
    {label: "代理监听", value: summary?.listeners ?? 0, icon: Network},
    {label: "活动隧道", value: summary?.active_tunnels ?? 0, icon: Activity},
    {label: "等待建链", value: summary?.pending_tunnels ?? 0, icon: Server},
    {label: "UDP 通道", value: summary?.udp_channels ?? 0, icon: Cable},
    {label: "UDP Flow", value: summary?.udp_flows ?? 0, icon: Network},
  ];
  return (
    <>
      <section className={summary?.degraded ? "stat-grid degraded-summary" : "stat-grid"}>
        {stats.map((item) => <div className="stat" key={item.label}>
          <item.icon size={18} /><span>{item.label}</span><strong>{item.value}</strong>
        </div>)}
      </section>
      <AttentionList onNavigate={onNavigate} onUnauthorized={onUnauthorized} />
      <section className="section-block">
        <div className="section-heading"><div><h2>客户端状态</h2><p>控制会话与 smux 池</p></div></div>
        <div className="table-wrap"><table className="overview-client-table"><thead><tr><th>客户端</th><th>状态 / 最后心跳</th><th>Epoch</th><th>smux</th><th>活动流</th><th aria-label="操作" /></tr></thead><tbody>
          {clients.map((client) => <tr key={client.client_id} className="clickable-row overview-client-row" onClick={() => setDetailClientID(client.client_id)}>
            <td><strong>{client.remark || client.client_id}</strong><small>{client.client_id}</small></td>
            <td><ClientStatus value={client.state} /><small>心跳 {formatClientTime(client.session?.last_seen_at ?? 0)}</small></td>
            <td>{client.epoch || "-"}</td><td>{client.mux_sessions}</td><td>{client.active_streams}</td>
            <td><button
              className="icon-button"
              title="查看网络活动"
              aria-label={`查看 ${client.client_id} 网络活动`}
              onClick={(event) => {
                event.stopPropagation();
                onNavigate({
                  view: "network",
                  params: {network_view: "client", client_id: client.client_id},
                });
              }}
            ><Activity size={17} /></button></td>
          </tr>)}
          {!clients.length && <tr><td colSpan={6}><div className="empty-inline">还没有客户端</div></td></tr>}
        </tbody></table></div>
      </section>
      <section className="section-block">
        <div className="section-heading"><div><h2>最近事件</h2><p>管理与客户端生命周期审计摘要</p></div></div>
        <div className="event-list">
          {events.map((event) => <div className="event-row" key={event.id}><Activity size={16} /><div className="grow"><strong>{event.event_type}</strong><p>{event.summary || event.reason_code}</p></div><time>{new Date(event.occurred_at).toLocaleString()}</time></div>)}
          {!events.length && <div className="empty-state">暂无事件</div>}
        </div>
      </section>
      <ClientDetailDialog client={detail} onClose={() => setDetailClientID("")} />
    </>
  );
}
