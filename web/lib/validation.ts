import type { Proxy } from "./types";

function portOf(value: string, allowEmptyHost: boolean): number | null {
  const match = value.match(/^(?:\[[0-9a-fA-F:]+\]|[^:]*):([0-9]+)$/);
  if (!match) return null;
  const separator = value.lastIndexOf(":");
  const host = value.slice(0, separator);
  if (!allowEmptyHost && host.length === 0) return null;
  const port = Number(match[1]);
  return Number.isInteger(port) && port >= 1 && port <= 65535 ? port : null;
}

export function validateProxyDraft(
  draft: Proxy,
  existing: Proxy[],
  originalProxyId?: string,
): string | null {
  if (!/^[A-Za-z0-9._-]+$/.test(draft.proxy_id)) {
    return "代理 ID 只能包含字母、数字、点、下划线和短横线";
  }
  if (portOf(draft.data.bind, true) === null) {
    return "服务端监听必须是有效的 host:port";
  }
  if (portOf(draft.data.connect, false) === null) {
    return "内网目标必须是有效的 host:port";
  }
  const bindPort = portOf(draft.data.bind, true);
  const duplicate = existing.some((proxy) => (
    proxy.proxy_id !== originalProxyId && portOf(proxy.data.bind, true) === bindPort
  ));
  if (duplicate) return `监听端口 ${bindPort} 已被其他代理使用`;
  if (draft.data.network === "udp" && draft.data.transport !== "smux-datagram") {
    return "UDP 代理只能使用 smux-datagram";
  }
  if (
    draft.data.network === "tcp"
    && !["auto", "smux", "direct"].includes(draft.data.transport)
  ) {
    return "TCP 代理承载方式无效";
  }
  return null;
}
