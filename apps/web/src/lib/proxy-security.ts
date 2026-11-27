import { createHmac } from "node:crypto";
import { isIP } from "node:net";

export function dashboardPath(host: string, pathname: string, appHost?: string): string {
  // Auth, docs and assets retain their paths. Only an explicitly configured
  // app-host root is an alias; arbitrary app.* hostnames have no effect.
  return appHost && host.toLowerCase() === appHost.toLowerCase() && pathname === "/" ? "/app" : pathname;
}

export function proxyHeaders(input: Headers, method: string, escapedPath: string, provider: string | undefined, key: string | undefined, now = Date.now()): Headers {
  const headers = new Headers(input);
  for (const name of ["X-Hedvi-Client-IP", "X-Hedvi-Proxy-Time", "X-Hedvi-Proxy-Signature"]) headers.delete(name);
  if (!key) return headers;
  const secret = Buffer.from(key, "base64");
  if (secret.length !== 32 || secret.toString("base64") !== key) throw new Error("HEDVI_PROXY_ATTESTATION_KEY must be a canonical base64-encoded 32-byte key.");
  // Requires exclusive ingress: Railway replaces X-Real-IP. ALB appends the
  // actual client to XFF and the web security group permits only that ALB.
  // Never enable these modes on an internet-accessible standalone Node port.
  const ip = provider === "railway" ? input.get("x-real-ip")?.trim()
    : provider === "alb" ? input.get("x-forwarded-for")?.split(",").at(-1)?.trim() : undefined;
  if (!ip || !isIP(ip)) return headers;
  const time = String(Math.floor(now / 1000));
  const signature = createHmac("sha256", secret).update(`${time}\n${method}\n${escapedPath}\n${ip}`).digest("hex");
  headers.set("X-Hedvi-Client-IP", ip);
  headers.set("X-Hedvi-Proxy-Time", time);
  headers.set("X-Hedvi-Proxy-Signature", signature);
  return headers;
}

export function pagePolicy(nonce: string): string {
  return [
    "default-src 'self'",
    `script-src 'self' 'nonce-${nonce}' 'strict-dynamic'${process.env.NODE_ENV === "development" ? " 'unsafe-eval'" : ""}`,
    "style-src 'self' 'unsafe-inline'", "img-src 'self' data: blob: https:",
    "font-src 'self'", "connect-src 'self'", "frame-src 'self'",
    "frame-ancestors 'none'", "form-action 'self'", "base-uri 'self'", "object-src 'none'",
  ].join("; ");
}
