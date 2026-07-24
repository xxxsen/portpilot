import { expect, test, type Page } from "@playwright/test";

const proxyIds = ["e2e-tcp-smux", "e2e-tcp-direct", "e2e-udp"];

async function login(page: Page) {
  await page.goto("/");
  await page.getByLabel("用户名").fill("admin");
  await page.getByLabel("密码").fill("admin");
  await page.getByRole("button", {name: "登录"}).click();
  await expect(page.getByRole("heading", {name: "总览"})).toBeVisible();
}

async function navigate(page: Page, label: "客户端" | "代理规则" | "网络活动" | "事件与审计") {
  const menu = page.getByRole("button", {name: "打开导航"});
  if (await menu.isVisible()) await menu.click();
  await page.getByRole("navigation").getByRole("button", {name: label}).click();
}

async function expectYAxisLabelsInsideChart(page: Page) {
  const chart = page.locator(".insight-chart").first();
  const svg = chart.locator("svg");
  const ticks = chart.locator(".recharts-yAxis .recharts-cartesian-axis-tick-value");
  await expect(ticks.first()).toBeVisible();

  const svgBox = await svg.boundingBox();
  expect(svgBox).not.toBeNull();
  const tickBoxes = await ticks.evaluateAll((elements) => elements.map((element) => {
    const box = element.getBoundingClientRect();
    return {top: box.top, right: box.right, bottom: box.bottom, left: box.left};
  }));
  expect(tickBoxes.length).toBeGreaterThan(0);
  for (const box of tickBoxes) {
    expect(box.top).toBeGreaterThanOrEqual(svgBox!.y);
    expect(box.left).toBeGreaterThanOrEqual(svgBox!.x);
    expect(box.right).toBeLessThanOrEqual(svgBox!.x + svgBox!.width);
    expect(box.bottom).toBeLessThanOrEqual(svgBox!.y + svgBox!.height);
  }
}

async function logout(page: Page) {
  const menu = page.getByRole("button", {name: "打开导航"});
  if (await menu.isVisible()) await menu.click();
  await page.getByRole("button", {name: "退出登录"}).click();
}

async function session(page: Page) {
  const response = await page.request.get("/api/auth/me");
  expect(response.ok()).toBeTruthy();
  return response.json() as Promise<{csrf_token: string}>;
}

async function currentConfig(page: Page) {
  const response = await page.request.get("/api/config");
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
  return page.request.put("/api/config", {
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

test.beforeEach(({page}) => {
  page.on("dialog", (dialog) => {
    void dialog.dismiss();
    throw new Error(`unexpected native browser dialog: ${dialog.type()}`);
  });
});

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
  await page.locator("label").filter({hasText: "10 分钟"}).click();
  await page.getByRole("button", {name: "登录"}).click();
  await expect(page.getByRole("heading", {name: "总览"})).toBeVisible();
  const meResponse = await page.request.get("/api/auth/me");
  expect(meResponse.ok()).toBeTruthy();
  const me = await meResponse.json() as {
    authenticated_at: number;
    expires_at: number;
    session_ttl: string;
  };
  expect(me.session_ttl).toBe("10m");
  expect(me.expires_at - me.authenticated_at).toBe(10 * 60 * 1000);
  const adminCookie = (await context.cookies()).find((cookie) => cookie.name === "portpilot_admin");
  expect(adminCookie).toBeDefined();
  expect(Math.abs((adminCookie?.expires ?? 0) * 1000 - me.expires_at)).toBeLessThan(1100);
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

test("live status and audit views expose the formal server state", async ({page}, testInfo) => {
  await login(page);
  await expect(page.getByRole("heading", {name: "需要处理"})).toBeVisible();
  await expect(page.getByText("dev-client", {exact: true}).first()).toBeVisible();
  await expect(page.getByText("ACTIVE", {exact: true}).first()).toBeVisible();
  await expect(page.getByText("UDP 通道", {exact: true})).toBeVisible();

  const overviewClient = page.locator(".overview-client-row").filter({hasText: "dev-client"});
  await expect(overviewClient).toContainText("心跳");
  await expect(overviewClient).not.toContainText("心跳 -");
  await expect(overviewClient.getByLabel("客户端版本 dev，服务端版本 dev")).toBeVisible();
  await expect(overviewClient.locator(".client-version")).toHaveClass(/version-match/);
  await overviewClient.locator("td").first().click();
  const overviewDetail = page.getByRole("dialog", {name: "本地开发客户端"});
  await expect(overviewDetail.getByRole("heading", {name: "Agent 环境"})).toBeVisible();
  await expect(overviewDetail.getByText("最后心跳")).toBeVisible();
  await overviewDetail.getByRole("button", {name: "关闭"}).click();
  await overviewClient.getByRole("button", {name: "查看 dev-client 网络活动"}).click();
  await expect(page.getByRole("heading", {name: "网络活动"})).toBeVisible();
  await expect(page.getByLabel("网络活动视图").getByRole("button", {name: "客户端", exact: true})).toHaveClass(/selected/);
  await expect(page.getByLabel("查看客户端")).toHaveValue("dev-client");
  expect(new URL(page.url()).searchParams.get("client_id")).toBe("dev-client");

  await navigate(page, "客户端");
  const client = page.locator(".client-row").filter({hasText: "dev-client"});
  await expect(client).toContainText("7 条代理");
  await client.getByRole("button", {name: "查看 dev-client 详情"}).click();
  const clientDetail = page.getByRole("dialog", {name: "本地开发客户端"});
  await expect(clientDetail.getByRole("heading", {name: "Agent 环境"})).toBeVisible();
  await expect(clientDetail).toContainText("tcp-smux");
  await expect(clientDetail.getByRole("heading", {name: "控制会话"})).toBeVisible();
  await clientDetail.getByRole("button", {name: "关闭"}).click();

  await page.keyboard.press("ControlOrMeta+K");
  const globalSearch = page.getByRole("dialog", {name: "全局搜索"});
  await expect(globalSearch).toBeVisible();
  await globalSearch.getByRole("textbox", {name: "全局搜索"}).fill("dev-client");
  await expect(globalSearch.getByRole("button").filter({hasText: "dev-client"}).first()).toBeVisible();
  await globalSearch.getByRole("textbox", {name: "全局搜索"}).press("Enter");
  await expect(page.getByRole("dialog", {name: "本地开发客户端"})).toBeVisible();
  expect(new URL(page.url()).searchParams.get("client_id")).toBe("dev-client");
  await page.getByRole("dialog", {name: "本地开发客户端"}).getByRole("button", {name: "关闭"}).click();

  await navigate(page, "网络活动");
  await expect(page.getByRole("heading", {name: "网络活动"})).toBeVisible();
  await expect(page.getByRole("button", {name: "历史洞察", exact: true})).toHaveClass(/selected/);
  await expect(page.getByRole("button", {name: "24h"})).toHaveClass(/selected/);
  await expect(page.getByRole("heading", {name: "历史指标"})).toBeVisible();
  await expect(page.getByRole("heading", {name: "变化提示"})).toBeVisible();
  await expectYAxisLabelsInsideChart(page);
  const chartBeforeRefresh = await page.locator(".insight-chart").first().boundingBox();
  const refreshResponse = page.waitForResponse((response) =>
    response.url().includes("/api/network/analytics/overview") && response.ok()
  );
  await page.getByRole("button", {name: "刷新历史洞察"}).click();
  await refreshResponse;
  const chartAfterRefresh = await page.locator(".insight-chart").first().boundingBox();
  expect(chartBeforeRefresh).not.toBeNull();
  expect(chartAfterRefresh).not.toBeNull();
  expect(Math.abs(chartAfterRefresh!.height - chartBeforeRefresh!.height)).toBeLessThan(1);
  expect(Math.abs(chartAfterRefresh!.width - chartBeforeRefresh!.width)).toBeLessThan(1);
  const analyticsResponse = await page.request.get("/api/network/analytics/overview?range=24h");
  expect(analyticsResponse.ok()).toBeTruthy();
  const analytics = await analyticsResponse.json() as {
    connections: number;
    series: Array<{bucket_at: number}>;
    completeness: {status: string; geoip_status: string};
  };
  expect(analytics.connections).toBeGreaterThan(0);
  expect(analytics.series.length).toBe(24);
  expect(["ready", "backfilling"]).toContain(analytics.completeness.status);
  expect(["disabled", "ready", "degraded"]).toContain(analytics.completeness.geoip_status);
  const connectionRows = page.locator(".ranking-connections tbody tr");
  const trafficRows = page.locator(".ranking-traffic tbody tr");
  const connectionTarget = connectionRows.filter({hasText: "127.0.0.2"});
  const trafficTarget = trafficRows.filter({hasText: "127.0.0.3"});
  await expect(connectionTarget).toBeVisible();
  await expect(connectionRows.filter({hasText: "127.0.0.3"})).toBeVisible();
  const connectionRanking = await connectionRows.allTextContents();
  expect(connectionRanking.findIndex((row) => row.includes("127.0.0.2"))).toBeLessThan(
    connectionRanking.findIndex((row) => row.includes("127.0.0.3")),
  );
  await connectionTarget.click();
  await expect(trafficTarget).toBeVisible();
  const connectionRankingBox = await page.locator(".ranking-connections").boundingBox();
  const trafficRankingBox = await page.locator(".ranking-traffic").boundingBox();
  expect(connectionRankingBox).not.toBeNull();
  expect(trafficRankingBox).not.toBeNull();
  expect(trafficRankingBox!.y).toBeGreaterThanOrEqual(connectionRankingBox!.y + connectionRankingBox!.height - 1);
  if (testInfo.project.name !== "mobile") {
    const rankingOverflow = await page.locator(".ranking-table .table-wrap").evaluateAll((elements) =>
      elements.map((element) => element.scrollWidth > element.clientWidth + 1)
    );
    expect(rankingOverflow).toEqual([false, false]);
  }
  await expect(trafficRows.filter({hasText: "127.0.0.2"})).toBeVisible();
  const trafficRanking = await trafficRows.allTextContents();
  expect(trafficRanking.findIndex((row) => row.includes("127.0.0.3"))).toBeLessThan(
    trafficRanking.findIndex((row) => row.includes("127.0.0.2")),
  );
  await expect(page.getByRole("heading", {name: /IP 详情.*127\.0\.0\.2/})).toBeVisible();
  await expect(page.locator(".ranking-table tbody tr.selected-row").filter({hasText: "127.0.0.2"})).toHaveCount(2);
  await expect(page.locator(".ip-detail-section .insight-chart")).toHaveCount(2);
  await page.getByRole("button", {name: "查看该 IP 的历史连接"}).click();
  await expect(page.getByRole("heading", {name: "历史连接"})).toBeVisible();
  await expect(page.getByLabel("搜索连接")).toHaveValue("127.0.0.2");
  await page.getByRole("button", {name: "历史洞察", exact: true}).click();
  const proxyDimension = page.locator(".analytics-dimension-table tbody tr").filter({hasText: "dev-tcp-echo"});
  await expect(proxyDimension).toBeVisible();
  await proxyDimension.click();
  await expect(page.locator(".scope-breadcrumb")).toContainText("Local TCP echo");
  expect(new URL(page.url()).searchParams.get("proxy_id")).toBe("dev-tcp-echo");
  const hasHorizontalOverflow = await page.evaluate(
    () => document.documentElement.scrollWidth > document.documentElement.clientWidth + 1,
  );
  expect(hasHorizontalOverflow).toBe(false);
  await page.getByRole("button", {name: "连接明细"}).click();
  await expect(page.getByRole("heading", {name: "历史连接"})).toBeVisible();
  await expect(page.getByLabel("搜索连接")).toHaveValue("127.0.0.2");
  await page.getByRole("button", {name: "实时连接"}).click();
  await expect(page.getByRole("heading", {name: "实时连接"})).toBeVisible();
  await expect(page.locator(".live-connections-table th:visible").filter({hasText: /上行速率|实时速率/})).toBeVisible();
  await page.getByRole("button", {name: "历史连接"}).click();
  await expect(page.getByRole("heading", {name: "历史连接"})).toBeVisible();
  await expect(page.getByRole("heading", {name: "实时连接"})).toHaveCount(0);
  const exportResponse = page.waitForResponse((response) =>
    response.url().includes("/api/exports/network-connections") && response.request().method() === "POST"
  );
  await page.getByRole("button", {name: "导出连接"}).click();
  const exportDialog = page.getByRole("dialog", {name: "导出连接明细"});
  await expect(exportDialog).toContainText("最多 10,000 行");
  const download = page.waitForEvent("download");
  await exportDialog.getByRole("button", {name: "下载 ZIP"}).click();
  const response = await exportResponse;
  expect(response.ok()).toBeTruthy();
  expect(response.headers()["content-type"]).toBe("application/zip");
  expect(response.headers()["content-disposition"]).toContain("attachment;");
  expect(response.headers()["content-disposition"]).toContain(".zip");
  const downloaded = await download;
  expect(downloaded.suggestedFilename()).toMatch(/^portpilot-network_connections-.*\.zip$/);
  await expect(exportDialog).toBeHidden();
  const networkResponse = await page.request.get("/api/network/connections?scope=history&page=1&page_size=25");
  expect(networkResponse.ok()).toBeTruthy();
  const network = await networkResponse.json() as {
    items: Array<{proxy_id: string; protocol: string}>;
  };
  expect(network.items.length).toBeGreaterThan(0);
  const visibleTCPConnection = page.locator(".history-connections-table tbody tr")
    .filter({hasText: "TCP"}).first();
  await expect(visibleTCPConnection).toBeVisible();
  await visibleTCPConnection.click();
  await expect(page.getByRole("dialog", {name: "连接详情"})).toBeVisible();
  await expect(page.getByText("外部来源", {exact: true})).toBeVisible();
  await expect(page.getByText("PROXY Protocol", {exact: true})).toBeVisible();
  await expect(page.getByText("连接限速", {exact: true})).toBeVisible();
  await page.getByRole("button", {name: "关闭"}).click();

  await navigate(page, "事件与审计");
  await expect(page.getByRole("heading", {name: "事件与审计", level: 1})).toBeVisible();
  await expect(page.locator(".audit-table tbody tr").first()).toBeVisible();
  await expect(page.locator(".audit-table thead th").filter({hasText: /^IP$/})).toBeVisible();
  const loginAudit = page.locator(".audit-table tbody tr").filter({hasText: "管理员登录"}).first();
  await expect(loginAudit).not.toContainText("admin_login");
  await expect(loginAudit.locator(".audit-source-ip")).not.toHaveText("-");
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
    await expect(page.locator("#proxy-client")).toHaveValue("");
    await page.getByRole("button", {name: "新增代理"}).click();
    const editor = page.getByRole("dialog", {name: "新增代理"});
    await expect(editor).toBeVisible();
    await expect(editor.getByRole("button", {name: "保存配置"})).toBeDisabled();
    await editor.getByLabel("客户端").selectOption("dev-client");
    await editor.getByLabel("代理 ID").fill(proxyIds[0]);
    await editor.getByLabel("备注").fill(`E2E ${proxyIds[0]}`);
    await editor.getByLabel("网络").selectOption("tcp");
    await editor.getByLabel("承载").selectOption("smux");
    await editor.getByLabel("服务端监听").fill("127.0.0.1:7100");
    await editor.getByLabel("内网目标").fill("127.0.0.1:18080");
    await editor.getByLabel("允许来源 CIDR").fill("127.0.0.0/8");
    await editor.getByLabel("拒绝来源 CIDR").fill("127.0.0.9/32");
    const proxyProtocolSetting = editor.locator("label.toggle-setting", {hasText: "启用 PROXY Protocol v1"});
    await proxyProtocolSetting.locator(".switch").click();
    await expect(editor.getByLabel("启用 PROXY Protocol v1")).toBeChecked();
    const rateEditors = editor.locator(".rate-editor");
    await rateEditors.nth(0).getByRole("checkbox", {name: "不限速"}).uncheck();
    await rateEditors.nth(0).getByRole("spinbutton").fill("64");
    await rateEditors.nth(1).getByRole("checkbox", {name: "不限速"}).uncheck();
    await rateEditors.nth(1).getByRole("spinbutton").fill("128");
    await editor.getByRole("button", {name: "保存配置"}).click();
    await expect(editor).toBeHidden();
    const securedRow = page.getByRole("row").filter({hasText: proxyIds[0]});
    await securedRow.click();
    const runtimeDrawer = page.getByRole("dialog").filter({hasText: proxyIds[0]});
    await expect(runtimeDrawer).toContainText("127.0.0.0/8");
    await expect(runtimeDrawer).toContainText("v1 已启用");
    await expect(runtimeDrawer).toContainText("64 KiB/s");
    await runtimeDrawer.getByRole("button", {name: "关闭"}).click();
    await createProxy(page, proxyIds[1], "tcp", "direct", "127.0.0.1:7101", "127.0.0.1:18080");
    await createProxy(page, proxyIds[2], "udp", "smux-datagram", "127.0.0.1:7102", "127.0.0.1:18081");

    await page.locator("#proxy-client").selectOption("dev-client");
    await page.getByRole("button", {name: "新增代理"}).click();
    const protocolSwitch = page.getByLabel("启用 PROXY Protocol v1");
    await page.locator("label.toggle-setting", {hasText: "启用 PROXY Protocol v1"}).locator(".switch").click();
    await expect(protocolSwitch).toBeChecked();
    await page.getByLabel("网络").selectOption("udp");
    await expect(protocolSwitch).toBeDisabled();
    await expect(protocolSwitch).not.toBeChecked();
    await page.getByLabel("代理 ID").fill("e2e-duplicate");
    await page.getByLabel("服务端监听").fill(":7103");
    await page.getByLabel("内网目标").fill("127.0.0.1:18081");
    await page.getByLabel("允许来源 CIDR").fill("192.0.2.1/24");
    await page.getByRole("button", {name: "保存配置"}).click();
    await expect(page.getByText(/包含无效 CIDR/)).toBeVisible();
    await page.getByLabel("允许来源 CIDR").fill("");
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

    await navigate(page, "事件与审计");
    const configAudit = page.getByRole("row").filter({hasText: "配置变更"}).first();
    await expect(configAudit).toBeVisible();
    await expect(configAudit).not.toContainText("config_update");
  } finally {
    await removeE2EProxies(page, csrf);
  }
});
