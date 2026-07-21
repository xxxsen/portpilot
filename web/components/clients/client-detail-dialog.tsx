"use client";

import type { ClientView } from "@/lib/types";
import { Dialog } from "@/components/ui/dialog";

export function ClientDetailDialog({
  client,
  onClose,
}: {
  client: ClientView | null;
  onClose: () => void;
}) {
  return (
    <Dialog
      open={Boolean(client)}
      onOpenChange={(open) => { if (!open) onClose(); }}
      title={client?.remark ?? "客户端详情"}
      description={client?.client_id}
      side
    >
      {client && <div className="drawer-body">
        <section>
          <h3>客户端资产</h3>
          <dl className="detail-grid">
            <dt>客户端 ID</dt><dd className="mono">{client.client_id}</dd>
            <dt>状态</dt><dd><ClientStatus value={client.state} /></dd>
            <dt>创建时间</dt><dd>{formatClientTime(client.created_at)}</dd>
            <dt>修改时间</dt><dd>{formatClientTime(client.updated_at)}</dd>
            <dt>代理 / smux</dt><dd>{client.proxy_count} / {client.mux_sessions}</dd>
          </dl>
        </section>
        <section>
          <h3>Agent 环境</h3>
          {client.agent ? <dl className="detail-grid">
            <dt>版本</dt><dd>{client.agent.version}<small className="mono">{client.agent.commit}</small></dd>
            <dt>构建时间</dt><dd>{client.agent.build_time || "-"}</dd>
            <dt>Go 版本</dt><dd>{client.agent.go_version}</dd>
            <dt>运行平台</dt><dd>{client.agent.os} / {client.agent.arch}</dd>
            <dt>主机名</dt><dd>{client.agent.hostname || "-"}</dd>
            <dt>进程启动</dt><dd>{formatClientTime(client.agent.started_at)}</dd>
            <dt>协议版本</dt><dd>{client.agent.protocol_version}</dd>
            <dt>能力</dt><dd>{client.agent.capabilities.join("、") || "-"}</dd>
          </dl> : <div className="empty-inline">Agent 信息不可用（{client.agent_status}）</div>}
        </section>
        <section>
          <h3>控制会话</h3>
          {client.session ? <dl className="detail-grid">
            <dt>来源</dt><dd className="mono">{client.session.source_ip || client.session.source_addr}</dd>
            <dt>上线时间</dt><dd>{formatClientTime(client.session.connected_at)}</dd>
            <dt>最后心跳</dt><dd>{formatClientTime(client.session.last_seen_at)}</dd>
            <dt>控制 RTT</dt><dd>{client.session.control_rtt_millis ? `${client.session.control_rtt_millis} ms` : "-"}</dd>
            <dt>最近离线</dt><dd>{formatClientTime(client.session.last_offline_at ?? 0)}</dd>
            <dt>离线原因</dt><dd>{client.session.last_offline_reason || "-"}</dd>
          </dl> : <div className="empty-inline">尚无会话快照</div>}
        </section>
      </div>}
    </Dialog>
  );
}

export function ClientStatus({value}: {value: string}) {
  return <span className={`status status-${value.toLowerCase()}`}><i />{value}</span>;
}

export function formatClientTime(value: number) {
  return value > 0 ? new Date(value).toLocaleString() : "-";
}
