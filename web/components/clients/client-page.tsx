"use client";

import { useEffect, useState } from "react";
import { Check, Copy, Info, KeyRound, Pencil, Plus, Server, Unplug } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import type { ClientList, ClientView } from "@/lib/types";
import { AlertDialog, Dialog } from "@/components/ui/dialog";
import { ClientDetailDialog, ClientStatus } from "@/components/clients/client-detail-dialog";

type ClientEditor = {mode: "create"; client?: undefined} | {mode: "edit"; client: ClientView};

export function ClientPage({
  data,
  csrf,
  onRefresh,
  onError,
}: {
  data: ClientList;
  csrf: string;
  onRefresh: () => Promise<void>;
  onError: (message: string) => void;
}) {
  const [editor, setEditor] = useState<ClientEditor | null>(null);
  const [rotate, setRotate] = useState<ClientView | null>(null);
  const [oneTimeToken, setOneTimeToken] = useState("");
  const [tokenVisible, setTokenVisible] = useState(false);
  const [copied, setCopied] = useState(false);
  const [busy, setBusy] = useState(false);
  const initialClientID = typeof window === "undefined" ? "" : new URLSearchParams(window.location.search).get("client_id") ?? "";
  const [detail, setDetail] = useState<ClientView | null>(() => data.items.find((client) => client.client_id === initialClientID) ?? null);

  const updateDetailURL = (clientID = "") => {
    const params = new URLSearchParams(window.location.search);
    params.set("view", "clients");
    if (clientID) params.set("client_id", clientID);
    else params.delete("client_id");
    window.history.replaceState(null, "", `?`);
  };

  useEffect(() => {
    if (!initialClientID || detail) return;
    updateDetailURL();
    const timer = window.setTimeout(() => onError("对象不存在或已被删除"), 0);
    return () => window.clearTimeout(timer);
  }, [detail, initialClientID, onError]);

  const mutationError = async (caught: unknown) => {
    if (caught instanceof ApiError && caught.status === 409) {
      await onRefresh();
      onError("配置已被其他会话更新，请确认最新内容后重试");
      return;
    }
    onError(caught instanceof ApiError ? caught.message : "操作失败，请稍后重试");
  };

  const disconnect = async (clientID: string) => {
    setBusy(true);
    try {
      await api(`/api/clients/${encodeURIComponent(clientID)}/disconnect`, {method: "POST", csrf});
      await onRefresh();
    } catch (caught) {
      await mutationError(caught);
    } finally {
      setBusy(false);
    }
  };

  const rotateToken = async () => {
    if (!rotate) return;
    setBusy(true);
    try {
      const result = await api<{one_time_token: string}>(
        `/api/clients/${encodeURIComponent(rotate.client_id)}/rotate-token`,
        {method: "POST", csrf, body: JSON.stringify({expected_revision: data.revision})},
      );
      setRotate(null);
      setOneTimeToken(result.one_time_token);
      setTokenVisible(false);
      await onRefresh();
    } catch (caught) {
      await mutationError(caught);
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="section-block first">
      <div className="section-heading">
        <div><h2>受控客户端</h2><p>身份、在线状态与代理归属</p></div>
        <button className="command-button" onClick={() => setEditor({mode: "create"})}>
          <Plus size={17} />新增客户端
        </button>
      </div>
      <div className="list">
        {data.items.map((client) => (
          <div className="client-row" key={client.client_id}>
            <div className="client-icon"><Server size={19} /></div>
            <div className="grow client-main">
              <div className="row-title">
                <strong>{client.remark}</strong>
                <ClientStatus value={client.state} />
              </div>
              <button
                className="copy-id mono"
                onClick={() => void navigator.clipboard.writeText(client.client_id)}
                title="复制客户端 ID"
              >
                {client.client_id}<Copy size={13} />
              </button>
              <div className="client-meta">
                <span>{client.proxy_count} 条代理</span>
                <span>{client.mux_sessions} 个 smux session</span>
                <span>{client.agent ? `${client.agent.version} · ${client.agent.os}/${client.agent.arch}` : "Agent 信息不可用"}</span>
                <span>最后通信 {client.session ? localTime(client.session.last_seen_at) : "-"}</span>
                <time title={utcTime(client.created_at)}>创建 {localTime(client.created_at)}</time>
                <time title={utcTime(client.updated_at)}>修改 {localTime(client.updated_at)}</time>
              </div>
            </div>
            <button className="icon-button" onClick={() => {setDetail(client); updateDetailURL(client.client_id);}} title="客户端详情" aria-label={`查看 ${client.client_id} 详情`}>
              <Info size={17} />
            </button>
            <button className="icon-button" onClick={() => setEditor({mode: "edit", client})} title="编辑客户端" aria-label={`编辑 ${client.client_id}`}>
              <Pencil size={17} />
            </button>
            <button className="icon-button" onClick={() => setRotate(client)} title="轮换密钥" aria-label={`轮换 ${client.client_id} 密钥`}>
              <KeyRound size={17} />
            </button>
            {client.online && (
              <button className="icon-button danger" disabled={busy} onClick={() => void disconnect(client.client_id)} title="断开连接" aria-label={`断开 ${client.client_id}`}>
                <Unplug size={17} />
              </button>
            )}
          </div>
        ))}
        {!data.items.length && <div className="empty-state">还没有客户端</div>}
      </div>
      {editor && (
        <ClientEditorDialog
          editor={editor}
          revision={data.revision}
          csrf={csrf}
          onClose={() => setEditor(null)}
          onToken={(token) => {
            setEditor(null);
            setOneTimeToken(token);
            setTokenVisible(false);
            void onRefresh().catch(() => onError("客户端已创建，但列表刷新失败"));
          }}
          onSaved={async () => {
            setEditor(null);
            await onRefresh();
          }}
          onError={mutationError}
        />
      )}
      <AlertDialog
        open={Boolean(rotate)}
        onOpenChange={(open) => { if (!open && !busy) setRotate(null); }}
        title="轮换客户端密钥"
        description={`轮换 ${rotate?.remark ?? ""} 的密钥后，当前控制连接和数据会话会立即断开。`}
        confirmLabel="确认轮换"
        busy={busy}
        onConfirm={() => void rotateToken()}
      />
      <ClientDetailDialog client={detail} onClose={() => {setDetail(null); updateDetailURL();}} />
      <Dialog
        open={Boolean(oneTimeToken)}
        onOpenChange={() => undefined}
        title="一次性客户端密钥"
        description="关闭后无法再次读取，请先保存到客户端配置。"
        lockClose
      >
        <div className="secret-body">
          <code>{tokenVisible ? oneTimeToken : "•".repeat(32)}</code>
          <div className="secret-actions">
            <button className="secondary-button" onClick={() => setTokenVisible((value) => !value)}>
              {tokenVisible ? "隐藏" : "显示"}
            </button>
            <button
              className="secondary-button"
              onClick={async () => {
                await navigator.clipboard.writeText(oneTimeToken);
                setCopied(true);
              }}
            >
              {copied ? <Check size={16} /> : <Copy size={16} />}{copied ? "已复制" : "复制"}
            </button>
          </div>
        </div>
        <div className="modal-actions">
          <button
            className="primary-button"
            onClick={() => {
              setOneTimeToken("");
              setCopied(false);
              setTokenVisible(false);
            }}
          >
            我已保存
          </button>
        </div>
      </Dialog>
    </section>
  );
}

function ClientEditorDialog({
  editor,
  revision,
  csrf,
  onClose,
  onToken,
  onSaved,
  onError,
}: {
  editor: ClientEditor;
  revision: number;
  csrf: string;
  onClose: () => void;
  onToken: (token: string) => void;
  onSaved: () => Promise<void>;
  onError: (caught: unknown) => Promise<void>;
}) {
  const [clientID, setClientID] = useState(editor.client?.client_id ?? "");
  const [remark, setRemark] = useState(editor.client?.remark ?? "");
  const [enabled, setEnabled] = useState(editor.client?.enabled ?? true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return (
    <Dialog
      open
      onOpenChange={(open) => { if (!open && !busy) onClose(); }}
      title={editor.mode === "create" ? "新增客户端" : "编辑客户端"}
      description={editor.mode === "create" ? "服务端将生成只显示一次的客户端密钥。" : "客户端 ID 不可修改。"}
    >
      <form
        onSubmit={async (event) => {
          event.preventDefault();
          setBusy(true);
          setError("");
          try {
            if (editor.mode === "create") {
              const result = await api<{one_time_token: string}>("/api/clients", {
                method: "POST", csrf,
                body: JSON.stringify({expected_revision: revision, client_id: clientID, remark}),
              });
              onToken(result.one_time_token);
            } else {
              await api(`/api/clients/${encodeURIComponent(clientID)}`, {
                method: "PATCH", csrf,
                body: JSON.stringify({expected_revision: revision, remark, enabled}),
              });
              await onSaved();
            }
          } catch (caught) {
            setError(caught instanceof ApiError ? caught.message : "保存失败");
            await onError(caught);
          } finally {
            setBusy(false);
          }
        }}
      >
        <div className="form-grid single">
          <label>客户端 ID<input value={clientID} onChange={(event) => setClientID(event.target.value)} disabled={editor.mode === "edit"} required pattern="[A-Za-z0-9][A-Za-z0-9._-]*" maxLength={64} /></label>
          <label>备注<input value={remark} onChange={(event) => setRemark(event.target.value)} required maxLength={200} /></label>
          {editor.mode === "edit" && <label className="checkbox-row"><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} />启用客户端</label>}
          {error && <div className="form-error">{error}</div>}
        </div>
        <div className="modal-actions">
          <button type="button" className="secondary-button" onClick={onClose} disabled={busy}>取消</button>
          <button className="primary-button" disabled={busy}>{busy ? "保存中..." : "保存"}</button>
        </div>
      </form>
    </Dialog>
  );
}

function localTime(value: number) {
  return value > 0 ? new Date(value).toLocaleString() : "-";
}

function utcTime(value: number) {
  return value > 0 ? new Date(value).toISOString() : "";
}
