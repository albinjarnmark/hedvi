"use client";

import { useRef, useState, type FormEvent } from "react";
import { useMe } from "@/components/AppShell";
import { ScrollTable } from "@/components/ScrollTable";
import { Status } from "@/components/Status";
import { api, formatDate } from "@/lib/api";
import { useAction, useLoad } from "@/lib/hooks";

type Organization = { id: string; name: string; owner_email?: string };
type Account = { id: string; email: string; verified: boolean; created_at: string; organizations: Organization[] };
type Grant = { id: string; org_id: string; organization: string; plan: string; ends_at: string; state: string; reason: string; revoke_reason: string | null };
type Project = { id: string; name: string; organization: string; org_id: string; state: string; hourly_limit: number; daily_limit: number; verified_domains: number };
type Incident = { id: string; org_id: string | null; category: string; severity: string; created_at: string };
type Purchase = { id: string; org_id: string; organization: string; sku: string; state: string; amount: number; currency: string; created_at: string };
type Summary = { users: Account[]; grants: Grant[]; reviews: Project[]; errors: Incident[]; purchases?: Purchase[] };
type Tab = "overview" | "accounts" | "sending" | "billing" | "errors";
const tabs: { id: Tab; label: string }[] = [{id:"overview",label:"Overview"},{id:"accounts",label:"Accounts"},{id:"sending",label:"Sending"},{id:"billing",label:"Billing"},{id:"errors",label:"Errors"}];
const plans: Record<string, string> = {flex:"Flex 10k",pro_50k:"Pro 50k",pro_100k:"Pro 100k"};
const allowance: Record<string, string> = {flex:"10,000 recipients · 250 MiB · 365 days",pro_50k:"50,000 recipients · 1 GiB · 30 days",pro_100k:"100,000 recipients · 2 GiB · 30 days"};
const post = (path: string, body: unknown) => api(path, {method:"POST",body});
const formData = (e: FormEvent<HTMLFormElement>) => {e.preventDefault();return new FormData(e.currentTarget);};
const empty = (title: string, text: string) => <div className="owner-empty"><h3>{title}</h3><p className="small muted">{text}</p></div>;

export default function AdminPage() {
  const {user}=useMe();
  if (!user.is_staff || !user.mfa_enabled || !user.session_mfa) return <h1>Page unavailable</h1>;
  return <OwnerConsole />;
}

function OwnerConsole() {
  const [tab,setTab]=useState<Tab>("overview");
  const [org,setOrg]=useState<Organization|null>(null);
  const summary=useLoad<Summary>("/admin/summary",30000);
  const {pending,error,run}=useAction();
  const manage=(value: Organization)=>{setOrg(value);setTab("accounts");};
  const navigate=(value:Tab)=>{setOrg(null);setTab(value);};
  if(summary.loading)return <p>Loading owner console…</p>;
  if(summary.error?.name==="owner_unlock_required")return <section className="card stack owner-unlock"><p className="eyebrow">Hedvi · Owner</p><h1>Welcome back.</h1><p className="muted">Unlock account management with your separate Hedvi Owner authenticator code.</p>
    <form className="stack" onSubmit={e=>{const f=formData(e);void run(async()=>{await post("/admin/unlock",{code:f.get("code")});await summary.reload();});}}>
      <div className="field"><label htmlFor="owner-code">6-digit owner code</label><input id="owner-code" name="code" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" maxLength={6} required /></div>
      {error&&<p role="alert" className="error">{error}</p>}<button className="btn btn-primary" disabled={pending}>Unlock console</button><p className="tiny muted">Access stays unlocked for 30 minutes.</p>
    </form></section>;
  if(summary.error||!summary.data)return <p role="alert">{summary.error?.status===404?"Page unavailable.":"Could not load the owner console. Refresh to try again."}</p>;
  const data=summary.data;
  return <div className="owner-console">
    <header className="page-head"><div><p className="eyebrow">Hedvi administration</p><h1>Owner console</h1><p className="muted small">Accounts, sending and operations in one place.</p></div><button className="btn btn-small" disabled={pending} onClick={()=>void run(async()=>{await post("/admin/lock",{});await summary.reload();})}>Lock console</button></header>
    <nav className="owner-tabs" aria-label="Owner sections">{tabs.map(t=><button key={t.id} type="button" aria-current={tab===t.id?"page":undefined} onClick={()=>navigate(t.id)}>{t.label}{t.id==="errors"&&data.errors.length>0&&<span className="owner-count">{data.errors.length}</span>}</button>)}</nav>
    {error&&<p role="alert" className="error">{error}</p>}
    <section className="owner-panel" aria-label={tabs.find(t=>t.id===tab)?.label}>
      {tab==="overview"&&<>
        <div className="owner-metrics">
          <button onClick={()=>navigate("accounts")}><span>Recent accounts</span><strong>{data.users.length}</strong><small>Browse registrations</small></button>
          <button onClick={()=>navigate("sending")}><span>Live projects</span><strong>{data.reviews.filter(p=>p.state==="approved").length}</strong><small>View sending status</small></button>
          <button onClick={()=>navigate("errors")}><span>Events to review</span><strong>{data.errors.length}</strong><small>Open the error log</small></button>
        </div>
        <div className="owner-section-head"><div><h2>Latest registrations</h2><p className="small muted">Your newest accounts, with their verification status.</p></div><button className="btn btn-small" onClick={()=>navigate("accounts")}>View accounts</button></div>
        <AccountRows users={data.users.slice(0,5)} manage={manage}/>
        <p className="tiny muted">Overview shows the most recent 50 accounts, projects and events. Search Accounts to find older customers.</p>
      </>}
      {tab==="accounts"&&(org?<AccountDetail key={org.id} org={org} back={()=>setOrg(null)}/>:<Accounts users={data.users} manage={manage}/>)}
      {tab==="sending"&&<><div className="owner-section-head"><div><h2>Sending</h2><p className="small muted">Verified domains activate automatically. Open a project only to change limits or handle a restriction.</p></div></div><Sending projects={data.reviews} reload={summary.reload}/></>}
      {tab==="billing"&&<><div className="owner-section-head"><div><h2>Billing</h2><p className="small muted">Recent purchases and complimentary memberships. Open an account to grant access without payment.</p></div><button className="btn btn-primary btn-small" onClick={()=>navigate("accounts")}>Find an account</button></div><Purchases purchases={data.purchases??[]}/><h3 className="owner-subheading">Complimentary memberships</h3><GrantRows grants={data.grants} manage={manage}/></>}
      {tab==="errors"&&<Errors errors={data.errors}/>}
    </section>
  </div>;
}

function AccountRows({users,manage}:{users:Account[];manage:(o:Organization)=>void}) {
  if(!users.length)return empty("No accounts yet","New registrations will appear here.");
  return <div className="owner-list">{users.map(u=><div className="owner-list-row" key={u.id}><div className="owner-row-main"><strong>{u.email}</strong><span className="tiny muted">Joined {formatDate(u.created_at)}</span></div><Status value={u.verified?"verified":"pending"} label={u.verified?"Verified":"Email pending"}/><div className="owner-row-actions">{u.organizations.map(o=><button className="btn btn-small" key={o.id} onClick={()=>manage({...o,owner_email:u.email})}>Open {o.name}</button>)}</div></div>)}</div>;
}
function Accounts({users,manage}:{users:Account[];manage:(o:Organization)=>void}) {
  const [matches,setMatches]=useState<Organization[]|null>(null);
  const {pending,error,run}=useAction();
  return <><div className="owner-section-head"><div><h2>Accounts</h2><p className="small muted">Find a customer to manage their membership and projects.</p></div></div>
    <form className="owner-search" onSubmit={e=>{const f=formData(e);void run(async()=>{const result=await api<{organizations:Organization[]}>("/admin/search",{method:"POST",body:{query:f.get("query")}});setMatches(result.organizations);});}}>
      <label className="sr-only" htmlFor="owner-search">Email or organization</label><input id="owner-search" name="query" type="search" minLength={2} maxLength={128} required placeholder="Search by email or organization…"/><button className="btn" disabled={pending}>Search</button>{matches&&<button type="button" className="btn" onClick={()=>setMatches(null)}>Clear</button>}
    </form>{error&&<p role="alert" className="error">{error}</p>}
    {matches?matches.length?<div className="owner-list">{matches.map(o=><div key={o.id} className="owner-list-row"><div className="owner-row-main"><strong>{o.name}</strong><span className="small muted">{o.owner_email}</span></div><button className="btn btn-small" onClick={()=>manage(o)}>Open account</button></div>)}</div>:empty("No matching accounts","Try the beginning of the email address or organization name."):<AccountRows users={users} manage={manage}/>}
  </>;
}
function AccountDetail({org,back}:{org:Organization;back:()=>void}) {
  const [section,setSection]=useState<"membership"|"projects"|"purchases">("membership");
  const detail=useLoad<Summary>(`/admin/summary?org=${encodeURIComponent(org.id)}`,30000);
  return <><button className="owner-back" onClick={back}>← All accounts</button><div className="owner-section-head"><div><h2>{org.name}</h2><p className="small muted">{org.owner_email}</p></div></div>
    <nav className="owner-subtabs" aria-label="Account sections">{(["membership","projects","purchases"] as const).map(s=><button key={s} aria-current={section===s?"page":undefined} onClick={()=>setSection(s)}>{s==="membership"?"Membership":s==="projects"?"Sending projects":"Purchases"}</button>)}</nav>
    {detail.error?<p role="alert" className="error">Could not load this account. Return to the overview and unlock the console if needed.</p>:!detail.data?<p>Loading account…</p>:<>
      {section==="membership"&&<Membership org={org} grants={detail.data.grants} reload={detail.reload}/>}
      {section==="projects"&&<Sending projects={detail.data.reviews} reload={detail.reload}/>}
      {section==="purchases"&&<Purchases purchases={detail.data.purchases??[]}/>}
    </>}
  </>;
}
function Membership({org,grants,reload}:{org:Organization;grants:Grant[];reload:()=>Promise<void>}) {
  const [editing,setEditing]=useState(false),[revoking,setRevoking]=useState<string|null>(null);
  const request=useRef<{input:string;id:string}|null>(null);
  const {pending,error,run}=useAction();
  const active=grants.filter(g=>g.state==="active");
  return <><div className="owner-section-head"><div><h3>Complimentary access</h3><p className="small muted">A gift allowance expires automatically. The customer is never charged.</p></div>{!editing&&<button className="btn btn-primary btn-small" onClick={()=>setEditing(true)}>Give membership</button>}</div>
    {error&&<p role="alert" className="error">{error}</p>}
    {editing&&<form className="owner-editor stack" onSubmit={e=>{const f=formData(e);const body={org_id:org.id,plan:f.get("plan"),reason:String(f.get("reason")).trim()};const input=JSON.stringify(body);if(request.current?.input!==input)request.current={input,id:crypto.randomUUID()};const id=request.current.id;void run(async()=>{await post("/admin/memberships",{...body,request_id:id});request.current=null;setEditing(false);await reload();});}}>
      <h3>Give {org.name} a membership</h3><label>Plan<select name="plan">{Object.entries(plans).map(([key,label])=><option key={key} value={key}>{label} · {allowance[key]}</option>)}</select></label>
      <label>Reason (shown in the account audit log)<input name="reason" minLength={10} maxLength={500} required defaultValue="Complimentary access from Hedvi owner"/></label><p className="tiny muted">The customer can read this reason in their audit log. It is prefilled; no customer application is required.</p>
      <div className="row"><button className="btn btn-primary" disabled={pending}>Give membership</button><button type="button" className="btn" disabled={pending} onClick={()=>setEditing(false)}>Cancel</button></div>
    </form>}
    {!active.length&&!editing&&empty("No active gift membership","Give Flex credits or a Pro allowance using the button above. Existing paid purchases remain separate.")}
    {active.map(g=><div className="owner-list-row" key={g.id}><div className="owner-row-main"><strong>{plans[g.plan]??g.plan}</strong><span className="small muted">{allowance[g.plan]} · Ends {formatDate(g.ends_at)}</span></div><Status value="approved" label="Active gift"/><button className="btn btn-small" onClick={()=>setRevoking(g.id)}>End gift</button></div>)}
    {revoking&&<form className="owner-editor stack" onSubmit={e=>{const f=formData(e);void run(async()=>{await post(`/admin/memberships/${revoking}/revoke`,{reason:f.get("reason")});setRevoking(null);await reload();});}}><h3>End this gift?</h3><p className="small">Unused gift allowance is removed. Paid purchases are unaffected.</p><label>Reason (shown in the account audit log)<input name="reason" minLength={10} maxLength={500} required defaultValue="Complimentary access ended by Hedvi owner"/></label><div className="row"><button className="btn btn-danger" disabled={pending}>End remaining allowance</button><button type="button" className="btn" disabled={pending} onClick={()=>setRevoking(null)}>Cancel</button></div></form>}
    {grants.some(g=>g.state!=="active")&&<><h3 className="owner-subheading">Gift history</h3><div className="owner-list">{grants.filter(g=>g.state!=="active").map(g=><div className="owner-list-row" key={g.id}><div className="owner-row-main"><strong>{plans[g.plan]??g.plan}</strong><span className="small muted">{g.revoke_reason||g.reason}</span></div><Status value={g.state} label={g.state==="revoked"?"Ended":"Expired"}/></div>)}</div></>}
  </>;
}
function Sending({projects,reload}:{projects:Project[];reload:()=>Promise<void>}) {
  const [selected,setSelected]=useState<Project|null>(null);
  const {pending,error,run}=useAction();
  if(!projects.length)return empty("No sending projects yet","Projects appear when an account is created. Domain verification activates sending automatically.");
  return <>{error&&<p role="alert" className="error">{error}</p>}<div className="owner-list">{projects.map(p=><div className="owner-list-row" key={p.id}><div className="owner-row-main"><strong>{p.organization} / {p.name}</strong><span className="small muted">{p.verified_domains} verified domains · {p.hourly_limit}/hour · {p.daily_limit}/day</span></div><Status value={p.state} label={p.state==="pending_review"||p.state==="test_only"?"Awaiting domain":undefined}/><button className="btn btn-small" onClick={()=>setSelected(p)}>Manage</button></div>)}</div>
    {selected&&<form key={selected.id} className="owner-editor stack" onSubmit={e=>{const button=(e.nativeEvent as SubmitEvent).submitter as HTMLButtonElement|null;const f=formData(e);void run(async()=>{await post(`/admin/projects/${selected.id}/decision`,{action:button?.value,hourly_limit:Number(f.get("hourly")),daily_limit:Number(f.get("daily")),reason:f.get("reason")});setSelected(null);await reload();});}}>
      <div className="owner-section-head"><h3>{selected.organization} / {selected.name}</h3><button type="button" className="btn btn-small" disabled={pending} onClick={()=>setSelected(null)}>Close</button></div>
      <p className="small muted">Normal activation is automatic. Use these controls for a sending restriction or a specific limit adjustment.</p>
      <div className="owner-editor-grid"><label>Recipients per hour<input name="hourly" type="number" min={1} max={50000} defaultValue={selected.hourly_limit||25} required/></label><label>Recipients per day<input name="daily" type="number" min={1} max={250000} defaultValue={selected.daily_limit||100} required/></label></div>
      <label>Reason (shown in the account audit log)<input name="reason" minLength={10} maxLength={500} required placeholder="Why are you making this change?"/></label><div className="row">{selected.state!=="test_only"&&selected.state!=="pending_review"&&<button className="btn btn-primary" value="approve" disabled={pending}>{selected.state==="paused"?"Resume sending":"Save limits"}</button>}<button className="btn btn-danger" value="pause" disabled={pending||selected.state==="paused"}>Pause sending</button></div>
    </form>}
  </>;
}
function Purchases({purchases}:{purchases:Purchase[]}) {
  if(!purchases.length)return empty("No purchases yet","Stripe purchases appear here after a customer starts Checkout.");
  const states:Record<string,string>={creating:"Preparing checkout",open:"Checkout open",paid:"Paid",active:"Subscription active",ended:"Ended",expired:"Checkout expired",review:"Needs review",reversed:"Reversed",payment_review:"Payment under review",past_due:"Payment overdue",payment_pending:"Confirming payment"};
  return <ScrollTable><thead><tr><th>Account</th><th>Product</th><th>Price before tax</th><th>Status</th><th>Created</th></tr></thead><tbody>{purchases.map(p=><tr key={p.id}><td>{p.organization}</td><td>{plans[p.sku]??p.sku}</td><td>{new Intl.NumberFormat("en",{style:"currency",currency:p.currency}).format(p.amount/100)}</td><td>{states[p.state]??p.state}</td><td className="small">{formatDate(p.created_at)}</td></tr>)}</tbody></ScrollTable>;
}
function GrantRows({grants,manage}:{grants:Grant[];manage:(o:Organization)=>void}) {
  if(!grants.length)return empty("No gifts yet","Give a membership from the customer’s account page.");
  return <div className="owner-list">{grants.map(g=><div className="owner-list-row" key={g.id}><div className="owner-row-main"><strong>{g.organization}</strong><span className="small muted">{plans[g.plan]??g.plan} · {g.state==="revoked"?"Ended":g.state} · {formatDate(g.ends_at)}</span></div><button className="btn btn-small" onClick={()=>manage({id:g.org_id,name:g.organization})}>Open account</button></div>)}</div>;
}
function Errors({errors}:{errors:Incident[]}) {
  const [severity,setSeverity]=useState("all");
  const shown=errors.filter(e=>severity==="all"||e.severity===severity);
  return <><div className="owner-section-head"><div><h2>Errors and alerts</h2><p className="small muted">Latest 50 operational events. Customer message content and credentials are excluded.</p></div><label>Severity<select value={severity} onChange={e=>setSeverity(e.target.value)}><option value="all">All events</option><option value="critical">Critical</option><option value="warning">Warnings</option><option value="info">Information</option></select></label></div>
    {!shown.length?empty("No matching events","There are no errors or alerts in this view."):<ScrollTable><thead><tr><th>Event</th><th>Severity</th><th>Time</th><th>Reference</th></tr></thead><tbody>{shown.map(e=><tr key={e.id+e.category}><td>{e.category}</td><td><Status value={e.severity==="critical"?"failed":e.severity==="warning"?"pending":"test_only"} label={e.severity}/></td><td className="small nowrap">{formatDate(e.created_at)}</td><td><code className="tiny">{e.id}</code></td></tr>)}</tbody></ScrollTable>}
  </>;
}
