import type { Proxy } from "./types";

export function parseCIDRLines(value: string): string[] {
  return value
    .split(/\r\n|[\n\r\u2028\u2029]/u)
    .map((item) => item.trim())
    .filter(Boolean);
}

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
  if (draft.data.network === "udp" && draft.data.enable_proxy_protocol) {
    return "UDP 代理不能启用 PROXY Protocol";
  }
  const access = draft.data.access_policy;
  for (const [label, values] of [
    ["允许来源", access?.allow_cidrs ?? []],
    ["拒绝来源", access?.deny_cidrs ?? []],
  ] as const) {
    if (values.length > 256) return `${label}最多填写 256 项`;
    if (new Set(values).size !== values.length) return `${label}包含重复 CIDR`;
    if (values.some((value) => !isCIDR(value))) return `${label}包含无效 CIDR`;
  }
  const limits = draft.data.rate_limit;
  for (const [label, value] of [
    ["上行", limits?.up_bytes_per_second ?? 0],
    ["下行", limits?.down_bytes_per_second ?? 0],
  ] as const) {
    if (!Number.isSafeInteger(value) || (value !== 0 && (value < 1024 || value > 2 ** 40))) {
      return `${label}限速必须为不限速或 1 KiB/s 到 1 TiB/s`;
    }
  }
  return null;
}

function isCIDR(value: string): boolean {
  const [address, prefix, ...rest] = value.split("/");
  if (!address || prefix === undefined || rest.length > 0 || !/^\d+$/.test(prefix)) return false;
  const bits = Number(prefix);
  if (address.includes(":")) return bits >= 0 && bits <= 128 && /^[0-9a-fA-F:]+$/.test(address);
  const octets = address.split(".");
  if (bits < 0 || bits > 32 || octets.length !== 4) return false;
  if (octets.some((octet) => !/^\d+$/.test(octet) || Number(octet) > 255)) return false;
  const numeric = octets.reduce((result, octet) => (result * 256) + Number(octet), 0);
  const hostBits = 32 - bits;
  return hostBits === 32 || numeric % (2 ** hostBits) === 0;
}
