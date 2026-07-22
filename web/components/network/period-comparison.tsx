"use client";

import {ArrowDown, ArrowUp, Minus, ShieldAlert} from "lucide-react";
import type {AnalyticsComparison, MetricChange, RankChange} from "@/lib/types";

type RankingHighlight = RankChange & {metric: "连接" | "流量"};

export function PeriodComparison({
  comparison,
  unavailable = false,
  overlay,
  onOverlayChange,
}: {
  comparison: AnalyticsComparison | null;
  unavailable?: boolean;
  overlay: boolean;
  onOverlayChange: (value: boolean) => void;
}) {
  if (!comparison) {
    return <section className="insight-section comparison-section">
      <div className="section-heading"><div><h2>变化提示</h2><p>{unavailable ? "上一周期暂不可用" : "正在计算上一周期"}</p></div></div>
      {unavailable
        ? <div className="insight-note">周期对比请求失败，当前周期数据仍可使用</div>
        : <div className="comparison-placeholder" />}
    </section>;
  }
  const rankingHighlights = rankHighlights(comparison);
  const hasChanges = comparison.proxy_changes.length > 0 || comparison.new_source_count > 0 ||
    comparison.new_countries.length > 0 || rankingHighlights.length > 0;
  return <section className="insight-section comparison-section">
    <div className="section-heading">
      <div><h2>变化提示</h2><p>当前范围与紧邻的上一周期</p></div>
      <label className="toggle-setting comparison-overlay">
        <span>叠加上一周期</span>
        <span className="switch"><input type="checkbox" checked={overlay} onChange={(event) => onOverlayChange(event.target.checked)} /><span /></span>
      </label>
    </div>
    {!comparison.comparable ? <div className="insight-note">
      {comparisonReason(comparison.reason)}
    </div> : <div className="change-groups">
      {comparison.proxy_changes.length > 0 && <div className="change-group">
        <h3>代理变化</h3>
        {comparison.proxy_changes.map((item) => <div className="change-row" key={`${item.client_id}/${item.proxy_id}/${item.protocol}`}>
          <ShieldAlert size={17} />
          <span><strong>{item.client_id} / {item.proxy_id}</strong><small>{item.signals.map(signalLabel).join(" · ")}</small></span>
        </div>)}
      </div>}
      {comparison.new_source_count > 0 && <div className="change-group">
        <h3>新来源 <span>{comparison.new_source_count}</span></h3>
        {comparison.new_sources.map((item) => <div className="change-row" key={item.source_ip}>
          <span className="mono">{item.source_ip}</span>
          <small>{item.connections} 次 · {formatBytes(item.up_bytes + item.down_bytes)}</small>
        </div>)}
      </div>}
      {comparison.new_countries.length > 0 && <div className="change-group">
        <h3>新国家</h3>
        <div className="country-chips">{comparison.new_countries.map((country) => <span key={country}>{country}</span>)}</div>
      </div>}
      {rankingHighlights.length > 0 && <div className="change-group">
        <h3>排名变化</h3>
        {rankingHighlights.map((item) => <div className="change-row" key={`${item.metric}/${item.source_ip}`}>
          <span className="mono"><strong>{item.source_ip}</strong><small>{item.metric} · {rankChangeLabel(item)}</small></span>
        </div>)}
      </div>}
      {!hasChanges && <div className="attention-empty"><Minus size={17} />当前周期没有显著变化</div>}
    </div>}
  </section>;
}

export function MetricDelta({
  change,
  tone = "neutral",
}: {
  change?: MetricChange;
  tone?: "neutral" | "failure" | "success";
}) {
  if (!change) return <small className="metric-delta">较上一周期 -</small>;
  if (change.state === "new") return <small className="metric-delta">上周期无活动</small>;
  const Arrow = change.absolute_change > 0 ? ArrowUp : change.absolute_change < 0 ? ArrowDown : Minus;
  const percent = change.relative_change === null
    ? formatSigned(change.absolute_change)
    : `${change.relative_change > 0 ? "+" : ""}${(change.relative_change * 100).toFixed(1)}%`;
  const className = tone === "failure"
    ? change.absolute_change > 0 ? "danger" : change.absolute_change < 0 ? "success" : ""
    : tone === "success" ? "success" : "";
  return <small className={`metric-delta ${className}`}><Arrow size={12} />较上一周期 {percent}{change.estimated ? "（估算）" : ""}</small>;
}

function rankHighlights(comparison: AnalyticsComparison): RankingHighlight[] {
  return [
    ...comparison.connections_rank_changes.map((item) => ({...item, metric: "连接" as const})),
    ...comparison.traffic_rank_changes.map((item) => ({...item, metric: "流量" as const})),
  ].filter((item) => item.status !== "same").slice(0, 8);
}

function rankChangeLabel(value: RankChange) {
  switch (value.status) {
  case "new": return `新入榜 #${value.current_rank ?? "-"}`;
  case "departed": return `离榜（上期 #${value.previous_rank ?? "-"}）`;
  case "up": return `上升 ${value.rank_change ?? 0} 位至 #${value.current_rank ?? "-"}`;
  case "down": return `下降 ${Math.abs(value.rank_change ?? 0)} 位至 #${value.current_rank ?? "-"}`;
  default: return `保持 #${value.current_rank ?? "-"}`;
  }
}

function signalLabel(value: string) {
  return {
    traffic_spike: "流量显著上升",
    connection_spike: "连接数显著上升",
    failure_rate_spike: "失败率显著上升",
  }[value] ?? value;
}

function comparisonReason(value: string) {
  return {
    previous_outside_retention: "上一周期已超出统计保留期，当前数据仍可查看",
    analytics_not_ready: "历史统计尚未就绪，暂不显示周期变化",
    current_incomplete: "当前周期统计尚不完整",
    previous_incomplete: "上一周期统计尚不完整",
  }[value] ?? "周期对比暂不可用";
}

function formatSigned(value: number) {
  return `${value > 0 ? "+" : ""}${value.toFixed(2)}`;
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
