import { defineConfig, devices } from "@playwright/test";
export default defineConfig({
  testDir: "./tests/browser", workers: 1, retries: 0,
  use: { baseURL: "http://127.0.0.1:3030", trace: "retain-on-failure" },
  projects: [{ name: "desktop-chromium", use: { ...devices["Desktop Chrome"], viewport: { width: 1440, height: 1000 } } }, { name: "mobile-webkit", use: { ...devices["iPhone 13"] } }],
  webServer: { command: "pnpm start", url: "http://127.0.0.1:3030", reuseExistingServer: !process.env.CI, timeout: 60000 },
});
