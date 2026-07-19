import { expect, test, type Page } from "@playwright/test";

const proxyIds = ["e2e-tcp-smux", "e2e-tcp-direct", "e2e-udp"];

async function login(page: Page) {
  await page.goto("/");
  await page.getByLabel("用户名").fill("admin");
  await page.getByLabel("密码").fill("admin");
  await page.getByRole("button", {name: "登录"}).click();
  await expect(page.getByRole("heading", {name: "总览"})).toBeVisible();
}

async function navigate(page: Page, label: "客户端" | "代理规则" | "活动记录") {
  const menu = page.getByRole("button", {name: "打开导航"});
  if (await menu.isVisible()) await menu.click();
  await page.getByRole("button", {name: label}).click();
}

async function logout(page: Page) {
  const menu = page.getByRole("button", {name: "打开导航"});
  if (await menu.isVisible()) await menu.click();
  await page.getByRole("button", {name: "退出登录"}).click();
}

async function session(page: Page) {
  const response = await page.request.get("/backend/api/auth/me");
  expect(response.ok()).toBeTruthy();
  return response.json() as Promise<{csrf_token: string}>;
}

async function currentConfig(page: Page) {
  const response = await page.request.get("/backend/api/config");
  expect(response.ok()).toBeTruthy();
  return response.json() as Promise<{
    revision: number;
    client_config: Array<{
      client_id: string;
      client_token: string;
      enabled: boolean;
      proxy_list: Array<{
        proxy_id: string;
        remark: string;
        kind: "tunnel";
        enabled: boolean;
        data: {network: "tcp" | "udp"; transport: string; bind: string; connect: string};
      }>;
    }>;
  }>;
}

async function putConfig(page: Page, csrf: string, expectedRevision: number, config: Awaited<ReturnType<typeof currentConfig>>) {
  return page.request.put("/backend/api/config", {
    headers: {"X-CSRF-Token": csrf},
    data: {expected_revision: expectedRevision, config},
  });
}

async function removeE2EProxies(page: Page, csrf: string) {
  const config = await currentConfig(page);
  let changed = false;
  for (const client of config.client_config) {
    const filtered = client.proxy_list.filter((proxy) => !proxyIds.includes(proxy.proxy_id) && proxy.proxy_id !== "e2e-duplicate");
    changed ||= filtered.length !== client.proxy_list.length;
    client.proxy_list = filtered;
  }
  if (!changed) return;
  const response = await putConfig(page, csrf, config.revision, config);
  expect(response.ok()).toBeTruthy();
}

async function createProxy(
  page: Page,
  id: string,
  network: "tcp" | "udp",
  transport: string,
  bind: string,
  connect: string,
) {
  await page.locator("#proxy-client").selectOption("dev-client");
  await page.getByRole("button", {name: "新增代理"}).click();
  await page.getByLabel("代理 ID").fill(id);
  await page.getByLabel("备注").fill(`E2E ${id}`);
  await page.getByLabel("网络").selectOption(network);
  await page.getByLabel("承载").selectOption(transport);
  await page.getByLabel("服务端监听").fill(bind);
  await page.getByLabel("内网目标").fill(connect);
  await page.getByRole("button", {name: "保存配置"}).click();
  await expect(page.getByRole("dialog")).toBeHidden();
  await expect(page.getByRole("row").filter({hasText: id})).toBeVisible();
}

test("authentication lifecycle and secret redaction", async ({page, context}) => {
  const consoleErrors: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "error") consoleErrors.push(message.text());
  });
  await page.goto("/");
  await page.getByLabel("用户名").fill("admin");
  await page.getByLabel("密码").fill("wrong-password");
  await page.getByRole("button", {name: "登录"}).click();
  await expect(page.getByText("用户名或密码不正确")).toBeVisible();

  await page.getByLabel("密码").fill("admin");
  await page.getByRole("button", {name: "登录"}).click();
  await expect(page.getByRole("heading", {name: "总览"})).toBeVisible();
  const config = await currentConfig(page);
  expect(JSON.stringify(config)).not.toContain("cG9ydHBpbG90LWRldi");
  expect(config.client_config[0]?.client_token).toBe("__REDACTED__");

  await logout(page);
  await expect(page.getByRole("heading", {name: "管理控制台"})).toBeVisible();
  await login(page);
  await context.clearCookies();
  await page.reload();
  await expect(page.getByRole("heading", {name: "管理控制台"})).toBeVisible();
  expect(consoleErrors.filter((message) => !message.includes("status of 401"))).toEqual([]);
});

test("live status and audit views expose the formal server state", async ({page}) => {
  await login(page);
  await expect(page.getByText("dev-client", {exact: true}).first()).toBeVisible();
  await expect(page.getByText("ACTIVE", {exact: true}).first()).toBeVisible();
  await expect(page.getByText("UDP 通道", {exact: true})).toBeVisible();

  await navigate(page, "客户端");
  const client = page.locator(".client-row").filter({hasText: "dev-client"});
  await expect(client).toContainText("epoch");
  await expect(client).toContainText("3 条代理");

  await navigate(page, "活动记录");
  await expect(page.getByRole("heading", {name: "事件与审计"})).toBeVisible();
  await expect(page.locator(".event-row").first()).toBeVisible();
});

test("proxy CRUD validates drafts, rejects stale revisions, and records audit", async ({page}, testInfo) => {
  test.skip(testInfo.project.name !== "desktop", "configuration mutation runs once");
  await login(page);
  const {csrf_token: csrf} = await session(page);
  await removeE2EProxies(page, csrf);
  await page.reload();
  await expect(page.getByRole("heading", {name: "总览"})).toBeVisible();

  try {
    await navigate(page, "代理规则");
    await createProxy(page, proxyIds[0], "tcp", "smux", "127.0.0.1:7100", "127.0.0.1:18080");
    await createProxy(page, proxyIds[1], "tcp", "direct", "127.0.0.1:7101", "127.0.0.1:18080");
    await createProxy(page, proxyIds[2], "udp", "smux-datagram", "127.0.0.1:7102", "127.0.0.1:18081");

    await page.locator("#proxy-client").selectOption("dev-client");
    await page.getByRole("button", {name: "新增代理"}).click();
    await page.getByLabel("代理 ID").fill("e2e-duplicate");
    await page.getByLabel("服务端监听").fill(":7100");
    await page.getByLabel("内网目标").fill("invalid-target");
    await page.getByRole("button", {name: "保存配置"}).click();
    await expect(page.getByText("内网目标必须是有效的 host:port")).toBeVisible();
    await page.getByRole("button", {name: "关闭"}).click();

    const invalid = await currentConfig(page);
    invalid.client_config[0].proxy_list.push({
      proxy_id: "e2e-duplicate",
      remark: "server validation",
      kind: "tunnel",
      enabled: true,
      data: {network: "tcp", transport: "auto", bind: ":7100", connect: "127.0.0.1:18080"},
    });
    const invalidResponse = await putConfig(page, csrf, invalid.revision, invalid);
    expect(invalidResponse.status()).toBe(400);

    const concurrent = await currentConfig(page);
    concurrent.client_config[0].proxy_list.find((proxy) => proxy.proxy_id === proxyIds[0])!.remark = "concurrent update";
    const first = await putConfig(page, csrf, concurrent.revision, concurrent);
    expect(first.ok()).toBeTruthy();

    const staleRow = page.getByRole("row").filter({hasText: proxyIds[1]});
    await staleRow.locator(".switch").click();
    await expect(page.getByText("配置已被其他会话更新，已重新载入最新版本")).toBeVisible();

    await navigate(page, "活动记录");
    await expect(page.locator(".event-row").filter({hasText: "config_update"}).first()).toBeVisible();
  } finally {
    await removeE2EProxies(page, csrf);
  }
});
