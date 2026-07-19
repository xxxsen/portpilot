import {expect, test, type Page} from "@playwright/test";

const clientId = "e2e-token-client";

async function login(page: Page) {
  await page.goto("/");
  await page.getByLabel("用户名").fill("admin");
  await page.getByLabel("密码").fill("admin");
  await page.getByRole("button", {name: "登录"}).click();
  await expect(page.getByRole("heading", {name: "总览"})).toBeVisible();
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
    remark: string;
    enabled: boolean;
    created_at: number;
    updated_at: number;
    proxy_list: [];
    }>;
  }>;
}

async function saveConfig(
  page: Page,
  csrf: string,
  config: Awaited<ReturnType<typeof currentConfig>>,
) {
  const response = await page.request.put("/backend/api/config", {
    headers: {"X-CSRF-Token": csrf},
    data: {expected_revision: config.revision, config},
  });
  expect(response.ok()).toBeTruthy();
}

test("client creation and token rotation are one-time, redacted, and audited", async ({page}, testInfo) => {
  test.skip(testInfo.project.name !== "desktop", "configuration mutation runs once");
  await login(page);
  const {csrf_token: csrf} = await session(page);
  const setup = await currentConfig(page);
  const withoutFixture = setup.client_config.filter((client) => client.client_id !== clientId);
  if (withoutFixture.length !== setup.client_config.length) {
    setup.client_config = withoutFixture;
    await saveConfig(page, csrf, setup);
  }

  try {
    await page.reload();
    await page.getByRole("button", {name: "客户端"}).click();
    await page.getByRole("button", {name: "新增客户端"}).click();
    const createDialog = page.getByRole("dialog", {name: "新增客户端"});
    await createDialog.getByLabel("客户端 ID").fill(clientId);
    await createDialog.getByLabel("备注").fill("E2E token client");
    await createDialog.getByRole("button", {name: "保存"}).click();
    const createdSecretDialog = page.getByRole("dialog", {name: "一次性客户端密钥"});
    await expect(createdSecretDialog).toBeVisible();
    await createdSecretDialog.getByRole("button", {name: "显示"}).click();
    await expect(createdSecretDialog.locator("code")).toHaveText(/^[A-Za-z0-9_-]{43}$/);
    await createdSecretDialog.getByRole("button", {name: "我已保存"}).click();

    const row = page.locator(".client-row").filter({hasText: clientId});
    await expect(row).toBeVisible();
    await expect(row).toContainText("创建");
    await expect(row).toContainText("修改");
    await row.getByRole("button", {name: `轮换 ${clientId} 密钥`}).click();
    await page.getByRole("button", {name: "确认轮换"}).click();
    const secretDialog = page.getByRole("dialog", {name: "一次性客户端密钥"});
    await expect(secretDialog).toBeVisible();
    await secretDialog.getByRole("button", {name: "显示"}).click();
    await expect(secretDialog.locator("code")).toHaveText(/^[A-Za-z0-9_-]{43}$/);
    await secretDialog.getByRole("button", {name: "我已保存"}).click();

    const redacted = await currentConfig(page);
    expect(redacted.client_config.find((client) => client.client_id === clientId)?.client_token)
      .toBe("__REDACTED__");
    await expect.poll(async () => {
      const response = await page.request.get("/backend/api/events?page=1&page_size=100");
      if (!response.ok()) return false;
      const payload = await response.json() as {items: Array<{event_type: string; client_id: string}>};
      return payload.items.some((event) => event.event_type === "client_token_rotated" && event.client_id === clientId);
    }, {timeout: 5000}).toBe(true);
  } finally {
    const cleanup = await currentConfig(page);
    cleanup.client_config = cleanup.client_config.filter((client) => client.client_id !== clientId);
    await saveConfig(page, csrf, cleanup);
  }
});
