"use client";

import {useCallback, useEffect, useState} from "react";
import {
  Activity,
  Cable,
  CircleGauge,
  LogOut,
  Menu,
  Network,
  RefreshCw,
  Search,
  Users,
  X,
} from "lucide-react";
import {api, ApiError} from "@/lib/api";
import type {
  AdminSession,
  AuditEvent,
  ClientFile,
  ClientList,
  NavigationDestination,
  Page,
  Summary,
} from "@/lib/types";
import {OverviewPage} from "@/components/overview/overview-page";
import {ClientPage} from "@/components/clients/client-page";
import {ProxyPage} from "@/components/proxies/proxy-page";
import {NetworkPage} from "@/components/network/network-page";
import {AuditPage} from "@/components/audit/audit-page";
import {GlobalSearch} from "@/components/search/global-search";

type View = "overview" | "clients" | "proxies" | "network" | "audit";

const nav: Array<{id: View; label: string; icon: typeof Activity}> = [
  {id: "overview", label: "总览", icon: CircleGauge},
  {id: "clients", label: "客户端", icon: Users},
  {id: "proxies", label: "代理规则", icon: Network},
  {id: "network", label: "网络活动", icon: Cable},
  {id: "audit", label: "事件与审计", icon: Activity},
];
const views = new Set<View>(nav.map((item) => item.id));
const emptyClients: ClientList = {revision: 0, items: [], clients: []};

function viewFromURL(): View {
  if (typeof window === "undefined") return "overview";
  const value = new URLSearchParams(window.location.search).get("view") as View | null;
  return value && views.has(value) ? value : "overview";
}

export default function Home() {
  const [checking, setChecking] = useState(true);
  const [session, setSession] = useState<AdminSession | null>(null);
  const [view, setView] = useState<View>(viewFromURL);
  const [navigationKey, setNavigationKey] = useState(0);
  const [searchOpen, setSearchOpen] = useState(false);
  const [mobileNav, setMobileNav] = useState(false);
  const [clients, setClients] = useState<ClientList>(emptyClients);
  const [config, setConfig] = useState<ClientFile | null>(null);
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [summary, setSummary] = useState<Summary | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  const clearSession = useCallback(() => {
    setSession(null);
    setConfig(null);
    setClients(emptyClients);
    setEvents([]);
  }, []);

  const refresh = useCallback(async () => {
    const [clientData, configData, eventData, summaryData] = await Promise.all([
      api<ClientList>("/api/clients"),
      api<ClientFile>("/api/config"),
      api<Page<AuditEvent>>("/api/events?page=1&page_size=25"),
      api<Summary>("/api/metrics/summary"),
    ]);
    setClients({...clientData, items: clientData.items ?? clientData.clients ?? []});
    setConfig(configData);
    setEvents(eventData.items.slice(0, 8));
    setSummary(summaryData);
  }, []);

  const navigate = useCallback((destination: NavigationDestination, replace = false) => {
    const params = new URLSearchParams();
    params.set("view", destination.view);
    for (const [key, value] of Object.entries(destination.params)) {
      if (value) params.set(key, value);
    }
    const target = `${window.location.pathname}?${params}`;
    if (replace) window.history.replaceState(null, "", target);
    else window.history.pushState(null, "", target);
    setView(destination.view);
    setNavigationKey((value) => value + 1);
    setMobileNav(false);
  }, []);

  useEffect(() => {
    const sync = () => {
      setView(viewFromURL());
      setNavigationKey((value) => value + 1);
    };
    window.addEventListener("popstate", sync);
    if (!new URLSearchParams(window.location.search).has("view")) {
      window.history.replaceState(null, "", `?view=overview`);
    }
    return () => window.removeEventListener("popstate", sync);
  }, []);

  useEffect(() => {
    const shortcut = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setSearchOpen(true);
      }
    };
    window.addEventListener("keydown", shortcut);
    return () => window.removeEventListener("keydown", shortcut);
  }, []);

  useEffect(() => {
    api<AdminSession>("/api/auth/me")
      .then(async (me) => {
        setSession(me);
        await refresh();
      })
      .catch(() => undefined)
      .finally(() => setChecking(false));
  }, [refresh]);

  useEffect(() => {
    const label = nav.find((item) => item.id === view)?.label ?? "PortPilot";
    document.title = `${label} · PortPilot`;
  }, [view]);

  const login = async (userName: string, password: string, sessionTTL: string) => {
    setBusy(true);
    setError("");
    try {
      const result = await api<AdminSession>("/api/auth/login", {
        method: "POST",
        body: JSON.stringify({user_name: userName, password, session_ttl: sessionTTL}),
      });
      setSession(result);
      await refresh();
    } catch (caught) {
      setError(caught instanceof ApiError && caught.status === 401 ? "用户名或密码不正确" : "无法登录管理服务");
    } finally {
      setBusy(false);
    }
  };

  const logout = async () => {
    if (!session) return;
    try {
      await api("/api/auth/logout", {method: "POST", csrf: session.csrf_token});
    } finally {
      clearSession();
    }
  };

  const saveConfig = async (next: ClientFile) => {
    if (!config || !session) return;
    setBusy(true);
    setError("");
    try {
      const updated = await api<ClientFile>("/api/config", {
        method: "PUT",
        csrf: session.csrf_token,
        body: JSON.stringify({expected_revision: config.revision, config: next}),
      });
      setConfig(updated);
      await refresh();
    } catch (caught) {
      if (caught instanceof ApiError && caught.status === 409) {
        setError("配置已被其他会话更新，已重新载入最新版本");
        await refresh();
      } else if (caught instanceof ApiError && caught.status === 401) {
        clearSession();
      } else {
        setError(caught instanceof ApiError ? caught.message : "保存配置失败");
      }
      throw caught;
    } finally {
      setBusy(false);
    }
  };

  if (checking) return <div className="center-state"><RefreshCw className="spin" aria-label="加载中" /></div>;
  if (!session) return <Login busy={busy} error={error} onSubmit={login} />;

  const currentNav = nav.find((item) => item.id === view)!;
  return (
    <div className="app-shell">
      <aside className={mobileNav ? "sidebar sidebar-open" : "sidebar"}>
        <div className="brand">
          <div className="brand-mark"><Cable size={19} /></div>
          <span>PortPilot</span>
          <button className="icon-button mobile-only" onClick={() => setMobileNav(false)} aria-label="关闭导航"><X /></button>
        </div>
        <nav>
          {nav.map((item) => {
            const Icon = item.icon;
            return <button
              key={item.id}
              className={view === item.id ? "nav-item active" : "nav-item"}
              onClick={() => navigate({view: item.id, params: {}})}
            ><Icon size={18} />{item.label}</button>;
          })}
        </nav>
        <div className="sidebar-footer">
          <div className="user-session"><span className="user-label">{session.user_name}</span><small title={new Date(session.expires_at).toISOString()}>会话 {session.session_ttl}</small></div>
          <button className="icon-button" onClick={() => void logout()} title="退出登录" aria-label="退出登录"><LogOut size={18} /></button>
        </div>
      </aside>
      <main>
        <header className="topbar">
          <button className="icon-button mobile-only" onClick={() => setMobileNav(true)} aria-label="打开导航"><Menu /></button>
          <div><h1>{currentNav.label}</h1><p>配置版本 {config?.revision ?? "-"}</p></div>
          <button className="icon-button" onClick={() => setSearchOpen(true)} title="全局搜索" aria-label="全局搜索"><Search size={18} /></button>
          {view !== "network" && view !== "audit" && <button className="icon-button" onClick={() => void refresh()} title="刷新" aria-label="刷新"><RefreshCw size={18} /></button>}
        </header>
        {error && <div className="error-banner">{error}<button onClick={() => setError("")} aria-label="关闭"><X size={16} /></button></div>}
        <div className="workspace">
          {view === "overview" && <OverviewPage
            key={navigationKey}
            summary={summary}
            clients={clients.items}
            events={events}
            onNavigate={navigate}
            onUnauthorized={clearSession}
          />}
          {view === "clients" && <ClientPage
            key={navigationKey}
            data={clients}
            csrf={session.csrf_token}
            onRefresh={refresh}
            onError={setError}
          />}
          {view === "proxies" && config && <ProxyPage
            key={navigationKey}
            config={config}
            busy={busy}
            onUnauthorized={clearSession}
            onSave={saveConfig}
            onNavigate={navigate}
          />}
          {view === "network" && <NetworkPage
            key={navigationKey}
            csrf={session.csrf_token}
            onUnauthorized={clearSession}
          />}
          {view === "audit" && <AuditPage
            key={navigationKey}
            csrf={session.csrf_token}
            onUnauthorized={clearSession}
          />}
        </div>
      </main>
      <GlobalSearch
        open={searchOpen}
        onOpenChange={setSearchOpen}
        onNavigate={navigate}
        onUnauthorized={clearSession}
      />
    </div>
  );
}

function Login({
  busy,
  error,
  onSubmit,
}: {
  busy: boolean;
  error: string;
  onSubmit: (user: string, pass: string, ttl: string) => Promise<void>;
}) {
  const [user, setUser] = useState("");
  const [password, setPassword] = useState("");
  const [ttl, setTTL] = useState("30m");
  return (
    <main className="login-page">
      <form className="login-form" onSubmit={(event) => {event.preventDefault(); void onSubmit(user, password, ttl);}}>
        <div className="login-brand"><div className="brand-mark"><Cable size={21} /></div><strong>PortPilot</strong></div>
        <div><h1>管理控制台</h1><p>使用管理员凭据继续</p></div>
        {error && <div className="form-error">{error}</div>}
        <label>用户名<input autoComplete="username" value={user} onChange={(event) => setUser(event.target.value)} required /></label>
        <label>密码<input type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} required /></label>
        <fieldset className="ttl-field">
          <legend>登录有效期</legend>
          <div className="segmented">
            {[["10m", "10 分钟"], ["30m", "30 分钟"], ["1h", "1 小时"], ["24h", "24 小时"]].map(([value, label]) => <label key={value} className={ttl === value ? "selected" : ""}><input type="radio" name="session-ttl" value={value} checked={ttl === value} onChange={() => setTTL(value)} />{label}</label>)}
          </div>
        </fieldset>
        <button className="primary-button" disabled={busy}>{busy ? "正在验证..." : "登录"}</button>
      </form>
    </main>
  );
}
