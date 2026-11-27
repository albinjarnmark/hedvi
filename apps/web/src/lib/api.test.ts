import { describe, expect, it } from "vitest";
import { bytes, statusLabel, tone } from "./api";

describe("formatting", () => {
  it("formats sizes", () => {
    expect(bytes(512)).toBe("512 B");
    expect(bytes(1536)).toBe("1.5 KB");
    expect(bytes(5 * 1024 * 1024)).toBe("5.0 MB");
  });
  it("labels every toned status", () => {
    for (const s of ["delivered", "bounced", "delivery_unknown", "test_captured"]) {
      expect(statusLabel[s]).toBeTruthy();
      expect(tone(s)).not.toBe(undefined);
    }
    expect(statusLabel.delivered).not.toMatch(/inbox/i);
  });
});
