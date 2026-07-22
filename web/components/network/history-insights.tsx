"use client";

import {useCallback, useEffect, useMemo, useRef, useState} from "react";
import {ChevronLeft, ChevronRight, Download, RefreshCw, Search, X} from "lucide-react";
import {
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import {api} from "@/lib/api";
import type {ReactNode} from "react";
import type {
  AnalyticsComparison,
  AnalyticsCountry,
  AnalyticsDimension,
  AnalyticsIPSeries,
  AnalyticsOverview,
  AnalyticsTopIP,
  AnalyticsTopIPs,
  NetworkClient,
  Page,
} from "@/lib/types";
import {ExportDialog} from "@/components/export/export-dialog";
import {MetricDelta, PeriodComparison} from "@/components/network/period-comparison";

export type AnalyticsRangeSelection = "1h" | "24h" | "7d" | "30d" | "90d" | "custom";
type TopMetric = "connections" | "traffic";

type DimensionsResponse = Page<AnalyticsDimension> & Pick<AnalyticsOverview, "range" | "scope" | "completeness">;

type Props = {
  view: "overall" | "client";
  clientID: string;
  protocol: string;
  range: AnalyticsRangeSelection;
  customFrom: string;
  customTo: string;
  proxyID: string;
  sourceIP: string;
  topK: 5 | 10 | 20;
  clients: NetworkClient[];
  onRangeChange: (range: AnalyticsRangeSelection) => void;
  onCustomFromChange: (value: string) => void;
  onCustomToChange: (value: string) => void;
  onScopeChange: (clientID: string, proxyID: string) => void;
  onSourceIPChange: (sourceIP: string) => void;
  onTopKChange: (value: 5 | 10 | 20) => void;
  onViewHistory: (sourceIP: string) => void;
  csrf: string;
  onUnauthorized: () => void;
};

const chartMargin = {top: 18, right: 12, bottom: 8, left: 9};
const ranges: AnalyticsRangeSelection[] = ["1h", "24h", "7d", "30d", "90d", "custom"];
const rangeSeconds: Partial<Record<AnalyticsRangeSelection, number>> = {
  "1h": 3600,
  "24h": 86400,
  "7d": 7 * 86400,
  "30d": 30 * 86400,
  "90d": 90 * 86400,
};

export function HistoryInsights(props: Props) {
  const view = props.view;
  const clientID = props.clientID;
  const topK = props.topK;
  const sourceIP = props.sourceIP;
  const onUnauthorized = props.onUnauthorized;
  const [overview, setOverview] = useState<AnalyticsOverview | null>(null);
  const [dimensions, setDimensions] = useState<DimensionsResponse>({
    items: [], page: 1, page_size: 25, total: 0, total_pages: 0,
    range: emptyRange(), scope: {client_id: "", proxy_id: "", protocol: ""},
    completeness: emptyCompleteness(),
  });
  const [rankings, setRankings] = useState<AnalyticsTopIPs | null>(null);
  const [comparison, setComparison] = useState<AnalyticsComparison | null>(null);
  const [comparisonUnavailable, setComparisonUnavailable] = useState(false);
  const [overlayPrevious, setOverlayPrevious] = useState(false);
  const [dimensionExportOpen, setDimensionExportOpen] = useState(false);
  const [ipDetail, setIPDetail] = useState<AnalyticsIPSeries | null>(null);
  const [dimensionPage, setDimensionPage] = useState(1);
  const [dimensionSort, setDimensionSort] = useState("traffic_desc");
  const [dimensionQuery, setDimensionQuery] = useState("");
  const [highlightedCountry, setHighlightedCountry] = useState("");
  const [stale, setStale] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [maximumCustomTime] = useState(() => toLocalInput(Date.now()));
  const requestSequence = useRef(0);
  const activeRequest = useRef<AbortController | null>(null);

  const commonParams = useMemo(() => {
    const params = new URLSearchParams({range: props.range});
    if (props.range === "custom") {
      const from = new Date(props.customFrom).getTime();
      const to = new Date(props.customTo).getTime();
      if (!Number.isFinite(from) || !Number.isFinite(to) || from >= to) return null;
      params.set("from", String(from));
      params.set("to", String(to));
    }
    if (props.clientID && (props.view === "client" || props.proxyID)) params.set("client_id", props.clientID);
    if (props.proxyID) params.set("proxy_id", props.proxyID);
    if (props.protocol) params.set("protocol", props.protocol);
    return params;
  }, [
    props.clientID,
    props.customFrom,
    props.customTo,
    props.protocol,
    props.proxyID,
    props.range,
    props.view,
  ]);

  const load = useCallback(async () => {
    if ((view === "client" && !clientID) || commonParams === null) return;
    activeRequest.current?.abort();
    const controller = new AbortController();
    activeRequest.current = controller;
    const sequence = ++requestSequence.current;
    const dimensionParams = new URLSearchParams(commonParams);
    dimensionParams.set("page", String(dimensionPage));
    dimensionParams.set("page_size", "25");
    dimensionParams.set("sort", dimensionSort);
    if (dimensionQuery.trim()) dimensionParams.set("query", dimensionQuery.trim());
    const topParams = new URLSearchParams(commonParams);
    topParams.set("k", String(topK));
    try {
      const [overviewResult, dimensionResult, rankingResult, comparisonResult] = await Promise.all([
        api<AnalyticsOverview>(`/api/network/analytics/overview?${commonParams}`, {signal: controller.signal}),
        api<DimensionsResponse>(`/api/network/analytics/dimensions?${dimensionParams}`, {signal: controller.signal}),
        api<AnalyticsTopIPs>(`/api/network/analytics/top-ips?${topParams}`, {signal: controller.signal}),
        api<AnalyticsComparison>(`/api/network/analytics/comparison?`, {signal: controller.signal})
          .then((value) => ({value, failed: false}))
          .catch((caught) => {
            if (controller.signal.aborted || (caught instanceof Error && "status" in caught && caught.status === 401)) {
              throw caught;
            }
            return {value: null, failed: true};
          }),
      ]);
      if (sequence !== requestSequence.current) return;
      setOverview(overviewResult);
      setDimensions(dimensionResult);
      setRankings(rankingResult);
      if (!comparisonResult.failed) {
        setComparison(comparisonResult.value);
        setComparisonUnavailable(false);
      } else {
        setComparisonUnavailable(true);
      }
      setLoaded(true);
      setStale(false);
    } catch (caught) {
      if (controller.signal.aborted || sequence !== requestSequence.current) return;
      if (caught instanceof Error && "status" in caught && caught.status === 401) onUnauthorized();
      setStale(true);
    } finally {
      if (sequence === requestSequence.current && activeRequest.current === controller) {
        activeRequest.current = null;
      }
    }
  }, [
    commonParams,
    dimensionPage,
    dimensionQuery,
    dimensionSort,
    clientID,
    onUnauthorized,
    topK,
    view,
  ]);

  useEffect(() => {
    const initialTimer = window.setTimeout(() => void load(), 0);
    let refreshTimer: number | undefined;
    const schedule = () => {
      if (document.visibilityState === "visible") {
        refreshTimer = window.setInterval(() => void load(), 60000);
      }
    };
    const handleVisibility = () => {
      if (refreshTimer) window.clearInterval(refreshTimer);
      refreshTimer = undefined;
      if (document.visibilityState === "visible") {
        void load();
        schedule();
      }
    };
    schedule();
    document.addEventListener("visibilitychange", handleVisibility);
    return () => {
      window.clearTimeout(initialTimer);
      if (refreshTimer) window.clearInterval(refreshTimer);
      activeRequest.current?.abort();
      document.removeEventListener("visibilitychange", handleVisibility);
    };
  }, [load]);

  useEffect(() => {
    if (!sourceIP || commonParams === null) return;
    const controller = new AbortController();
    const params = new URLSearchParams(commonParams);
    params.set("source_ip", sourceIP);
    void api<AnalyticsIPSeries>(`/api/network/analytics/ip-series?${params}`, {signal: controller.signal})
      .then(setIPDetail)
      .catch((caught) => {
        if (!controller.signal.aborted && caught instanceof Error && "status" in caught && caught.status === 401) {
          onUnauthorized();
        }
      });
    return () => controller.abort();
  }, [commonParams, onUnauthorized, sourceIP]);

  if (props.view === "client" && !props.clientID) {
    return <div className="network-empty-state">选择一个客户端后查看历史洞察</div>;
  }
  if (commonParams === null) {
    return <div className="network-empty-state">请选择有效的自定义时间范围</div>;
  }

  const selectedClient = props.clients.find((client) => client.client_id === props.clientID);
  const selectedDimension = dimensions.items.find((item) => item.proxy_id === props.proxyID);
  const scopeLabel = [
    selectedClient?.client_remark || props.clientID || "全部客户端",
    props.proxyID ? selectedDimension?.proxy_remark || props.proxyID : "",
  ].filter(Boolean).join(" / ");
  return <div className="history-insights">
    <div className="insight-toolbar">
      <div className="segmented compact insight-ranges" aria-label="历史洞察时间范围">
        {ranges.map((value) => <button
          key={value}
          className={props.range === value ? "selected" : ""}
          disabled={Boolean(
            overview &&
            rangeSeconds[value] &&
            rangeSeconds[value]! > overview.completeness.statistics_retention_seconds
          )}
          onClick={() => {
            props.onRangeChange(value);
            setDimensionPage(1);
          }}
        >{value === "custom" ? "自定义" : value}</button>)}
      </div>
      <button className="icon-button" onClick={() => void load()} title="刷新历史洞察" aria-label="刷新历史洞察">
        <RefreshCw size={17} />
      </button>
    </div>
    {props.range === "custom" && <div className="custom-range">
      <label>开始时间<input
        type="datetime-local"
        min={toLocalInput(overview?.completeness.statistics_available_from ?? 0)}
        max={props.customTo}
        value={props.customFrom}
        onChange={(event) => props.onCustomFromChange(event.target.value)}
      /></label>
      <label>结束时间<input
        type="datetime-local"
        min={props.customFrom}
        max={maximumCustomTime}
        value={props.customTo}
        onChange={(event) => props.onCustomToChange(event.target.value)}
      /></label>
    </div>}
    <div className="scope-breadcrumb" aria-label="当前历史洞察范围">
      <span>全部客户端</span>
      {props.clientID && <><b>/</b><span>{selectedClient?.client_remark || props.clientID}</span></>}
      {props.proxyID && <><b>/</b><span>{selectedDimension?.proxy_remark || props.proxyID}</span><button className="icon-button" onClick={() => props.onScopeChange(props.view === "client" ? props.clientID : "", "")} title="清除代理范围" aria-label="清除代理范围"><X size={14} /></button></>}
    </div>
    {stale && <div className="stale-banner">历史洞察刷新失败，保留上次成功数据</div>}
    {overview?.completeness.status === "backfilling" && <div className="stale-banner">历史统计正在回填，当前结果可能只覆盖 {formatDate(overview.completeness.complete_from)} 之后的数据</div>}
    {overview?.completeness.status === "degraded" && <div className="stale-banner">历史统计处于降级状态，结果可能不完整</div>}
    {overview?.completeness.geoip_status === "disabled" && <div className="insight-note">GeoIP 未配置，来源国家统一显示为未知，连接与流量统计不受影响。</div>}
    {overview?.completeness.geoip_status === "degraded" && <div className="stale-banner">GeoIP 数据库重载失败，当前统计继续使用最近一次可用的国家库。</div>}
    {overview && overview.range.effective_from < overview.completeness.details_available_from && <div className="insight-note">当前范围的汇总统计仍可用，较早的单条连接明细已超过保留期。</div>}

    <section className="insight-section" aria-label="历史指标">
      <div className="section-heading"><div><h2>历史指标</h2><p>只统计已关闭、失败或中断的连接</p></div></div>
      <div className="metric-strip history-metrics">
        <Metric label="历史连接" value={overview?.connections ?? 0} delta={<MetricDelta change={comparison?.metrics.connections} />} />
        <Metric label="独立来源 IP" value={overview?.unique_ips ?? 0} delta={<MetricDelta change={comparison?.metrics.unique_ips} />} />
        <Metric label="国家/地区" value={overview?.countries ?? 0} />
        <Metric label="失败率" value={formatPercent(overview?.failure_rate ?? 0)} delta={<MetricDelta change={comparison?.metrics.failure_rate} tone="failure" />} />
        <Metric label="上传" value={formatBytes(overview?.up_bytes ?? 0)} delta={<MetricDelta change={comparison?.metrics.up_bytes} />} />
        <Metric label="下载" value={formatBytes(overview?.down_bytes ?? 0)} delta={<MetricDelta change={comparison?.metrics.down_bytes} />} />
      </div>
    </section>

    <section className="insight-section">
      <div className="section-heading"><div><h2>历史总趋势</h2><p>流量与连接使用独立纵轴，时间按本地时区展示</p></div></div>
      <div className="insight-chart-grid">
        <TrendChart
          title="流量趋势"
          data={overview?.series ?? []}
          lines={[
            {key: "up_bytes", name: "上传", color: "#087f75"},
            {key: "down_bytes", name: "下载", color: "#b35f20", dashed: true},
          ]}
          formatter={formatBytes}
          previous={comparison?.previous_series ?? []}
          overlay={overlayPrevious}
        />
        <TrendChart
          title="连接趋势"
          data={overview?.series ?? []}
          lines={[
            {key: "connections", name: "历史连接", color: "#325d88"},
            {key: "failures", name: "失败", color: "#ba3d3d", dashed: true},
          ]}
          formatter={(value) => String(value)}
          previous={comparison?.previous_series ?? []}
          overlay={overlayPrevious}
        />
      </div>
      <p className="chart-summary">估算流量 {formatBytes(overview?.completeness.estimated_bytes ?? 0)}，占区间总流量 {formatPercent(overview?.completeness.estimated_ratio ?? 0)}。</p>
    </section>

    <PeriodComparison comparison={comparison} unavailable={comparisonUnavailable} overlay={overlayPrevious} onOverlayChange={setOverlayPrevious} />

    <section className="insight-section">
      <div className="section-heading dimension-heading">
        <div><h2>客户端 / 代理汇总</h2><p>选择一行可将下方排名和 IP 曲线限定到该代理</p></div>
        <div className="heading-actions"><strong className="section-count">{dimensions.total}</strong><button className="icon-button" onClick={() => setDimensionExportOpen(true)} title="导出筛选结果" aria-label="导出代理汇总"><Download size={17} /></button></div>
      </div>
      <div className="filter-bar insight-dimension-filters">
        <label className="search-control"><Search size={16} /><input
          aria-label="搜索客户端或代理"
          maxLength={128}
          value={dimensionQuery}
          onChange={(event) => {
            setDimensionQuery(event.target.value);
            setDimensionPage(1);
          }}
          placeholder="客户端、代理或备注"
        /></label>
        <select aria-label="维度排序" value={dimensionSort} onChange={(event) => {
          setDimensionSort(event.target.value);
          setDimensionPage(1);
        }}>
          <option value="traffic_desc">流量优先</option>
          <option value="connections_desc">连接数优先</option>
          <option value="unique_ips_desc">独立 IP 优先</option>
          <option value="failures_desc">失败数优先</option>
        </select>
      </div>
      <div className="table-wrap"><table className="analytics-dimension-table responsive-table">
        <thead><tr><th>客户端 / 代理</th><th>协议</th><th>历史连接</th><th>独立 IP</th><th>失败率</th><th>上传</th><th>下载</th><th>主要国家</th><th>最近活动</th></tr></thead>
        <tbody>
          {props.view === "client" && props.clientID && <tr
            className={`clickable-row all-proxies-row ${props.proxyID ? "" : "selected-row"}`}
            tabIndex={0}
            onClick={() => props.onScopeChange(props.clientID, "")}
            onKeyDown={(event) => {
              if (event.key === "Enter" || event.key === " ") props.onScopeChange(props.clientID, "");
            }}
          >
            <td><strong>{selectedClient?.client_remark || props.clientID}</strong><small>全部代理</small></td>
            <td>全部</td>
            <td colSpan={7}>查看该客户端所有代理的历史合计</td>
          </tr>}
          {dimensions.items.map((item) => <tr
            key={`${item.client_id}/${item.proxy_id}/${item.protocol}`}
            className={`clickable-row ${props.proxyID === item.proxy_id && props.clientID === item.client_id ? "selected-row" : ""}`}
            tabIndex={0}
            onClick={() => props.onScopeChange(item.client_id, item.proxy_id)}
            onKeyDown={(event) => {
              if (event.key === "Enter" || event.key === " ") props.onScopeChange(item.client_id, item.proxy_id);
            }}
          >
            <td><strong>{item.client_remark || item.client_id}{item.client_deleted ? "（已删除）" : ""}</strong><small>{item.proxy_remark || item.proxy_id}{item.proxy_deleted ? "（已删除）" : ""} · {item.proxy_id}</small></td>
            <td><span className={`network-tag ${item.protocol}`}>{item.protocol.toUpperCase()}</span></td>
            <td>{item.connections}</td><td>{item.unique_ips}</td>
            <td>{formatPercent(item.connections ? item.failures / item.connections : 0)}</td>
            <td>{formatBytes(item.up_bytes)}</td><td>{formatBytes(item.down_bytes)}</td>
            <td>{countryLabel("country", item.top_country_code)}</td>
            <td>{formatDate(item.last_seen_at)}</td>
          </tr>)}
          {!dimensions.items.length && <tr><td colSpan={9}><div className="empty-inline">{loaded ? "当前范围没有代理统计" : "正在加载历史洞察"}</div></td></tr>}
        </tbody>
      </table></div>
      <Pagination page={dimensionPage} total={dimensions.total} totalPages={dimensions.total_pages} onPageChange={setDimensionPage} />
    </section>

    <section className="insight-section">
      <div className="section-heading ranking-heading">
        <div><h2>Top IP</h2><p>相同 IP 保留在两份独立排名中的原始名次</p></div>
        <select aria-label="Top IP 数量" value={props.topK} onChange={(event) => props.onTopKChange(Number(event.target.value) as 5 | 10 | 20)}>
          <option value={5}>Top 5</option><option value={10}>Top 10</option><option value={20}>Top 20</option>
        </select>
      </div>
      <div className="ranking-grid">
        <RankingTable title="连接数 Top K" metric="connections" items={rankings?.by_connections ?? []} rankChanges={comparison?.connections_rank_changes ?? []} selectedIP={props.sourceIP} highlightedCountry={highlightedCountry} onSelect={props.onSourceIPChange} />
        <RankingTable title="流量 Top K" metric="traffic" items={rankings?.by_traffic ?? []} rankChanges={comparison?.traffic_rank_changes ?? []} selectedIP={props.sourceIP} highlightedCountry={highlightedCountry} onSelect={props.onSourceIPChange} />
      </div>
    </section>

    <ExportDialog
      open={dimensionExportOpen}
      onOpenChange={setDimensionExportOpen}
      title="导出历史维度"
      dataset="network_dimensions"
      endpoint="/api/exports/network-dimensions"
      filters={{range: props.range, from: props.range === "custom" ? new Date(props.customFrom).getTime() : 0, to: props.range === "custom" ? new Date(props.customTo).getTime() : 0, client_id: props.clientID && (props.view === "client" || props.proxyID) ? props.clientID : "", proxy_id: props.proxyID, protocol: props.protocol, query: dimensionQuery.trim(), sort: dimensionSort}}
      filterSummary={["范围：" + props.range, props.clientID ? "客户端：" + props.clientID : "", props.proxyID ? "代理：" + props.proxyID : "", props.protocol ? "协议：" + props.protocol.toUpperCase() : "", dimensionQuery.trim() ? "搜索：" + dimensionQuery.trim() : ""].filter(Boolean)}
      estimatedRows={dimensions.total}
      csrf={props.csrf}
      onUnauthorized={props.onUnauthorized}
    />

    {props.sourceIP && <IPDetail
      detail={ipDetail?.source_ip === props.sourceIP ? ipDetail : null}
      sourceIP={props.sourceIP}
      scopeLabel={scopeLabel}
      onClose={() => props.onSourceIPChange("")}
      onViewHistory={props.onViewHistory}
    />}

    <section className="insight-section">
      <div className="section-heading"><div><h2>国家与地址分类</h2><p>GeoIP 仅用于国家级分析，不代表精确位置</p></div></div>
      <CountryDistribution
        items={overview?.country_distribution ?? []}
        selected={highlightedCountry}
        onSelect={(value) => setHighlightedCountry((current) => current === value ? "" : value)}
      />
    </section>
  </div>;
}

function RankingTable({
  title,
  metric,
  items,
  rankChanges,
  selectedIP,
  highlightedCountry,
  onSelect,
}: {
  title: string;
  metric: TopMetric;
  items: AnalyticsTopIP[];
  rankChanges: AnalyticsComparison["connections_rank_changes"];
  selectedIP: string;
  highlightedCountry: string;
  onSelect: (ip: string) => void;
}) {
  return <div className={`ranking-table ranking-${metric}`}>
    <h3>{title}</h3>
    <div className="table-wrap"><table>
      <thead><tr><th>排名 / IP</th><th>变化</th><th>国家</th><th>连接</th><th>流量</th><th>占比</th><th>最近活动</th></tr></thead>
      <tbody>
        {items.map((item) => {
          const share = metric === "connections" ? item.connection_share : item.traffic_share;
          return <tr
            key={item.source_ip}
            className={`clickable-row ${selectedIP === item.source_ip ? "selected-row" : ""} ${highlightedCountry === countryKey(item.geo_kind, item.country_code) ? "country-highlight-row" : ""}`}
            tabIndex={0}
            onClick={() => onSelect(item.source_ip)}
            onKeyDown={(event) => {
              if (event.key === "Enter" || event.key === " ") onSelect(item.source_ip);
            }}
          >
            <td><strong>#{item.rank} <span className="mono">{item.source_ip}</span></strong></td>
            <td>{rankChangeLabel(rankChanges.find((value) => value.source_ip === item.source_ip))}</td>
            <td>{countryLabel(item.geo_kind, item.country_code)}</td>
            <td>{item.connections}</td><td>{formatBytes(item.up_bytes + item.down_bytes)}</td>
            <td><div className="share-value"><span style={{width: `${Math.min(100, share * 100)}%`}} /><b>{formatPercent(share)}</b></div></td>
            <td>{formatDate(item.last_seen_at)}</td>
          </tr>;
        })}
        {!items.length && <tr><td colSpan={7}><div className="empty-inline">当前范围没有来源 IP</div></td></tr>}
      </tbody>
    </table></div>
  </div>;
}

function IPDetail({
  detail,
  sourceIP,
  scopeLabel,
  onClose,
  onViewHistory,
}: {
  detail: AnalyticsIPSeries | null;
  sourceIP: string;
  scopeLabel: string;
  onClose: () => void;
  onViewHistory: (sourceIP: string) => void;
}) {
  return <section className="insight-section ip-detail-section">
    <div className="section-heading">
      <div><h2>IP 详情 · <span className="mono">{sourceIP}</span></h2><p>{detail ? `${countryLabel(detail.geo_kind, detail.country_code)} · ${scopeLabel}` : "正在加载时间序列"}</p></div>
      <button className="icon-button" onClick={onClose} title="关闭 IP 详情" aria-label="关闭 IP 详情"><X size={17} /></button>
    </div>
    <div className="metric-strip ip-metrics">
      <Metric label="历史连接" value={detail?.connections ?? 0} />
      <Metric label="失败率" value={formatPercent(detail?.connections ? detail.failures / detail.connections : 0)} />
      <Metric label="上传" value={formatBytes(detail?.up_bytes ?? 0)} />
      <Metric label="下载" value={formatBytes(detail?.down_bytes ?? 0)} />
      <Metric label="首次活动" value={formatDate(detail?.first_seen_at ?? 0)} />
      <Metric label="最近活动" value={formatDate(detail?.last_seen_at ?? 0)} />
    </div>
    <div className="insight-chart-grid">
      <TrendChart
        title="IP 流量"
        data={detail?.series ?? []}
        lines={[
          {key: "up_bytes", name: "上传", color: "#087f75"},
          {key: "down_bytes", name: "下载", color: "#b35f20", dashed: true},
        ]}
        formatter={formatBytes}
      />
      <TrendChart
        title="IP 连接"
        data={detail?.series ?? []}
        lines={[
          {key: "connections", name: "历史连接", color: "#325d88"},
          {key: "failures", name: "失败", color: "#ba3d3d", dashed: true},
        ]}
        formatter={(value) => String(value)}
      />
    </div>
    <button className="secondary-button view-history-command" onClick={() => onViewHistory(sourceIP)}>查看该 IP 的历史连接</button>
  </section>;
}

function CountryDistribution({
  items,
  selected,
  onSelect,
}: {
  items: AnalyticsCountry[];
  selected: string;
  onSelect: (value: string) => void;
}) {
  const [sort, setSort] = useState<"traffic" | "connections" | "unique_ips">("traffic");
  const maximum = Math.max(1, ...items.map((item) => item.up_bytes + item.down_bytes));
  const sortedItems = [...items].sort((left, right) => {
    if (sort === "connections" && left.connections !== right.connections) {
      return right.connections - left.connections;
    }
    if (sort === "unique_ips" && left.unique_ips !== right.unique_ips) {
      return right.unique_ips - left.unique_ips;
    }
    const leftTraffic = left.up_bytes + left.down_bytes;
    const rightTraffic = right.up_bytes + right.down_bytes;
    if (sort === "traffic" && leftTraffic !== rightTraffic) {
      return rightTraffic - leftTraffic;
    }
    return countryLabel(left.geo_kind, left.country_code).localeCompare(
      countryLabel(right.geo_kind, right.country_code),
      "zh-CN",
    );
  });
  return <><div className="country-table-toolbar">
    <select aria-label="国家分类排序" value={sort} onChange={(event) => setSort(event.target.value as typeof sort)}>
      <option value="traffic">按流量</option>
      <option value="connections">按连接数</option>
      <option value="unique_ips">按独立 IP</option>
    </select>
  </div><div className="table-wrap"><table className="country-table responsive-table">
    <thead><tr><th>国家 / 分类</th><th>独立 IP</th><th>连接数</th><th>流量</th><th>流量占比</th></tr></thead>
    <tbody>
      {sortedItems.map((item) => <tr
        key={countryKey(item.geo_kind, item.country_code)}
        className={`clickable-row ${selected === countryKey(item.geo_kind, item.country_code) ? "selected-row" : ""}`}
        aria-label={`按国家分类高亮：${countryLabel(item.geo_kind, item.country_code)}`}
        tabIndex={0}
        onClick={() => onSelect(countryKey(item.geo_kind, item.country_code))}
        onKeyDown={(event) => {
          if (event.key === "Enter" || event.key === " ") onSelect(countryKey(item.geo_kind, item.country_code));
        }}
      >
        <td><strong>{countryLabel(item.geo_kind, item.country_code)}</strong></td>
        <td>{item.unique_ips}</td><td>{item.connections}</td><td>{formatBytes(item.up_bytes + item.down_bytes)}</td>
        <td><div className="country-bar"><span style={{width: `${((item.up_bytes + item.down_bytes) / maximum) * 100}%`}} /><b>{formatPercent(item.traffic_share)}</b></div></td>
      </tr>)}
      {!items.length && <tr><td colSpan={5}><div className="empty-inline">当前范围没有国家或地址分类数据</div></td></tr>}
    </tbody>
  </table></div></>;
}

function TrendChart({
  title,
  data,
  lines,
  formatter,
  previous = [],
  overlay = false,
}: {
  title: string;
  data: AnalyticsOverview["series"];
  lines: Array<{key: "up_bytes" | "down_bytes" | "connections" | "failures"; name: string; color: string; dashed?: boolean}>;
  formatter: (value: number) => string;
  previous?: AnalyticsComparison["previous_series"];
  overlay?: boolean;
}) {
  const points = new Map<number, Record<string, number>>();
  for (const point of data) points.set(point.bucket_at, {...point});
  if (overlay) {
    for (const point of previous) {
      const target = points.get(point.aligned_bucket_at) ?? {bucket_at: point.aligned_bucket_at};
      for (const line of lines) target[line.key + "_previous"] = point[line.key];
      points.set(point.aligned_bucket_at, target);
    }
  }
  const chartData = [...points.values()].sort((left, right) => left.bucket_at - right.bucket_at);
  return <div className="insight-chart" aria-label={title}>
    <h3>{title}</h3>
    <div>
      <ResponsiveContainer width="100%" height="100%">
        <LineChart data={chartData} margin={chartMargin}>
          <CartesianGrid stroke="#e4e9eb" vertical={false} />
          <XAxis dataKey="bucket_at" tickFormatter={(value) => formatChartTime(Number(value))} minTickGap={30} />
          <YAxis tickFormatter={(value) => formatter(Number(value))} tickMargin={8} width={78} />
          <Tooltip labelFormatter={(value) => formatDate(Number(value))} formatter={(value) => formatter(Number(value))} />
          {lines.map((line) => <Line
            key={line.key}
            type="monotone"
            dataKey={line.key}
            name={line.name}
            stroke={line.color}
            strokeWidth={2}
            strokeDasharray={line.dashed ? "6 4" : undefined}
            dot={false}
            isAnimationActive={false}
          />)}
          {overlay && lines.map((line) => <Line
            key={line.key + "_previous"}
            type="monotone"
            dataKey={line.key + "_previous"}
            name={line.name + "（上一周期）"}
            stroke={line.color}
            strokeOpacity={0.55}
            strokeWidth={1.5}
            strokeDasharray="3 4"
            dot={false}
            isAnimationActive={false}
          />)}
        </LineChart>
      </ResponsiveContainer>
    </div>
    <p>{lines.map((line) => line.name).join(" / ")}</p>
    <details className="chart-data-table">
      <summary>查看数据表</summary>
      <div className="table-wrap"><table>
        <thead><tr><th>时间</th>{lines.map((line) => <th key={line.key}>{line.name}</th>)}</tr></thead>
        <tbody>{data.map((point) => <tr key={point.bucket_at}>
          <td>{formatDate(point.bucket_at)}</td>
          {lines.map((line) => <td key={line.key}>{formatter(point[line.key])}</td>)}
        </tr>)}</tbody>
      </table></div>
    </details>
  </div>;
}

function Pagination({
  page,
  total,
  totalPages,
  onPageChange,
}: {
  page: number;
  total: number;
  totalPages: number;
  onPageChange: (page: number) => void;
}) {
  return <div className="pagination">
    <span>共 {total} 条</span>
    <button className="icon-button" disabled={page <= 1} onClick={() => onPageChange(page - 1)} aria-label="代理汇总上一页"><ChevronLeft size={17} /></button>
    <span>{page} / {Math.max(totalPages, 1)}</span>
    <button className="icon-button" disabled={page >= totalPages} onClick={() => onPageChange(page + 1)} aria-label="代理汇总下一页"><ChevronRight size={17} /></button>
  </div>;
}

function Metric({label, value, delta}: {label: string; value: string | number; delta?: ReactNode}) {
  return <div><span>{label}</span><strong>{value}</strong>{delta}</div>;
}

function countryLabel(kind: string, code: string) {
  if (kind === "private") return "内网地址";
  if (kind === "reserved") return "保留地址";
  if (kind === "invalid") return "非法地址";
  if (kind === "unknown") return "未知";
  if (kind === "other") return "其他国家";
  if (!code) return "-";
  try {
    const names = new Intl.DisplayNames(["zh-CN"], {type: "region"});
    return `${names.of(code) ?? code} (${code})`;
  } catch {
    return code;
  }
}

function countryKey(kind: string, code: string) {
  return `${kind}/${code}`;
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

function formatPercent(value: number) {
  return `${(value * 100).toFixed(1)}%`;
}

function formatDate(value: number) {
  return value > 0 ? new Date(value).toLocaleString() : "-";
}

function formatChartTime(value: number) {
  const date = new Date(value);
  return date.toLocaleString([], {month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit"});
}

function toLocalInput(value: number) {
  if (!Number.isFinite(value) || value <= 0) return "";
  const date = new Date(value);
  const offset = date.getTimezoneOffset() * 60 * 1000;
  return new Date(value - offset).toISOString().slice(0, 16);
}

function emptyRange(): AnalyticsOverview["range"] {
  return {requested: "24h", effective_from: 0, effective_to: 0, resolution_seconds: 3600, timezone: "UTC"};
}

function emptyCompleteness(): AnalyticsOverview["completeness"] {
  return {
    status: "unavailable",
    complete_from: 0,
    statistics_available_from: 0,
    statistics_retention_seconds: 0,
    details_available_from: 0,
    estimated_bytes: 0,
    estimated_ratio: 0,
    geoip_status: "disabled",
    geoip_database_fingerprint: "",
  };
}

function rankChangeLabel(value: AnalyticsComparison["connections_rank_changes"][number] | undefined) {
  if (!value) return "-";
  if (value.status === "new") return "新";
  if (value.status === "same") return "—";
  if (value.status === "up") return `↑${value.rank_change ?? ""}`;
  if (value.status === "down") return `↓${Math.abs(value.rank_change ?? 0)}`;
  return "退出";
}
