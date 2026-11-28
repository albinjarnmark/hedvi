import { randomBytes } from "node:crypto";
import { NextResponse, type NextRequest } from "next/server";
import { canonicalPage, isAppPage } from "@/lib/navigation";
import { consoleUpstream, dashboardPath, pagePolicy, proxyHeaders, stripOwnerCookie } from "@/lib/proxy-security";

export function proxy(request: NextRequest) {
  const { pathname, search } = request.nextUrl;
  let hostname = request.nextUrl.hostname;
  try { hostname = new URL(`http://${request.headers.get("host") ?? hostname}`).hostname; } catch { /* Ignore malformed host aliases. */ }
  const appHost = process.env.HEDVI_APP_HOST?.toLowerCase();
  if ((pathname === "/console-api" || pathname.startsWith("/console-api/")) &&
      appHost && hostname.toLowerCase() !== appHost) {
    return new NextResponse(null, { status: 404, headers: { "Cache-Control": "no-store" } });
  }
  if (request.method === "GET" || request.method === "HEAD") {
    const canonical = canonicalPage(hostname, pathname, search, appHost, process.env.HEDVI_SITE_URL);
    if (canonical) {
      const response = NextResponse.redirect(canonical, 307);
      response.headers.set("Cache-Control", "private, no-store");
      response.headers.set("Referrer-Policy", "no-referrer");
      return response;
    }
  }
  const svHost = process.env.HEDVI_SWEDISH_HOST?.toLowerCase();
  if (svHost && hostname.toLowerCase() === svHost && process.env.HEDVI_SITE_URL) {
    return NextResponse.redirect(new URL(`${pathname === "/priser" ? "/pricing" : pathname}${search}`, process.env.HEDVI_SITE_URL), 308);
  }
  if (pathname === "/console-api" || pathname.startsWith("/console-api/")) {
    const publicAPI = process.env.HEDVI_API_INTERNAL_URL ?? "http://127.0.0.1:8780";
    const {base: upstream, owner} = consoleUpstream(pathname, publicAPI, process.env.HEDVI_ADMIN_INTERNAL_URL, Boolean(appHost));
    if (!upstream) return new NextResponse(null, {status: 404, headers: {"Cache-Control": "no-store"}});
    const destination = new URL(`/console/v1${pathname.slice("/console-api".length)}${search}`, upstream);
    const headers = proxyHeaders(request.headers, request.method, destination.pathname, process.env.HEDVI_INGRESS_PROVIDER, process.env.HEDVI_PROXY_ATTESTATION_KEY);
    if (!owner) stripOwnerCookie(headers);
    return NextResponse.rewrite(destination, { request: { headers } });
  }
  const target = dashboardPath(hostname, pathname, process.env.HEDVI_APP_HOST);
  const headers = proxyHeaders(request.headers, request.method, pathname, undefined, undefined);
  const securedPage = isAppPage(target);
  const csp = securedPage ? pagePolicy(randomBytes(24).toString("base64")) : undefined;
  // Next.js extracts the nonce from the request CSP during dynamic rendering.
  headers.delete("content-security-policy");
  headers.delete("x-nonce");
  if (csp) headers.set("Content-Security-Policy", csp);
  const destination = request.nextUrl.clone();
  destination.pathname = target;
  const response = target !== pathname ? NextResponse.rewrite(destination, { request: { headers } }) : NextResponse.next({ request: { headers } });
  if (csp) {
    response.headers.set("Content-Security-Policy", csp);
    response.headers.set("Cache-Control", "private, no-store");
    response.headers.set("Referrer-Policy", "no-referrer");
  }
  return response;
}

export const config = { matcher: ["/((?!_next/|favicon.svg|icon-|hedvi-).*)"] };
