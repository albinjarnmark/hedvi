import { describe, expect, it } from "vitest";
import { sandboxDoc, sanitizedEmail } from "./email-preview";

describe("untrusted email preview", () => {
  it("removes network hints and active nested markup before iframe parsing", () => {
    const html = `<LINK rel=dns-prefetch href=//unique.example><link rel=preconnect href=https://track.example><meta http-equiv=refresh content='0;url=https://track.example'><base href=https://track.example><template><link rel=prefetch href=/x></template><svg><foreignObject><iframe src=//track.example></iframe></foreignObject></svg><p onmouseover=alert(1)>Receipt</p>`;
    expect(sanitizedEmail(html)).toBe("<p>Receipt</p>");
  });
  it("bounds deeply nested attacker content without exhausting the stack", () => {
    expect(sanitizedEmail("<div>".repeat(12000) + "deep" + "</div>".repeat(12000)).length).toBeLessThan(2000);
  });
  it("handles malformed table markup and keeps the preview policy ahead of content", () => {
    const html = `<table><tr><td>Hello<link rel=dns-prefetch href=//track.example></table></body></html><script>alert(1)</script><img src=https://images.example/a.png onerror=alert(1)>`;
    const doc = sandboxDoc(html, false);
    expect(doc).not.toMatch(/<link|<script|onerror|track\.example/i);
    expect(doc).toContain("img-src data: ;");
    expect(doc.indexOf("Content-Security-Policy")).toBeLessThan(doc.indexOf("<table>"));
    expect(doc).toContain("<img src=\"https://images.example/a.png\">");
    expect(sandboxDoc(html, true)).toContain("img-src data: https:");
  });
});
