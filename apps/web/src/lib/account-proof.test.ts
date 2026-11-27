import { describe, it, expect } from "vitest";
import { consumeProofFragment, legalConsent } from "./account-proof";

const challengeID = [8, 4, 4, 4, 12].map(width => "0".repeat(width)).join("-");
const token = `${challengeID}.${"a".repeat(52)}`;
describe("account proof browser boundary", () => {
  it("removes a proof from history and accepts only an exact fragment", () => {
    let clean = "";
    expect(consumeProofFragment(`https://app.example.com/verify-email#token=${token}`, (v) => { clean = v; })).toBe(token);
    expect(clean).toBe("/verify-email");
  });
  it("never accepts query tokens, duplicate tokens, extra fields or malformed credentials", () => {
    for (const suffix of [`?token=${token}`, `#token=${token}&token=${token}`, `#token=${token}&redirect=https://evil.test`, "#token=short"]) {
      expect(consumeProofFragment(`https://app.example.com/reset-password${suffix}`, () => {})).toBe("");
    }
  });
  it("submits the exact displayed version and distinguishes notice receipt", () => {
    expect(legalConsent("2026-11-28")).toEqual({ version: "2026-11-28", terms: true, aup: true, dpa:true, privacy_notice: true, subprocessors_notice:true, cookies_notice:true });
  });
});
