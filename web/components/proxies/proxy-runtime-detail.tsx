"use client";

import {Activity, Clock, Server} from "lucide-react";
import {Dialog} from "@/components/ui/dialog";
import type {Proxy, ProxyRuntime, TunnelData} from "@/lib/types";

export function ProxyRuntimeDetail({
  runtime,
  proxy,
  onClose,
  onOpenNetwork,
}: {
  runtime: ProxyRuntime | null;
  proxy?: Proxy | null;
  onClose: () => void;
  onOpenNetwork: (clientID: string, proxyID: string) => void;
}) {
  return <Dialog
    open={Boolean(runtime)}
    onOpenChange={(open) => {if (!open) onClose();}}
    title={runtime?.proxy_remark || runtime?.proxy_id || "代理运行详情"}
    description={runtime ? `${runtime.client_id} / ${runtime.proxy_id}` : undefined}
    side
  >
    {runtime && <div className="drawer-body proxy-runtime-drawer">
      <section className="runtime-summary">
        <Availability value={runtime.availability} />
        <strong>{reasonLabel(runtime.reason_code)}</strong>
        <span>{runtime.protocol.toUpperCase()} · {runtime.transport} → {runtime.effective_transport}</span>
      </section>
      <dl className="detail-grid">
        <dt>服务端监听</dt><dd className="mono">{runtime.bind}</dd>
        <dt>内网目标</dt><dd className="mono">{runtime.connect}</dd>
        <dt>Listener</dt><dd>{listenerLabel(runtime.listener.state)}<small>绑定 {formatTime(runtime.listener.bound_at)}</small></dd>
        <dt>最近接入</dt><dd>{formatTime(runtime.listener.last_accept_at)}</dd>
        <dt>客户端会话</dt><dd>{runtime.session.current ? runtime.session.state : "当前离线"}<small>心跳 {formatTime(runtime.session.last_seen_at)}</small></dd>
        <dt>控制 RTT</dt><dd>{runtime.session.control_rtt_millis > 0 ? `${runtime.session.control_rtt_millis} ms` : "-"}</dd>
        <dt>smux / stream</dt><dd>{runtime.session.mux_sessions} / {runtime.session.active_streams}</dd>
      </dl>
      {(runtime.listener.last_error_code || runtime.listener.last_error) && <section className="runtime-error">
        <strong>{runtime.listener.last_error_code || "listener_failed"}</strong>
        <p>{runtime.listener.last_error || "监听循环异常退出"}</p>
        <time>{formatTime(runtime.listener.failed_at)}</time>
      </section>}
      <section className="runtime-group">
        <h3>连接策略</h3>
        <dl className="detail-grid">
          <dt>允许来源</dt><dd className="mono">{policyValue(proxy?.data.access_policy?.allow_cidrs, "全部")}</dd>
          <dt>拒绝来源</dt><dd className="mono">{policyValue(proxy?.data.access_policy?.deny_cidrs, "无")}</dd>
          <dt>邮件临时授权</dt><dd>{proxy?.data.allow_knock_only ? "必须获得授权" : "不要求授权"}</dd>
          <dt>PROXY Protocol</dt><dd>{proxy?.data.enable_proxy_protocol ? "v1 已启用" : "关闭"}</dd>
          <dt>上行 / 下行限速</dt><dd>{rateLimitValue(proxy?.data.rate_limit)}</dd>
        </dl>
      </section>
      <section className="runtime-group">
        <h3><Activity size={16} />当前活动</h3>
        <div className="runtime-metrics">
          <Metric label="活动连接" value={String(runtime.realtime.active_connections)} />
          <Metric label="等待建链" value={String(runtime.realtime.pending_connections)} />
          <Metric label="上行速率" value={formatRate(runtime.realtime.up_bytes_per_second)} />
          <Metric label="下行速率" value={formatRate(runtime.realtime.down_bytes_per_second)} />
        </div>
      </section>
      <section className="runtime-group">
        <h3><Clock size={16} />24 小时观测</h3>
        {runtime.history.status === "unavailable" ? <div className="stale-banner">历史观测暂不可用</div> : <div className="runtime-metrics">
          <Metric label="连接" value={nullableNumber(runtime.history.connections)} />
          <Metric label="失败率" value={runtime.history.failure_rate === null ? "-" : `${(runtime.history.failure_rate * 100).toFixed(1)}%`} />
          <Metric label="上传" value={nullableBytes(runtime.history.up_bytes)} />
          <Metric label="下载" value={nullableBytes(runtime.history.down_bytes)} />
        </div>}
        {runtime.history.last_failure_at && <p className="runtime-last-failure">
          最近失败 {formatTime(runtime.history.last_failure_at)} · {runtime.history.last_failure_reason_code || "unknown"}
        </p>}
      </section>
      <button className="secondary-button runtime-network-link" onClick={() => onOpenNetwork(runtime.client_id, runtime.proxy_id)}>
        <Server size={17} />查看网络活动
      </button>
    </div>}
  </Dialog>;
}

export function Availability({value}: {value: ProxyRuntime["availability"]}) {
  const labels = {
    disabled: "已禁用",
    waiting_client: "等待客户端",
    degraded: "降级",
    ready: "就绪",
    error: "监听异常",
  };
  return <span className={`availability availability-${value}`}><i />{labels[value]}</span>;
}

function listenerLabel(value: string) {
  return {stopped: "已停止", listening: "监听中", error: "异常"}[value] ?? value;
}

function reasonLabel(value: string) {
  return {
    client_disabled: "客户端已禁用",
    proxy_disabled: "代理已禁用",
    listener_missing: "预期监听不存在",
    listener_failed: "监听循环异常退出",
    client_offline: "客户端当前离线",
    client_degraded: "客户端会话降级",
    client_draining: "客户端会话排空中",
    mux_unavailable: "smux 承载不可用",
    ready: "建链前置条件已满足",
  }[value] ?? value;
}

function formatTime(value: number | null) {
  return value && value > 0 ? new Date(value).toLocaleString() : "-";
}

function nullableNumber(value: number | null) {
  return value === null ? "-" : value.toLocaleString();
}

function nullableBytes(value: number | null) {
  return value === null ? "-" : formatBytes(value);
}

function policyValue(values: string[] | undefined, empty: string) {
  return values?.length ? values.join(", ") : empty;
}

function rateLimitValue(value: TunnelData["rate_limit"] | undefined) {
  return `↑ ${formatLimit(value?.up_bytes_per_second ?? 0)} / ↓ ${formatLimit(value?.down_bytes_per_second ?? 0)}`;
}

function formatLimit(value: number) {
  if (!value) return "不限";
  if (value >= 2 ** 30) return `${value / 2 ** 30} GiB/s`;
  if (value >= 2 ** 20) return `${value / 2 ** 20} MiB/s`;
  return `${value / 2 ** 10} KiB/s`;
}

function formatRate(value: number) {
  return `${formatBytes(value)}/s`;
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let amount = value / 1024;
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit += 1;
  }
  return `${amount.toFixed(amount >= 10 ? 1 : 2)} ${units[unit]}`;
}

function Metric({label, value}: {label: string; value: string}) {
  return <div><span>{label}</span><strong>{value}</strong></div>;
}
