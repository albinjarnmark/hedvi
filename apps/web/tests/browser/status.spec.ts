import { test, expect } from "@playwright/test";
import { readFile } from "node:fs/promises";
import path from "node:path";

test("independent status fits the viewport and rejects stale passing results", async ({ page }, info) => {
  let stale = false, activeIncident = false, savedUnavailable = false, savedFailure = false, olderLive = false, liveFailure = false, liveUnavailable = false, savedOlder = false;
  await page.clock.install();
  await page.route("https://hedvi-status-probe.albinff.workers.dev/status.json", async route => {
    if (liveUnavailable) { await route.abort(); return; }
    await route.fulfill({headers:{"Access-Control-Allow-Origin":"*"},json:{checked_at:new Date(Date.now()-(stale?21*60*1000:olderLive?50000:0)).toISOString(),components:["website","dashboard","api"].map(id=>({id,status:liveFailure && id==="api"?"failing":"passing"}))}});
  });
  await page.route("https://status.hedvi.test/**", async route => {
    const name = new URL(route.request().url()).pathname.replace(/^\/hedvi\//, "") || "index.html";
    if (name === "status.json") {
      if (savedUnavailable) { await route.abort(); return; }
      await route.fulfill({json: {checked_at: new Date(Date.now() - (stale ? 21 * 60 * 1000 : savedOlder ? 300000 : 0)).toISOString(), components: ["website", "dashboard", "api"].map(id => ({id, status: savedFailure && id === "api" ? "failing" : "passing"})), incident: activeIncident ? {state:"active",title:"Sending delay",message:"We are investigating",updated_at:new Date().toISOString()} : {state: "none"}}});
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
  stale = false; savedFailure = true; olderLive = true;
  await page.reload();
  await expect(page.getByRole("heading",{name:"A service issue needs attention"})).toBeVisible();
  savedFailure = false; olderLive = false; activeIncident = true;
  await page.reload();
  await expect(page.getByRole("heading",{name:"Sending delay"})).toBeVisible();
  savedUnavailable = true;
  await page.clock.runFor(61000);
  await expect(page.getByRole("heading",{name:"Sending delay"})).toBeVisible();
  await expect(page.getByRole("heading",{name:"A service issue needs attention"})).toBeVisible();
  savedUnavailable = false; activeIncident = false; savedOlder = true; liveFailure = true;
  await page.reload();
  await expect(page.getByRole("heading",{name:"A service issue needs attention"})).toBeVisible();
  liveUnavailable = true;
  await page.clock.runFor(61000);
  await expect(page.getByRole("heading",{name:"A service issue needs attention"})).toBeVisible();
});
