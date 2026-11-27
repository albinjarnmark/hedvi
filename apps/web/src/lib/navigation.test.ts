import { describe, expect, it } from "vitest";
import { appLink, appOrigin, canonicalPage } from "./navigation";

describe("separate public and account origins", () => {
  const app = "app.example.test";
  const site = "https://example.test";
  it("publishes direct signup links including the requested locale", () => {
    expect(appLink("/signup?locale=sv", app)).toBe("https://app.example.test/signup?locale=sv");
    expect(appLink("/login", "")).toBe("/login");
    expect(() => appLink("//other.test", app)).toThrow();
    expect(() => appOrigin("attacker.test@real.test")).toThrow();
  });
  it("routes private pages on the public host to the trusted app without using the inbound host as a destination", () => {
    for (const host of ["example.test", "www.example.test", "other.test"]) {
      for (const path of ["/signup", "/login", "/forgot-password", "/verify-email", "/reset-password", "/legal/accept", "/app/p/settings"]) {
        expect(canonicalPage(host, path, "?locale=sv", app, site)?.href).toBe(`https://${app}${path}?locale=sv`);
      }
    }
    expect(canonicalPage(app, "/verify-email", "", app, site)).toBeNull();
    expect(canonicalPage(app, "/reset-password", "", app, site)).toBeNull();
  });
  it("uses the legal site for document versions, while the acceptance form stays on the app", () => {
    const version = "/legal/versions/2026-11-28.2/terms";
    expect(canonicalPage(app, version, "", app, site)?.href).toBe(site + version);
    expect(canonicalPage(app, "/legal/accept", "", app, site)).toBeNull();
    expect(canonicalPage("example.test", version, "", app, site)).toBeNull();
    expect(canonicalPage("localhost", "/signup", "")).toBeNull();
  });
});
