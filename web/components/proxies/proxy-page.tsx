"use client";

import { useEffect, useMemo, useState } from "react";
import { Plus, Settings2, Trash2 } from "lucide-react";
import type { ClientFile, Proxy, TunnelData } from "@/lib/types";
import { api } from "@/lib/api";
import { validateProxyDraft } from "@/lib/validation";
import { AlertDialog, Dialog } from "@/components/ui/dialog";

type Editor = {clientId: string; proxy?: Proxy};
type AccessPolicy = {allow_cidrs: string[]; deny_cidrs: string[]};
type AccessPolicyResponse = {global: AccessPolicy};

export function ProxyPage({
  config,
  busy,
  onSave,
}: {
  config: ClientFile;
  busy: boolean;
  onSave: (next: ClientFile) => Promise<void>;
}) {
  const [selectedClient, setSelectedClient] = useState("");
  const [editor, setEditor] = useState<Editor | null>(null);
  const [removeTarget, setRemoveTarget] = useState<{clientId: string; proxy: Proxy} | null>(null);
  const [globalAccess, setGlobalAccess] = useState<AccessPolicy>({allow_cidrs: [], deny_cidrs: []});
  const rows = useMemo(
    () => config.client_config.flatMap((client) => client.proxy_list.map((proxy) => ({client, proxy}))),
    [config],
  );
  useEffect(() => {
    void api<AccessPolicyResponse>("/api/access-policy")
      .then((result) => setGlobalAccess(result.global))
      .catch(() => undefined);
  }, []);

  const remove = async () => {
    if (!removeTarget) return;
    const next = structuredClone(config);
    const client = next.client_config.find((item) => item.client_id === removeTarget.clientId);
    if (!client) return;
    client.proxy_list = client.proxy_list.filter((item) => item.proxy_id !== removeTarget.proxy.proxy_id);
    await onSave(next);
    setRemoveTarget(null);
  };

  const toggle = async (clientID: string, proxyID: string) => {
    const next = structuredClone(config);
    const proxy = next.client_config
      .find((client) => client.client_id === clientID)
      ?.proxy_list.find((item) => item.proxy_id === proxyID);
    if (!proxy) return;
    proxy.enabled = !proxy.enabled;
    await onSave(next);
  };

  return (
    <section className="section-block first">
      <div className="section-heading">
        <div><h2>代理监听</h2><p>服务端端口到客户端内网目标</p></div>
        <div className="select-action">
          <select id="proxy-client" value={selectedClient} onChange={(event) => setSelectedClient(event.target.value)} aria-label="代理客户端筛选">
            <option value="">选择客户端</option>
            {config.client_config.map((client) => <option key={client.client_id} value={client.client_id}>{client.remark} · {client.client_id}</option>)}
          </select>
          <button
            className="icon-button primary-icon"
            disabled={busy || config.client_config.length === 0}
            onClick={() => setEditor({clientId: selectedClient})}
            title={config.client_config.length ? "新增代理" : "请先创建客户端"}
            aria-label="新增代理"
          >
            <Plus size={18} />
          </button>
        </div>
      </div>
      {!config.client_config.length ? (
        <div className="empty-state">请先在客户端页面创建客户端</div>
      ) : (
        <div className="table-wrap">
          <table>
            <thead><tr><th>代理</th><th>客户端</th><th>协议</th><th>服务端监听</th><th>内网目标</th><th>来源策略</th><th>PROXY</th><th>限速</th><th>承载</th><th>启用</th><th /></tr></thead>
            <tbody>
              {rows.map(({client, proxy}) => (
                <tr key={`${client.client_id}-${proxy.proxy_id}`}>
                  <td><button className="link-button" onClick={() => setEditor({clientId: client.client_id, proxy})}>{proxy.remark || proxy.proxy_id}</button><small>{proxy.proxy_id}</small></td>
                  <td><strong>{client.remark}</strong><small>{client.client_id}</small></td>
                  <td><span className={`network-tag ${proxy.data.network}`}>{proxy.data.network.toUpperCase()}</span></td>
                  <td className="mono">{proxy.data.bind}</td>
                  <td className="mono">{proxy.data.connect}</td>
                  <td>{accessSummary(proxy.data.access_policy)}</td>
                  <td>{proxy.data.enable_proxy_protocol ? "v1" : "关闭"}</td>
                  <td>{rateSummary(proxy.data)}</td>
                  <td>{proxy.data.transport}</td>
                  <td>
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
                  <td><button className="icon-button danger" onClick={() => setRemoveTarget({clientId: client.client_id, proxy})} title="删除代理" aria-label={`删除 ${proxy.proxy_id}`}><Trash2 size={17} /></button></td>
                </tr>
              ))}
              {!rows.length && <tr><td colSpan={11}><div className="empty-inline">暂无代理规则</div></td></tr>}
            </tbody>
          </table>
        </div>
      )}
      {editor && (
        <ProxyEditor
          editor={editor}
          config={config}
          globalAccess={globalAccess}
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
          }}
        />
      )}
      <AlertDialog
        open={Boolean(removeTarget)}
        onOpenChange={(open) => { if (!open && !busy) setRemoveTarget(null); }}
        title="删除代理规则"
        description={`删除 ${removeTarget?.proxy.remark || removeTarget?.proxy.proxy_id || ""} 后，服务端将停止对应监听。`}
        confirmLabel="确认删除"
        busy={busy}
        onConfirm={() => void remove()}
      />
    </section>
  );
}

function ProxyEditor({
  editor,
  config,
  globalAccess,
  busy,
  onClose,
  onSave,
}: {
  editor: Editor;
  config: ClientFile;
  globalAccess: AccessPolicy;
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
    access_policy: initialValue.access_policy ?? {allow_cidrs: [], deny_cidrs: []},
    rate_limit: initialValue.rate_limit ?? {up_bytes_per_second: 0, down_bytes_per_second: 0},
  };
  const [clientID, setClientID] = useState(editor.clientId);
  const [proxyID, setProxyID] = useState(proxy?.proxy_id ?? "");
  const [remark, setRemark] = useState(proxy?.remark ?? "");
  const [data, setData] = useState<TunnelData>(initial);
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
        const draft: Proxy = {proxy_id: proxyID, remark, kind: "tunnel", enabled: proxy?.enabled ?? true, data};
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
                  value={(data.access_policy?.allow_cidrs ?? []).join("\n")}
                  onChange={(event) => setData({...data, access_policy: {
                    ...(data.access_policy ?? {allow_cidrs: [], deny_cidrs: []}),
                    allow_cidrs: cidrLines(event.target.value),
                  }})}
                />
              </label>
              <label>拒绝来源 CIDR
                <textarea
                  className="mono"
                  placeholder="每行一个 CIDR"
                  value={(data.access_policy?.deny_cidrs ?? []).join("\n")}
                  onChange={(event) => setData({...data, access_policy: {
                    ...(data.access_policy ?? {allow_cidrs: [], deny_cidrs: []}),
                    deny_cidrs: cidrLines(event.target.value),
                  }})}
                />
              </label>
            </div>
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

function cidrLines(value: string) {
  return value.split(/\r?\n/).map((item) => item.trim()).filter(Boolean);
}

function accessSummary(policy?: AccessPolicy) {
  if (!policy || (!policy.allow_cidrs.length && !policy.deny_cidrs.length)) return "继承全局";
  return `允许 ${policy.allow_cidrs.length || "全部"} · 拒绝 ${policy.deny_cidrs.length}`;
}

function rateSummary(data: TunnelData) {
  const up = data.rate_limit?.up_bytes_per_second ?? 0;
  const down = data.rate_limit?.down_bytes_per_second ?? 0;
  return `↑ ${formatLimit(up)} · ↓ ${formatLimit(down)}`;
}

function formatLimit(value: number) {
  if (!value) return "不限";
  if (value >= 2 ** 30) return `${value / 2 ** 30} GiB/s`;
  if (value >= 2 ** 20) return `${value / 2 ** 20} MiB/s`;
  return `${value / 2 ** 10} KiB/s`;
}

function rateUnit(value: number) {
  if (value >= 2 ** 30 && value % (2 ** 30) === 0) return "GiB/s";
  if (value >= 2 ** 20 && value % (2 ** 20) === 0) return "MiB/s";
  return "KiB/s";
}
