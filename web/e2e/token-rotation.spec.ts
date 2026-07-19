import {expect, test, type Page} from "@playwright/test";

const clientId = "e2e-token-client";
const initialToken = "e2e-initial-token-0123456789abcdef0123456789abcdef";

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
      enabled: boolean;
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

test("client token rotation is one-time, redacted, and audited", async ({page}, testInfo) => {
  test.skip(testInfo.project.name !== "desktop", "configuration mutation runs once");
  await login(page);
  const {csrf_token: csrf} = await session(page);
  const setup = await currentConfig(page);
  setup.client_config = setup.client_config.filter((client) => client.client_id !== clientId);
  setup.client_config.push({
    client_id: clientId,
    client_token: initialToken,
    enabled: true,
    proxy_list: [],
  });
  await saveConfig(page, csrf, setup);

  try {
    await page.reload();
    await page.getByRole("button", {name: "客户端"}).click();
    const rotatedToken = new Promise<string>((resolve) => {
      page.on("dialog", async (dialog) => {
        if (dialog.type() === "confirm") {
          await dialog.accept();
          return;
        }
        resolve(dialog.defaultValue());
        await dialog.dismiss();
      });
    });
    const row = page.locator(".client-row").filter({hasText: clientId});
    await row.getByRole("button", {name: `轮换 ${clientId} Token`}).click();
    await expect(rotatedToken).resolves.toMatch(/^[A-Za-z0-9_-]{43}$/);

    const redacted = await currentConfig(page);
    expect(redacted.client_config.find((client) => client.client_id === clientId)?.client_token)
      .toBe("__REDACTED__");
    await expect.poll(async () => {
      const response = await page.request.get("/backend/api/events?limit=100");
      if (!response.ok()) return false;
      const payload = await response.json() as {events: Array<{kind: string; client_id: string}>};
      return payload.events.some((event) => event.kind === "client_token_rotated" && event.client_id === clientId);
    }, {timeout: 5000}).toBe(true);
  } finally {
    const cleanup = await currentConfig(page);
    cleanup.client_config = cleanup.client_config.filter((client) => client.client_id !== clientId);
    await saveConfig(page, csrf, cleanup);
  }
});
