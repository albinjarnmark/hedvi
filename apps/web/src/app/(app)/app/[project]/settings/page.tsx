"use client";
import { SendingLimits } from "@/components/SendingLimits";
import { ScrollTable } from "@/components/ScrollTable";
import Link from "next/link";
import { BillingSettings } from "@/components/BillingSettings";
import { MFASettings } from "@/components/MFASettings";
import { Suppressions } from "@/components/Suppressions";
import { useProjectId } from "@/components/AppShell";
import { Status } from "@/components/Status";
import { api, bytes, formatDate } from "@/lib/api";
import { useAction, useLoad } from "@/lib/hooks";

type Settings = {
  organization: { id: string; name: string; plan_name: string; currency: string; use_credits_for_overage: boolean; overage_enabled: boolean; overage_cap: number; overage_allowed: boolean };
  retention: { metadata_days: number; content_days: number; plan_metadata_days: number; plan_max_content_days: number; test_inbox_days: number };
  project: { name: string; sending_status: string; status_reason: string; hourly_limit: number; daily_limit: number; org_hourly_limit: number; org_daily_limit: number };
  role: string;
};
type Usage = {
  period_start: string; period_end: string;
  recipients: { included: number; reserved: number; consumed: number; remaining: number };
  overage: { cap: number; reserved: number; consumed: number };
  attachment_data_bytes: { included: number; reserved: number; consumed: number };
  credits: { id: string; granted: number; remaining: number; expires_at: string; expired_at: string | null }[];
};

function LiveAccess({ pid, status }: { pid: string; status: string }) {
  if (status === "approved") return <div className="stack"><Status value={status} /><p className="small muted">You can send from every verified domain in this project.</p></div>;
  if (status === "paused") return <p className="notice notice-warn">Sending is paused. <a href="mailto:hej@stackx.se">Contact support</a> to resolve the restriction.</p>;
  return <div className="stack">
    <p>Verify your domain and start sending. Activation is automatic once your DNS records are confirmed.</p>
    <p className="small muted">Your account’s accepted terms already apply. No application form or manual approval is needed.</p>
    <Link className="btn btn-primary" href={`/app/${pid}/domains`}>Set up sending domain</Link>
  </div>;
}

export default function SettingsPage() {
  const pid = useProjectId();
  const s = useLoad<Settings>(`/projects/${pid}/settings`);
  const u = useLoad<Usage>(`/projects/${pid}/usage`, 30000);
  const members = useLoad<{ data: { id: string; email: string; role: string; mfa_enabled: boolean }[] }>(`/projects/${pid}/members`);
  const audit = useLoad<{ data: { action: string; actor: string; actor_kind: string; created_at: string }[] }>(s.data && ["owner", "admin"].includes(s.data.role) ? `/projects/${pid}/audit` : null);
  const { pending, error, run } = useAction();
  if (!s.data) return <p className="muted">Loading…</p>;
  const o = s.data.organization, r = s.data.retention;
  const save = (body: Record<string, unknown>) => run(async () => { await api(`/projects/${pid}/settings/org`, { method: "PATCH", body }); await s.reload(); await u.reload(); });
  return (
    <>
      <div className="page-head"><div><h1>Settings</h1><p className="muted small">{o.name} · {o.plan_name}</p></div></div>
      {error && <p className="error" role="alert">{error}</p>}
      <div className="split">
        <section className="card stack">
          <h2>Live sending</h2>
          <LiveAccess pid={pid} status={s.data.project.sending_status} />
          {s.data.project.sending_status === "paused" && s.data.project.status_reason && <p className="small">Note: {s.data.project.status_reason}</p>}
          <SendingLimits limits={s.data.project} />
        </section>
        <section className="card stack">
          <h2>Usage and cost control</h2>
          {u.data && (
            <>
              {u.data.recipients.included > 0 ? <>
                <p>Monthly allowance: <strong>{u.data.recipients.remaining.toLocaleString()}</strong> of {u.data.recipients.included.toLocaleString()} recipients left until {formatDate(u.data.period_end)}.</p>
                <p className="small">{u.data.recipients.consumed.toLocaleString()} sent · {u.data.recipients.reserved.toLocaleString()} reserved. Attachment data: {bytes(u.data.attachment_data_bytes.consumed)} of {bytes(u.data.attachment_data_bytes.included)} used this period.</p>
              </> : <p>Flex uses prepaid credits. Each pack has its own expiry date and includes 250 MiB of attachment data.</p>}
              {u.data.credits.length > 0 && (
                <ul className="small">{u.data.credits.map((c) => <li key={c.id}>Flex credits: {c.remaining.toLocaleString()} of {c.granted.toLocaleString()} left · {c.expired_at ? "expired" : `expire ${formatDate(c.expires_at)}`}</li>)}</ul>
              )}
            </>
          )}
          <p className="notice small"><strong>Hard stop is on.</strong> Your application’s sending stops when it runs out of eligible units, including its sign-in and password emails. Warnings arrive at 80 % and 95 % in the dashboard and by webhook (<code>hedvi.quota.warning</code>).</p>
          {u.data && u.data.recipients.included > 0 && <label className="row" style={{ fontWeight: 400 }}>
            <input type="checkbox" checked={o.use_credits_for_overage} disabled={pending} onChange={(e) => save({ use_credits_for_overage: e.target.checked })} />
            Use Flex credits when monthly quota is used up
          </label>}
          <p className="small muted">Sending stops when included and eligible prepaid units run out.</p>
        </section>
        {["owner","admin","billing"].includes(s.data.role)&&<BillingSettings project={pid} preferred={o.currency}/>}
        <section className="card stack">
          <h2>Retention</h2>
          <p className="small">Metadata (recipients, statuses) is kept <strong>{r.metadata_days} days</strong>; content (subject, body, attachments) <strong>{r.content_days} days</strong>. Test inbox: {r.test_inbox_days} days.</p>
          <form className="row" onSubmit={(e) => { e.preventDefault(); const f = new FormData(e.currentTarget); void save({ content_retention_days: Number(f.get("content")), metadata_retention_days: Number(f.get("metadata")) }); }}>
            <div><label htmlFor="content">Content days (0 = delete after processing)</label><input id="content" name="content" type="number" min={0} max={r.plan_max_content_days} defaultValue={r.content_days} /></div>
            <div><label htmlFor="metadata">Metadata days (max {r.plan_metadata_days})</label><input id="metadata" name="metadata" type="number" min={1} max={r.plan_metadata_days} defaultValue={r.metadata_days} /></div>
            <button className="btn btn-small" disabled={pending} style={{ alignSelf: "flex-end" }}>Save</button>
          </form>
          <p className="tiny muted">After content is deleted you cannot preview, search or resend the email.</p>
        </section>
        <section className="card stack">
          <h2>Security</h2>
          <MFASettings />
          <h3>Members</h3>
          <ul className="small">{(members.data?.data ?? []).map((m) => <li key={m.id}>{m.email} · {m.role}{m.mfa_enabled ? " · 2FA" : ""}</li>)}</ul>
          <p className="tiny muted">Team invitations are not available yet.</p>
        </section>
      </div>
      {["owner", "admin", "developer"].includes(s.data.role) && <Suppressions pid={pid} role={s.data.role} />}
      {audit.data && (
        <section className="card" style={{ marginTop: 20 }}>
          <h2>Audit log</h2>
          <ScrollTable><tbody>
            {audit.data.data.slice(0, 50).map((a, i) => <tr key={i}><td className="small nowrap">{formatDate(a.created_at)}</td><td><code>{a.action}</code></td><td className="small">{a.actor || a.actor_kind}</td></tr>)}
          </tbody></ScrollTable>
        </section>
      )}
    </>
  );
}
