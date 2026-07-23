"use client";

import { useCallback, useEffect, useState } from "react";
import {ChevronLeft, ChevronRight, Download, RefreshCw, Search, X} from "lucide-react";
import { api } from "@/lib/api";
import type { AuditEvent, Page } from "@/lib/types";
import {Dialog} from "@/components/ui/dialog";
import {ExportDialog} from "@/components/export/export-dialog";
import {
  auditCategoryLabel,
  auditEventLabel,
  auditEventOptions,
  auditOutcomeLabel,
} from "@/lib/audit-events";

export function AuditPage({csrf, onUnauthorized}: {csrf: string; onUnauthorized: () => void}) {
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
  const [exportOpen, setExportOpen] = useState(false);

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
        <div><h2>事件与审计</h2><p>管理员操作、配置变更和客户端生命周期</p></div>
        <div className="heading-actions"><button className="icon-button" onClick={() => setExportOpen(true)} title="导出筛选结果" aria-label="导出审计"><Download size={17} /></button><button className="icon-button" onClick={() => void load()} title="刷新" aria-label="刷新审计"><RefreshCw size={17} /></button></div>
      </div>
      <div className="filter-bar audit-filters">
        <label className="search-control"><Search size={16} /><input aria-label="搜索事件" value={query} onChange={(event) => setQuery(event.target.value)} placeholder="搜索摘要、客户端或原因" maxLength={128} /></label>
        <select aria-label="事件分类" value={category} onChange={(event) => {setCategory(event.target.value); setPage(1);}}>
          <option value="">全部分类</option><option value="admin">管理</option><option value="auth">认证</option><option value="config">配置</option><option value="client">客户端</option><option value="security">安全</option><option value="system">系统</option>
        </select>
        <select aria-label="事件类型" value={eventType} onChange={(event) => {setEventType(event.target.value); setPage(1);}}>
          <option value="">全部事件</option>{auditEventOptions.map((event) => <option key={event.value} value={event.value}>{event.label}</option>)}
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
          <thead><tr><th>时间</th><th>事件</th><th>操作者</th><th>IP</th><th>对象</th><th>结果</th><th>摘要</th></tr></thead>
          <tbody>
            {result.items.map((event) => (
              <tr key={event.id} className="clickable-row" onClick={() => void api<AuditEvent>(`/api/events/${event.id}`).then(setDetail)}>
                <td><time title={new Date(event.occurred_at).toISOString()}>{new Date(event.occurred_at).toLocaleString()}</time></td>
                <td><strong>{auditEventLabel(event.event_type)}</strong><small>{auditCategoryLabel(event.category)}</small></td>
                <td>{event.actor_id || event.actor_type}</td>
                <td className="mono audit-source-ip">{event.source_addr || "-"}</td>
                <td>{event.client_id || event.proxy_id || event.connection_id || "-"}</td>
                <td><Outcome value={event.outcome} /></td>
                <td>{event.summary || event.reason_code}</td>
              </tr>
            ))}
            {!loading && !result.items.length && <tr><td colSpan={7}><div className="empty-inline">没有符合条件的审计事件</div></td></tr>}
          </tbody>
        </table>
      </div>
      <Pagination page={page} totalPages={result.total_pages} total={result.total} pageSize={pageSize} setPage={setPage} setPageSize={setPageSize} />
      <ExportDialog
        open={exportOpen}
        onOpenChange={setExportOpen}
        title="导出事件与审计"
        dataset="audit_events"
        endpoint="/api/exports/audit-events"
        filters={auditExportFilters(query, category, eventType, outcome, dateFrom, dateTo)}
        filterSummary={[query ? "搜索：" + query : "", category ? "分类：" + auditCategoryLabel(category) : "", eventType ? "事件：" + auditEventLabel(eventType) : "", outcome ? "结果：" + auditOutcomeLabel(outcome) : "", dateFrom ? "开始：" + dateFrom : "", dateTo ? "结束：" + dateTo : ""].filter(Boolean)}
        estimatedRows={result.total}
        csrf={csrf}
        onUnauthorized={onUnauthorized}
      />
      <Dialog open={Boolean(detail)} onOpenChange={(open) => {if (!open) setDetail(null);}} title={detail ? auditEventLabel(detail.event_type) : "事件详情"} description={detail ? `${new Date(detail.occurred_at).toLocaleString()} · ${detail.id}` : undefined} side>
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
      {event.event_type === "client_session" && <ClientSessionContext details={event.details ?? {}} />}
      <details><summary>结构化上下文</summary><pre>{JSON.stringify(event.details ?? {}, null, 2)}</pre></details>
    </div>
  );
}

function ClientSessionContext({details}: {details: Record<string, unknown>}) {
  const agent = (details.agent ?? {}) as Record<string, unknown>;
  return <section className="diff-block">
    <h3>客户端环境</h3>
    <dl className="detail-grid">
      <dt>Agent 状态</dt><dd>{String(details.agent_status ?? "-")}</dd>
      <dt>版本 / commit</dt><dd>{String(agent.version ?? "-")}<small className="mono">{String(agent.commit ?? "")}</small></dd>
      <dt>运行平台</dt><dd>{String(agent.os ?? "-")} / {String(agent.arch ?? "-")}</dd>
      <dt>主机名</dt><dd>{String(agent.hostname ?? "-")}</dd>
      <dt>来源</dt><dd className="mono">{String(details.source_ip ?? details.source_addr ?? "-")}</dd>
      <dt>上线 / 最后通信</dt><dd>{formatAuditTime(details.connected_at)} / {formatAuditTime(details.last_seen_at)}</dd>
      <dt>控制 RTT</dt><dd>{Number(details.control_rtt_millis ?? 0) > 0 ? `${details.control_rtt_millis} ms` : "-"}</dd>
      <dt>结束原因</dt><dd>{String(details.close_reason ?? "-")}</dd>
      <dt>最终资源</dt><dd>smux {String(details.final_mux_sessions ?? "-")} · stream {String(details.final_active_streams ?? "-")}</dd>
    </dl>
  </section>;
}

function formatAuditTime(value: unknown) {
  return typeof value === "number" && value > 0 ? new Date(value).toLocaleString() : "-";
}

function Outcome({value}: {value: string}) {
  return <span className={`outcome outcome-${value}`}>{auditOutcomeLabel(value)}</span>;
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

function auditExportFilters(
  query: string,
  category: string,
  eventType: string,
  outcome: string,
  dateFrom: string,
  dateTo: string,
) {
  let effectiveTo = "";
  if (dateTo) {
    const end = new Date(dateTo + "T00:00:00");
    end.setDate(end.getDate() + 1);
    effectiveTo = end.toISOString();
  }
  return {
    query,
    category,
    event_type: eventType,
    outcome,
    date_from: dateFrom ? new Date(dateFrom + "T00:00:00").toISOString() : "",
    date_to: effectiveTo,
  };
}
