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
import {
  HistoryInsights,
  type AnalyticsRangeSelection,
  type TopMetric,
} from "@/components/network/history-insights";

type ConnectionView = "active" | "history";
type NetworkSection = "insights" | "connections";
const protocols = ["", "tcp", "udp"];
const historicalStates = ["", "closed", "failed", "interrupted"];
const sorts = ["accepted_desc", "accepted_asc", "traffic_desc", "duration_desc"];
const detailChartMargin = {top: 16, right: 10, bottom: 6, left: 4};

export function NetworkPage({onUnauthorized}: {onUnauthorized: () => void}) {
  const initial = useMemo(() => typeof window === "undefined" ? new URLSearchParams() : new URLSearchParams(window.location.search), []);
  const [view, setView] = useState<"overall" | "client">(initial.get("network_view") === "client" ? "client" : "overall");
  const [section, setSection] = useState<NetworkSection>(initial.get("network_section") === "connections" ? "connections" : "insights");
  const [connectionView, setConnectionView] = useState<ConnectionView>(initial.get("connection_view") === "history" ? "history" : "active");
  const initialRange = initial.get("range") ?? "";
  const initialActivePage = Number(initial.get("active_page"));
  const initialHistoryPage = Number(initial.get("history_page") ?? initial.get("page"));
  const [range, setRange] = useState<AnalyticsRangeSelection>(
    ["1h", "24h", "7d", "30d", "90d", "custom"].includes(initialRange) ? initialRange as AnalyticsRangeSelection : "24h",
  );
  const [customFrom, setCustomFrom] = useState(() => initial.get("from") ? toLocalInput(Number(initial.get("from"))) : toLocalInput(Date.now() - 24 * 60 * 60 * 1000));
  const [customTo, setCustomTo] = useState(() => initial.get("to") ? toLocalInput(Number(initial.get("to"))) : toLocalInput(Date.now()));
  const [clientID, setClientID] = useState(initial.get("client_id") ?? "");
  const [protocol, setProtocol] = useState(protocols.includes(initial.get("protocol") ?? "") ? initial.get("protocol") ?? "" : "");
  const [proxyID, setProxyID] = useState(initial.get("proxy_id") ?? "");
  const [sourceIP, setSourceIP] = useState(initial.get("source_ip") ?? "");
  const [topK, setTopK] = useState<5 | 10 | 20>([5, 10, 20].includes(Number(initial.get("top_k"))) ? Number(initial.get("top_k")) as 5 | 10 | 20 : 10);
  const [topMetric, setTopMetric] = useState<TopMetric>(initial.get("top_metric") === "traffic" ? "traffic" : "connections");
  const [historyState, setHistoryState] = useState(historicalStates.includes(initial.get("state") ?? "") ? initial.get("state") ?? "" : "");
  const [sort, setSort] = useState(sorts.includes(initial.get("sort") ?? "") ? initial.get("sort") ?? "accepted_desc" : "accepted_desc");
  const [query, setQuery] = useState((initial.get("query") ?? "").slice(0, 128));
  const [activePage, setActivePage] = useState(Number.isInteger(initialActivePage) && initialActivePage > 0 ? initialActivePage : 1);
  const [historyPage, setHistoryPage] = useState(Number.isInteger(initialHistoryPage) && initialHistoryPage > 0 ? initialHistoryPage : 1);
  const [summary, setSummary] = useState<NetworkSummary | null>(null);
  const [activeConnections, setActiveConnections] = useState<Page<NetworkConnection>>({items: [], page: 1, page_size: 25, total: 0, total_pages: 0});
  const [historyConnections, setHistoryConnections] = useState<Page<NetworkConnection>>({items: [], page: 1, page_size: 25, total: 0, total_pages: 0});
  const [clients, setClients] = useState<NetworkClient[]>([]);
  const [detail, setDetail] = useState<NetworkDetail | null>(null);
  const [loadedViews, setLoadedViews] = useState<Record<ConnectionView, boolean>>({active: false, history: false});
  const [stale, setStale] = useState(false);
  const requestSequence = useRef(0);
  const activeRequest = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    activeRequest.current?.abort();
    const controller = new AbortController();
    activeRequest.current = controller;
    const sequence = ++requestSequence.current;
    const selectedClient = view === "client" || proxyID ? clientID : "";
    const summaryParams = new URLSearchParams({range: "24h"});
    if (selectedClient) summaryParams.set("client_id", selectedClient);
    const connectionParams = new URLSearchParams({
      scope: connectionView,
      page: String(connectionView === "active" ? activePage : historyPage),
      page_size: "25",
    });
    if (selectedClient) connectionParams.set("client_id", selectedClient);
    if (protocol) connectionParams.set("protocol", protocol);
    if (query) connectionParams.set("query", query);
    if (connectionView === "history") {
      connectionParams.set("sort", sort);
      if (historyState) connectionParams.set("state", historyState);
    }
    try {
      const [summaryResult, clientResult] = await Promise.all([
        api<NetworkSummary>(`/api/network/summary?${summaryParams}`, {signal: controller.signal}),
        api<Page<NetworkClient>>("/api/network/clients?page=1&page_size=100", {signal: controller.signal}),
      ]);
      const connectionResult = section === "connections"
        ? await api<Page<NetworkConnection>>(`/api/network/connections?${connectionParams}`, {signal: controller.signal})
        : null;
      if (sequence !== requestSequence.current) return;
      setSummary(summaryResult);
      if (connectionResult) {
        if (connectionView === "active") setActiveConnections(connectionResult);
        else setHistoryConnections(connectionResult);
        setLoadedViews((current) => current[connectionView] ? current : {...current, [connectionView]: true});
      }
      setClients(clientResult.items);
      setStale(false);
    } catch (caught) {
      if (controller.signal.aborted || sequence !== requestSequence.current) return;
      if (caught instanceof Error && "status" in caught && caught.status === 401) onUnauthorized();
      setStale(true);
    } finally {
      if (sequence === requestSequence.current) {
        if (activeRequest.current === controller) activeRequest.current = null;
      }
    }
  }, [activePage, clientID, connectionView, historyPage, historyState, onUnauthorized, protocol, proxyID, query, section, sort, view]);

  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    params.set("network_view", view);
    params.set("network_section", section);
    params.set("connection_view", connectionView);
    params.set("range", range);
    if (range === "custom") {
      const from = new Date(customFrom).getTime();
      const to = new Date(customTo).getTime();
      setOptionalParam(params, "from", Number.isFinite(from) ? String(from) : "");
      setOptionalParam(params, "to", Number.isFinite(to) ? String(to) : "");
    } else {
      params.delete("from");
      params.delete("to");
    }
    if ((view === "client" || proxyID) && clientID) params.set("client_id", clientID);
    else params.delete("client_id");
    setOptionalParam(params, "protocol", protocol);
    setOptionalParam(params, "proxy_id", proxyID);
    setOptionalParam(params, "source_ip", sourceIP);
    setOptionalParam(params, "top_k", topK === 10 ? "" : String(topK));
    setOptionalParam(params, "top_metric", topMetric === "connections" ? "" : topMetric);
    setOptionalParam(params, "state", historyState);
    setOptionalParam(params, "sort", sort === "accepted_desc" ? "" : sort);
    setOptionalParam(params, "query", query);
    params.delete("page");
    setOptionalParam(params, "active_page", activePage === 1 ? "" : String(activePage));
    setOptionalParam(params, "history_page", historyPage === 1 ? "" : String(historyPage));
    window.history.replaceState(null, "", `${window.location.pathname}?${params}`);
  }, [activePage, clientID, connectionView, customFrom, customTo, historyPage, historyState, protocol, proxyID, query, range, section, sort, sourceIP, topK, topMetric, view]);

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
          <button className={view === "overall" ? "selected" : ""} onClick={() => {setView("overall"); setClientID(""); setProxyID(""); setActivePage(1); setHistoryPage(1);}}>整体</button>
          <button className={view === "client" ? "selected" : ""} onClick={() => {setView("client"); setProxyID(""); setActivePage(1); setHistoryPage(1);}}>客户端</button>
        </div>
        <select aria-label="网络活动协议" value={protocol} onChange={(event) => {setProtocol(event.target.value); setActivePage(1); setHistoryPage(1);}}>
          <option value="">全部协议</option><option value="tcp">TCP</option><option value="udp">UDP</option>
        </select>
        <button className="icon-button" onClick={() => void load()} title="刷新" aria-label="刷新网络活动"><RefreshCw size={17} /></button>
      </div>
      {view === "client" && (
        <div className="client-selector">
          <label>查看客户端
            <select value={clientID} onChange={(event) => {setClientID(event.target.value); setProxyID(""); setActivePage(1); setHistoryPage(1);}}>
              <option value="">选择客户端</option>
              {clients.map((client) => <option key={client.client_id} value={client.client_id}>{client.client_remark} · {client.client_id}{client.deleted ? "（已删除）" : ""}</option>)}
            </select>
          </label>
          {selectedClient && <div className="client-context"><strong>{selectedClient.client_remark}</strong><span className="mono">{selectedClient.client_id}</span><span>{selectedClient.online ? "在线" : "离线"} · epoch {selectedClient.session_epoch || "-"} · smux {selectedClient.mux_sessions}</span></div>}
        </div>
      )}
      {stale && <div className="stale-banner">网络活动刷新失败，当前数据可能已过期</div>}
      <section className="current-status-band" aria-label="当前状态">
        <div><span>当前活跃连接</span><strong>{summary?.active_connections ?? 0}</strong></div>
        <div><span>当前活跃客户端</span><strong>{summary?.active_clients ?? 0}</strong></div>
      </section>
      <div className="segmented network-section-tabs" aria-label="网络活动内容">
        <button className={section === "insights" ? "selected" : ""} onClick={() => setSection("insights")}>历史洞察</button>
        <button className={section === "connections" ? "selected" : ""} onClick={() => setSection("connections")}>连接明细</button>
      </div>
      {section === "insights" && <HistoryInsights
        view={view}
        clientID={clientID}
        protocol={protocol}
        range={range}
        customFrom={customFrom}
        customTo={customTo}
        proxyID={proxyID}
        sourceIP={sourceIP}
        topK={topK}
        topMetric={topMetric}
        clients={clients}
        onRangeChange={setRange}
        onCustomFromChange={setCustomFrom}
        onCustomToChange={setCustomTo}
        onScopeChange={(scopeClientID, scopeProxyID) => {
          setClientID(scopeClientID);
          setProxyID(scopeProxyID);
        }}
        onSourceIPChange={setSourceIP}
        onTopKChange={setTopK}
        onTopMetricChange={setTopMetric}
        onViewHistory={(ip) => {
          setSection("connections");
          setConnectionView("history");
          setQuery(ip);
          setHistoryPage(1);
        }}
        onUnauthorized={onUnauthorized}
      />}
      {section === "connections" && <section className="section-block connections-section">
        <div className="section-heading connections-heading">
          <div><h2>连接</h2><p>选择查看当前存活连接或已结束的历史连接</p></div>
          <div className="segmented connection-tabs" aria-label="连接类型">
            <button className={connectionView === "active" ? "selected" : ""} onClick={() => setConnectionView("active")}>实时连接</button>
            <button className={connectionView === "history" ? "selected" : ""} onClick={() => setConnectionView("history")}>历史连接</button>
          </div>
        </div>
        <div className="filter-bar network-filters">
          <label className="search-control"><Search size={16} /><input aria-label="搜索连接" maxLength={128} value={query} onChange={(event) => {setQuery(event.target.value); setActivePage(1); setHistoryPage(1);}} placeholder="活动、客户端、代理或地址" /></label>
          <select aria-label="连接协议" value={protocol} onChange={(event) => {setProtocol(event.target.value); setActivePage(1); setHistoryPage(1);}}><option value="">全部协议</option><option value="tcp">TCP</option><option value="udp">UDP</option></select>
        </div>
        {connectionView === "active" ? <section className="connection-panel live-connection-panel">
          <div className="section-heading"><div><h2>实时连接</h2><p>当前仍存活的连接；速率为服务端最近采样窗口的平均值</p></div><strong className="section-count">{activeConnections.total}</strong></div>
          <div className="table-wrap"><table className="live-connections-table responsive-table"><thead><tr><th>建链时间</th><th>客户端 / 代理</th><th>协议</th><th>来源 → 目标</th><th>状态 / 采样</th><th>上行速率</th><th>下行速率</th><th>累计流量</th><th className="live-rate-mobile">实时速率</th></tr></thead><tbody>
            {activeConnections.items.map((connection) => <tr key={connection.activity_id} className="clickable-row" onClick={() => void api<NetworkDetail>(`/api/network/connections/${connection.activity_id}`).then(setDetail)}>
              <td>{new Date(connection.connected_at ?? connection.accepted_at).toLocaleString()}</td><td><strong>{connection.client_remark}</strong><small>{connection.proxy_remark} · {connection.proxy_id}</small></td><td><span className={`network-tag ${connection.protocol}`}>{connection.protocol.toUpperCase()}</span></td><td className="mono address-cell">{connection.external_addr}<br />{connection.target_addr}</td><td>{formatConnectionState(connection.state)}<small>{formatRateWindow(connection.rate_window_millis)}</small></td><td className="rate-cell">{formatRate(connection.up_bytes_per_second)}</td><td className="rate-cell">{formatRate(connection.down_bytes_per_second)}</td><td>{formatBytes(connection.up_bytes + connection.down_bytes)}</td><td className="live-rate-mobile"><strong>↑ {formatRate(connection.up_bytes_per_second)}</strong><small>↓ {formatRate(connection.down_bytes_per_second)}</small></td>
            </tr>)}
            {!activeConnections.items.length && <tr><td colSpan={9}><div className="empty-inline">{loadedViews.active ? "当前没有符合条件的实时连接" : "正在加载实时连接"}</div></td></tr>}
          </tbody></table></div>
          <Pagination page={activePage} total={activeConnections.total} totalPages={activeConnections.total_pages} onPageChange={setActivePage} label="实时连接" />
        </section> : <section className="connection-panel history-connection-panel">
          <div className="section-heading"><div><h2>历史连接</h2><p>已经关闭、失败或中断的连接</p></div><strong className="section-count">{historyConnections.total}</strong></div>
          <div className="filter-bar history-filters">
            <select aria-label="历史连接状态" value={historyState} onChange={(event) => {setHistoryState(event.target.value); setHistoryPage(1);}}><option value="">全部结果</option><option value="closed">已关闭</option><option value="failed">失败</option><option value="interrupted">已中断</option></select>
            <select aria-label="历史连接排序" value={sort} onChange={(event) => {setSort(event.target.value); setHistoryPage(1);}}><option value="accepted_desc">最新优先</option><option value="accepted_asc">最早优先</option><option value="traffic_desc">流量优先</option><option value="duration_desc">时长优先</option></select>
          </div>
          <div className="table-wrap"><table className="history-connections-table responsive-table"><thead><tr><th>时间</th><th>客户端 / 代理</th><th>协议</th><th>来源 → 目标</th><th>结果</th><th>上传</th><th>下载</th><th className="mobile-table-cell">总流量</th></tr></thead><tbody>
            {historyConnections.items.map((connection) => <tr key={connection.activity_id} className="clickable-row" onClick={() => void api<NetworkDetail>(`/api/network/connections/${connection.activity_id}`).then(setDetail)}>
              <td>{new Date(connection.accepted_at).toLocaleString()}</td><td><strong>{connection.client_remark}</strong><small>{connection.proxy_remark} · {connection.proxy_id}</small></td><td><span className={`network-tag ${connection.protocol}`}>{connection.protocol.toUpperCase()}</span></td><td className="mono address-cell">{connection.external_addr}<br />{connection.target_addr}</td><td>{formatConnectionState(connection.state)}</td><td>{formatBytes(connection.up_bytes)}</td><td>{formatBytes(connection.down_bytes)}</td><td className="mobile-table-cell">{formatBytes(connection.up_bytes + connection.down_bytes)}</td>
            </tr>)}
            {!historyConnections.items.length && <tr><td colSpan={8}><div className="empty-inline">{loadedViews.history ? "没有符合条件的历史连接" : "正在加载历史连接"}</div></td></tr>}
          </tbody></table></div>
          <Pagination page={historyPage} total={historyConnections.total} totalPages={historyConnections.total_pages} onPageChange={setHistoryPage} label="历史连接" />
        </section>}
      </section>}
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
      {!detail.closed_at && <><dt>当前速率</dt><dd>↑ {formatRate(detail.up_bytes_per_second)} / ↓ {formatRate(detail.down_bytes_per_second)}<small>{formatRateWindow(detail.rate_window_millis)}</small></dd></>}
      <dt>连接限速</dt><dd>↑ {formatConfiguredLimit(detail.up_limit_bytes_per_second)} / ↓ {formatConfiguredLimit(detail.down_limit_bytes_per_second)}</dd>
      {detail.protocol === "tcp" ? <>
        <dt>限速等待</dt><dd>↑ {detail.up_throttled_millis} ms / ↓ {detail.down_throttled_millis} ms</dd>
        <dt>PROXY Protocol</dt><dd>{detail.proxy_protocol_enabled ? `v${detail.proxy_protocol_version} · ${detail.proxy_protocol_header_sent ? "已发送" : "未发送"}` : "关闭"}</dd>
      </> : <>
        <dt>限速丢弃</dt><dd>↑ {detail.up_rate_limited_packets} 包 / {formatBytes(detail.up_rate_limited_bytes)}<small>↓ {detail.down_rate_limited_packets} 包 / {formatBytes(detail.down_rate_limited_bytes)}</small></dd>
      </>}
      <dt>UDP 包</dt><dd>{detail.up_packets} / {detail.down_packets}</dd>
      <dt>关闭原因</dt><dd>{detail.close_reason || "-"}<small>{detail.close_reason_code}</small></dd>
    </dl>
    {detail.samples?.length > 0 && (
      <section className="detail-traffic" aria-label="连接流量序列">
        <h3>流量序列</h3>
        <div className="detail-chart">
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={detail.samples ?? []} margin={detailChartMargin}>
              <CartesianGrid stroke="#e4e9eb" vertical={false} />
              <XAxis dataKey="bucket_at" tickFormatter={(value) => new Date(value).toLocaleTimeString([], {hour: "2-digit", minute: "2-digit"})} minTickGap={24} />
              <YAxis tickFormatter={(value) => formatBytes(value)} tickMargin={6} width={76} />
              <Tooltip labelFormatter={(value) => new Date(Number(value)).toLocaleString()} formatter={(value) => formatBytes(Number(value))} />
              <Line type="monotone" dataKey="up_bytes_delta" name="上传" stroke="#087f75" strokeWidth={2} dot={false} isAnimationActive={false} />
              <Line type="monotone" dataKey="down_bytes_delta" name="下载" stroke="#b35f20" strokeWidth={2} strokeDasharray="6 4" dot={false} isAnimationActive={false} />
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

function formatConfiguredLimit(value: number) {
  return value > 0 ? formatRate(value) : "不限速";
}

function Pagination({
  page,
  total,
  totalPages,
  onPageChange,
  label,
}: {
  page: number;
  total: number;
  totalPages: number;
  onPageChange: (page: number) => void;
  label: string;
}) {
  return <div className="pagination">
    <span>共 {total} 条</span>
    <button className="icon-button" disabled={page <= 1} onClick={() => onPageChange(page - 1)} aria-label={`${label}上一页`}><ChevronLeft size={17} /></button>
    <span>{page} / {Math.max(totalPages, 1)}</span>
    <button className="icon-button" disabled={page >= totalPages} onClick={() => onPageChange(page + 1)} aria-label={`${label}下一页`}><ChevronRight size={17} /></button>
  </div>;
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let amount = value / 1024;
  let unit = 0;
  while (amount >= 1024 && unit < units.length - 1) { amount /= 1024; unit += 1; }
  return `${amount.toFixed(amount >= 10 ? 1 : 2)} ${units[unit]}`;
}

function formatRate(value: number | undefined) {
  return `${formatBytes(value ?? 0)}/s`;
}

function formatRateWindow(milliseconds: number | undefined) {
  if (!milliseconds) return "等待首次采样";
  const seconds = Math.max(1, Math.round(milliseconds / 1000));
  return `${seconds} 秒窗口`;
}

function formatConnectionState(state: string) {
  return {
    accepted: "已接受",
    connecting: "建链中",
    active: "活跃",
    closing: "关闭中",
    closed: "已关闭",
    failed: "失败",
    interrupted: "已中断",
  }[state] ?? state;
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

function toLocalInput(value: number) {
  if (!Number.isFinite(value) || value <= 0) return "";
  const date = new Date(value);
  const offset = date.getTimezoneOffset() * 60 * 1000;
  return new Date(value - offset).toISOString().slice(0, 16);
}
