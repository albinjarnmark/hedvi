"use client";
import { api, formatDate } from "@/lib/api";
import { useAction, useLoad } from "@/lib/hooks";
import { Status } from "@/components/Status";

type Review = { project_id: string; project: string; organization: string; plan: string; sending_status: string; verified_domains: number; review_request: Record<string, string> | null };
type Alert = { id: string; kind: string; severity: string; message: string; created_at: string };
type Unknown = { attempt_id: string; email_id: string; error_code: string; error_message: string; started_at: string };

export default function AdminPage() {
  const reviews = useLoad<{ data: Review[] }>("/admin/reviews", 30000);
  const alerts = useLoad<{ data: Alert[] }>("/admin/alerts", 30000);
  const unknown = useLoad<{ data: Unknown[] }>("/admin/unknown-attempts", 30000);
  const { pending, error, run } = useAction();
  if (reviews.error) return <p className="error">{reviews.error.message}</p>;
  return (
    <>
      <div className="page-head"><div><h1>Staff</h1><p className="muted small">Every action is written to the customer&apos;s audit log.</p></div></div>
      {error && <p className="error" role="alert">{error}</p>}
      <section className="card stack">
        <h2>Sender reviews</h2>
        {(reviews.data?.data ?? []).map((r) => (
          <div key={r.project_id} className="card stack">
            <div className="row between"><strong>{r.organization} / {r.project}</strong><Status value={r.sending_status} /></div>
            <p className="small">Plan {r.plan} · verified domains: {r.verified_domains}</p>
            {r.review_request && <pre className="code small" style={{ whiteSpace: "pre-wrap" }}>{JSON.stringify(r.review_request, null, 2)}</pre>}
            <div className="row">
              <button className="btn btn-primary btn-small" disabled={pending} onClick={() => run(async () => {
                const note = prompt("Approval note (kept in the audit log)");
                if (!note) return;
                await api(`/admin/projects/${r.project_id}/approve`, { method: "POST", body: { note, hourly_limit: 25, daily_limit: 100 } });
                await reviews.reload();
              })}>Approve (25/h, 100/day)</button>
              <button className="btn btn-danger btn-small" disabled={pending} onClick={() => run(async () => {
                const reason = prompt("Reason for pausing");
                if (!reason) return;
                await api(`/admin/projects/${r.project_id}/pause`, { method: "POST", body: { reason } });
                await reviews.reload();
              })}>Pause</button>
            </div>
          </div>
        ))}
        {reviews.data?.data.length === 0 && <p className="muted">Nothing to review.</p>}
      </section>
      <section className="card stack" style={{ marginTop: 20 }}>
        <h2>Unknown send outcomes</h2>
        <p className="small muted">Check SES for message tags <code>hedvi_attempt</code> before resolving. Resolving never resends.</p>
        {(unknown.data?.data ?? []).map((u) => (
          <div key={u.attempt_id} className="row between small">
            <span className="mono">{u.attempt_id}</span><span>{u.error_code}: {u.error_message}</span><span>{formatDate(u.started_at)}</span>
            {(["accepted", "not_sent"] as const).map((outcome) => (
              <button key={outcome} className="btn btn-small" disabled={pending} onClick={() => run(async () => {
                const note = prompt(`Why resolve as ${outcome}?`);
                if (!note) return;
                await api(`/admin/attempts/${u.attempt_id}/resolve`, { method: "POST", body: { outcome, note } });
                await unknown.reload();
              })}>{outcome}</button>
            ))}
          </div>
        ))}
        {unknown.data?.data.length === 0 && <p className="muted">None.</p>}
      </section>
      <section className="card stack" style={{ marginTop: 20 }}>
        <h2>Open alerts</h2>
        {(alerts.data?.data ?? []).map((a) => (
          <div key={a.id} className="row between small">
            <Status value={a.severity === "critical" ? "failed" : a.severity === "warning" ? "pending" : "queued"} label={a.severity} />
            <span style={{ flex: 1 }}>{a.kind}: {a.message}</span><span>{formatDate(a.created_at)}</span>
            <button className="btn btn-small" onClick={() => run(async () => { await api(`/admin/alerts/${a.id}/resolve`, { method: "POST" }); await alerts.reload(); })}>Resolve</button>
          </div>
        ))}
      </section>
    </>
  );
}
