import { test, expect, type Page } from "@playwright/test";

const pid = "11111111-1111-4111-8111-111111111111";
const password = "browser-fixture-password";

test("paid capacity is shown without a misleading zero sending allowance", async ({ page }) => {
  await fixture(page);
  await page.route(`**/console-api/projects/${pid}/settings`, route => route.fulfill({json: {
    organization: { id: "org", name: "Example", plan_name: "Pro 50k", currency: "USD", use_credits_for_overage: false },
    retention: { metadata_days: 90, content_days: 7, plan_metadata_days: 90, plan_max_content_days: 7, test_inbox_days: 7 },
    project: { name: "Default", sending_status: "approved", status_reason: "", hourly_limit: 0, daily_limit: 0, org_hourly_limit: 0, org_daily_limit: 0 }, role: "owner"
  }}));
  await page.goto(`/app/${pid}/settings`);
  await expect(page.getByText("No hourly or daily plan limit.", {exact:true})).toBeVisible();
  await expect(page.getByText("Available recipients and attachment data still apply.", {exact:false})).toBeVisible();
  await expect(page.getByText(/0 recipients\/hour|0\/day/)).toHaveCount(0);
  await fits(page);
});
async function fixture(page: Page, mfa = true) {
  const calls: { path: string; body: Record<string, unknown> }[] = [];
  await page.route("**/console-api/**", async route => {
    const path = new URL(route.request().url()).pathname.slice("/console-api".length);
    let result: unknown = { data: [] };
    if (route.request().method() === "POST") calls.push({ path, body: route.request().postDataJSON() });
    if (path === "/me") result = { user: { id: "fixture", email: "owner@example.com", name: "Owner", locale: "en", mfa_enabled: mfa, is_staff: false, session_mfa: mfa, legal_accepted: true }, organizations: [{ id: "org", name: "Example", role: "owner", plan: "free", currency: "USD", projects: [{ id: pid, name: "Default", sending_status: "test_only" }] }], environment: { aws_mode: "mock", env: "test", api_url: "https://api.hedvi.dev" } };
    if (path.endsWith("/settings")) result = { organization: { id: "org", name: "Example", plan_name: "Free", currency: "USD", use_credits_for_overage: false }, retention: { metadata_days: 14, content_days: 7, plan_metadata_days: 14, plan_max_content_days: 7, test_inbox_days: 7 }, project: { name: "Default", sending_status: "test_only", status_reason: "", hourly_limit: 25, daily_limit: 100 }, role: "owner" };
    if (path.endsWith("/usage")) result = { period_end: "2026-12-01T00:00:00Z", recipients: { included: 5000, consumed: 0, reserved: 0, remaining: 5000 }, attachment_data_bytes: { included: 104857600, consumed: 0, reserved: 0 }, credits: [] };
    if (path.endsWith("/billing")) result = { enabled: false, baseline: "free", has_credits: false, has_pro: false, currency: "USD", countries: ["SE"], has_customer: false, billing_profile: null, billing_profile_editable: true, orders: [] };
    if (path === "/auth/legal") result = { version: "fixture", adopted: true, documents: ["terms", "aup", "dpa", "privacy", "subprocessors", "cookies"].map(id => ({ id, url: "/legal/" + id, version: "fixture", sha256: "fixture", acknowledgement: "fixture" })) };
    if (path.endsWith("/api-keys")) result = { data: [{ id: "key", name: "Onboarding test key with a long descriptive name", hint: "hv_test_example…n3e2", mode: "test", permission: "full_access", last_used_at: null, revoked_at: null }] };
    if (path.endsWith("/webhooks")) result = { data: [{ id: "hook", url: "https://example.com/very/long/integration/webhooks/transactional-mail", events: ["email.delivered", "email.bounced"], status: "enabled", succeeded_7d: 0, failed_7d: 0 }], event_types: [] };
    if (path === "/me/mfa/setup") result = { secret: "JBSWY3DPEHPK3PXP", otpauth_url: "otpauth://totp/Hedvi:fixture?secret=JBSWY3DPEHPK3PXP&issuer=Hedvi" };
    await route.fulfill({ json: result });
  });
  return calls;
}
async function fits(page: Page) {
  const width = await page.evaluate(() => ({ content: document.documentElement.scrollWidth, viewport: document.documentElement.clientWidth }));
  expect(width.content).toBeLessThanOrEqual(width.viewport + 1);
}

test("developers content stays inside viewport; menu closes after navigation", async ({ page, isMobile }, info) => {
  await fixture(page);
  await page.goto(`/app/${pid}/developers`);
  await expect(page.getByText("Onboarding test key with a long descriptive name")).toBeVisible();
  await fits(page);
  const toolbar = page.locator(".code-toolbar").first();
  const code = page.locator(".code-tabs pre.code:visible").first();
  expect((await toolbar.boundingBox())!.y + (await toolbar.boundingBox())!.height).toBeLessThanOrEqual((await code.boundingBox())!.y + 1);
  await expect(page.getByRole("button", { name: "Copy setup prompt" })).toBeVisible();
  const revoke = page.getByRole("button", { name: "Revoke", exact: true });
  await revoke.scrollIntoViewIfNeeded();
  const revokeBounds = await revoke.boundingBox();
  expect(revokeBounds).not.toBeNull();
  expect(revokeBounds!.x).toBeGreaterThanOrEqual(0);
  expect(revokeBounds!.x + revokeBounds!.width).toBeLessThanOrEqual(page.viewportSize()!.width);
  await expect(page.getByLabel("Domain", { exact: true })).toBeDisabled();
  await page.getByLabel("Mode", { exact: true }).selectOption("live");
  await expect(page.getByLabel("Domain", { exact: true })).toBeEnabled();
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({ path: info.outputPath("developers.png"), fullPage: true });
  if (isMobile) {
    await page.getByRole("button", { name: "Open dashboard menu" }).click();
    await fits(page);
    await page.getByRole("navigation", { name: "Dashboard" }).getByRole("link", { name: "Settings", exact: true }).click();
    await expect(page.getByRole("button", { name: "Open dashboard menu" })).toBeVisible();
    await expect(page.getByRole("navigation", { name: "Dashboard" })).toBeHidden();
    await page.setViewportSize({ width: 320, height: 800 });
    await page.goto(`/app/${pid}/developers`);
    await expect(page.getByRole("heading", { name: "API keys", exact: true })).toBeVisible();
    await fits(page);
  }
});

test("enabled MFA is a completed state, disable is explicit and cancel clears fields", async ({ page }) => {
  await fixture(page);
  await page.goto(`/app/${pid}/settings`);
  await expect(page.getByText("Your account is protected.", { exact: false })).toBeVisible();
  await expect(page.getByLabel("Current password")).toHaveCount(0);
  await expect(page.getByLabel("6-digit code")).toHaveCount(0);
  await page.getByRole("button", { name: "Turn off two-factor authentication…" }).click();
  await page.getByLabel("Current password").fill(password);
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await expect(page.getByLabel("Current password")).toHaveCount(0);
  await fits(page);
});

test("MFA setup asks for password once, then QR/code, then clears secrets", async ({ page }, info) => {
  const calls = await fixture(page, false);
  await page.goto(`/app/${pid}/settings`);
  await page.getByRole("button", { name: "Set up authenticator", exact: true }).click();
  await page.getByLabel("Current password").fill(password);
  await page.getByRole("button", { name: "Continue", exact: true }).click();
  await expect(page.getByLabel("Current password")).toHaveCount(0);
  await expect(page.getByRole("img", { name: "Scan this QR code with your authenticator app" })).toBeVisible();
  await page.getByLabel("6-digit code").fill("123456");
  await page.getByRole("button", { name: "Verify and finish" }).click();
  await expect(page.getByRole("status")).toHaveText("All set. Two-factor authentication is enabled.");
  expect(calls.filter(c => c.path.startsWith("/me/mfa/")).map(c => c.body.password)).toEqual([password, password]);
  await expect(page.getByLabel("6-digit code")).toHaveCount(0);
  await expect(page.locator("canvas.authenticator-qr")).toHaveCount(0);
  await expect(page.getByText("JBSWY3DPEHPK3PXP")).toHaveCount(0);
  await page.screenshot({ path: info.outputPath("mfa-complete.png"), fullPage: true });
});

test("signup refuses mismatched passwords without consuming email proof", async ({ page }) => {
  const calls = await fixture(page);
  const token = "11111111-1111-4111-8111-111111111111." + "a".repeat(52);
  await page.goto(`/verify-email#token=${token}`);
  await page.getByLabel("New password (at least 12 characters)", { exact: true }).fill(password);
  await page.getByLabel("Confirm password", { exact: true }).fill(password + "x");
  await page.getByRole("checkbox").check();
  await page.getByRole("button", { name: "Create account", exact: true }).click();
  await expect(page.getByText("Passwords do not match.")).toBeVisible();
  expect(calls.filter(c => c.path === "/auth/signup/complete")).toHaveLength(0);
  await page.getByLabel("Confirm password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Create account", exact: true }).click();
  await expect.poll(() => calls.filter(c => c.path === "/auth/signup/complete").length).toBe(1);
});

test("public pages have no build warning and fit both narrow and desktop screens", async ({ page }, info) => {
  for (const path of ["/", "/pricing", "/docs/quickstart", "/docs/agent-setup", "/docs/account-and-security", "/docs/quotas-and-plans", "/login", "/signup"]) {
    const response = await page.goto(path);
    expect(response?.status()).toBe(200);
    await expect(page.locator("body")).not.toContainText("Private build");
    await fits(page);
  }
  await page.goto("/docs/agent-setup");
  await page.screenshot({ path: info.outputPath("agent-setup.png"), fullPage: true });
});

test("remember me is optional and carried through authenticator sign-in", async ({ page }) => {
  const calls = await fixture(page);
  await page.route("**/console-api/auth/login", async route => {
    const body = route.request().postDataJSON();
    calls.push({ path: "/auth/login", body });
    await route.fulfill(body.totp_code ? { json: { ok: true } } : { status: 401, json: { mfa_required: true, message: "Code required" } });
  });
  await page.goto("/login");
  const remember = page.getByRole("checkbox", { name: "Remember me" });
  await expect(remember).not.toBeChecked();
  await page.getByLabel("Email", { exact: true }).fill("owner@example.com");
  await page.getByLabel("Password", { exact: true }).fill(password);
  await remember.check();
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page.getByLabel("Authentication code").fill("123456");
  await expect(remember).toBeChecked();
  await fits(page);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect.poll(() => calls.filter(c => c.path === "/auth/login").length).toBe(2);
  expect(calls.filter(c => c.path === "/auth/login").map(c => c.body.remember_me)).toEqual([true, true]);
});

test("owner sections separate accounts, gifts, sending and errors", async ({ page }, info) => {
  await fixture(page);
  const org={id:"org",name:"Example AB",owner_email:"owner@example.com"};
  await page.route("**/console-api/me",route=>route.fulfill({json:{user:{id:"owner",email:org.owner_email,is_staff:true,mfa_enabled:true,session_mfa:true,legal_accepted:true},organizations:[{...org,role:"owner",projects:[{id:pid,name:"Default",sending_status:"test_only"}]}],environment:{aws_mode:"mock",env:"test"}}}));
  await page.route("**/console-api/admin/summary**",route=>route.fulfill({json:{users:[{id:"owner",email:org.owner_email,verified:true,created_at:"2026-11-28T10:00:00Z",organizations:[org]}],grants:[],reviews:[{id:pid,name:"Default",org_id:org.id,organization:org.name,state:"test_only",verified_domains:0,hourly_limit:25,daily_limit:100}],purchases:[],errors:[]}}));
  await page.goto("/app/admin");
  await expect(page.getByRole("heading",{name:"Latest registrations"})).toBeVisible();
  await expect(page.getByRole("button",{name:"Give membership",exact:true})).toHaveCount(0);
  await fits(page);
  await page.screenshot({path:info.outputPath("owner-overview.png"),fullPage:true});
  await page.getByRole("button",{name:"Open Example AB",exact:true}).click();
  await page.getByRole("button",{name:"Give membership",exact:true}).click();
  await expect(page.getByLabel("Reason (shown in the account audit log)")).toHaveValue("Complimentary access from Hedvi owner");
  await fits(page);
  await page.screenshot({path:info.outputPath("owner-account.png"),fullPage:true});
  await page.getByRole("button",{name:"Cancel",exact:true}).click();
  await page.getByRole("navigation",{name:"Owner sections"}).getByRole("button",{name:"Sending",exact:true}).click();
  await expect(page.getByText("Awaiting domain",{exact:true})).toBeVisible();
  await expect(page.getByLabel("Reason (shown in the account audit log)")).toHaveCount(0);
  await page.getByRole("button",{name:"Manage",exact:true}).click();
  await expect(page.getByRole("button",{name:"Approve live sending"})).toHaveCount(0);
  await page.getByRole("navigation",{name:"Owner sections"}).getByRole("button",{name:"Errors",exact:true}).click();
  await expect(page.getByRole("heading",{name:"No matching events"})).toBeVisible();
  await expect(page.getByRole("button",{name:"Pause sending"})).toHaveCount(0);
  await fits(page);
});

test("domain onboarding has no live sending application",async({page})=>{
  await fixture(page);
  await page.goto(`/app/${pid}/settings`);
  await expect(page.getByRole("link",{name:"Set up sending domain"})).toBeVisible();
  await expect(page.getByLabel("What will you send?")).toHaveCount(0);
  await expect(page.getByRole("button",{name:"Request live sending"})).toHaveCount(0);
  await fits(page);
});

test("Cloudflare quick setup downloads scoped DNS records and fits mobile", async ({ page }, info) => {
  await fixture(page);
  const domain = "mail.example.com";
  const fixtureDomain = {
    id:"domain", name:domain, status:"pending", records:[
      {record:"Ownership",type:"TXT",name:"_hedvi-verification",fqdn:`_hedvi-verification.${domain}`,value:"hedvi-domain=00000000-0000-4000-8000-000000000001",status:"verified",purpose:"Ownership"},
      {record:"DKIM",type:"CNAME",name:"selector._domainkey",fqdn:`selector._domainkey.${domain}`,value:"selector.dkim.amazonses.com",status:"pending",purpose:"Email signing"},
    ], hedvi:{analysis:{ns_provider:"Cloudflare",mail_from_safety:"clear",findings:[]},last_checked_at:null,next_check_at:null,mail_from_domain:`send.${domain}`}
  };
  await page.route(`**/console-api/projects/${pid}/domains/domain`,route=>route.fulfill({json:fixtureDomain}));
  await page.route(`**/console-api/projects/${pid}/domains/domain/dns-export`,route=>route.fulfill({json:fixtureDomain}));
  await page.goto(`/app/${pid}/domains/domain`);
  await page.getByRole("button",{name:"Quick setup with Cloudflare"}).click();
  await expect(page.getByRole("link",{name:"Open Cloudflare DNS"})).toHaveAttribute("href","https://dash.cloudflare.com/?to=/:account/:zone/dns/records");
  const downloading=page.waitForEvent("download");
  await page.getByRole("button",{name:"Download DNS records"}).click();
  const download=await downloading;
  expect(download.suggestedFilename()).toBe("hedvi-mail.example.com-dns.txt");
  await download.saveAs(info.outputPath(download.suggestedFilename()));
  await fits(page);
  await page.screenshot({path:info.outputPath("cloudflare-quick-setup.png"),fullPage:true});
});


test("legacy Swedish links and browser language still show the English product", async ({ page }) => {
  const calls = await fixture(page);
  await page.goto("/sv");
  await expect(page).toHaveURL(/\/$/);
  await expect(page.locator("html")).toHaveAttribute("lang", "en");
  await expect(page.getByRole("link", { name: "Svenska", exact: true })).toHaveCount(0);
  await page.goto("/sv/priser");
  await expect(page).toHaveURL(/\/pricing$/);
  await expect(page.getByRole("heading", { name: "Three simple choices" })).toBeVisible();
  await page.goto("/signup?locale=sv");
  await expect(page.getByRole("heading", { name: "Create your account" })).toBeVisible();
  await page.getByLabel("Email", {exact:true}).fill("fixture@example.com");
  await page.getByRole("button", { name: "Send link", exact:true }).click();
  expect(calls.find(c => c.path === "/auth/signup")?.body.locale).toBe("en");
});

test("international billing starts with a deliberate country and supports addresses without postcodes", async ({ page }) => {
  await fixture(page);
  await page.route(`**/console-api/projects/${pid}/billing`, route => route.fulfill({json: {
    enabled:true, baseline:"free", has_credits:false, has_pro:false, currency:"", countries:["US","SE","HK","DE"], has_customer:false, billing_profile:null, billing_profile_editable:true, orders:[]
  }}));
  await page.goto(`/app/${pid}/settings`);
  await expect(page.getByLabel("Billing currency", {exact:true})).toHaveValue("usd");
  await expect(page.getByLabel("Company billing country", {exact:true})).toHaveValue("");
  await page.getByLabel("Company billing country", {exact:true}).selectOption("US");
  await expect(page.locator("#billing-state")).toHaveAttribute("required", "");
  await expect(page.locator("#billing-postal")).toHaveAttribute("required", "");
  await page.getByLabel("Company billing country", {exact:true}).selectOption("HK");
  await expect(page.locator("#billing-postal")).not.toHaveAttribute("required", "");
  await fits(page);
});
