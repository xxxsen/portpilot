import { render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import Home from "./page";

describe("Home", () => {
  afterEach(() => vi.unstubAllGlobals());

  it("shows the login form when no admin session exists", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue({
      ok: false,
      status: 401,
      json: async () => ({error: "authentication_required"}),
    }));
    render(<Home />);
    expect(await screen.findByRole("heading", {name: "管理控制台"})).toBeInTheDocument();
    expect(screen.getByLabelText("用户名")).toBeInTheDocument();
    expect(screen.getByLabelText("密码")).toBeInTheDocument();
    expect(screen.getByLabelText("10 分钟")).toBeInTheDocument();
    expect(screen.getByLabelText("30 分钟")).toBeChecked();
    expect(screen.getByLabelText("1 小时")).toBeInTheDocument();
    expect(screen.getByLabelText("24 小时")).toBeInTheDocument();
    expect(screen.getByRole("link", {name: "邮件授权"})).toHaveAttribute("href", "/knock");
    expect(screen.getByText("为当前 IP 获取临时访问权限")).toBeInTheDocument();
  });
});
