import {cleanup, fireEvent, render, screen, waitFor} from "@testing-library/react";
import {afterEach, describe, expect, it, vi} from "vitest";
import KnockPage from "@/app/knock/page";
import KnockVerifyPage from "@/app/knock/verify/page";

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

describe("public knock pages", () => {
  it("shows source status and keeps the accepted response generic", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(response({
        enabled: true, source_ip: "203.0.113.10", authorized: false,
        granted_at: 0, expires_at: 0, remaining_seconds: 0,
      }))
      .mockResolvedValueOnce(response({
        message: "如果该邮箱允许访问，授权邮件将很快送达",
      }));
    vi.stubGlobal("fetch", fetchMock);
    render(<KnockPage />);

    expect(await screen.findByText("203.0.113.10")).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("授权邮箱"), {
      target: {value: "user@example.com"},
    });
    fireEvent.click(screen.getByRole("button", {name: "发送访问邮件"}));
    expect(await screen.findByText("如果该邮箱允许访问，授权邮件将很快送达。")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenLastCalledWith("/api/knock/requests", expect.objectContaining({
      method: "POST", body: JSON.stringify({email: "user@example.com"}),
    }));
  });

  it("removes the fragment before posting the token", async () => {
    window.history.replaceState(null, "", "/knock/verify#token=abc_test-token");
    let pathAtVerify = "";
    vi.stubGlobal("fetch", vi.fn(async (path: string) => {
      if (path === "/api/knock/verify") {
        pathAtVerify = window.location.href;
        return response({
          source_ip: "203.0.113.10", granted_at: Date.now(),
          expires_at: Date.now() + 600_000, remaining_seconds: 600, refreshed: false,
        });
      }
      return response({
        enabled: true, source_ip: "203.0.113.10", authorized: true,
        granted_at: Date.now(), expires_at: Date.now() + 600_000, remaining_seconds: 600,
      });
    }));
    render(<KnockVerifyPage />);

    expect(await screen.findByText("临时访问授权已生效")).toBeInTheDocument();
    expect(pathAtVerify).not.toContain("#token=");
    expect(window.location.hash).toBe("");
  });

  it("explains source mismatch without retrying or consuming another request", async () => {
    window.history.replaceState(null, "", "/knock/verify#token=mismatch-token");
    const fetchMock = vi.fn().mockResolvedValue({
      ok: false, status: 403, json: async () => ({error: "source_ip_mismatch"}),
    });
    vi.stubGlobal("fetch", fetchMock);
    render(<KnockVerifyPage />);

    expect(await screen.findByText("当前网络与申请时不同")).toBeInTheDocument();
    expect(screen.getByText(/链接尚未被消费/)).toBeInTheDocument();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
  });
});

function response(value: unknown) {
  return Promise.resolve({
    ok: true,
    status: 200,
    json: async () => value,
  });
}
