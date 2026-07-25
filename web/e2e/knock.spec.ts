import {expect, test} from "@playwright/test";

test("email Magic Link grants and admin revokes the current source", async ({page}, testInfo) => {
  test.skip(testInfo.project.name !== "desktop", "mailbox and grant mutation runs once");
  await page.request.delete("http://127.0.0.1:18083/messages");
  await page.goto("/");
  await page.getByRole("link", {name: "邮件授权"}).click();
  await expect(page).toHaveURL(/\/knock$/);
  await expect(page.getByRole("heading", {name: "临时访问授权"})).toBeVisible();
  await expect(page.getByText("127.0.0.1", {exact: true})).toBeVisible();
  await page.getByLabel("授权邮箱").fill("e2e@example.com");
  await page.getByRole("button", {name: "发送访问邮件"}).click();
  await expect(page.getByText("如果该邮箱允许访问，授权邮件将很快送达。")).toBeVisible();

  let token = "";
  await expect.poll(async () => {
    const response = await page.request.get("http://127.0.0.1:18083/messages?to=e2e@example.com");
    const mailbox = await response.json() as {items: Array<{data: string}>};
    const data = (mailbox.items.at(-1)?.data ?? "")
      .replace(/=\r?\n/g, "")
      .replace(/=3D/g, "=");
    token = data.match(/#token=([A-Za-z0-9_-]{43})/)?.[1] ?? "";
    return token.length;
  }, {timeout: 5000}).toBe(43);

  await page.goto(`/knock/verify#token=${token}`);
  await expect(page.getByRole("heading", {name: "临时访问授权已生效"})).toBeVisible();
  expect(page.url()).not.toContain("#token=");

  await page.goto("/");
  await page.getByLabel("用户名").fill("admin");
  await page.getByLabel("密码").fill("admin");
  await page.getByRole("button", {name: "登录"}).click();
  await page.getByRole("navigation").getByRole("button", {name: "代理规则"}).click();
  await expect(page.getByRole("heading", {name: "当前临时授权"})).toBeVisible();
  const grantRow = page.getByRole("row").filter({hasText: "e2e@example.com"});
  await expect(grantRow).toContainText("127.0.0.1");

  const protectedRow = page.getByRole("row").filter({hasText: "dev-knock-tcp"});
  await expect(protectedRow).toContainText("邮件授权");
  await protectedRow.getByRole("button", {name: "编辑 dev-knock-tcp"}).click();
  await expect(page.getByLabel("仅允许已邮件授权 IP")).toBeChecked();
  await page.getByRole("dialog", {name: "编辑代理"}).getByRole("button", {name: "关闭"}).click();

  await grantRow.getByRole("button", {name: "撤销 127.0.0.1 的临时授权"}).click();
  await page.getByRole("button", {name: "确认撤销"}).click();
  await expect(grantRow).toBeHidden();
});
