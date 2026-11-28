import { expect, test } from "@playwright/test";

test("www redirects pages, files and query parameters to the canonical host", async ({ request }) => {
  for (const path of ["/", "/docs/quickstart?tag=a&tag=b", "/docs/quickstart/", "/robots.txt", "/hedvi-share.png"]) {
    const response = await request.get(path, { headers: { host: "www.hedvi.dev" }, maxRedirects: 0 });
    expect(response.status()).toBe(301);
    expect(response.headers().location).toBe("https://hedvi.dev" + path);
  }
});

test("the sitemap lists canonical pages with distinct descriptions", async ({ request, page }) => {
  const robots = await request.get("/robots.txt");
  expect(await robots.text()).toContain("Sitemap: https://hedvi.dev/sitemap.xml");
  const sitemap = await request.get("/sitemap.xml");
  expect(sitemap.status()).toBe(200);
  const locations = [...(await sitemap.text()).matchAll(/<loc>([^<]+)<\/loc>/g)].map(match => match[1]);
  expect(new Set(locations).size).toBe(locations.length);
  expect(locations).toContain("https://hedvi.dev/docs/switch-from-resend");
  const descriptions = new Set<string>();
  for (const location of locations) {
    const url = new URL(location);
    expect(url.origin).toBe("https://hedvi.dev");
    expect(url.pathname).toMatch(/^\/(?:$|pricing$|docs(?:\/|$)|legal\/)/);
    const response = await request.get(url.pathname, { maxRedirects: 0 });
    expect(response.status()).toBe(200);
    const meta = await page.evaluate(html => {
      const head = new DOMParser().parseFromString(html, "text/html").head;
      return {
        links: Array.from(head.querySelectorAll('link[rel="canonical"]'), link => link.getAttribute("href")),
        description: head.querySelector('meta[name="description"]')?.getAttribute("content") ?? "",
      };
    }, await response.text());
    expect(meta.links).toEqual([location]);
    expect(meta.description.length).toBeGreaterThanOrEqual(90);
    expect(meta.description.length).toBeLessThanOrEqual(160);
    expect(descriptions.has(meta.description)).toBe(false);
    descriptions.add(meta.description);
  }
});
