"use client";

import {useCallback, useEffect, useMemo, useRef, useState} from "react";
import {
  ChevronLeft,
  ChevronRight,
  Pencil,
  Plus,
  ShieldCheck,
  Settings2,
  Trash2,
  X,
} from "lucide-react";
import type {
  ClientFile,
  KnockGrant,
  KnockGrantPage,
  KnockStatus,
  NavigationDestination,
  Proxy,
  ProxyRuntime,
  ProxyRuntimePage,
  TunnelData,
} from "@/lib/types";
import {api, ApiError} from "@/lib/api";
import {parseCIDRLines, validateProxyDraft} from "@/lib/validation";
import {AlertDialog, Dialog} from "@/components/ui/dialog";
import {
  Availability,
  ProxyRuntimeDetail,
} from "@/components/proxies/proxy-runtime-detail";

type Editor = {clientId: string; proxy?: Proxy};
type AccessPolicy = {allow_cidrs: string[]; deny_cidrs: string[]};
type AccessPolicyResponse = {global: AccessPolicy};
const emptyRuntime: ProxyRuntimePage = {
  revision: 0, snapshot_at: 0, items: [],
  page: 1, page_size: 25, total: 0, total_pages: 0,
};

export function ProxyPage({
  config,
  csrf,
  busy,
  onSave,
  onUnauthorized,
  onNavigate,
}: {
  config: ClientFile;
  csrf: string;
  busy: boolean;
  onSave: (next: ClientFile) => Promise<void>;
  onUnauthorized: () => void;
  onNavigate: (destination: NavigationDestination) => void;
}) {
  const initial = useMemo(
    () => typeof window === "undefined"
      ? new URLSearchParams()
      : new URLSearchParams(window.location.search),
    [],
  );
  const initialClient = initial.get("client_id") ?? "";
  const initialProxy = initial.get("proxy_id") ?? "";
  const initialTarget = initialClient && initialProxy ? runtimeKey(initialClient, initialProxy) : "";
  const [selectedClient, setSelectedClient] = useState("");
  const [editor, setEditor] = useState<Editor | null>(null);
  const [removeTarget, setRemoveTarget] = useState<{clientId: string; proxy: Proxy} | null>(null);
  const [globalAccess, setGlobalAccess] = useState<AccessPolicy>({allow_cidrs: [], deny_cidrs: []});
  const [runtime, setRuntime] = useState<ProxyRuntimePage>(emptyRuntime);
  const [runtimePage, setRuntimePage] = useState(1);
  const [runtimeStale, setRuntimeStale] = useState(false);
  const [knockEnabled, setKnockEnabled] = useState(false);
  const [grants, setGrants] = useState<KnockGrant[]>([]);
  const [grantsStale, setGrantsStale] = useState(false);
  const [revokeTarget, setRevokeTarget] = useState<KnockGrant | null>(null);
  const [deepLinkError, setDeepLinkError] = useState("");
  const [targetLookup, setTargetLookup] = useState(initialTarget);
  const [selectedRuntime, setSelectedRuntime] = useState(initialTarget);
  const runtimeRequest = useRef<AbortController | null>(null);

  const rows = useMemo(() => runtime.items.map((item) => {
    const client = config.client_config.find((value) => value.client_id === item.client_id);
    const proxy = client?.proxy_list.find((value) => value.proxy_id === item.proxy_id);
    return client && proxy ? {client, proxy, runtime: item} : null;
  }).filter((value): value is {
    client: ClientFile["client_config"][number];
    proxy: Proxy;
    runtime: ProxyRuntime;
  } => Boolean(value)), [config, runtime.items]);
  const detail = runtime.items.find(
    (item) => runtimeKey(item.client_id, item.proxy_id) === selectedRuntime,
  ) ?? null;
  const detailProxy = detail
    ? config.client_config.find((client) => client.client_id === detail.client_id)
      ?.proxy_list.find((proxy) => proxy.proxy_id === detail.proxy_id) ?? null
    : null;

  useEffect(() => {
    void api<AccessPolicyResponse>("/api/access-policy")
      .then((result) => setGlobalAccess(result.global))
      .catch(() => undefined);
  }, []);

  const loadGrants = useCallback(async () => {
    try {
      const [status, grantPage] = await Promise.all([
        api<KnockStatus>("/api/knock/status"),
        api<KnockGrantPage>("/api/knock/grants"),
      ]);
      setKnockEnabled(status.enabled);
      setGrants(grantPage.items);
      setGrantsStale(false);
    } catch (caught) {
      if (caught instanceof ApiError && caught.status === 401) onUnauthorized();
      setGrantsStale(true);
    }
  }, [onUnauthorized]);

  useEffect(() => {
    const initial = window.setTimeout(() => void loadGrants(), 0);
    const timer = window.setInterval(() => void loadGrants(), 15_000);
    return () => {
      window.clearTimeout(initial);
      window.clearInterval(timer);
    };
  }, [loadGrants]);

  const loadRuntime = useCallback(async () => {
    runtimeRequest.current?.abort();
    const controller = new AbortController();
    runtimeRequest.current = controller;
    try {
      const params = new URLSearchParams({
        page: String(targetLookup ? 1 : runtimePage),
        page_size: "25",
      });
      if (targetLookup) {
        const [clientID, proxyID] = targetLookup.split("\u0000");
        params.set("client_id", clientID);
        params.set("query", proxyID);
      }
      const value = await api<ProxyRuntimePage>(
        "/api/proxies/runtime?" + params.toString(),
        {signal: controller.signal},
      );
      setRuntime(value);
      setRuntimeStale(false);
      if (targetLookup) {
        const found = value.items.some(
          (item) => runtimeKey(item.client_id, item.proxy_id) === targetLookup,
        );
        if (found) {
          setSelectedRuntime(targetLookup);
          setDeepLinkError("");
        } else {
          setSelectedRuntime("");
          setTargetLookup("");
          setDeepLinkError("对象不存在或已被删除");
          const current = new URLSearchParams(window.location.search);
          current.delete("client_id");
          current.delete("proxy_id");
          window.history.replaceState(null, "", window.location.pathname + "?" + current.toString());
        }
      }
      if (value.total_pages > 0 && runtimePage > value.total_pages) {
        setRuntimePage(value.total_pages);
      }
    } catch (caught) {
      if (controller.signal.aborted) return;
      if (caught instanceof ApiError && caught.status === 401) onUnauthorized();
      setRuntimeStale(true);
    } finally {
      if (runtimeRequest.current === controller) runtimeRequest.current = null;
    }
  }, [onUnauthorized, runtimePage, targetLookup]);

  useEffect(() => {
    const initialTimer = window.setTimeout(() => void loadRuntime(), 0);
    let timer: number | undefined;
    const schedule = () => {
      if (document.visibilityState === "visible") {
        timer = window.setInterval(() => void loadRuntime(), 5000);
      }
    };
    const visibility = () => {
      if (timer) window.clearInterval(timer);
      timer = undefined;
      if (document.visibilityState === "visible") {
        void loadRuntime();
        schedule();
      }
    };
    schedule();
    document.addEventListener("visibilitychange", visibility);
    return () => {
      window.clearTimeout(initialTimer);
      if (timer) window.clearInterval(timer);
      runtimeRequest.current?.abort();
      document.removeEventListener("visibilitychange", visibility);
    };
  }, [loadRuntime]);

  const updateRuntimeURL = (clientID = "", proxyID = "") => {
    const params = new URLSearchParams(window.location.search);
    params.set("view", "proxies");
    if (clientID && proxyID) {
      params.set("client_id", clientID);
      params.set("proxy_id", proxyID);
    } else {
      params.delete("client_id");
      params.delete("proxy_id");
    }
    window.history.replaceState(null, "", `${window.location.pathname}?${params}`);
  };

  const remove = async () => {
    if (!removeTarget) return;
    const next = structuredClone(config);
    const client = next.client_config.find((item) => item.client_id === removeTarget.clientId);
    if (!client) return;
    client.proxy_list = client.proxy_list.filter((item) => item.proxy_id !== removeTarget.proxy.proxy_id);
    await onSave(next);
    setRemoveTarget(null);
    await loadRuntime();
  };

  const toggle = async (clientID: string, proxyID: string) => {
    const next = structuredClone(config);
    const proxy = next.client_config
      .find((client) => client.client_id === clientID)
      ?.proxy_list.find((item) => item.proxy_id === proxyID);
    if (!proxy) return;
    proxy.enabled = !proxy.enabled;
    await onSave(next);
    await loadRuntime();
  };

  return (
    <section className="section-block first">
      <div className="section-heading">
        <div><h2>代理监听</h2><p>服务端端口到客户端内网目标</p></div>
        <div className="select-action">
          <select id="proxy-client" value={selectedClient} onChange={(event) => setSelectedClient(event.target.value)} aria-label="新增代理所属客户端">
            <option value="">选择客户端</option>
            {config.client_config.map((client) => <option key={client.client_id} value={client.client_id}>{client.remark} · {client.client_id}</option>)}
          </select>
          <button
            className="icon-button primary-icon"
            disabled={busy || config.client_config.length === 0}
            onClick={() => setEditor({clientId: selectedClient})}
            title={config.client_config.length ? "新增代理" : "请先创建客户端"}
            aria-label="新增代理"
          ><Plus size={18} /></button>
        </div>
      </div>
      {deepLinkError && <div className="stale-banner">{deepLinkError}</div>}
      {runtimeStale && <div className="stale-banner">运行状态刷新失败，当前内容可能已过期</div>}
      {!config.client_config.length ? <div className="empty-state">请先在客户端页面创建客户端</div> : <>
        <div className="table-wrap">
          <table className="proxy-runtime-table">
            <thead><tr><th>代理</th><th>客户端</th><th>运行状态</th><th>协议 / 承载</th><th>服务端监听 → 内网目标</th><th>当前活动</th><th>24 小时</th><th>启用</th><th aria-label="操作" /></tr></thead>
            <tbody>
              {rows.map(({client, proxy, runtime: item}) => <tr
                key={`${client.client_id}-${proxy.proxy_id}`}
                className={`clickable-row proxy-runtime-row ${selectedRuntime === runtimeKey(client.client_id, proxy.proxy_id) ? "selected-row" : ""}`}
                onClick={() => {
                  setDeepLinkError("");
                  setSelectedRuntime(runtimeKey(client.client_id, proxy.proxy_id));
                  updateRuntimeURL(client.client_id, proxy.proxy_id);
                }}
              >
                <td><strong>{proxy.remark || proxy.proxy_id}</strong><small>{proxy.proxy_id}</small></td>
                <td><strong>{client.remark || client.client_id}</strong><small>{client.client_id}</small></td>
                <td><Availability value={item.availability} /><small>{item.reason_code}</small></td>
                <td><span className={`network-tag ${item.protocol}`}>{item.protocol.toUpperCase()}</span><small>{item.transport} → {item.effective_transport}{proxy.data.allow_knock_only ? " · 邮件授权" : ""}</small></td>
                <td className="proxy-route-cell"><span className="mono">{item.bind}</span><small className="mono">{item.connect}</small></td>
                <td className="stable-number"><strong>{item.realtime.active_connections}</strong><small>↑ {formatRuntimeRate(item.realtime.up_bytes_per_second)} · ↓ {formatRuntimeRate(item.realtime.down_bytes_per_second)}</small></td>
                <td className="stable-number">{item.history.connections === null ? "-" : item.history.connections}<small>{item.history.failure_rate === null ? "历史不可用" : `失败 ${(item.history.failure_rate * 100).toFixed(1)}%`}</small></td>
                <td onClick={(event) => event.stopPropagation()}>
                  <label className="switch" title={proxy.enabled ? "停用代理" : "启用代理"}>
                    <input
                      type="checkbox"
                      aria-label={`${proxy.enabled ? "停用" : "启用"} ${proxy.proxy_id}`}
                      checked={proxy.enabled}
                      disabled={busy}
                      onChange={() => void toggle(client.client_id, proxy.proxy_id)}
                    />
                    <span />
                  </label>
                </td>
                <td className="table-actions" onClick={(event) => event.stopPropagation()}>
                  <button className="icon-button" onClick={() => setEditor({clientId: client.client_id, proxy})} title="编辑代理" aria-label={`编辑 ${proxy.proxy_id}`}><Pencil size={16} /></button>
                  <button className="icon-button danger" onClick={() => setRemoveTarget({clientId: client.client_id, proxy})} title="删除代理" aria-label={`删除 ${proxy.proxy_id}`}><Trash2 size={16} /></button>
                </td>
              </tr>)}
              {!rows.length && <tr><td colSpan={9}><div className="empty-inline">{runtime.snapshot_at ? "暂无代理规则" : "正在加载运行状态"}</div></td></tr>}
            </tbody>
          </table>
        </div>
        <div className="pagination">
          <span>共 {runtime.total} 条</span>
          <button className="icon-button" disabled={runtimePage <= 1} onClick={() => setRuntimePage((value) => value - 1)} aria-label="代理上一页"><ChevronLeft size={17} /></button>
          <span>{runtimePage} / {Math.max(runtime.total_pages, 1)}</span>
          <button className="icon-button" disabled={runtimePage >= runtime.total_pages} onClick={() => setRuntimePage((value) => value + 1)} aria-label="代理下一页"><ChevronRight size={17} /></button>
        </div>
      </>}
      <section className="section-block knock-grants-section">
        <div className="section-heading">
          <div><h2>当前临时授权</h2><p>通过邮件链接获得代理端口访问权限的来源 IP</p></div>
          <span className={`knock-service-state ${knockEnabled ? "enabled" : ""}`}>
            <ShieldCheck size={15} />{knockEnabled ? "敲门已启用" : "敲门未启用"}
          </span>
        </div>
        {grantsStale && <div className="stale-banner">授权列表刷新失败，当前内容可能已过期</div>}
        <div className="table-wrap">
          <table className="knock-grants-table">
            <thead><tr><th>来源 IP</th><th>授权邮箱</th><th>授权时间</th><th>到期时间</th><th>剩余</th><th aria-label="操作" /></tr></thead>
            <tbody>
              {grants.map((grant) => <tr key={grant.source_ip}>
                <td className="mono"><strong>{grant.source_ip}</strong></td>
                <td>{grant.granted_by}</td>
                <td>{new Date(grant.granted_at).toLocaleString()}</td>
                <td>{new Date(grant.expires_at).toLocaleString()}</td>
                <td className="stable-number"><GrantCountdown expiresAt={grant.expires_at} /></td>
                <td><button
                  className="icon-button danger"
                  onClick={() => setRevokeTarget(grant)}
                  title="撤销临时授权"
                  aria-label={`撤销 ${grant.source_ip} 的临时授权`}
                ><X size={16} /></button></td>
              </tr>)}
              {!grants.length && <tr><td colSpan={6}><div className="empty-inline">当前没有生效中的临时授权</div></td></tr>}
            </tbody>
          </table>
        </div>
      </section>
      {editor && <ProxyEditor
        editor={editor}
        config={config}
        globalAccess={globalAccess}
        knockEnabled={knockEnabled}
        busy={busy}
        onClose={() => setEditor(null)}
        onSave={async (clientID, proxy) => {
          const next = structuredClone(config);
          const client = next.client_config.find((item) => item.client_id === clientID);
          if (!client) return;
          const index = client.proxy_list.findIndex((item) => item.proxy_id === proxy.proxy_id);
          if (index >= 0) client.proxy_list[index] = proxy;
          else client.proxy_list.push(proxy);
          await onSave(next);
          setEditor(null);
          await loadRuntime();
        }}
      />}
      <ProxyRuntimeDetail
        runtime={detail}
        proxy={detailProxy}
        onClose={() => {
          setSelectedRuntime("");
          setTargetLookup("");
          updateRuntimeURL();
        }}
        onOpenNetwork={(clientID, proxyID) => onNavigate({
          view: "network",
          params: {
            network_view: "client", network_section: "insights",
            client_id: clientID, proxy_id: proxyID,
          },
        })}
      />
      <AlertDialog
        open={Boolean(removeTarget)}
        onOpenChange={(open) => {if (!open && !busy) setRemoveTarget(null);}}
        title="删除代理规则"
        description={`删除 ${removeTarget?.proxy.remark || removeTarget?.proxy.proxy_id || ""} 后，服务端将停止对应监听。`}
        confirmLabel="确认删除"
        busy={busy}
        onConfirm={() => void remove()}
      />
      <AlertDialog
        open={Boolean(revokeTarget)}
        onOpenChange={(open) => {if (!open) setRevokeTarget(null);}}
        title="撤销临时授权"
        description={`撤销 ${revokeTarget?.source_ip ?? ""} 后，该 IP 的新连接将立即被拒绝。`}
        confirmLabel="确认撤销"
        onConfirm={() => {
          if (!revokeTarget) return;
          const sourceIP = revokeTarget.source_ip;
          void api(`/api/knock/grants/${encodeURIComponent(sourceIP)}`, {
            method: "DELETE", csrf,
          }).then(() => {
            setGrants((current) => current.filter((item) => item.source_ip !== sourceIP));
            setRevokeTarget(null);
          }).catch((caught) => {
            if (caught instanceof ApiError && caught.status === 401) onUnauthorized();
            setGrantsStale(true);
          });
        }}
      />
    </section>
  );
}

function formatRuntimeRate(value: number) {
  if (value < 1024) return `${value} B/s`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB/s`;
  return `${(value / (1024 * 1024)).toFixed(1)} MiB/s`;
}

function GrantCountdown({expiresAt}: {expiresAt: number}) {
  const [now, setNow] = useState(0);
  useEffect(() => {
    const initial = window.setTimeout(() => setNow(Date.now()), 0);
    const timer = window.setInterval(() => setNow(Date.now()), 1000);
    return () => {
      window.clearTimeout(initial);
      window.clearInterval(timer);
    };
  }, []);
  if (!now) return <>-</>;
  const seconds = Math.max(0, Math.ceil((expiresAt - now) / 1000));
  return <>{Math.floor(seconds / 60)}:{String(seconds % 60).padStart(2, "0")}</>;
}

function ProxyEditor({
  editor,
  config,
  globalAccess,
  knockEnabled,
  busy,
  onClose,
  onSave,
}: {
  editor: Editor;
  config: ClientFile;
  globalAccess: AccessPolicy;
  knockEnabled: boolean;
  busy: boolean;
  onClose: () => void;
  onSave: (clientID: string, proxy: Proxy) => Promise<void>;
}) {
  const proxy = editor.proxy;
  const initialValue = proxy?.data ?? {
    network: "tcp" as const, transport: "auto", bind: "127.0.0.1:7000", connect: "127.0.0.1:80",
  };
  const initial: TunnelData = {
    ...initialValue,
    enable_proxy_protocol: initialValue.enable_proxy_protocol ?? false,
    allow_knock_only: initialValue.allow_knock_only ?? false,
    access_policy: initialValue.access_policy ?? {allow_cidrs: [], deny_cidrs: []},
    rate_limit: initialValue.rate_limit ?? {up_bytes_per_second: 0, down_bytes_per_second: 0},
  };
  const [clientID, setClientID] = useState(editor.clientId);
  const [proxyID, setProxyID] = useState(proxy?.proxy_id ?? "");
  const [remark, setRemark] = useState(proxy?.remark ?? "");
  const [data, setData] = useState<TunnelData>(initial);
  const [allowCIDRs, setAllowCIDRs] = useState(initial.access_policy?.allow_cidrs.join("\n") ?? "");
  const [denyCIDRs, setDenyCIDRs] = useState(initial.access_policy?.deny_cidrs.join("\n") ?? "");
  const [validationError, setValidationError] = useState("");
  const selected = config.client_config.find((client) => client.client_id === clientID);
  return (
    <Dialog open onOpenChange={(open) => { if (!open && !busy) onClose(); }} title={proxy ? "编辑代理" : "新增代理"} description={proxy ? "代理所属客户端不可修改。" : "选择代理所属客户端并配置监听。"}>
      <form onSubmit={(event) => {
        event.preventDefault();
        if (!clientID) {
          setValidationError("请选择客户端");
          return;
        }
        const draft: Proxy = {
          proxy_id: proxyID,
          remark,
          kind: "tunnel",
          enabled: proxy?.enabled ?? true,
          data: {
            ...data,
            access_policy: {
              allow_cidrs: parseCIDRLines(allowCIDRs),
              deny_cidrs: parseCIDRLines(denyCIDRs),
            },
          },
        };
        const issue = validateProxyDraft(draft, config.client_config.flatMap((client) => client.proxy_list), proxy?.proxy_id);
        if (issue) {
          setValidationError(issue);
          return;
        }
        setValidationError("");
        void onSave(clientID, draft);
      }}>
        <div className="form-grid">
          <label>客户端
            <select value={clientID} onChange={(event) => setClientID(event.target.value)} disabled={Boolean(proxy)} required>
              <option value="">选择客户端</option>
              {config.client_config.map((client) => <option key={client.client_id} value={client.client_id}>{client.remark} · {client.client_id}</option>)}
            </select>
            {selected && !selected.enabled && <small className="field-note">该客户端已禁用，规则不会监听</small>}
          </label>
          <label>代理 ID<input value={proxyID} onChange={(event) => setProxyID(event.target.value)} disabled={Boolean(proxy)} required pattern="[A-Za-z0-9._-]+" /></label>
          <label>备注<input value={remark} onChange={(event) => setRemark(event.target.value)} /></label>
          <label>网络<select value={data.network} onChange={(event) => {
            const network = event.target.value as "tcp" | "udp";
            setData({
              ...data,
              network,
              transport: network === "udp" ? "smux-datagram" : "auto",
              enable_proxy_protocol: network === "tcp" ? data.enable_proxy_protocol : false,
            });
          }}><option value="tcp">TCP</option><option value="udp">UDP</option></select></label>
          <label>承载<select value={data.transport} onChange={(event) => setData({...data, transport: event.target.value})}>{data.network === "tcp" ? <><option value="auto">auto</option><option value="smux">smux</option><option value="direct">direct</option></> : <option value="smux-datagram">smux-datagram</option>}</select></label>
          <label>服务端监听<input className="mono" value={data.bind} onChange={(event) => setData({...data, bind: event.target.value})} required /></label>
          <label>内网目标<input className="mono" value={data.connect} onChange={(event) => setData({...data, connect: event.target.value})} required /></label>
          <fieldset className="form-section form-span">
            <legend>外部访问</legend>
            <p>服务端全局策略：允许 {globalAccess.allow_cidrs.length || "全部"}，拒绝 {globalAccess.deny_cidrs.length || "无"}</p>
            <div className="form-grid nested">
              <label>允许来源 CIDR
                <textarea
                  className="mono"
                  placeholder="每行一个 CIDR；留空不增加限制"
                  value={allowCIDRs}
                  onChange={(event) => setAllowCIDRs(event.target.value)}
                />
              </label>
              <label>拒绝来源 CIDR
                <textarea
                  className="mono"
                  placeholder="每行一个 CIDR"
                  value={denyCIDRs}
                  onChange={(event) => setDenyCIDRs(event.target.value)}
                />
              </label>
            </div>
            <label className="toggle-setting knock-toggle">
              <span><strong>仅允许已邮件授权 IP</strong><small>{knockEnabled ? "通过 Magic Link 临时授权后才允许访问" : "服务端未启用邮件敲门"}</small></span>
              <span className="switch">
                <input
                  type="checkbox"
                  checked={data.allow_knock_only ?? false}
                  disabled={!knockEnabled}
                  onChange={(event) => setData({...data, allow_knock_only: event.target.checked})}
                  aria-label="仅允许已邮件授权 IP"
                />
                <span />
              </span>
            </label>
          </fieldset>
          <fieldset className="form-section form-span">
            <legend>目标来源传递</legend>
            <label className="toggle-setting">
              <span><strong>启用 PROXY Protocol v1</strong><small>目标服务必须显式接受 PROXY Protocol v1</small></span>
              <span className="switch">
                <input
                  type="checkbox"
                  checked={data.enable_proxy_protocol ?? false}
                  disabled={data.network !== "tcp"}
                  onChange={(event) => setData({...data, enable_proxy_protocol: event.target.checked})}
                  aria-label="启用 PROXY Protocol v1"
                />
                <span />
              </span>
            </label>
          </fieldset>
          <fieldset className="form-section form-span">
            <legend>连接限速</legend>
            <div className="form-grid nested">
              <RateInput
                label="上行"
                value={data.rate_limit?.up_bytes_per_second ?? 0}
                onChange={(value) => setData({...data, rate_limit: {
                  ...(data.rate_limit ?? {up_bytes_per_second: 0, down_bytes_per_second: 0}),
                  up_bytes_per_second: value,
                }})}
              />
              <RateInput
                label="下行"
                value={data.rate_limit?.down_bytes_per_second ?? 0}
                onChange={(value) => setData({...data, rate_limit: {
                  ...(data.rate_limit ?? {up_bytes_per_second: 0, down_bytes_per_second: 0}),
                  down_bytes_per_second: value,
                }})}
              />
            </div>
          </fieldset>
          {validationError && <div className="form-error form-span">{validationError}</div>}
        </div>
        <div className="modal-actions">
          <button type="button" className="secondary-button" onClick={onClose}>取消</button>
          <button className="primary-button" disabled={busy || !clientID}><Settings2 size={17} />保存配置</button>
        </div>
      </form>
    </Dialog>
  );
}

function RateInput({label, value, onChange}: {label: string; value: number; onChange: (value: number) => void}) {
  const [unit, setUnit] = useState(() => rateUnit(value));
  const factor = unit === "GiB/s" ? 2 ** 30 : unit === "MiB/s" ? 2 ** 20 : 2 ** 10;
  const unlimited = value === 0;
  return (
    <div className="rate-editor">
      <label>{label}速率
        <div className="input-with-unit">
          <input
            type="number"
            min="1"
            step="1"
            disabled={unlimited}
            value={unlimited ? "" : value / factor}
            onChange={(event) => onChange(Math.round(Number(event.target.value) * factor))}
          />
          <select value={unit} disabled={unlimited} onChange={(event) => setUnit(event.target.value)}>
            <option>KiB/s</option><option>MiB/s</option><option>GiB/s</option>
          </select>
        </div>
      </label>
      <label className="checkbox-row">
        <input type="checkbox" checked={unlimited} onChange={(event) => onChange(event.target.checked ? 0 : factor)} />
        不限速
      </label>
    </div>
  );
}

function runtimeKey(clientID: string, proxyID: string) {
  return clientID + "\u0000" + proxyID;
}

function rateUnit(value: number) {
  if (value >= 2 ** 30 && value % (2 ** 30) === 0) return "GiB/s";
  if (value >= 2 ** 20 && value % (2 ** 20) === 0) return "MiB/s";
  return "KiB/s";
}
