"use client";

import {useCallback, useEffect, useRef, useState} from "react";
import {ArrowRight, CircleAlert, CircleCheck} from "lucide-react";
import {api, ApiError} from "@/lib/api";
import type {AttentionResponse, NavigationDestination} from "@/lib/types";

const emptyAttention: AttentionResponse = {
  snapshot_at: 0,
  items: [],
  total: 0,
  counts: {critical: 0, warning: 0, info: 0},
};

export function AttentionList({
  onNavigate,
  onUnauthorized,
}: {
  onNavigate: (destination: NavigationDestination) => void;
  onUnauthorized: () => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const [result, setResult] = useState(emptyAttention);
  const [stale, setStale] = useState(false);
  const controller = useRef<AbortController | null>(null);

  const load = useCallback(async () => {
    controller.current?.abort();
    const request = new AbortController();
    controller.current = request;
    try {
      const value = await api<AttentionResponse>(
        `/api/attention?limit=${expanded ? 20 : 8}`,
        {signal: request.signal},
      );
      setResult(value);
      setStale(false);
    } catch (caught) {
      if (request.signal.aborted) return;
      if (caught instanceof ApiError && caught.status === 401) onUnauthorized();
      setStale(true);
    } finally {
      if (controller.current === request) controller.current = null;
    }
  }, [expanded, onUnauthorized]);

  useEffect(() => {
    const initial = window.setTimeout(() => void load(), 0);
    let timer: number | undefined;
    const schedule = () => {
      if (document.visibilityState === "visible") {
        timer = window.setInterval(() => void load(), 10000);
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
      window.clearTimeout(initial);
      if (timer) window.clearInterval(timer);
      controller.current?.abort();
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [load]);

  return <section className="section-block attention-section">
    <div className="section-heading">
      <div><h2>需要处理</h2><p>当前运行状态中需要关注的事项</p></div>
      <div className="attention-counts">
        {result.counts.critical > 0 && <span className="critical">{result.counts.critical} 严重</span>}
        {result.counts.warning > 0 && <span className="warning">{result.counts.warning} 警告</span>}
        {result.counts.info > 0 && <span>{result.counts.info} 提示</span>}
      </div>
    </div>
    {stale && <div className="stale-banner">状态摘要刷新失败，当前内容可能已过期</div>}
    <div className="attention-list">
      {!result.items.length && <div className="attention-empty"><CircleCheck size={18} />当前没有需要处理的问题</div>}
      {result.items.map((item) => <button
        key={item.id}
        className={`attention-row severity-${item.severity}`}
        onClick={() => onNavigate(item.destination)}
      >
        <CircleAlert size={18} />
        <span className="grow"><strong>{item.title}</strong><small>{item.summary}</small></span>
        <time>{formatAge(item.first_observed_at)}</time>
        <ArrowRight size={17} />
      </button>)}
    </div>
    {result.total > 8 && <button className="attention-expand" onClick={() => setExpanded((value) => !value)}>
      {expanded ? "收起" : `展开更多（${Math.min(12, result.total - 8)}）`}
    </button>}
    {expanded && result.total > 20 && <div className="attention-remaining">还有 {result.total - 20} 项未展开</div>}
  </section>;
}

function formatAge(value: number) {
  const seconds = Math.max(0, Math.floor((Date.now() - value) / 1000));
  if (seconds < 60) return "刚刚";
  if (seconds < 3600) return `${Math.floor(seconds / 60)} 分钟`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} 小时`;
  return `${Math.floor(seconds / 86400)} 天`;
}
