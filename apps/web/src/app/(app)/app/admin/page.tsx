"use client";

import { useRef, useState, type FormEvent } from "react";
import { useMe } from "@/components/AppShell";
import { api, formatDate, statusLabel } from "@/lib/api";
import { useAction, useLoad } from "@/lib/hooks";

type Organization = { id: string; name: string; owner_email?: string };
type Account = { id: string; email: string; verified: boolean; created_at: string; organizations: Organization[] };
type Grant = { id: string; org_id: string; organization: string; plan: string; ends_at: string; state: string; reason: string; revoke_reason: string | null };
type Review = { id: string; name: string; organization: string; org_id: string; state: string; hourly_limit: number; daily_limit: number; verified_domains: number; website: string; use_case: string; consent: string; volume: string };
type Incident = { id: string; org_id: string | null; category: string; severity: string; created_at: string };
type Summary = { users: Account[]; grants: Grant[]; reviews: Review[]; errors: Incident[] };
const memberships: Record<string, string> = {flex: "Flex · 10,000 recipients and 250 MiB for 365 days", pro_50k: "Pro 50k · 50,000 recipients and 1 GiB for 30 days", pro_100k: "Pro 100k · 100,000 recipients and 2 GiB for 30 days"};
const post = (path: string, body: unknown) => api(path, { method: "POST", body });
const formData = (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); return new FormData(event.currentTarget); };

export default function AdminPage() {
  const { user } = useMe();
  if (!user.is_staff || !user.mfa_enabled || !user.session_mfa) return <h1>Page unavailable</h1>;
  return <OwnerConsole />;
}

function OwnerConsole() {
  const [org, selectOrg] = useState<Organization | null>(null);
  const [matches, setMatches] = useState<Organization[] | null>(null);
  const summary = useLoad<Summary>(org ? `/admin/summary?org=${encodeURIComponent(org.id)}` : "/admin/summary", 30000);
  const { pending, error, run } = useAction();
  const request = useRef<{ input: string; id: string } | null>(null);
  const failure = error && <p role="alert" className="error">{error}</p>;
  const refreshAfter = (fn: () => Promise<unknown>) => run(async () => { try { await fn(); } finally { await summary.reload(); } });
  if (summary.loading) return <p>Loading owner console…</p>;
  if (summary.error?.name === "owner_unlock_required") return <section className="card stack"><h1>Unlock owner console</h1>
    <p>Enter the code from your separate Hedvi Owner authenticator entry to unlock the console for 30 minutes.</p>{failure}
    <form className="stack" onSubmit={e => { const f = formData(e); void refreshAfter(() => post("/admin/unlock", {code: f.get("code")})); }}>
      <label>Hedvi Owner code<input name="code" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" maxLength={6} required /></label>
      <button className="btn btn-primary" disabled={pending}>Unlock</button>
    </form></section>;
  if (summary.error || !summary.data) return <p role="alert">{summary.error?.status === 404 ? "Page unavailable." : "Could not load the owner console."}</p>;
  const { users, grants, reviews, errors } = summary.data;
  return <div className="stack">
    <div className="page-head"><h1>Owner console</h1><button className="btn" disabled={pending} onClick={() => void refreshAfter(() => post("/admin/lock", {}))}>Lock console</button></div>
    {failure}
    <section className="card stack"><h2>Find an account</h2>
      <form className="stack" onSubmit={e => { const f = formData(e); setMatches(null);
        void run(async () => { const result = await api<{organizations: Organization[]}>("/admin/search", {method: "POST", body: {query: f.get("search")}}); setMatches(result.organizations); });
      }}>
        <label>Account email or organization name<input name="search" type="search" minLength={2} maxLength={128} required placeholder="Start typing an email or organization name" /></label>
        <button className="btn" disabled={pending}>Search accounts</button>
      </form>
      {matches && (matches.length === 0 ? <p>No matching organizations.</p> : <div className="stack">
        <p className="small muted">Select an organization to manage its membership and sending access. Showing up to 20 matches.</p>
        {matches.map(o => <div key={o.id}><button className="btn btn-small" disabled={pending} onClick={() => selectOrg(o)}>{o.name}</button> {o.owner_email}</div>)}
      </div>)}
      <details><summary>Latest registrations ({users.length})</summary><div className="stack">
        {users.length === 0 && <p>No registrations yet.</p>}
        {users.map(u => <div className="stack" key={u.id}><div><strong>{u.email}</strong> · {u.verified ? "Verified" : "Unverified"} · {formatDate(u.created_at)}</div>
          {u.organizations.map(o => <div key={o.id}><button className="btn btn-small" disabled={pending} onClick={() => selectOrg({...o, owner_email: u.email})}>Manage {o.name}</button></div>)}</div>)}
      </div></details>
      {org && <div className="card stack"><strong>Managing {org.name}</strong><span>{org.owner_email}</span>
        <details><summary>Organization ID</summary><code>{org.id}</code></details><button className="btn btn-small" disabled={pending} onClick={() => selectOrg(null)}>Show all organizations</button></div>}
    </section>
    <section className="card stack"><h2>Complimentary membership</h2><p>Give one organization a fixed allowance. It expires automatically and never creates a charge or enables live sending.</p>
      <form className="stack" onSubmit={e => { const form = e.currentTarget; const f = formData(e); if (!org) return; const body = {org_id: org.id, plan: f.get("plan"), reason: String(f.get("reason")).trim()}; const input = JSON.stringify(body);
        if (request.current?.input !== input) request.current = {input, id: crypto.randomUUID()};
        const id = request.current.id;
        void refreshAfter(async () => {
          await post("/admin/memberships", {...body, request_id: id});
          request.current = null;
          form.reset();
        });
      }}>
        <p>{org ? <>Recipient organization · <strong>{org.name}</strong> {org.owner_email}</> : "Select an organization under Find an account to grant a membership."}</p>
        <label>Membership<select name="plan">{Object.entries(memberships).map(([key, label]) => <option key={key} value={key}>{label}</option>)}</select></label>
        <label>Reason for the audit log<textarea name="reason" minLength={10} maxLength={500} required /></label>
        <button className="btn btn-primary" disabled={pending || !org}>Grant membership</button>
      </form>
      <h3>Recent grants</h3>{grants.length === 0 && <p>No complimentary memberships.</p>}
      {grants.filter(g => !org || g.org_id === org.id).map(g => <div key={g.id} className="card stack"><strong>{g.organization} · {memberships[g.plan]?.split(" · ")[0] ?? g.plan} · {g.state}</strong><details><summary>Organization ID</summary><code>{g.org_id}</code></details><p>{g.reason} · Expires {formatDate(g.ends_at)}</p>
        {g.revoke_reason && <p>Revocation reason · {g.revoke_reason}</p>}
        {g.state === "active" && <form className="stack" onSubmit={e => { const f = formData(e); void refreshAfter(() => post(`/admin/memberships/${g.id}/revoke`, {reason: f.get("reason")})); }}>
          <label>Revocation reason<input name="reason" minLength={10} maxLength={500} required /></label><button className="btn btn-danger" disabled={pending}>Revoke remaining allowance</button></form>}</div>)}
    </section>
    <section className="card stack"><h2>Live sending</h2><p>Review consent, sending purpose and expected volume before setting delivery limits. Start new senders at 25 recipients per hour and 100 per day.</p>
      {reviews.length === 0 && <p>No projects in review or approved for sending.</p>}
      {reviews.filter(p => !org || p.org_id === org.id).map(p => <div className="card stack" key={p.id}><strong>{p.organization} / {p.name} · {statusLabel[p.state] ?? p.state}</strong><details><summary>Project ID</summary><code>{p.id}</code></details>
        <div>Verified sending domains · {p.verified_domains}</div><div>Website · {p.website || "Unavailable"}</div><div>Use case · {p.use_case || "Unavailable"}</div><div>Recipient consent · {p.consent || "Unavailable"}</div><div>Estimated monthly recipients · {p.volume || "Unavailable"}</div>
        <form className="stack" onSubmit={e => { const submitter = (e.nativeEvent as SubmitEvent).submitter as HTMLButtonElement | null; const f = formData(e);
          void refreshAfter(() => post(`/admin/projects/${p.id}/decision`, {action: submitter?.value, hourly_limit: Number(f.get("hourly")), daily_limit: Number(f.get("daily")), reason: f.get("reason")}));
        }}>
          <label>Recipients per hour<input type="number" min={1} max={50000} defaultValue={p.hourly_limit || 25} name="hourly" required /></label>
          <label>Recipients per day<input type="number" min={1} max={250000} defaultValue={p.daily_limit || 100} name="daily" required /></label>
          <label>Decision reason<input name="reason" minLength={10} maxLength={500} required /></label>
          <div className="row"><button className="btn btn-primary" value="approve" disabled={pending}>{p.state === "approved" ? "Save limits" : "Approve live sending"}</button>
            <button className="btn btn-danger" value="pause" disabled={pending}>Pause sending</button></div>
        </form></div>)}
    </section>
    <section className="card stack"><h2>Operational events</h2><p className="muted small">Latest 50 open alerts, send errors, account email errors and failed webhook deliveries. Use the event ID for incident follow-up.</p>
      {errors.length === 0 && <p>No matching events.</p>}
      {errors.map(e => <div className="stack" key={e.id}><strong>{e.category} · {e.severity}</strong><span>{formatDate(e.created_at)} · <code>{e.id}</code></span>{e.org_id && <span>Organization <code>{e.org_id}</code></span>}</div>)}
    </section>
  </div>;
}
