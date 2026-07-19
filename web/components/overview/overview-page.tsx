"use client";

import { Activity, Cable, Network, Server, Users } from "lucide-react";
import type { AuditEvent, ClientView, Summary } from "@/lib/types";

export function OverviewPage({
  summary,
  clients,
  events,
}: {
  summary: Summary | null;
  clients: ClientView[];
  events: AuditEvent[];
}) {
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
      <section className="stat-grid">
        {stats.map((item) => <div className="stat" key={item.label}><item.icon size={18} /><span>{item.label}</span><strong>{item.value}</strong></div>)}
      </section>
      <section className="section-block">
        <div className="section-heading"><div><h2>客户端状态</h2><p>控制会话与 smux 池</p></div></div>
        <div className="table-wrap"><table><thead><tr><th>客户端</th><th>状态</th><th>Epoch</th><th>smux</th><th>活动流</th></tr></thead><tbody>
          {clients.map((client) => <tr key={client.client_id}><td><strong>{client.remark}</strong><small>{client.client_id}</small></td><td><Status value={client.state} /></td><td>{client.epoch || "-"}</td><td>{client.mux_sessions}</td><td>{client.active_streams}</td></tr>)}
        </tbody></table></div>
      </section>
      <section className="section-block">
        <div className="section-heading"><div><h2>最近事件</h2><p>管理与安全审计摘要</p></div></div>
        <div className="event-list">
          {events.map((event) => <div className="event-row" key={event.id}><Activity size={16} /><div className="grow"><strong>{event.event_type}</strong><p>{event.summary || event.reason_code}</p></div><time>{new Date(event.occurred_at).toLocaleString()}</time></div>)}
          {!events.length && <div className="empty-state">暂无事件</div>}
        </div>
      </section>
    </>
  );
}

function Status({value}: {value: string}) {
  return <span className={`status status-${value.toLowerCase()}`}><i />{value}</span>;
}
