import { describe, expect, it } from "vitest";
import { dict } from "./i18n";

function shape(v: unknown): unknown {
  if (Array.isArray(v)) return v.map(shape);
  if (v && typeof v === "object") return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, shape(x)]));
  return typeof v;
}

describe("dictionaries", () => {
  it("have identical structure in English and Swedish", () => {
    expect(shape(dict.sv)).toEqual(shape(dict.en));
  });
  it("avoid unverified claims", () => {
    const text = JSON.stringify(dict).toLowerCase();
    for (const banned of ["best deliverability", "100% gdpr", "uptime", "99.9", "guarantee"]) {
      expect(text).not.toContain(banned);
    }
  });
});
