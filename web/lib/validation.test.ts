import { describe, expect, it } from "vitest";
import type { Proxy } from "./types";
import { validateProxyDraft } from "./validation";

function proxy(id: string, network: "tcp" | "udp", transport: string, bind: string): Proxy {
  return {
    proxy_id: id,
    remark: "",
    kind: "tunnel",
    enabled: true,
    data: {network, transport, bind, connect: "127.0.0.1:18080"},
  };
}

describe("validateProxyDraft", () => {
  it("accepts all supported proxy transports", () => {
    expect(validateProxyDraft(proxy("tcp-smux", "tcp", "smux", ":7100"), [])).toBeNull();
    expect(validateProxyDraft(proxy("tcp-direct", "tcp", "direct", ":7101"), [])).toBeNull();
    expect(validateProxyDraft(proxy("udp", "udp", "smux-datagram", ":7102"), [])).toBeNull();
  });

  it("rejects malformed addresses, protocol mismatch, and duplicate ports", () => {
    const existing = [proxy("existing", "tcp", "auto", "127.0.0.1:7100")];
    expect(validateProxyDraft(proxy("duplicate", "tcp", "auto", ":7100"), existing)).toContain("已被");
    expect(validateProxyDraft({
      ...proxy("bad-target", "tcp", "auto", ":7101"),
      data: {...proxy("x", "tcp", "auto", ":7101").data, connect: "missing-port"},
    }, existing)).toContain("内网目标");
    expect(validateProxyDraft(proxy("bad-udp", "udp", "direct", ":7102"), existing)).toContain("UDP");
  });

  it("validates external CIDRs, rate limits, and PROXY protocol", () => {
    const valid = proxy("secured", "tcp", "auto", ":7200");
    valid.data.access_policy = {
      allow_cidrs: ["192.0.2.0/24", "2001:db8::/32"],
      deny_cidrs: ["192.0.2.128/25"],
    };
    valid.data.rate_limit = {
      up_bytes_per_second: 1024,
      down_bytes_per_second: 2 ** 40,
    };
    valid.data.enable_proxy_protocol = true;
    expect(validateProxyDraft(valid, [])).toBeNull();

    const badCIDR = structuredClone(valid);
    badCIDR.data.access_policy!.allow_cidrs = ["192.0.2.1/24"];
    expect(validateProxyDraft(badCIDR, [])).toContain("CIDR");

    const badRate = structuredClone(valid);
    badRate.data.rate_limit!.up_bytes_per_second = 1023;
    expect(validateProxyDraft(badRate, [])).toContain("限速");

    const udpProxy = proxy("udp-proxy", "udp", "smux-datagram", ":7201");
    udpProxy.data.enable_proxy_protocol = true;
    expect(validateProxyDraft(udpProxy, [])).toContain("PROXY Protocol");
  });
});
