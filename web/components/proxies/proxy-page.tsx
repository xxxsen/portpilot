"use client";

import { useMemo, useState } from "react";
import { Plus, Settings2, Trash2 } from "lucide-react";
import type { ClientFile, Proxy, TunnelData } from "@/lib/types";
import { validateProxyDraft } from "@/lib/validation";
import { AlertDialog, Dialog } from "@/components/ui/dialog";

type Editor = {clientId: string; proxy?: Proxy};

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
  const rows = useMemo(
    () => config.client_config.flatMap((client) => client.proxy_list.map((proxy) => ({client, proxy}))),
    [config],
  );

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
            <thead><tr><th>代理</th><th>客户端</th><th>协议</th><th>服务端监听</th><th>内网目标</th><th>承载</th><th>启用</th><th /></tr></thead>
            <tbody>
              {rows.map(({client, proxy}) => (
                <tr key={`${client.client_id}-${proxy.proxy_id}`}>
                  <td><button className="link-button" onClick={() => setEditor({clientId: client.client_id, proxy})}>{proxy.remark || proxy.proxy_id}</button><small>{proxy.proxy_id}</small></td>
                  <td><strong>{client.remark}</strong><small>{client.client_id}</small></td>
                  <td><span className={`network-tag ${proxy.data.network}`}>{proxy.data.network.toUpperCase()}</span></td>
                  <td className="mono">{proxy.data.bind}</td>
                  <td className="mono">{proxy.data.connect}</td>
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
              {!rows.length && <tr><td colSpan={8}><div className="empty-inline">暂无代理规则</div></td></tr>}
            </tbody>
          </table>
        </div>
      )}
      {editor && (
        <ProxyEditor
          editor={editor}
          config={config}
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
  busy,
  onClose,
  onSave,
}: {
  editor: Editor;
  config: ClientFile;
  busy: boolean;
  onClose: () => void;
  onSave: (clientID: string, proxy: Proxy) => Promise<void>;
}) {
  const proxy = editor.proxy;
  const initial: TunnelData = proxy?.data ?? {
    network: "tcp", transport: "auto", bind: "127.0.0.1:7000", connect: "127.0.0.1:80",
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
            setData({...data, network, transport: network === "udp" ? "smux-datagram" : "auto"});
          }}><option value="tcp">TCP</option><option value="udp">UDP</option></select></label>
          <label>承载<select value={data.transport} onChange={(event) => setData({...data, transport: event.target.value})}>{data.network === "tcp" ? <><option value="auto">auto</option><option value="smux">smux</option><option value="direct">direct</option></> : <option value="smux-datagram">smux-datagram</option>}</select></label>
          <label>服务端监听<input className="mono" value={data.bind} onChange={(event) => setData({...data, bind: event.target.value})} required /></label>
          <label>内网目标<input className="mono" value={data.connect} onChange={(event) => setData({...data, connect: event.target.value})} required /></label>
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
