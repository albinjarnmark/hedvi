"use client";
import Link from "next/link";
import { useProjectId } from "@/components/AppShell";
import { Status } from "@/components/Status";
import { formatDate } from "@/lib/api";
import { useLoad } from "@/lib/hooks";

type Overview = {
  project: { name: string; sending_status: string; status_reason: string; hourly_limit: number; daily_limit: number };
  checklist: { id: string; done: boolean; optional?: boolean; state?: string }[];
  last_24h: Record<string, number>;
  usage: { recipients: { included: number; reserved: number; consumed: number; remaining: number }; period_end: string; credits: { remaining: number; expires_at: string }[] };
  plan: string;
  alerts: { id: string; severity: string; message: string; created_at: string }[];
};

const steps: Record<string, { label: string; href: string }> = {
  test_key: { label: "Get a test key", href: "/developers" },
  test_email: { label: "Send a test email", href: "/test-inbox" },
  domain_added: { label: "Add your domain", href: "/domains" },
  domain_verified: { label: "Verify DNS records", href: "/domains" },
  live_access: { label: "Request live sending (manual review)", href: "/settings" },
  live_key: { label: "Create a live key", href: "/developers" },
  first_live_email: { label: "Send your first live email", href: "/emails" },
  webhook: { label: "Add a webhook (optional)", href: "/developers" },
};

export default function OverviewPage() {
  const pid = useProjectId();
  const { data, error } = useLoad<Overview>(`/projects/${pid}/overview`, 15000);
  if (error) return <p className="error">{error.message}</p>;
  if (!data) return <p className="muted">Loading…</p>;
  const u = data.usage.recipients;
  const pct = u.included ? Math.min(100, ((u.consumed + u.reserved) / u.included) * 100) : 0;
  const sent24 = Object.entries(data.last_24h).filter(([k]) => k.startsWith("live:")).reduce((a, [, v]) => a + v, 0);
  const test24 = Object.entries(data.last_24h).filter(([k]) => k.startsWith("test:")).reduce((a, [, v]) => a + v, 0);
  return (
    <>
      <div className="page-head">
        <div><p className="small muted">Project</p><h1>{data.project.name}</h1></div>
        <Status value={data.project.sending_status} />
      </div>
      {data.alerts.map((a) => (
        <p key={a.id} className={`notice ${a.severity === "critical" ? "notice-bad" : "notice-warn"}`} role="status">{a.message} <span className="tiny muted">{formatDate(a.created_at)}</span></p>
      ))}
      <div className="grid grid-3" style={{ marginTop: 16 }}>
        <div className="card"><p className="small muted">Live emails, last 24 h</p><div className="kpi">{sent24}</div></div>
        <div className="card"><p className="small muted">Test emails, last 24 h</p><div className="kpi">{test24}</div></div>
        <div className="card">
          <p className="small muted">Included recipients this month ({data.plan})</p>
          <div className="kpi">{u.remaining.toLocaleString()} <span className="small muted">left of {u.included.toLocaleString()}</span></div>
          <div className="meter" aria-label={`${pct.toFixed(0)} percent used`}><span style={{ width: `${pct}%` }} /></div>
          <p className="tiny muted" style={{ marginTop: 6 }}>Hard stop at 100 %. Resets {formatDate(data.usage.period_end)}.</p>
        </div>
      </div>
      <div className="split" style={{ marginTop: 20 }}>
        <div className="card">
          <h2>From test to production</h2>
          <ul className="checklist">
            {data.checklist.map((c) => (
              <li key={c.id} className={c.done ? "done" : ""}>
                <span className="box" aria-hidden="true">{c.done ? "✓" : ""}</span>
                <Link href={`/app/${pid}${steps[c.id]?.href ?? ""}`}>{steps[c.id]?.label ?? c.id}</Link>
                <span className="tiny muted">{c.done ? "Done" : c.id === "live_access" && c.state === "pending_review" ? "In review" : ""}</span>
              </li>
            ))}
          </ul>
        </div>
        <div className="card">
          <h2>Limits</h2>
          <p>Live sending: <strong>{data.project.hourly_limit}</strong> recipients/hour, <strong>{data.project.daily_limit}</strong>/day.</p>
          <p className="small muted">New senders start low and limits are raised after review — not automatically after a quiet period.</p>
          {data.project.status_reason && <p className="small">Status note: {data.project.status_reason}</p>}
        </div>
      </div>
    </>
  );
}
