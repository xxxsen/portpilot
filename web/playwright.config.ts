import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  retries: process.env.CI ? 2 : 0,
  workers: 1,
  reporter: [["list"], ["html", {open: "never"}]],
  use: {
    baseURL: process.env.PORTPILOT_WEB_URL ?? "http://127.0.0.1:3000",
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  projects: [
    {name: "desktop", use: {viewport: {width: 1440, height: 900}}},
    {name: "compact", use: {viewport: {width: 1024, height: 768}}},
    {name: "mobile", use: {viewport: {width: 390, height: 844}}},
  ],
});

