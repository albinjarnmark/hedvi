"use client";
import { useState } from "react";
import { useProjectId } from "@/components/AppShell";
import { EmailTable, type EmailSummary } from "@/components/EmailList";
import { useLoad } from "@/lib/hooks";

const filters = ["", "queued", "sent", "delivered", "delivery_delayed", "bounced", "complained", "failed", "suppressed", "delivery_unknown"];

export default function EmailsPage() {
  const pid = useProjectId();
  const [mode, setMode] = useState("live");
  const [status, setStatus] = useState("");
  const [q, setQ] = useState("");
  const qs = new URLSearchParams({ mode, limit: "50", ...(status ? { status } : {}), ...(q ? { q } : {}) });
  const { data, error } = useLoad<{ data: EmailSummary[]; has_more: boolean }>(`/projects/${pid}/emails?${qs}`, 5000);
  return (
    <>
      <div className="page-head"><div><h1>Emails</h1><p className="muted small">Status per recipient: queued → accepted by provider → receiving server&apos;s answer.</p></div></div>
      <div className="row" style={{ marginBottom: 16 }}>
        <select aria-label="Mode" value={mode} onChange={(e) => setMode(e.target.value)} style={{ width: 140 }}><option value="live">Live</option><option value="test">Test</option></select>
        <select aria-label="Status" value={status} onChange={(e) => setStatus(e.target.value)} style={{ width: 200 }}>
          {filters.map((f) => <option key={f} value={f}>{f || "All statuses"}</option>)}
        </select>
        <input aria-label="Search recipient, subject or id" placeholder="Search recipient, subject or id" value={q} onChange={(e) => setQ(e.target.value)} style={{ maxWidth: 360 }} />
      </div>
      {error && <p className="error">{error.message}</p>}
      {data && <EmailTable emails={data.data} hrefBase={`/app/${pid}/emails`} empty="No emails match." />}
      {data?.has_more && <p className="small muted">Showing the 50 most recent. Narrow the search to find older emails.</p>}
    </>
  );
}
