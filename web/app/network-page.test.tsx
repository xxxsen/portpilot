import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { NetworkPage } from "@/components/network/network-page";
import { api } from "@/lib/api";
import type {
  AnalyticsOverview,
  AnalyticsTopIPs,
  NetworkClient,
  NetworkConnection,
  NetworkSummary,
  Page,
} from "@/lib/types";

vi.mock("@/lib/api", () => ({api: vi.fn()}));
vi.mock("recharts", () => ({
  ResponsiveContainer: ({children}: {children: ReactNode}) => <div>{children}</div>,
  LineChart: ({children, margin}: {
    children: ReactNode;
    margin?: {top?: number; right?: number; bottom?: number; left?: number};
  }) => <div data-testid="line-chart" data-margin-top={margin?.top}>{children}</div>,
  CartesianGrid: () => null,
  Line: () => null,
  Tooltip: () => null,
  XAxis: () => null,
  YAxis: ({tickMargin, width}: {tickMargin?: number; width?: number}) => (
    <div data-testid="y-axis" data-tick-margin={tickMargin} data-width={width} />
  ),
}));

afterEach(cleanup);

const activeConnection: NetworkConnection = {
  activity_id: "active-connection",
  protocol: "tcp",
  state: "active",
  client_id: "client-a",
  client_remark: "Client A",
  proxy_id: "proxy-a",
  proxy_remark: "SSH",
  external_addr: "192.0.2.10:50000",
  external_ip: "192.0.2.10",
  server_bind: ":7000",
  target_addr: "10.0.0.2:22",
  transport: "smux",
  session_epoch: 1,
  accepted_at: 1000,
  connected_at: 1100,
  last_activity_at: 2000,
  up_bytes: 4096,
  down_bytes: 8192,
  up_packets: 0,
  down_packets: 0,
  up_bytes_per_second: 2048,
  down_bytes_per_second: 1024,
  rate_sampled_at: 2000,
  rate_window_millis: 10000,
  close_reason_code: "",
  close_reason: "",
};

const historyConnection: NetworkConnection = {
  ...activeConnection,
  activity_id: "history-connection",
  state: "closed",
  closed_at: 3000,
  up_bytes_per_second: 0,
  down_bytes_per_second: 0,
  rate_sampled_at: 0,
  rate_window_millis: 0,
};

const networkClient: NetworkClient = {
  client_id: "client-a",
  client_remark: "Client A",
  enabled: true,
  online: true,
  deleted: false,
  session_epoch: 1,
  mux_sessions: 1,
  active_connections: 1,
  historical_connections: 2,
  failures: 0,
  up_bytes: 4096,
  down_bytes: 8192,
  last_seen_at: 3000,
};

const analyticsMetadata = {
  range: {
    requested: "24h" as const,
    effective_from: 0,
    effective_to: 0,
    resolution_seconds: 3600,
    timezone: "UTC" as const,
  },
  scope: {client_id: "", proxy_id: "", protocol: ""},
  completeness: {
    status: "ready" as const,
    complete_from: 0,
    statistics_available_from: 0,
    statistics_retention_seconds: 7776000,
    details_available_from: 0,
    estimated_bytes: 0,
    estimated_ratio: 0,
    geoip_status: "disabled" as const,
    geoip_database_fingerprint: "",
  },
};

describe("NetworkPage", () => {
  beforeEach(() => {
    window.history.replaceState(null, "", "/");
    vi.mocked(api).mockReset();
    vi.mocked(api).mockImplementation(async (path) => {
      if (path.startsWith("/api/network/summary")) {
        return {
          range: "24h",
          client_id: "",
          active_connections: 1,
          active_clients: 1,
          connections: 2,
          failures: 0,
          failure_rate: 0,
          up_bytes: 4096,
          down_bytes: 8192,
          series: [],
          clients: [],
        } satisfies NetworkSummary;
      }
      if (path.includes("scope=active")) {
        return pageOf(activeConnection);
      }
      if (path.includes("scope=history")) {
        return pageOf(historyConnection);
      }
      if (path.startsWith("/api/network/clients")) {
        return pageOf(networkClient);
      }
      if (path.startsWith("/api/network/analytics/overview")) {
        return {
          ...analyticsMetadata,
          connections: 2,
          failures: 0,
          failure_rate: 0,
          unique_ips: 1,
          countries: 0,
          up_bytes: 4096,
          down_bytes: 8192,
          estimated_bytes: 0,
          series: [],
          country_distribution: [{
            geo_kind: "reserved",
            country_code: "",
            connections: 2,
            unique_ips: 1,
            up_bytes: 4096,
            down_bytes: 8192,
            traffic_share: 1,
          }],
        } satisfies AnalyticsOverview;
      }
      if (path.startsWith("/api/network/analytics/dimensions")) {
        return {
          ...analyticsMetadata,
          items: [{
            client_id: "client-a",
            client_remark: "Client A",
            client_deleted: false,
            proxy_id: "proxy-a",
            proxy_remark: "SSH",
            proxy_deleted: false,
            protocol: "tcp",
            connections: 2,
            failures: 0,
            unique_ips: 1,
            countries: 0,
            up_bytes: 4096,
            down_bytes: 8192,
            top_country_code: "",
            first_seen_at: 1000,
            last_seen_at: 3000,
          }],
          page: 1,
          page_size: 25,
          total: 1,
          total_pages: 1,
        };
      }
      if (path.startsWith("/api/network/analytics/top-ips")) {
        const item = {
          rank: 1,
          source_ip: "192.0.2.10",
          geo_kind: "reserved",
          country_code: "",
          connections: 2,
          failures: 0,
          up_bytes: 4096,
          down_bytes: 8192,
          connection_share: 1,
          traffic_share: 1,
          first_seen_at: 1000,
          last_seen_at: 3000,
        };
        return {
          ...analyticsMetadata,
          by_connections: [item],
          by_traffic: [item],
        } satisfies AnalyticsTopIPs;
      }
      if (path.startsWith("/api/network/analytics/ip-series")) {
        return {
          ...analyticsMetadata,
          source_ip: "192.0.2.10",
          geo_kind: "reserved",
          country_code: "",
          connections: 2,
          failures: 0,
          up_bytes: 4096,
          down_bytes: 8192,
          first_seen_at: 1000,
          last_seen_at: 3000,
          series: [],
        };
      }
      throw new Error(`unexpected API path: ${path}`);
    });
  });

  it("opens on historical insights and keeps connection details inside the page", async () => {
    render(<NetworkPage onUnauthorized={vi.fn()} />);

    expect(await screen.findByRole("heading", {name: "历史指标"})).toBeInTheDocument();
    expect(screen.getAllByText("查看数据表")).toHaveLength(2);
    expect(screen.queryByRole("heading", {name: "实时连接"})).not.toBeInTheDocument();
    expect(vi.mocked(api)).not.toHaveBeenCalledWith(
      expect.stringContaining("scope=active"),
      expect.anything(),
    );

    fireEvent.click(screen.getByRole("button", {name: "连接明细"}));
    expect(await screen.findByRole("heading", {name: "实时连接"})).toBeInTheDocument();
    expect(screen.queryByRole("heading", {name: "历史连接"})).not.toBeInTheDocument();
    expect((await screen.findAllByText("2.00 KiB/s")).length).toBeGreaterThan(0);
    expect(screen.getAllByText("1.00 KiB/s").length).toBeGreaterThan(0);
    expect(screen.getByText("10 秒窗口")).toBeInTheDocument();

    await waitFor(() => {
      expect(vi.mocked(api)).toHaveBeenCalledWith(
        expect.stringContaining("scope=active"),
        expect.anything(),
      );
    });

    fireEvent.click(screen.getByRole("button", {name: "历史连接"}));
    expect(await screen.findByRole("heading", {name: "历史连接"})).toBeInTheDocument();
    expect(screen.queryByRole("heading", {name: "实时连接"})).not.toBeInTheDocument();
    await waitFor(() => {
      expect(vi.mocked(api)).toHaveBeenCalledWith(
        expect.stringContaining("scope=history"),
        expect.anything(),
      );
    });
  });

  it("keeps the current live rows visible while a refresh is in flight", async () => {
    render(<NetworkPage onUnauthorized={vi.fn()} />);
    await screen.findByRole("heading", {name: "历史指标"});
    fireEvent.click(screen.getByRole("button", {name: "连接明细"}));
    expect(await screen.findByText("10 秒窗口")).toBeInTheDocument();

    vi.mocked(api).mockImplementation(() => new Promise(() => {}));
    fireEvent.click(screen.getByRole("button", {name: "刷新网络活动"}));

    expect(screen.getByText("10 秒窗口")).toBeInTheDocument();
    expect(screen.getAllByText("2.00 KiB/s").length).toBeGreaterThan(0);
    expect(screen.queryByText("正在加载实时连接")).not.toBeInTheDocument();
  });

  it("updates scope and lazily opens an IP drill-down", async () => {
    render(<NetworkPage onUnauthorized={vi.fn()} />);
    const dimension = await screen.findByRole("row", {name: /Client A.*SSH/});
    fireEvent.click(dimension);
    await waitFor(() => {
      expect(vi.mocked(api)).toHaveBeenCalledWith(
        expect.stringMatching(/client_id=client-a.*proxy_id=proxy-a|proxy_id=proxy-a.*client_id=client-a/),
        expect.anything(),
      );
    });
    expect(window.location.search).toContain("proxy_id=proxy-a");

    const rankingRows = await screen.findAllByRole("row", {name: /192.0.2.10/});
    fireEvent.click(screen.getByRole("row", {name: "按国家分类高亮：保留地址"}));
    expect(rankingRows.every((row) => row.classList.contains("country-highlight-row"))).toBe(true);
    fireEvent.click(rankingRows[0]);
    await waitFor(() => {
      expect(vi.mocked(api)).toHaveBeenCalledWith(
        expect.stringContaining("/api/network/analytics/ip-series"),
        expect.anything(),
      );
    });
    expect(await screen.findByRole("heading", {name: /IP 详情/})).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", {name: "查看该 IP 的历史连接"}));
    expect(await screen.findByRole("heading", {name: "历史连接"})).toBeInTheDocument();
    expect(screen.getByLabelText("搜索连接")).toHaveValue("192.0.2.10");
  });

  it("keeps an explicit all-proxies scope in client view", async () => {
    render(<NetworkPage onUnauthorized={vi.fn()} />);
    await screen.findByRole("heading", {name: "历史指标"});
    fireEvent.click(screen.getByRole("button", {name: "客户端"}));
    expect(await screen.findByText("选择一个客户端后查看历史洞察")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("查看客户端"), {target: {value: "client-a"}});
    const allProxies = await screen.findByRole("row", {name: /Client A.*全部代理/});
    expect(allProxies).toHaveClass("selected-row");
    const proxy = screen.getByRole("row", {name: /Client A.*SSH/});
    fireEvent.click(proxy);
    await waitFor(() => expect(window.location.search).toContain("proxy_id=proxy-a"));
    fireEvent.click(allProxies);
    await waitFor(() => {
      expect(window.location.search).toContain("client_id=client-a");
      expect(window.location.search).not.toContain("proxy_id=");
    });
  });
});

function pageOf<T>(item?: T): Page<T> {
  return {
    items: item ? [item] : [],
    page: 1,
    page_size: 25,
    total: item ? 1 : 0,
    total_pages: item ? 1 : 0,
  };
}
