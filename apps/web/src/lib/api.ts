// Client for the console API, proxied by Next.js from /console-api to the Go
// service. Cookies are HttpOnly; state-changing requests send the CSRF header.
export class ApiError extends Error {
  constructor(public status: number, public name: string, message: string, public body: Record<string, unknown> = {}) {
    super(message);
  }
}

export async function api<T = Record<string, unknown>>(path: string, init: { method?: string; body?: unknown; idempotencyKey?: string } = {}): Promise<T> {
  const method = init.method ?? "GET";
  const res = await fetch(`/console-api${path}`, {
    method,
    credentials: "same-origin",
    headers: {
      ...(init.idempotencyKey ? { "Idempotency-Key": init.idempotencyKey } : {}),
      ...(init.body !== undefined ? { "Content-Type": "application/json" } : {}),
      ...(method !== "GET" ? { "X-Hedvi-Console": "1" } : {}),
    },
    body: init.body !== undefined ? JSON.stringify(init.body) : undefined,
    cache: "no-store",
  });
  const text = await res.text();
  let data: Record<string, unknown> = {};
  try {
    data = text ? JSON.parse(text) : {};
  } catch {
    data = { message: text };
  }
  if (!res.ok) {
    throw new ApiError(res.status, String(data.name ?? "error"), String(data.message ?? res.statusText), data);
  }
  return data as T;
}

export function formatDate(iso: string | null | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  return isNaN(d.getTime()) ? "—" : d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

export function bytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
  if (n < 1024 ** 3) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 ** 3).toFixed(2)} GB`;
}

// Status labels. Color is paired with text everywhere (accessibility).
const statusTone: Record<string, "ok" | "warn" | "bad" | "neutral"> = {
  delivered: "ok", sent: "ok", verified: "ok", approved: "ok", succeeded: "ok", test_captured: "neutral", queued: "neutral",
  pending: "warn", partially_verified: "warn", delivery_delayed: "warn", delivery_unknown: "warn", pending_review: "warn",
  temporary_failure: "warn", test_only: "neutral", retry_wait: "warn", held: "warn",
  bounced: "bad", complained: "bad", failed: "bad", suppressed: "bad", paused: "bad", not_started: "neutral",
};

export const statusLabel: Record<string, string> = {
  test_captured: "Captured (test)", queued: "Queued", sent: "Accepted by provider", delivered: "Delivered to server",
  delivery_delayed: "Delayed", delivery_unknown: "Outcome unknown", bounced: "Bounced", complained: "Complaint",
  failed: "Failed", suppressed: "Suppressed", verified: "Verified", pending: "Pending", partially_verified: "Partly verified",
  temporary_failure: "DNS lookup failed", not_started: "Not started", test_only: "Test only", pending_review: "In review",
  approved: "Live sending on", paused: "Paused", succeeded: "Succeeded", retry_wait: "Retrying", held: "Held (paused)",
};

export function tone(status: string) {
  return statusTone[status] ?? "neutral";
}
