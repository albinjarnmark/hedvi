import { createHmac } from "node:crypto";
import { describe, expect, it } from "vitest";
import { dashboardPath, pagePolicy, proxyHeaders } from "./proxy-security";

describe("ingress boundary", () => {
  const key = Buffer.alloc(32, 7).toString("base64");
  it("preserves login/signup/docs and only aliases the configured app root", () => {
    for (const path of ["/login", "/signup", "/docs", "/legal/acceptable-use", "/console-api/me", "/app/project"]) expect(dashboardPath("app.example.com", path, "app.example.com")).toBe(path);
    expect(dashboardPath("app.example.com", "/", "app.example.com")).toBe("/app");
    expect(dashboardPath("app.attacker.example", "/", "app.example.com")).toBe("/");
  });
  it("replaces spoofed attestations and signs the rewritten path", () => {
    const input = new Headers({ "x-real-ip": "192.0.2.1", "x-hedvi-client-ip": "198.51.100.1", "x-hedvi-proxy-time": "1", "x-hedvi-proxy-signature": "forged" });
    const h = proxyHeaders(input, "POST", "/console/v1/auth/login", "railway", key, 123000);
    expect(h.get("x-hedvi-client-ip")).toBe("192.0.2.1");
    expect(h.get("x-hedvi-proxy-signature")).toBe(createHmac("sha256", Buffer.from(key, "base64")).update("123\nPOST\n/console/v1/auth/login\n192.0.2.1").digest("hex"));
    expect(proxyHeaders(input, "GET", "/", undefined, key).has("x-hedvi-client-ip")).toBe(false);
    expect(proxyHeaders(input, "GET", "/", "railway", undefined).has("x-hedvi-proxy-signature")).toBe(false);
  });
  it("uses the ALB-appended hop and rejects malformed client headers", () => {
    expect(proxyHeaders(new Headers({ "x-forwarded-for": "198.51.100.1, 192.0.2.2" }), "GET", "/", "alb", key).get("x-hedvi-client-ip")).toBe("192.0.2.2");
    expect(proxyHeaders(new Headers({ "x-real-ip": "192.0.2.1,198.51.100.1" }), "GET", "/", "railway", key).has("x-hedvi-proxy-signature")).toBe(false);
    expect(() => proxyHeaders(new Headers(), "GET", "/", "railway", "invalid")).toThrow();
  });
  it("requires nonces for dashboard inline scripts", () => {
    expect(pagePolicy("uniqueNonce").split(";").map((p) => p.trim()).find((p) => p.startsWith("script-src"))).toBe("script-src 'self' 'nonce-uniqueNonce' 'strict-dynamic'");
  });
});
