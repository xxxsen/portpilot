import {cleanup, fireEvent, render, screen} from "@testing-library/react";
import {afterEach, describe, expect, it, vi} from "vitest";
import {ClientPage} from "@/components/clients/client-page";
import type {ClientList} from "@/lib/types";

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

const clients: ClientList = {
  revision: 1,
  clients: [],
  items: [{
    client_id: "branch-a",
    remark: "Branch A",
    enabled: true,
    online: false,
    state: "OFFLINE",
    session_epoch: 0,
    epoch: 0,
    mux_sessions: 0,
    active_streams: 0,
    proxy_count: 0,
    created_at: Date.now(),
    updated_at: Date.now(),
    proxies: [],
    agent: null,
    agent_status: "unavailable",
    session: null,
  }],
};

describe("ClientPage", () => {
  it("copies only from the compact button and shows success feedback", async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("navigator", {clipboard: {writeText}});

    render(<ClientPage
      data={clients}
      csrf="csrf"
      onRefresh={vi.fn().mockResolvedValue(undefined)}
      onError={vi.fn()}
    />);

    const clientID = screen.getByText("branch-a");
    expect(clientID.closest("button")).toBeNull();
    fireEvent.click(clientID);
    expect(writeText).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", {name: "复制 branch-a 客户端 ID"}));
    expect(await screen.findByText("已复制")).toBeInTheDocument();
    expect(writeText).toHaveBeenCalledOnce();
    expect(writeText).toHaveBeenCalledWith("branch-a");
  });
});
