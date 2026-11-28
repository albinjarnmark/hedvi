import { describe, expect, it } from "vitest";
import { dict } from "./i18n";

describe("dictionaries", () => {
  it("avoid unverified claims", () => {
    const text = JSON.stringify(dict).toLowerCase();
    for (const banned of ["best deliverability", "100% gdpr", "uptime", "99.9", "guarantee"]) {
      expect(text).not.toContain(banned);
    }
  });
});
