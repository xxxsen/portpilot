"use client";

import {useEffect, useMemo, useRef, useState} from "react";
import {Activity, Cable, Network, Search, Server, Users} from "lucide-react";
import {api, ApiError} from "@/lib/api";
import type {NavigationDestination, SearchItem, SearchResponse} from "@/lib/types";
import {Dialog} from "@/components/ui/dialog";

const typeOrder: SearchItem["type"][] = ["client", "proxy", "source_ip", "activity"];
const typeLabels: Record<SearchItem["type"], string> = {
  client: "客户端",
  proxy: "代理",
  source_ip: "来源 IP",
  activity: "连接",
};
const typeIcons = {
  client: Users,
  proxy: Network,
  source_ip: Cable,
  activity: Activity,
};

export function GlobalSearch({
  open,
  onOpenChange,
  onNavigate,
  onUnauthorized,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onNavigate: (destination: NavigationDestination) => void;
  onUnauthorized: () => void;
}) {
  const [query, setQuery] = useState("");
  const [result, setResult] = useState<SearchResponse | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const flatItems = useMemo(() => result?.items ?? [], [result]);

  useEffect(() => {
    if (!open) return;
    const timer = window.setTimeout(() => inputRef.current?.focus(), 0);
    return () => window.clearTimeout(timer);
  }, [open]);

  useEffect(() => {
    if (!open || query.trim().length < 2) return;
    const controller = new AbortController();
    const timer = window.setTimeout(() => {
      setLoading(true);
      setError("");
      void api<SearchResponse>(`/api/search?q=${encodeURIComponent(query.trim())}&limit=20`, {
        signal: controller.signal,
      }).then((value) => {
        setResult(value);
        setSelected(0);
      }).catch((caught) => {
        if (controller.signal.aborted) return;
        if (caught instanceof ApiError && caught.status === 401) onUnauthorized();
        else if (caught instanceof ApiError && caught.status === 429) setError("搜索请求过于频繁，请稍后重试");
        else setError("搜索暂时不可用");
      }).finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    }, 200);
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [onUnauthorized, open, query]);

  const changeOpen = (value: boolean) => {
    if (!value) {
      setQuery("");
      setResult(null);
      setLoading(false);
      setError("");
      setSelected(0);
    }
    onOpenChange(value);
  };

  const groups = useMemo(() => typeOrder.map((type) => ({
    type,
    items: flatItems.filter((item) => item.type === type),
  })).filter((group) => group.items.length), [flatItems]);

  const selectItem = (item: SearchItem) => {
    onNavigate(item.destination);
    changeOpen(false);
  };

  return <Dialog
    open={open}
    onOpenChange={changeOpen}
    title="全局搜索"
    description="客户端、代理、来源 IP 与连接"
  >
    <div className="global-search">
      <label className="search-control global-search-input">
        <Search size={18} />
        <input
          ref={inputRef}
          value={query}
          maxLength={128}
          aria-label="全局搜索"
          placeholder="输入名称、地址或活动 ID"
          onChange={(event) => {
            const value = event.target.value;
            setQuery(value);
            if (value.trim().length < 2) {
              setResult(null);
              setLoading(false);
              setError("");
              setSelected(0);
            }
          }}
          onKeyDown={(event) => {
            if (event.key === "ArrowDown") {
              event.preventDefault();
              setSelected((value) => Math.min(flatItems.length - 1, value + 1));
            } else if (event.key === "ArrowUp") {
              event.preventDefault();
              setSelected((value) => Math.max(0, value - 1));
            } else if (event.key === "Enter" && flatItems[selected]) {
              event.preventDefault();
              selectItem(flatItems[selected]);
            }
          }}
        />
      </label>
      <div className="search-results" aria-live="polite">
        {loading && <SearchState icon={Search} label="正在搜索" />}
        {!loading && error && <SearchState icon={Server} label={error} />}
        {!loading && !error && query.trim().length < 2 && <SearchState icon={Search} label="输入至少 2 个字符" />}
        {!loading && !error && query.trim().length >= 2 && result && !flatItems.length && <SearchState icon={Search} label="没有匹配对象" />}
        {!loading && !error && groups.map((group) => <section key={group.type} className="search-group">
          <h3>{typeLabels[group.type]}</h3>
          {group.items.map((item) => {
            const Icon = typeIcons[item.type];
            const index = flatItems.indexOf(item);
            return <button
              key={`${item.type}:${item.id}`}
              className={index === selected ? "search-result selected" : "search-result"}
              onMouseEnter={() => setSelected(index)}
              onClick={() => selectItem(item)}
            >
              <Icon size={17} />
              <span><strong>{item.title}</strong><small>{item.subtitle}</small></span>
            </button>;
          })}
        </section>)}
      </div>
      {result?.partial && <div className="search-partial">历史数据暂不可用，已显示配置结果</div>}
    </div>
  </Dialog>;
}

function SearchState({icon: Icon, label}: {icon: typeof Search; label: string}) {
  return <div className="search-state"><Icon size={20} /><span>{label}</span></div>;
}
