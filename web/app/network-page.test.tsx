import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { NetworkPage } from "@/components/network/network-page";
import { api } from "@/lib/api";
import type { NetworkConnection, NetworkSummary, Page } from "@/lib/types";

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
        return pageOf();
      }
      throw new Error(`unexpected API path: ${path}`);
    });
  });

  it("switches between stable live and historical connection panels", async () => {
    render(<NetworkPage onUnauthorized={vi.fn()} />);

    expect(await screen.findByRole("heading", {name: "实时连接"})).toBeInTheDocument();
    expect(screen.queryByRole("heading", {name: "历史连接"})).not.toBeInTheDocument();
    expect(screen.getAllByText("2.00 KiB/s").length).toBeGreaterThan(0);
    expect(screen.getAllByText("1.00 KiB/s").length).toBeGreaterThan(0);
    expect(screen.getByText("10 秒窗口")).toBeInTheDocument();
    expect(screen.getByTestId("line-chart")).toHaveAttribute("data-margin-top", "18");
    expect(screen.getByTestId("y-axis")).toHaveAttribute("data-tick-margin", "8");
    expect(screen.getByTestId("y-axis")).toHaveAttribute("data-width", "84");

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
    expect(await screen.findByText("10 秒窗口")).toBeInTheDocument();

    vi.mocked(api).mockImplementation(() => new Promise(() => {}));
    fireEvent.click(screen.getByRole("button", {name: "刷新网络活动"}));

    expect(screen.getByText("10 秒窗口")).toBeInTheDocument();
    expect(screen.getAllByText("2.00 KiB/s").length).toBeGreaterThan(0);
    expect(screen.queryByText("正在加载实时连接")).not.toBeInTheDocument();
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
