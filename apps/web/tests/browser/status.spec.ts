import { test, expect } from "@playwright/test";
import { readFile } from "node:fs/promises";
import path from "node:path";

test("independent status fits the viewport and rejects stale passing results", async ({ page }, info) => {
  let stale = false;
  await page.route("https://status.hedvi.test/**", async route => {
    const name = new URL(route.request().url()).pathname.replace(/^\/hedvi\//, "") || "index.html";
    if (name === "status.json") {
      await route.fulfill({json: {checked_at: new Date(Date.now() - (stale ? 21 * 60 * 1000 : 0)).toISOString(), components: ["website", "dashboard", "api"].map(id => ({id, status: "passing"})), incident: {state: "none"}}});
      return;
    }
    const files: Record<string, [string, string]> = {
      "index.html": ["../status/index.html", "text/html"],
      "style.css": ["../status/style.css", "text/css"],
      "status.mjs": ["../status/status.mjs", "text/javascript"],
      "logo.svg": ["../../brand/logo/hedvi-lockup-white.svg", "image/svg+xml"],
    };
    if (!files[name]) { await route.fulfill({status:404,body:"Not found"}); return; }
    await route.fulfill({body:await readFile(path.resolve(files[name][0])),contentType:files[name][1]});
  });
  await page.goto("https://status.hedvi.test/hedvi/");
  await expect(page.getByRole("heading", {name:"Latest checks are passing"})).toBeVisible();
  await expect(page.getByText("Passing",{exact:true})).toHaveCount(3);
  const width = await page.evaluate(() => ({content:document.documentElement.scrollWidth,viewport:document.documentElement.clientWidth}));
  expect(width.content).toBeLessThanOrEqual(width.viewport);
  await page.screenshot({path:info.outputPath("status.png"),fullPage:true});
  stale = true;
  await page.reload();
  await expect(page.getByRole("heading", {name:"Current status is unavailable"})).toBeVisible();
  await expect(page.getByText("Unknown",{exact:true})).toHaveCount(3);
});
