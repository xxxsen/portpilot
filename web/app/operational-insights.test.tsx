import {cleanup, fireEvent, render, screen, waitFor, within} from "@testing-library/react";
import {afterEach, describe, expect, it, vi} from "vitest";
import {useState} from "react";
import {downloadExport} from "@/lib/download";
import {ExportDialog} from "@/components/export/export-dialog";
import {GlobalSearch} from "@/components/search/global-search";
import {AttentionList} from "@/components/overview/attention-list";
import {MetricDelta, PeriodComparison} from "@/components/network/period-comparison";
import {ProxyRuntimeDetail} from "@/components/proxies/proxy-runtime-detail";
import type {AnalyticsComparison, ProxyRuntime} from "@/lib/types";

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

describe("operational insight controls", () => {
  it("downloads a server-built ZIP with CSRF and releases the Blob URL", async () => {
    const createObjectURL = vi.fn(() => "blob:portpilot");
    const revokeObjectURL = vi.fn();
    Object.defineProperty(URL, "createObjectURL", {configurable: true, value: createObjectURL});
    Object.defineProperty(URL, "revokeObjectURL", {configurable: true, value: revokeObjectURL});
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(() => undefined);
    const fetchMock = vi.fn().mockResolvedValue(new Response("zip-body", {
      status: 200,
      headers: {
        "Content-Type": "application/zip",
        "Content-Disposition": "attachment; filename=\"portpilot-audit_events-20260722T120000Z.zip\"",
      },
    }));
    vi.stubGlobal("fetch", fetchMock);

    const filename = await downloadExport("/api/exports/audit-events", "json", {outcome: "success"}, "csrf-value");

    expect(filename).toBe("portpilot-audit_events-20260722T120000Z.zip");
    expect(fetchMock).toHaveBeenCalledWith("/backend/api/exports/audit-events", expect.objectContaining({
      method: "POST",
      headers: expect.objectContaining({"X-CSRF-Token": "csrf-value"}),
      body: JSON.stringify({format: "json", filters: {outcome: "success"}}),
    }));
    expect(createObjectURL).toHaveBeenCalledOnce();
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:portpilot");
  });

  it("keeps the export dialog open for limits and returns 401 to login", async () => {
    const onUnauthorized = vi.fn();
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(JSON.stringify({error: "export_too_large"}), {
        status: 413, headers: {"Content-Type": "application/json"},
      }))
      .mockResolvedValueOnce(new Response(JSON.stringify({error: "unauthorized"}), {
        status: 401, headers: {"Content-Type": "application/json"},
      }));
    vi.stubGlobal("fetch", fetchMock);
    const {rerender} = render(<ExportDialog
      open onOpenChange={vi.fn()} title="导出事件" dataset="audit_events"
      endpoint="/api/exports/audit-events" filters={{query: "needle"}}
      filterSummary={["搜索：needle"]} estimatedRows={10001}
      csrf="csrf" onUnauthorized={onUnauthorized}
    />);

    fireEvent.click(screen.getByRole("button", {name: "下载 ZIP"}));
    expect(await screen.findByRole("alert")).toHaveTextContent("超过导出限制");
    expect(screen.getByRole("dialog")).toBeInTheDocument();

    rerender(<ExportDialog
      open onOpenChange={vi.fn()} title="导出事件" dataset="audit_events"
      endpoint="/api/exports/audit-events" filters={{}}
      filterSummary={[]} estimatedRows={0} csrf="csrf" onUnauthorized={onUnauthorized}
    />);
    fireEvent.click(screen.getByRole("button", {name: "下载 ZIP"}));
    await waitFor(() => expect(onUnauthorized).toHaveBeenCalledOnce());
  });

  it("groups search results, reports partial data, and supports keyboard navigation", async () => {
    const onNavigate = vi.fn();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      query: "branch", partial: true, unavailable_sources: ["history"],
      items: [{
        type: "client", id: "branch-a", title: "Branch A", subtitle: "branch-a · ACTIVE", score: 100,
        destination: {view: "clients", params: {client_id: "branch-a"}},
      }, {
        type: "proxy", id: "branch-a/ssh", title: "SSH", subtitle: "branch-a · tcp · :7000", score: 90,
        destination: {view: "proxies", params: {client_id: "branch-a", proxy_id: "ssh"}},
      }],
    }), {status: 200, headers: {"Content-Type": "application/json"}})));

    render(<SearchHarness onNavigate={onNavigate} />);
    const input = screen.getByRole("textbox", {name: "全局搜索"});
    fireEvent.change(input, {target: {value: "branch"}});
    expect(await screen.findByText("历史数据暂不可用，已显示配置结果")).toBeInTheDocument();
    expect(screen.getByRole("heading", {name: "客户端"})).toBeInTheDocument();
    expect(screen.getByRole("heading", {name: "代理"})).toBeInTheDocument();

    fireEvent.keyDown(input, {key: "ArrowDown"});
    fireEvent.keyDown(input, {key: "Enter"});
    expect(onNavigate).toHaveBeenCalledWith({view: "proxies", params: {client_id: "branch-a", proxy_id: "ssh"}});
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  });

  it("navigates attention items using their server destination", async () => {
    const onNavigate = vi.fn();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      snapshot_at: Date.now(), total: 1, counts: {critical: 0, warning: 1, info: 0},
      items: [{
        id: "client_offline:branch-a", severity: "warning", kind: "client_offline",
        title: "Branch A 离线", summary: "2 条代理正在等待客户端",
        first_observed_at: Date.now() - 60000, updated_at: Date.now(),
        client_id: "branch-a", proxy_id: "", count: 2,
        destination: {view: "clients", params: {client_id: "branch-a"}},
      }],
    }), {status: 200, headers: {"Content-Type": "application/json"}})));

    render(<AttentionList onNavigate={onNavigate} onUnauthorized={vi.fn()} />);
    fireEvent.click(await screen.findByRole("button", {name: /Branch A 离线/}));
    expect(onNavigate).toHaveBeenCalledWith({view: "clients", params: {client_id: "branch-a"}});
  });

  it("renders comparison semantics, departures, unavailable state, and overlay toggle", () => {
    const onOverlayChange = vi.fn();
    const comparison = comparisonFixture();
    const {rerender} = render(<PeriodComparison
      comparison={comparison} overlay={false} onOverlayChange={onOverlayChange}
    />);
    expect(screen.getByText(/离榜（上期 #2）/)).toBeInTheDocument();
    expect(screen.getByText("周期来源")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("checkbox"));
    expect(onOverlayChange).toHaveBeenCalledWith(true);

    rerender(<PeriodComparison comparison={null} unavailable overlay={false} onOverlayChange={onOverlayChange} />);
    expect(screen.getByText("周期对比请求失败，当前周期数据仍可使用")).toBeInTheDocument();

    rerender(<MetricDelta change={{current: 4, previous: 0, absolute_change: 4, relative_change: null, state: "new", estimated: false}} />);
    expect(screen.getByText("上周期无活动")).toBeInTheDocument();
  });

  it("shows unavailable proxy history as unavailable instead of zero", () => {
    render(<ProxyRuntimeDetail runtime={runtimeFixture()} onClose={vi.fn()} onOpenNetwork={vi.fn()} />);
    const dialog = screen.getByRole("dialog", {name: "SSH"});
    expect(within(dialog).getByText("历史观测暂不可用")).toBeInTheDocument();
    expect(within(dialog).queryByText("0.0%")).not.toBeInTheDocument();
  });
});

function SearchHarness({onNavigate}: {onNavigate: ReturnType<typeof vi.fn>}) {
  const [open, setOpen] = useState(true);
  return <GlobalSearch open={open} onOpenChange={setOpen} onNavigate={onNavigate} onUnauthorized={vi.fn()} />;
}

function comparisonFixture(): AnalyticsComparison {
  const unchanged = {current: 1, previous: 1, absolute_change: 0, relative_change: 0, state: "unchanged" as const, estimated: false};
  return {
    current: {range: {requested: "24h", effective_from: 1, effective_to: 2, resolution_seconds: 3600, timezone: "UTC"}, scope: {client_id: "", proxy_id: "", protocol: ""}, completeness: {} as never},
    previous: {range: {requested: "24h", effective_from: 0, effective_to: 1, resolution_seconds: 3600, timezone: "UTC"}, scope: {client_id: "", proxy_id: "", protocol: ""}, completeness: {} as never},
    comparable: true, reason: "", metrics: {
      connections: unchanged, failures: unchanged, failure_rate: unchanged,
      unique_ips: unchanged, up_bytes: unchanged, down_bytes: unchanged, total_bytes: unchanged,
    },
    current_series: [], previous_series: [], new_source_count: 1,
    new_sources: [{source_ip: "周期来源", geo_kind: "country", country_code: "CN", connections: 3, failures: 0, up_bytes: 100, down_bytes: 200, connection_share: 1, traffic_share: 1, first_seen_at: 1, last_seen_at: 2, rank: 1}],
    new_countries: [], proxy_changes: [],
    connections_rank_changes: [{source_ip: "198.51.100.2", geo_kind: "country", country_code: "US", current_rank: null, previous_rank: 2, rank_change: null, status: "departed"}],
    traffic_rank_changes: [],
  };
}

function runtimeFixture(): ProxyRuntime {
  return {
    client_id: "branch-a", client_remark: "Branch A", proxy_id: "ssh", proxy_remark: "SSH",
    enabled: true, protocol: "tcp", transport: "auto", effective_transport: "smux",
    bind: ":7000", connect: "192.168.50.11:22", availability: "ready", reason_code: "ready",
    listener: {state: "listening", bound_at: Date.now(), last_accept_at: 0, failed_at: 0, last_error_code: "", last_error: ""},
    session: {current: true, state: "ACTIVE", last_seen_at: Date.now(), control_rtt_millis: 10, mux_sessions: 1, active_streams: 2},
    realtime: {active_connections: 1, pending_connections: 0, up_bytes_per_second: 10, down_bytes_per_second: 20},
    history: {status: "unavailable", range: "24h", connections: null, failures: null, failure_rate: null, up_bytes: null, down_bytes: null, last_activity_at: null, last_failure_at: null, last_failure_reason_code: ""},
  };
}
