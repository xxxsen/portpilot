import { cleanup, render, screen, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { AuditPage } from "@/components/audit/audit-page";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("AuditPage", () => {
  it("shows the operator IP and excludes runtime log event filters", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        items: [{
          id: "event-1", occurred_at: Date.now(), category: "auth",
          event_type: "admin_login", outcome: "success", actor_type: "admin",
          actor_id: "operator", source_addr: "203.0.113.8", client_id: "",
          proxy_id: "", connection_id: "", summary: "管理员登录成功",
          reason_code: "", reason: "", details: {}, config_changes: [],
        }],
        page: 1, page_size: 25, total: 1, total_pages: 1,
      }),
    }));

    render(<AuditPage csrf="csrf-test" onUnauthorized={vi.fn()} />);
    expect(await screen.findByText("admin_login")).toBeInTheDocument();
    expect(screen.getByRole("columnheader", {name: "IP"})).toBeInTheDocument();
    expect(await screen.findByText("203.0.113.8")).toBeInTheDocument();

    const eventFilter = screen.getByLabelText("事件类型");
    for (const value of ["security", "external_access_denied", "udp_drop", "proxy_protocol_write_failed"]) {
      expect(within(eventFilter).queryByRole("option", {name: value})).not.toBeInTheDocument();
    }
  });

  it("shows client session source IP in the audit table", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({
        items: [{
          id: "event-2", occurred_at: Date.now(), category: "client",
          event_type: "client_session", outcome: "success", actor_type: "client",
          actor_id: "branch-a", source_addr: "198.51.100.12", client_id: "branch-a",
          proxy_id: "", connection_id: "", summary: "客户端会话上线",
          reason_code: "online", reason: "", details: {}, config_changes: [],
        }],
        page: 1, page_size: 25, total: 1, total_pages: 1,
      }),
    }));

    render(<AuditPage csrf="csrf-test" onUnauthorized={vi.fn()} />);
    expect(await screen.findByText("198.51.100.12")).toBeInTheDocument();
  });
});
