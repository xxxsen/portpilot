"use client";

import { useCallback, useEffect, useState } from "react";
import { ChevronLeft, ChevronRight, RefreshCw, Search, X } from "lucide-react";
import { api } from "@/lib/api";
import type { AuditEvent, Page } from "@/lib/types";
import { Dialog } from "@/components/ui/dialog";

const eventTypes = [
  "admin_login", "admin_logout", "config_update", "client_created", "client_updated",
  "client_token_rotated", "admin_client_disconnect", "client_session", "security",
  "udp_drop", "database_rebuilt", "system_start", "system_stop", "config_metadata_migrated",
];

export function AuditPage({onUnauthorized}: {onUnauthorized: () => void}) {
  const [result, setResult] = useState<Page<AuditEvent>>({items: [], page: 1, page_size: 25, total: 0, total_pages: 0});
  const [query, setQuery] = useState("");
  const [debouncedQuery, setDebouncedQuery] = useState("");
  const [category, setCategory] = useState("");
  const [eventType, setEventType] = useState("");
  const [outcome, setOutcome] = useState("");
  const [dateFrom, setDateFrom] = useState("");
  const [dateTo, setDateTo] = useState("");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(25);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [detail, setDetail] = useState<AuditEvent | null>(null);

  useEffect(() => {
    const timer = window.setTimeout(() => {
      setDebouncedQuery(query);
      setPage(1);
    }, 300);
    return () => window.clearTimeout(timer);
  }, [query]);

  const load = useCallback(async (signal?: AbortSignal) => {
    setLoading(true);
    const params = new URLSearchParams({
      page: String(page), page_size: String(pageSize),
    });
    if (debouncedQuery) params.set("query", debouncedQuery);
    if (category) params.set("category", category);
    if (eventType) params.set("event_type", eventType);
    if (outcome) params.set("outcome", outcome);
    if (dateFrom) params.set("date_from", new Date(`${dateFrom}T00:00:00`).toISOString());
    if (dateTo) {
      const end = new Date(`${dateTo}T00:00:00`);
      end.setDate(end.getDate() + 1);
      params.set("date_to", end.toISOString());
    }
    try {
      setResult(await api<Page<AuditEvent>>(`/api/events?${params}`, {signal}));
      setError("");
    } catch (caught) {
      if (signal?.aborted) return;
      if (caught instanceof Error && "status" in caught && caught.status === 401) onUnauthorized();
      setError("事件查询失败，当前显示可能已过期");
    } finally {
      if (!signal?.aborted) setLoading(false);
    }
  }, [category, dateFrom, dateTo, debouncedQuery, eventType, onUnauthorized, outcome, page, pageSize]);

  useEffect(() => {
    const controller = new AbortController();
    const timer = window.setTimeout(() => void load(controller.signal), 0);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [load]);

  const clear = () => {
    setQuery(""); setCategory(""); setEventType(""); setOutcome(""); setDateFrom(""); setDateTo(""); setPage(1);
  };

  return (
    <section className="section-block first">
      <div className="section-heading">
        <div><h2>事件与审计</h2><p>管理员、配置、客户端和安全事件</p></div>
        <button className="icon-button" onClick={() => void load()} title="刷新" aria-label="刷新审计"><RefreshCw size={17} /></button>
      </div>
      <div className="filter-bar audit-filters">
        <label className="search-control"><Search size={16} /><input aria-label="搜索事件" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索摘要、客户端或原因" maxLength={128} /></label>
        <select aria-label="事件分类" value={category} onChange={(event) => {setCategory(event.target.value); setPage(1);}}>
          <option value="">全部分类</option><option value="auth">认证</option><option value="config">配置</option><option value="client">客户端</option><option value="security">安全</option><option value="system">系统</option>
        </select>
        <select aria-label="事件类型" value={eventType} onChange={(event) => {setEventType(event.target.value); setPage(1);}}>
          <option value="">全部事件</option>{eventTypes.map((value) => <option key={value}>{value}</option>)}
        </select>
        <select aria-label="事件结果" value={outcome} onChange={(event) => {setOutcome(event.target.value); setPage(1);}}>
          <option value="">全部结果</option><option value="success">成功</option><option value="failure">失败</option><option value="denied">拒绝</option><option value="pending">等待</option><option value="unknown">未知</option>
        </select>
        <input aria-label="开始日期" type="date" value={dateFrom} onChange={(event) => {setDateFrom(event.target.value); setPage(1);}} />
        <input aria-label="结束日期" type="date" value={dateTo} onChange={(event) => {setDateTo(event.target.value); setPage(1);}} />
        <button className="icon-button" onClick={clear} title="清除筛选" aria-label="清除筛选"><X size={17} /></button>
      </div>
      {error && <div className="stale-banner">{error}</div>}
      <div className="table-wrap">
        <table className="audit-table responsive-table">
          <thead><tr><th>时间</th><th>事件</th><th>操作者</th><th>对象</th><th>结果</th><th>摘要</th></tr></thead>
          <tbody>
            {result.items.map((event) => (
              <tr key={event.id} className="clickable-row" onClick={() => void api<AuditEvent>(`/api/events/${event.id}`).then(setDetail)}>
                <td><time title={new Date(event.occurred_at).toISOString()}>{new Date(event.occurred_at).toLocaleString()}</time></td>
                <td><strong>{event.event_type}</strong><small>{event.category}</small></td>
                <td>{event.actor_id || event.actor_type}</td>
                <td>{event.client_id || event.proxy_id || event.connection_id || "-"}</td>
                <td><Outcome value={event.outcome} /></td>
                <td>{event.summary || event.reason_code}</td>
              </tr>
            ))}
            {!loading && !result.items.length && <tr><td colSpan={6}><div className="empty-inline">没有符合条件的审计事件</div></td></tr>}
          </tbody>
        </table>
      </div>
      <Pagination page={page} totalPages={result.total_pages} total={result.total} pageSize={pageSize} setPage={setPage} setPageSize={setPageSize} />
      <Dialog open={Boolean(detail)} onOpenChange={(open) => {if (!open) setDetail(null);}} title={detail?.event_type ?? "事件详情"} description={detail ? `${new Date(detail.occurred_at).toLocaleString()} · ${detail.id}` : undefined} side>
        {detail && <AuditDetail event={detail} />}
      </Dialog>
    </section>
  );
}

function AuditDetail({event}: {event: AuditEvent}) {
  return (
    <div className="drawer-body">
      <dl className="detail-grid">
        <dt>结果</dt><dd><Outcome value={event.outcome} /></dd>
        <dt>操作者</dt><dd>{event.actor_id || event.actor_type}</dd>
        <dt>来源</dt><dd className="mono">{event.source_addr || "-"}</dd>
        <dt>客户端</dt><dd>{event.client_id || "-"}</dd>
        <dt>代理</dt><dd>{event.proxy_id || "-"}</dd>
        <dt>连接</dt><dd className="mono">{event.connection_id || "-"}</dd>
        <dt>原因码</dt><dd className="mono">{event.reason_code || "-"}</dd>
        <dt>原因</dt><dd>{event.reason || "-"}</dd>
      </dl>
      {event.config_changes?.map((change) => (
        <section className="diff-block" key={change.id}>
          <h3>{change.operation} {change.entity_type} · {change.entity_id}</h3>
          <div className="diff-grid">
            <div><strong>变更前</strong><pre>{change.before ? JSON.stringify(change.before, null, 2) : "新增"}</pre></div>
            <div><strong>变更后</strong><pre>{change.after ? JSON.stringify(change.after, null, 2) : "删除"}</pre></div>
          </div>
        </section>
      ))}
      <details><summary>结构化上下文</summary><pre>{JSON.stringify(event.details ?? {}, null, 2)}</pre></details>
    </div>
  );
}

function Outcome({value}: {value: string}) {
  const labels: Record<string, string> = {
    success: "成功", failure: "失败", denied: "拒绝",
    pending: "等待", unknown: "未知",
  };
  return <span className={`outcome outcome-${value}`}>{labels[value] ?? value}</span>;
}

function Pagination({page, totalPages, total, pageSize, setPage, setPageSize}: {
  page: number; totalPages: number; total: number; pageSize: number;
  setPage: (value: number) => void; setPageSize: (value: number) => void;
}) {
  return (
    <div className="pagination">
      <span>共 {total} 条</span>
      <button className="icon-button" disabled={page <= 1} onClick={() => setPage(page - 1)} aria-label="上一页"><ChevronLeft size={17} /></button>
      <span>{page} / {Math.max(totalPages, 1)}</span>
      <button className="icon-button" disabled={page >= totalPages} onClick={() => setPage(page + 1)} aria-label="下一页"><ChevronRight size={17} /></button>
      <select aria-label="每页数量" value={pageSize} onChange={(event) => {setPageSize(Number(event.target.value)); setPage(1);}}><option value={25}>25 / 页</option><option value={50}>50 / 页</option><option value={100}>100 / 页</option></select>
    </div>
  );
}
