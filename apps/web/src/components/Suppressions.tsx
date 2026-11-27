"use client";
import { useState } from "react";
import { api, formatDate } from "@/lib/api";
import { useAction, useLoad } from "@/lib/hooks";
import { supportEmail } from "@/lib/public-site";

type Suppression = { id: string; email: string; reason: string; note: string; created_at: string; expires_at: string | null };

export function Suppressions({ pid, role }: { pid: string; role: string }) {
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState("");
  const { data, error: loadError, reload } = useLoad<{ data: Suppression[] }>(`/projects/${pid}/suppressions?q=${encodeURIComponent(filter)}`);
  const { pending, error, run } = useAction();
  const canRemove = ["owner", "admin"].includes(role);
  return <section className="card stack" id="suppressions" style={{ marginTop: 20 }}>
    <h2>Suppressions</h2>
    <p className="small muted">Blocked recipients apply to every project in your organization. Complaints require a support review before sending can resume.</p>
    <form className="row" onSubmit={(e) => { e.preventDefault(); setFilter(query.trim()); }}>
      <div className="field"><label htmlFor="suppression-search">Find a recipient</label><input id="suppression-search" value={query} onChange={(e) => setQuery(e.target.value)} maxLength={254} /></div>
      <button className="btn" style={{ alignSelf: "flex-end" }}>Search</button>
    </form>
    <form className="row" onSubmit={(e) => {
      e.preventDefault(); const form = e.currentTarget; const values = new FormData(form);
      void run(async () => { await api(`/projects/${pid}/suppressions`, { method: "POST", body: { email: values.get("email"), note: values.get("note") } }); form.reset(); await reload(); });
    }}>
      <div className="field"><label htmlFor="suppression-email">Email to block</label><input id="suppression-email" name="email" type="email" maxLength={254} required /></div>
      <div className="field"><label htmlFor="suppression-note">Reason</label><input id="suppression-note" name="note" maxLength={300} required /></div>
      <button className="btn" disabled={pending} style={{ alignSelf: "flex-end" }}>Block recipient</button>
    </form>
    {(error || loadError) && <p className="error" role="alert">{error || loadError?.message}</p>}
    {data && <>
      <p className="tiny muted">Showing up to 200 active suppressions. Narrow the search to find a specific address.</p>
      {data.data.length ? <table className="table"><thead><tr><th>Recipient</th><th>Reason</th><th>Expires</th><th>Action</th></tr></thead><tbody>
        {data.data.map((s) => <tr key={s.id}>
          <td className="break">{s.email}</td><td>{s.reason}{s.note && <div className="tiny muted">{s.note}</div>}</td><td className="small">{s.expires_at ? formatDate(s.expires_at) : "No expiry"}</td>
          <td>{s.reason === "complaint" ? <a className="small" href={`mailto:${supportEmail}`}>Contact support</a> : canRemove ? <button className="btn btn-small" disabled={pending} onClick={() => {
            const note = prompt(`Remove the block for ${s.email}? Only resume sending with the recipient's permission. Enter the reason for the audit log:`);
            if (!note?.trim()) return;
            void run(async () => { await api(`/projects/${pid}/suppressions/${s.id}`, { method: "DELETE", body: { note: note.trim().slice(0, 300) } }); await reload(); });
          }}>Remove block</button> : <span className="tiny muted">Admin required</span>}</td>
        </tr>)}
      </tbody></table> : <p className="small">No active suppressions match this search.</p>}
    </>}
  </section>;
}
