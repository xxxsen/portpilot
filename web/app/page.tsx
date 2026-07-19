"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Activity,
  Cable,
  CircleGauge,
  KeyRound,
  LogOut,
  Menu,
  Network,
  Plus,
  RefreshCw,
  Server,
  Settings2,
  Trash2,
  Users,
  X,
} from "lucide-react";
import { ApiError, api } from "@/lib/api";
import { validateProxyDraft } from "@/lib/validation";
import type {
  ClientFile,
  ClientView,
  EventItem,
  Proxy,
  Summary,
  TunnelData,
} from "@/lib/types";

type View = "overview" | "clients" | "proxies" | "activity";

const nav: Array<{id: View; label: string; icon: typeof Activity}> = [
  {id: "overview", label: "总览", icon: CircleGauge},
  {id: "clients", label: "客户端", icon: Users},
  {id: "proxies", label: "代理规则", icon: Network},
  {id: "activity", label: "活动记录", icon: Activity},
];

export default function Home() {
  const [checking, setChecking] = useState(true);
  const [user, setUser] = useState("");
  const [csrf, setCSRF] = useState("");
  const [view, setView] = useState<View>("overview");
  const [mobileNav, setMobileNav] = useState(false);
  const [clients, setClients] = useState<ClientView[]>([]);
  const [config, setConfig] = useState<ClientFile | null>(null);
  const [events, setEvents] = useState<EventItem[]>([]);
  const [summary, setSummary] = useState<Summary | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [editor, setEditor] = useState<{clientId: string; proxy?: Proxy} | null>(null);

  const refresh = useCallback(async () => {
    const [clientData, configData, eventData, summaryData] = await Promise.all([
      api<{clients: ClientView[]}>("/api/clients"),
      api<ClientFile>("/api/config"),
      api<{events: EventItem[]}>("/api/events?limit=100"),
      api<Summary>("/api/metrics/summary"),
    ]);
    setClients(clientData.clients);
    setConfig(configData);
    setEvents(eventData.events ?? []);
    setSummary(summaryData);
  }, []);

  useEffect(() => {
    api<{user_name: string; csrf_token: string}>("/api/auth/me")
      .then(async (me) => {
        setUser(me.user_name);
        setCSRF(me.csrf_token);
        await refresh();
      })
      .catch(() => undefined)
      .finally(() => setChecking(false));
  }, [refresh]);

  const login = async (userName: string, password: string) => {
    setBusy(true);
    setError("");
    try {
      const result = await api<{user_name: string; csrf_token: string}>("/api/auth/login", {
        method: "POST",
        body: JSON.stringify({user_name: userName, password}),
      });
      setUser(result.user_name);
      setCSRF(result.csrf_token);
      await refresh();
    } catch (caught) {
      setError(caught instanceof ApiError ? "用户名或密码不正确" : "无法连接管理服务");
    } finally {
      setBusy(false);
    }
  };

  const logout = async () => {
    await api("/api/auth/logout", {method: "POST", csrf});
    setUser("");
    setCSRF("");
    setConfig(null);
  };

  const saveConfig = async (next: ClientFile) => {
    if (!config) return;
    setBusy(true);
    setError("");
    try {
      const updated = await api<ClientFile>("/api/config", {
        method: "PUT",
        csrf,
        body: JSON.stringify({expected_revision: config.revision, config: next}),
      });
      setConfig(updated);
      await refresh();
    } catch (caught) {
      if (caught instanceof ApiError && caught.status === 409) {
        setError("配置已被其他会话更新，已重新载入最新版本");
        await refresh();
      } else {
        setError(caught instanceof ApiError ? caught.message : "保存配置失败");
      }
      throw caught;
    } finally {
      setBusy(false);
    }
  };

  if (checking) {
    return <div className="center-state"><RefreshCw className="spin" aria-label="加载中" /></div>;
  }
  if (!user) {
    return <Login busy={busy} error={error} onSubmit={login} />;
  }

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
            return (
              <button
                key={item.id}
                className={view === item.id ? "nav-item active" : "nav-item"}
                onClick={() => { setView(item.id); setMobileNav(false); }}
              >
                <Icon size={18} /> {item.label}
              </button>
            );
          })}
        </nav>
        <div className="sidebar-footer">
          <span className="user-label">{user}</span>
          <button className="icon-button" onClick={logout} title="退出登录" aria-label="退出登录"><LogOut size={18} /></button>
        </div>
      </aside>
      <main>
        <header className="topbar">
          <button className="icon-button mobile-only" onClick={() => setMobileNav(true)} aria-label="打开导航"><Menu /></button>
          <div>
            <h1>{currentNav.label}</h1>
            <p>配置版本 {config?.revision ?? "-"}</p>
          </div>
          <button className="icon-button" onClick={() => void refresh()} title="刷新" aria-label="刷新"><RefreshCw size={18} /></button>
        </header>
        {error && <div className="error-banner">{error}<button onClick={() => setError("")} aria-label="关闭"><X size={16} /></button></div>}
        <div className="workspace">
          {view === "overview" && <Overview summary={summary} clients={clients} events={events} />}
          {view === "clients" && config && (
            <Clients
              clients={clients}
              config={config}
              busy={busy}
              onSave={saveConfig}
              onDisconnect={async (id) => {
                await api(`/api/clients/${encodeURIComponent(id)}/disconnect`, {method: "POST", csrf});
                await refresh();
              }}
            />
          )}
          {view === "proxies" && config && (
            <Proxies
              config={config}
              busy={busy}
              onEdit={(clientId, proxy) => setEditor({clientId, proxy})}
              onCreate={(clientId) => setEditor({clientId})}
              onSave={saveConfig}
            />
          )}
          {view === "activity" && <ActivityView events={events} />}
        </div>
      </main>
      {editor && config && (
        <ProxyEditor
          clientId={editor.clientId}
          proxy={editor.proxy}
          busy={busy}
          existing={config.client_config.flatMap((client) => client.proxy_list)}
          onClose={() => setEditor(null)}
          onSave={async (proxy) => {
            const next = structuredClone(config);
            const client = next.client_config.find((item) => item.client_id === editor.clientId)!;
            const index = client.proxy_list.findIndex((item) => item.proxy_id === proxy.proxy_id);
            if (index >= 0) client.proxy_list[index] = proxy;
            else client.proxy_list.push(proxy);
            await saveConfig(next);
            setEditor(null);
          }}
        />
      )}
    </div>
  );
}

function Login({busy, error, onSubmit}: {busy: boolean; error: string; onSubmit: (user: string, pass: string) => Promise<void>}) {
  const [user, setUser] = useState("");
  const [password, setPassword] = useState("");
  return (
    <main className="login-page">
      <form className="login-form" onSubmit={(event) => {event.preventDefault(); void onSubmit(user, password);}}>
        <div className="login-brand"><div className="brand-mark"><Cable size={21} /></div><strong>PortPilot</strong></div>
        <div><h1>管理控制台</h1><p>使用管理员凭据继续</p></div>
        {error && <div className="form-error">{error}</div>}
        <label>用户名<input autoComplete="username" value={user} onChange={(event) => setUser(event.target.value)} required /></label>
        <label>密码<input type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} required /></label>
        <button className="primary-button" disabled={busy}>{busy ? "正在验证..." : "登录"}</button>
      </form>
    </main>
  );
}

function Overview({summary, clients, events}: {summary: Summary | null; clients: ClientView[]; events: EventItem[]}) {
  const stats = [
    {label: "在线客户端", value: summary?.online_clients ?? 0, icon: Users},
    {label: "代理监听", value: summary?.listeners ?? 0, icon: Network},
    {label: "活动隧道", value: summary?.active_tunnels ?? 0, icon: Activity},
    {label: "等待建链", value: summary?.pending_tunnels ?? 0, icon: Server},
    {label: "UDP 通道", value: summary?.udp_channels ?? 0, icon: Cable},
    {label: "UDP Flow", value: summary?.udp_flows ?? 0, icon: Network},
  ];
  return (
    <>
      <section className="stat-grid">
        {stats.map((item) => <div className="stat" key={item.label}><item.icon size={18} /><span>{item.label}</span><strong>{item.value}</strong></div>)}
      </section>
      <section className="section-block">
        <div className="section-heading"><div><h2>客户端状态</h2><p>控制会话与 smux 池</p></div></div>
        <div className="table-wrap"><table><thead><tr><th>客户端</th><th>状态</th><th>Epoch</th><th>smux</th><th>活动流</th></tr></thead><tbody>
          {clients.map((client) => <tr key={client.client_id}><td className="strong-cell">{client.client_id}</td><td><Status value={client.state} /></td><td>{client.epoch || "-"}</td><td>{client.mux_sessions}</td><td>{client.active_streams}</td></tr>)}
        </tbody></table></div>
      </section>
      <section className="section-block">
        <div className="section-heading"><div><h2>最近活动</h2><p>最新连接和管理事件</p></div></div>
        <EventRows events={events.slice(0, 8)} />
      </section>
    </>
  );
}

function createClientToken() {
  const bytes = crypto.getRandomValues(new Uint8Array(32));
  return btoa(String.fromCharCode(...bytes)).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
}

function Clients({clients, config, busy, onSave, onDisconnect}: {
  clients: ClientView[];
  config: ClientFile;
  busy: boolean;
  onSave: (next: ClientFile) => Promise<void>;
  onDisconnect: (id: string) => Promise<void>;
}) {
  const addClient = async () => {
    const id = window.prompt("客户端 ID");
    if (!id) return;
    const token = createClientToken();
    const next = structuredClone(config);
    next.client_config.push({client_id: id, client_token: token, enabled: true, proxy_list: []});
    await onSave(next);
    window.prompt("客户端 Token（仅显示一次）", token);
  };
  const toggle = async (id: string) => {
    const next = structuredClone(config);
    const item = next.client_config.find((client) => client.client_id === id)!;
    item.enabled = !item.enabled;
    await onSave(next);
  };
  const rotateToken = async (id: string) => {
    if (!window.confirm("轮换 " + id + " 的 Token？当前连接将立即断开。")) return;
    const token = createClientToken();
    const next = structuredClone(config);
    next.client_config.find((client) => client.client_id === id)!.client_token = token;
    await onSave(next);
    window.prompt("新客户端 Token（仅显示一次）", token);
  };
  return (
    <section className="section-block first">
      <div className="section-heading"><div><h2>受控客户端</h2><p>凭据、会话和代理归属</p></div><button className="command-button" onClick={() => void addClient()} disabled={busy}><Plus size={17} />新增客户端</button></div>
      <div className="list">
        {clients.map((client) => (
          <div className="client-row" key={client.client_id}>
            <div className="client-icon"><Server size={19} /></div>
            <div className="grow"><div className="row-title"><strong>{client.client_id}</strong><Status value={client.state} /></div><p>{client.proxies.length} 条代理 · epoch {client.epoch || "-"}</p></div>
            <label className="switch" title="启用客户端"><input type="checkbox" checked={client.enabled} onChange={() => void toggle(client.client_id)} /><span /></label>
            <button className="icon-button" disabled={busy} onClick={() => void rotateToken(client.client_id)} title="轮换 Token" aria-label={"轮换 " + client.client_id + " Token"}><KeyRound size={17} /></button>
            {client.state !== "OFFLINE" && client.state !== "DISABLED" && <button className="icon-button danger" onClick={() => void onDisconnect(client.client_id)} title="断开连接" aria-label="断开连接"><X size={17} /></button>}
          </div>
        ))}
      </div>
    </section>
  );
}

function Proxies({config, busy, onEdit, onCreate, onSave}: {
  config: ClientFile;
  busy: boolean;
  onEdit: (clientId: string, proxy: Proxy) => void;
  onCreate: (clientId: string) => void;
  onSave: (next: ClientFile) => Promise<void>;
}) {
  const rows = useMemo(() => config.client_config.flatMap((client) => client.proxy_list.map((proxy) => ({client, proxy}))), [config]);
  const remove = async (clientId: string, proxyId: string) => {
    if (!window.confirm(`删除代理 ${proxyId}？`)) return;
    const next = structuredClone(config);
    const client = next.client_config.find((item) => item.client_id === clientId)!;
    client.proxy_list = client.proxy_list.filter((item) => item.proxy_id !== proxyId);
    await onSave(next);
  };
  const toggle = async (clientId: string, proxyId: string) => {
    const next = structuredClone(config);
    const proxy = next.client_config.find((item) => item.client_id === clientId)!.proxy_list.find((item) => item.proxy_id === proxyId)!;
    proxy.enabled = !proxy.enabled;
    await onSave(next);
  };
  return (
    <section className="section-block first">
      <div className="section-heading"><div><h2>代理监听</h2><p>服务端端口到客户端内网目标</p></div>
        <div className="select-action"><select id="proxy-client"><option value="">选择客户端</option>{config.client_config.map((client) => <option key={client.client_id}>{client.client_id}</option>)}</select><button className="icon-button primary-icon" disabled={busy} onClick={() => {const select = document.querySelector<HTMLSelectElement>("#proxy-client"); if (select?.value) onCreate(select.value);}} title="新增代理" aria-label="新增代理"><Plus size={18} /></button></div>
      </div>
      <div className="table-wrap"><table><thead><tr><th>代理</th><th>客户端</th><th>协议</th><th>服务端监听</th><th>内网目标</th><th>承载</th><th>启用</th><th /></tr></thead><tbody>
        {rows.map(({client, proxy}) => {
          const data = proxy.data;
          return <tr key={proxy.proxy_id}><td><button className="link-button" onClick={() => onEdit(client.client_id, proxy)}>{proxy.remark || proxy.proxy_id}</button><small>{proxy.proxy_id}</small></td><td>{client.client_id}</td><td><span className={`network-tag ${data.network}`}>{data.network.toUpperCase()}</span></td><td className="mono">{data.bind}</td><td className="mono">{data.connect}</td><td>{data.transport}</td><td><label className="switch"><input type="checkbox" checked={proxy.enabled} onChange={() => void toggle(client.client_id, proxy.proxy_id)} /><span /></label></td><td><button className="icon-button danger" onClick={() => void remove(client.client_id, proxy.proxy_id)} title="删除代理" aria-label="删除代理"><Trash2 size={17} /></button></td></tr>;
        })}
      </tbody></table></div>
    </section>
  );
}

function ProxyEditor({clientId, proxy, busy, existing, onClose, onSave}: {clientId: string; proxy?: Proxy; busy: boolean; existing: Proxy[]; onClose: () => void; onSave: (proxy: Proxy) => Promise<void>}) {
  const initial = proxy?.data ?? {network: "tcp", transport: "auto", bind: "127.0.0.1:7000", connect: "127.0.0.1:80"};
  const [proxyId, setProxyId] = useState(proxy?.proxy_id ?? "");
  const [remark, setRemark] = useState(proxy?.remark ?? "");
  const [data, setData] = useState<TunnelData>(initial);
  const [validationError, setValidationError] = useState("");
  return (
    <div className="modal-backdrop" onMouseDown={(event) => {if (event.currentTarget === event.target) onClose();}}>
      <form className="modal" onSubmit={(event) => {
        event.preventDefault();
        const draft: Proxy = {proxy_id: proxyId, remark, kind: "tunnel", enabled: proxy?.enabled ?? true, data};
        const issue = validateProxyDraft(draft, existing, proxy?.proxy_id);
        if (issue) { setValidationError(issue); return; }
        setValidationError("");
        void onSave(draft);
      }}>
        <div className="modal-header"><div><h2>{proxy ? "编辑代理" : "新增代理"}</h2><p>{clientId}</p></div><button type="button" className="icon-button" onClick={onClose} aria-label="关闭"><X /></button></div>
        <div className="form-grid">
          <label>代理 ID<input value={proxyId} onChange={(event) => setProxyId(event.target.value)} disabled={Boolean(proxy)} required pattern="[A-Za-z0-9._-]+" /></label>
          <label>备注<input value={remark} onChange={(event) => setRemark(event.target.value)} /></label>
          <label>网络<select value={data.network} onChange={(event) => {const network = event.target.value as "tcp" | "udp"; setData({...data, network, transport: network === "udp" ? "smux-datagram" : "auto"});}}><option value="tcp">TCP</option><option value="udp">UDP</option></select></label>
          <label>承载<select value={data.transport} onChange={(event) => setData({...data, transport: event.target.value})}>{data.network === "tcp" ? <><option value="auto">auto</option><option value="smux">smux</option><option value="direct">direct</option></> : <option value="smux-datagram">smux-datagram</option>}</select></label>
          <label>服务端监听<input className="mono" value={data.bind} onChange={(event) => setData({...data, bind: event.target.value})} required /></label>
          <label>内网目标<input className="mono" value={data.connect} onChange={(event) => setData({...data, connect: event.target.value})} required /></label>
        </div>
        {validationError && <div className="form-error">{validationError}</div>}
        <div className="modal-actions"><button type="button" className="secondary-button" onClick={onClose}>取消</button><button className="primary-button" disabled={busy}><Settings2 size={17} />保存配置</button></div>
      </form>
    </div>
  );
}

function ActivityView({events}: {events: EventItem[]}) {
  return <section className="section-block first"><div className="section-heading"><div><h2>事件与审计</h2><p>连接、配置与安全事件</p></div></div><EventRows events={events} /></section>;
}

function EventRows({events}: {events: EventItem[]}) {
  if (!events.length) return <div className="empty-state">暂无活动记录</div>;
  return <div className="event-list">{events.map((event, index) => <div className="event-row" key={`${event.created_at}-${index}`}><Activity size={16} /><div className="grow"><strong>{event.kind}</strong><p>{[event.client_id, event.proxy_id, event.reason].filter(Boolean).join(" · ")}</p></div><time>{new Date(event.created_at).toLocaleString()}</time></div>)}</div>;
}

function Status({value}: {value: string}) {
  return <span className={`status status-${value.toLowerCase()}`}><i />{value}</span>;
}

