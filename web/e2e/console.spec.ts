import { expect, test } from "@playwright/test";

test("admin can log in and inspect the live client", async ({page}) => {
  await page.goto("/");
  await page.getByLabel("用户名").fill("admin");
  await page.getByLabel("密码").fill("admin");
  await page.getByRole("button", {name: "登录"}).click();
  await expect(page.getByRole("heading", {name: "总览"})).toBeVisible();
  await expect(page.getByText("dev-client", {exact: true}).first()).toBeVisible();
  await expect(page.getByText("ACTIVE", {exact: true}).first()).toBeVisible();
});
