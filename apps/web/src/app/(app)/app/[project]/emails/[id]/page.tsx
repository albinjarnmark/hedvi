"use client";
import Link from "next/link";
import { useRef } from "react";
import { useParams, useRouter } from "next/navigation";
import { useProjectId } from "@/components/AppShell";
import { EmailPreview, type Content } from "@/components/EmailPreview";
import { Status } from "@/components/Status";
import { api, formatDate } from "@/lib/api";
import { useAction, useLoad } from "@/lib/hooks";

type Detail = {
  id: string; mode: string; from: string; subject: string | null; last_event: string; send_state: string; created_at: string;
  content_available: boolean; content_expires_at: string; provider_message_id: string | null; resend_of: string | null;
  recipients: { email: string; field: string; status: string; status_detail: string; status_at: string; billing: string }[] | null;
  events: { type: string; recipient?: string; occurred_at: string; detail?: Record<string, string> }[] | null;
  attempts: { id: string; state: string; started_at: string; error_code?: string }[] | null;
  content?: Content;
};

export default function EmailDetailPage() {
  const pid = useProjectId();
  const { id } = useParams<{ id: string }>();
  const router = useRouter();
  const resend = useRef<{ email: string; key: string } | null>(null);
  const { data, error } = useLoad<Detail>(`/projects/${pid}/emails/${id}`, 5000);
  const { pending, error: actionError, run } = useAction();
  if (error) return <p className="error">{error.message}</p>;
  if (!data) return <p className="muted">Loading…</p>;
  return (
    <>
      <p className="small"><Link href={`/app/${pid}/${data.mode === "test" ? "test-inbox" : "emails"}`}>← Back</Link></p>
      <div className="page-head">
        <div><h1>{data.subject ?? "(content deleted)"}</h1><p className="small muted mono">{data.id}</p></div>
        <div className="row">
          <Status value={data.last_event} />
          {data.content_available && (
            <button className="btn btn-small" disabled={pending} onClick={() => run(async () => {
              if (!confirm("Resending creates a NEW email. It uses quota again and recipients may get a duplicate. Continue?")) return;
              // Retain the same key after an uncertain response. A subsequent
              // intentional resend on another email gets its own key.
              if (resend.current?.email !== data.id) resend.current = { email: data.id, key: crypto.randomUUID() };
              const res = await api<{ id: string }>(`/projects/${pid}/emails/${data.id}/resend`, { method: "POST", idempotencyKey: resend.current.key });
              router.push(`/app/${pid}/emails/${res.id}`);
            })}>Resend as new email</button>
          )}
        </div>
      </div>
      {actionError && <p className="error" role="alert">{actionError}</p>}
      {data.send_state === "unknown" && (
        <p className="notice notice-warn">The sending provider did not confirm whether it accepted this email. Hedvi does not retry automatically to avoid duplicates and is reconciling it from provider events. Quota stays reserved until it is resolved.</p>
      )}
      {data.mode === "test" && <p className="notice notice-lime">Test email: captured in the test inbox, not delivered externally. It does not show inbox placement.</p>}
      <div className="split">
        <div className="stack">
          <div className="card">
            <h2>Recipients</h2>
            <table className="table">
              <thead><tr><th>Address</th><th>Status</th><th>Detail</th></tr></thead>
              <tbody>
                {(data.recipients ?? []).map((r) => (
                  <tr key={r.email}>
                    <td className="break">{r.email} <span className="tiny muted">{r.field}</span></td>
                    <td><Status value={r.status} /><div className="tiny muted">{formatDate(r.status_at)}</div></td>
                    <td className="small break">{r.status_detail || "—"}<div className="tiny muted">quota: {r.billing}</div></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="card">
            <h2>Timeline</h2>
            <ul className="timeline">
              <li><strong>Accepted by Hedvi</strong> <span className="small muted">{formatDate(data.created_at)}</span></li>
              {(data.attempts ?? []).map((a) => (
                <li key={a.id}><strong>Send attempt: {a.state}</strong> <span className="small muted">{formatDate(a.started_at)}</span>
                  {a.error_code && <div className="tiny">{a.error_code}</div>}</li>
              ))}
              {(data.events ?? []).map((e, i) => (
                <li key={i}><strong>{e.type}</strong>{e.recipient ? ` · ${e.recipient}` : ""} <span className="small muted">{formatDate(e.occurred_at)}</span>
                  {e.detail && Object.keys(e.detail).length > 0 && <div className="tiny muted break">{Object.entries(e.detail).map(([k, v]) => `${k}: ${v}`).join(" · ")}</div>}</li>
              ))}
            </ul>
            {data.provider_message_id && <p className="tiny muted">Provider message id: <span className="mono">{data.provider_message_id}</span></p>}
          </div>
        </div>
        <div className="card">
          <h2>Content</h2>
          <p className="tiny muted">From {data.from}. Content is kept until {formatDate(data.content_expires_at)}.</p>
          {data.content ? (
            <EmailPreview content={data.content} attachmentHref={(i) => `/console-api/projects/${pid}/emails/${data.id}/attachments/${i}`} />
          ) : <p className="muted">The content was deleted under your retention settings. Metadata is still available.</p>}
        </div>
      </div>
    </>
  );
}
