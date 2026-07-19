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
  });
});

