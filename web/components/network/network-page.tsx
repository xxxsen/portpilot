"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ChevronLeft, ChevronRight, RefreshCw, Search } from "lucide-react";
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { api } from "@/lib/api";
import type {
  NetworkClient,
  NetworkConnection,
  NetworkDetail,
  NetworkSummary,
  Page,
} from "@/lib/types";
import { Dialog } from "@/components/ui/dialog";

type Range = "1h" | "24h" | "7d" | "30d";
const ranges: Range[] = ["1h", "24h", "7d", "30d"];
const protocols = ["", "tcp", "udp"];
const states = ["", "active", "closed", "failed", "interrupted"];
const sorts = ["accepted_desc", "accepted_asc", "traffic_desc", "duration_desc"];

export function NetworkPage({onUnauthorized}: {onUnauthorized: () => void}) {
  const initial = useMemo(() => typeof window === "undefined" ? new URLSearchParams() : new URLSearchParams(window.location.search), []);
  const [view, setView] = useState<"overall" | "client">(initial.get("network_view") === "client" ? "client" : "overall");
  const initialRange = initial.get("range") ?? "";
  const initialPage = Number(initial.get("page"));
  const [range, setRange] = useState<Range>(ranges.includes(initialRange as Range) ? initialRange as Range : "24h");
  const [clientID, setClientID] = useState(initial.get("client_id") ?? "");
  const [protocol, setProtocol] = useState(protocols.includes(initial.get("protocol") ?? "") ? initial.get("protocol") ?? "" : "");
  const [state, setState] = useState(states.includes(initial.get("state") ?? "") ? initial.get("state") ?? "" : "");
  const [sort, setSort] = useState(sorts.includes(initial.get("sort") ?? "") ? initial.get("sort") ?? "accepted_desc" : "accepted_desc");
  const [query, setQuery] = useState((initial.get("query") ?? "").slice(0, 128));
  const [page, setPage] = useState(Number.isInteger(initialPage) && initialPage > 0 ? initialPage : 1);
  const [summary, setSummary] = useState<NetworkSummary | null>(null);
  const [connections, setConnections] = useState<Page<NetworkConnection>>({items: [], page: 1, page_size: 25, total: 0, total_pages: 0});
  const [clients, setClients] = useState<NetworkClient[]>([]);
  const [detail, setDetail] = useState<NetworkDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [stale, setStale] = useState(false);
  const requestSequence = useRef(0);
  const activeRequest = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    activeRequest.current?.abort();
    const controller = new AbortController();
    activeRequest.current = controller;
    const sequence = ++requestSequence.current;
    setLoading(true);
    const selectedClient = view === "client" ? clientID : "";
    const summaryParams = new URLSearchParams({range});
    if (selectedClient) summaryParams.set("client_id", selectedClient);
    const connectionParams = new URLSearchParams({page: String(page), page_size: "25", sort});
    if (selectedClient) connectionParams.set("client_id", selectedClient);
    if (protocol) connectionParams.set("protocol", protocol);
    if (state) connectionParams.set("state", state);
    if (query) connectionParams.set("query", query);
    try {
      const [summaryResult, connectionResult, clientResult] = await Promise.all([
        api<NetworkSummary>(`/api/network/summary?${summaryParams}`, {signal: controller.signal}),
        api<Page<NetworkConnection>>(`/api/network/connections?${connectionParams}`, {signal: controller.signal}),
        api<Page<NetworkClient>>("/api/network/clients?page=1&page_size=100", {signal: controller.signal}),
      ]);
      if (sequence !== requestSequence.current) return;
      setSummary(summaryResult);
      setConnections(connectionResult);
      setClients(clientResult.items);
      setStale(false);
    } catch (caught) {
      if (controller.signal.aborted || sequence !== requestSequence.current) return;
      if (caught instanceof Error && "status" in caught && caught.status === 401) onUnauthorized();
      setStale(true);
    } finally {
      if (sequence === requestSequence.current) {
        setLoading(false);
        if (activeRequest.current === controller) activeRequest.current = null;
      }
    }
  }, [clientID, onUnauthorized, page, protocol, query, range, sort, state, view]);

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    params.set("network_view", view);
    params.set("range", range);
    if (view === "client" && clientID) params.set("client_id", clientID);
    else params.delete("client_id");
    setOptionalParam(params, "protocol", protocol);
    setOptionalParam(params, "state", state);
    setOptionalParam(params, "sort", sort === "accepted_desc" ? "" : sort);
    setOptionalParam(params, "query", query);
    setOptionalParam(params, "page", page === 1 ? "" : String(page));
    window.history.replaceState(null, "", `${window.location.pathname}?${params}`);
  }, [clientID, page, protocol, query, range, sort, state, view]);

  useEffect(() => {
    const initialTimer = window.setTimeout(() => void load(), 0);
    let timer: number | undefined;
    const schedule = () => {
      if (document.visibilityState === "visible") {
        timer = window.setInterval(() => void load(), 5000);
      }
    };
    const visibility = () => {
      if (timer) window.clearInterval(timer);
      timer = undefined;
      if (document.visibilityState === "visible") {
        void load();
        schedule();
      }
    };
    schedule();
    document.addEventListener("visibilitychange", visibility);
    return () => {
      window.clearTimeout(initialTimer);
      activeRequest.current?.abort();
      if (timer) window.clearInterval(timer);
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [load]);

  const selectedClient = clients.find((client) => client.client_id === clientID);
  return (
    <section className="section-block first">
      <div className="section-heading network-heading">
        <div className="segmented" aria-label="网络活动视图">
          <button className={view === "overall" ? "selected" : ""} onClick={() => {setView("overall"); setPage(1);}}>整体</button>
          <button className={view === "client" ? "selected" : ""} onClick={() => {setView("client"); setPage(1);}}>客户端</button>
        </div>
        <div className="segmented compact" aria-label="时间范围">
          {ranges.map((value) => <button key={value} className={range === value ? "selected" : ""} onClick={() => setRange(value)}>{value}</button>)}
        </div>
        <button className="icon-button" onClick={() => void load()} title="刷新" aria-label="刷新网络活动"><RefreshCw size={17} /></button>
      </div>
      {view === "client" && (
        <div className="client-selector">
          <label>查看客户端
            <select value={clientID} onChange={(event) => {setClientID(event.target.value); setPage(1);}}>
              <option value="">选择客户端</option>
              {clients.map((client) => <option key={client.client_id} value={client.client_id}>{client.client_remark} · {client.client_id}{client.deleted ? "（已删除）" : ""}</option>)}
            </select>
          </label>
          {selectedClient && <div className="client-context"><strong>{selectedClient.client_remark}</strong><span className="mono">{selectedClient.client_id}</span><span>{selectedClient.online ? "在线" : "离线"} · epoch {selectedClient.session_epoch || "-"} · smux {selectedClient.mux_sessions}</span></div>}
        </div>
      )}
      {stale && <div className="stale-banner">网络活动刷新失败，当前数据可能已过期</div>}
      <div className="metric-strip">
        <Metric label="活跃连接" value={summary?.active_connections ?? 0} />
        <Metric label="活跃客户端" value={summary?.active_clients ?? 0} />
        <Metric label="区间连接" value={summary?.connections ?? 0} />
        <Metric label="失败率" value={`${((summary?.failure_rate ?? 0) * 100).toFixed(1)}%`} />
        <Metric label="上传" value={formatBytes(summary?.up_bytes ?? 0)} />
        <Metric label="下载" value={formatBytes(summary?.down_bytes ?? 0)} />
      </div>
      <section className="chart-section" aria-label="网络流量趋势">
        <div className="section-heading"><div><h2>流量趋势</h2><p>实线上传，虚线下载；连接峰值见文本摘要</p></div></div>
        <div className="chart-frame">
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={summary?.series ?? []}>
              <CartesianGrid stroke="#e4e9eb" vertical={false} />
              <XAxis dataKey="bucket_at" tickFormatter={(value) => new Date(value).toLocaleTimeString([], {hour: "2-digit", minute: "2-digit"})} minTickGap={24} />
              <YAxis tickFormatter={(value) => formatBytes(value)} width={72} />
              <Tooltip labelFormatter={(value) => new Date(Number(value)).toLocaleString()} formatter={(value) => formatBytes(Number(value))} />
              <Line type="monotone" dataKey="up_bytes" name="上传" stroke="#087f75" strokeWidth={2} dot={false} />
              <Line type="monotone" dataKey="down_bytes" name="下载" stroke="#b35f20" strokeWidth={2} strokeDasharray="6 4" dot={false} />
            </LineChart>
          </ResponsiveContainer>
        </div>
        <p className="chart-summary">本区间上传 {formatBytes(summary?.up_bytes ?? 0)}，下载 {formatBytes(summary?.down_bytes ?? 0)}，失败 {summary?.failures ?? 0} 条。</p>
      </section>
      {view === "overall" && (
        <section className="section-block">
          <div className="section-heading"><div><h2>客户端汇总</h2><p>当前状态与保留期内历史</p></div></div>
          <div className="table-wrap"><table className="network-client-table responsive-table"><thead><tr><th>客户端</th><th>状态</th><th>活跃</th><th>历史</th><th>失败</th><th>上传</th><th>下载</th></tr></thead><tbody>
            {(summary?.clients ?? []).map((client) => <tr key={client.client_id} className="clickable-row" onClick={() => {setClientID(client.client_id); setView("client");}}>
              <td><strong>{client.client_remark}</strong><small>{client.client_id}{client.deleted ? " · 已删除" : ""}</small></td><td>{client.online ? "在线" : "离线"}</td><td>{client.active_connections}</td><td>{client.historical_connections}</td><td>{client.failures}</td><td>{formatBytes(client.up_bytes)}</td><td>{formatBytes(client.down_bytes)}</td>
            </tr>)}
          </tbody></table></div>
        </section>
      )}
      <section className="section-block">
        <div className="section-heading"><div><h2>连接</h2><p>实时 registry 与 SQLite 历史合并结果</p></div></div>
        <div className="filter-bar network-filters">
          <label className="search-control"><Search size={16} /><input aria-label="搜索连接" maxLength={128} value={query} onChange={(event) => {setQuery(event.target.value); setPage(1);}} placeholder="活动、客户端、代理或地址" /></label>
          <select aria-label="连接协议" value={protocol} onChange={(event) => {setProtocol(event.target.value); setPage(1);}}><option value="">全部协议</option><option value="tcp">TCP</option><option value="udp">UDP</option></select>
          <select aria-label="连接状态" value={state} onChange={(event) => {setState(event.target.value); setPage(1);}}><option value="">全部状态</option><option value="active">活跃</option><option value="closed">已关闭</option><option value="failed">失败</option><option value="interrupted">已中断</option></select>
          <select aria-label="连接排序" value={sort} onChange={(event) => setSort(event.target.value)}><option value="accepted_desc">最新优先</option><option value="accepted_asc">最早优先</option><option value="traffic_desc">流量优先</option><option value="duration_desc">时长优先</option></select>
        </div>
        <div className="table-wrap"><table className="network-connections-table responsive-table"><thead><tr><th>时间</th><th>客户端 / 代理</th><th>协议</th><th>来源 → 目标</th><th>状态</th><th>上传</th><th>下载</th><th className="mobile-table-cell">总流量</th></tr></thead><tbody>
          {connections.items.map((connection) => <tr key={connection.activity_id} className="clickable-row" onClick={() => void api<NetworkDetail>(`/api/network/connections/${connection.activity_id}`).then(setDetail)}>
            <td>{new Date(connection.accepted_at).toLocaleString()}</td><td><strong>{connection.client_remark}</strong><small>{connection.proxy_remark} · {connection.proxy_id}</small></td><td><span className={`network-tag ${connection.protocol}`}>{connection.protocol.toUpperCase()}</span></td><td className="mono address-cell">{connection.external_addr}<br />{connection.target_addr}</td><td>{connection.state}</td><td>{formatBytes(connection.up_bytes)}</td><td>{formatBytes(connection.down_bytes)}</td><td className="mobile-table-cell">{formatBytes(connection.up_bytes + connection.down_bytes)}</td>
          </tr>)}
          {!loading && !connections.items.length && <tr><td colSpan={8}><div className="empty-inline">没有符合条件的连接</div></td></tr>}
        </tbody></table></div>
        <div className="pagination"><span>共 {connections.total} 条</span><button className="icon-button" disabled={page <= 1} onClick={() => setPage(page - 1)} aria-label="上一页"><ChevronLeft size={17} /></button><span>{page} / {Math.max(connections.total_pages, 1)}</span><button className="icon-button" disabled={page >= connections.total_pages} onClick={() => setPage(page + 1)} aria-label="下一页"><ChevronRight size={17} /></button></div>
      </section>
      <Dialog open={Boolean(detail)} onOpenChange={(open) => {if (!open) setDetail(null);}} title="连接详情" description={detail?.activity_id} side>
        {detail && <NetworkDetailView detail={detail} />}
      </Dialog>
    </section>
  );
}

function NetworkDetailView({detail}: {detail: NetworkDetail}) {
  const duration = (detail.closed_at ?? detail.last_activity_at) - detail.accepted_at;
  return <div className="drawer-body">
    <dl className="detail-grid">
      <dt>协议 / 状态</dt><dd>{detail.protocol.toUpperCase()} · {detail.state}</dd>
      <dt>客户端</dt><dd>{detail.client_remark}<small>{detail.client_id}</small></dd>
      <dt>代理</dt><dd>{detail.proxy_remark}<small>{detail.proxy_id}</small></dd>
      <dt>外部来源</dt><dd className="mono">{detail.external_addr}</dd>
      <dt>服务端监听</dt><dd className="mono">{detail.server_bind}</dd>
      <dt>内网目标</dt><dd className="mono">{detail.target_addr}</dd>
      <dt>承载 / epoch</dt><dd>{detail.transport} · {detail.session_epoch}</dd>
      <dt>接受</dt><dd>{new Date(detail.accepted_at).toLocaleString()}</dd>
      <dt>建链</dt><dd>{detail.connected_at ? new Date(detail.connected_at).toLocaleString() : "-"}</dd>
      <dt>断链</dt><dd>{detail.closed_at ? new Date(detail.closed_at).toLocaleString() : "-"}</dd>
      <dt>总时长</dt><dd>{formatDuration(duration)}</dd>
      <dt>上传 / 下载</dt><dd>{formatBytes(detail.up_bytes)} / {formatBytes(detail.down_bytes)}</dd>
      <dt>UDP 包</dt><dd>{detail.up_packets} / {detail.down_packets}</dd>
      <dt>关闭原因</dt><dd>{detail.close_reason || "-"}<small>{detail.close_reason_code}</small></dd>
    </dl>
    {detail.samples?.length > 0 && (
      <section className="detail-traffic" aria-label="连接流量序列">
        <h3>流量序列</h3>
        <div className="detail-chart">
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={detail.samples ?? []}>
              <CartesianGrid stroke="#e4e9eb" vertical={false} />
              <XAxis dataKey="bucket_at" tickFormatter={(value) => new Date(value).toLocaleTimeString([], {hour: "2-digit", minute: "2-digit"})} minTickGap={24} />
              <YAxis tickFormatter={(value) => formatBytes(value)} width={68} />
              <Tooltip labelFormatter={(value) => new Date(Number(value)).toLocaleString()} formatter={(value) => formatBytes(Number(value))} />
              <Line type="monotone" dataKey="up_bytes_delta" name="上传" stroke="#087f75" strokeWidth={2} dot={false} />
              <Line type="monotone" dataKey="down_bytes_delta" name="下载" stroke="#b35f20" strokeWidth={2} strokeDasharray="6 4" dot={false} />
            </LineChart>
          </ResponsiveContainer>
        </div>
      </section>
    )}
    <section className="timeline">
      <h3>连接时间线</h3>
      <ol><li>accepted · {new Date(detail.accepted_at).toLocaleTimeString()}</li>{detail.connected_at && <li>active · {new Date(detail.connected_at).toLocaleTimeString()}</li>}<li>traffic · {detail.up_bytes + detail.down_bytes} bytes</li>{detail.closed_at && <li>{detail.state} · {new Date(detail.closed_at).toLocaleTimeString()}</li>}</ol>
    </section>
    {detail.samples_expired && <div className="stale-banner">细粒度流量序列已过保留期</div>}
  </div>;
}

function Metric({label, value}: {label: string; value: string | number}) {
  return <div><span>{label}</span><strong>{value}</strong></div>;
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let amount = value / 1024;
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) { amount /= 1024; unit += 1; }
  return `${amount.toFixed(amount >= 10 ? 1 : 2)} ${units[unit]}`;
}

function formatDuration(milliseconds: number) {
  const seconds = Math.max(0, Math.round(milliseconds / 1000));
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
  return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
}

function setOptionalParam(params: URLSearchParams, key: string, value: string) {
  if (value) params.set(key, value);
  else params.delete(key);
}
