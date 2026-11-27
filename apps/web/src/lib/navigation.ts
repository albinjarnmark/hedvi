// The app host is deployment configuration, never a user-provided return URL.
export function appOrigin(host?: string): string | undefined {
  if (!host) return undefined;
  const url = new URL(`https://${host}`);
  if (url.host !== host.toLowerCase() || url.username || url.password || url.pathname !== "/" || url.search || url.hash) {
    throw new Error("HEDVI_APP_HOST must be a hostname.");
  }
  return url.origin;
}

export function isAppPage(path: string): boolean {
  return path === "/app" || path.startsWith("/app/") ||
    ["/login", "/signup", "/forgot-password", "/verify-email", "/reset-password", "/legal/accept"].includes(path);
}

export function appLink(path: string, host = process.env.HEDVI_APP_HOST): string {
  if (!isAppPage(path.split(/[?#]/)[0] ?? "")) throw new Error("Unexpected application link.");
  return (appOrigin(host) ?? "") + path;
}

export function canonicalPage(host: string, path: string, search: string, appHost?: string, siteURL?: string): URL | null {
  const app = appOrigin(appHost);
  if (!app) return null;
  const onApp = host.toLowerCase() === new URL(app).hostname;
  const publicPage = ["/docs", "/legal", "/pricing", "/sv"].some(prefix => path === prefix || path.startsWith(prefix + "/"));
  const origin = !onApp && isAppPage(path) ? app : onApp && publicPage && !isAppPage(path) ? siteURL : undefined;
  if (!origin) return null;
  const url = new URL(origin);
  if (url.protocol !== "https:" || url.username || url.password || url.host === host.toLowerCase()) return null;
  url.pathname = path;
  url.search = search;
  return url;
}
