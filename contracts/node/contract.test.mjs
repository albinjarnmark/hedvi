// Contract tests for resend-node 6.39.0 against Hedvi.
// Run with a local API: `make dev` in one terminal, then `make contracts`.
import { execFileSync } from "node:child_process";
import assert from "node:assert/strict";
import { before, test } from "node:test";
import { fileURLToPath } from "node:url";

const API = process.env.HEDVI_API_URL ?? "http://127.0.0.1:8780";
process.env.RESEND_BASE_URL = API; // documented migration path for Node

const { Resend } = await import("resend");
let fx;
let test_, live;

before(() => {
  const script = fileURLToPath(new URL("../fixture.sh", import.meta.url));
  fx = JSON.parse(execFileSync(script, { encoding: "utf8" }));
  test_ = new Resend(fx.test_key);
  live = new Resend(fx.live_key);
});

test("send and get in test mode", async () => {
  const { data, error } = await test_.emails.send({
    from: "Acme <onboarding@hedvi.dev>", to: ["ada@example.com"], subject: "Contract", html: "<p>Hi</p>", text: "Hi",
    tags: [{ name: "suite", value: "node" }], headers: { "X-Entity-Ref-ID": "42" },
    attachments: [{ filename: "a.txt", content: Buffer.from("hello") }],
  });
  assert.equal(error, null);
  assert.match(data.id, /^[0-9a-f-]{36}$/);
  const got = await test_.emails.get(data.id);
  assert.equal(got.error, null);
  assert.equal(got.data.object, "email");
  assert.equal(got.data.html, "<p>Hi</p>");
  assert.deepEqual(got.data.to, ["ada@example.com"]);
  assert.equal(got.data.last_event, "test_captured");
});

test("idempotency key replays the same id", async () => {
  const payload = { from: "onboarding@hedvi.dev", to: "x@example.com", subject: "Idem", text: "t" };
  const a = await test_.emails.send(payload, { idempotencyKey: "node-contract-1" });
  const b = await test_.emails.send(payload, { idempotencyKey: "node-contract-1" });
  assert.equal(a.error, null);
  assert.equal(b.data.id, a.data.id);
  const c = await test_.emails.send({ ...payload, subject: "Other" }, { idempotencyKey: "node-contract-1" });
  assert.equal(c.error.name, "invalid_idempotent_request");
  assert.equal(c.error.statusCode, 409);
});

test("unsupported fields fail loudly with Resend's error shape", async () => {
  const { error } = await test_.emails.send({ from: "a@hedvi.dev", to: "b@example.com", subject: "s", text: "t", scheduledAt: "in 1 hour" });
  assert.equal(error.name, "validation_error");
  assert.equal(error.statusCode, 422);
  assert.match(error.message, /scheduled_at/);
});

test("invalid key is rejected", async () => {
  const { error } = await new Resend("re_not_a_hedvi_key").emails.get("00000000-0000-0000-0000-000000000000");
  assert.equal(error.name, "invalid_api_key");
});

test("live send from a verified domain and list", async () => {
  const { data, error } = await live.emails.send({ from: `App <app@${fx.domain}>`, to: "ok@example.com", subject: "Live", text: "t" });
  assert.equal(error, null);
  assert.ok(data.id);
  const list = await live.emails.list({ limit: 5 });
  assert.equal(list.error, null);
  assert.equal(list.data.object, "list");
  assert.ok(list.data.data.some((e) => e.id === data.id));
  const domains = await live.domains.list();
  assert.equal(domains.error, null);
  assert.equal(domains.data.data[0].name, fx.domain);
  const domain = await live.domains.get(domains.data.data[0].id);
  assert.equal(domain.data.object, "domain");
  assert.equal(domain.data.region, "eu-north-1");
});

test("live send from an unverified domain is refused", async () => {
  const { error } = await live.emails.send({ from: "app@not-added.example", to: "ok@example.com", subject: "x", text: "t" });
  assert.equal(error.statusCode, 403);
});

test("resend.webhooks.verify accepts Hedvi signatures", () => {
  const w = fx.webhook;
  const event = live.webhooks.verify({ payload: w.body, headers: { id: w.id, timestamp: w.timestamp, signature: w.signature }, webhookSecret: w.secret });
  assert.equal(event.type, "email.delivered");
  assert.throws(() => live.webhooks.verify({ payload: w.body + " ", headers: { id: w.id, timestamp: w.timestamp, signature: w.signature }, webhookSecret: w.secret }));
});

test("unsupported endpoints are explicit", async () => {
  const { error } = await live.batch.send([{ from: `a@${fx.domain}`, to: "b@example.com", subject: "s", text: "t" }]);
  assert.equal(error.statusCode, 404);
  assert.match(error.message, /not supported/);
});
