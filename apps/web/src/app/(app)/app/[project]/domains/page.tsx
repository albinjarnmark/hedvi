"use client";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useMe, useProjectId } from "@/components/AppShell";
import { Status } from "@/components/Status";
import { api, formatDate } from "@/lib/api";
import { useAction, useLoad } from "@/lib/hooks";

type Domain = { id: string; name: string; status: string; created_at: string; hedvi?: { last_checked_at: string | null } };

export default function DomainsPage() {
  const pid = useProjectId();
  const me = useMe();
  const organization = me.organizations.find((org) => org.projects.some((project) => project.id === pid));
  const router = useRouter();
  const { data } = useLoad<{ data: Domain[] }>(`/projects/${pid}/domains`, 10000);
  const { pending, error, run } = useAction();
  return (
    <>
      <div className="page-head"><div><h1>Domains</h1><p className="muted small">Send from your own domain. Hedvi never changes your existing SPF or DMARC.</p></div></div>
      {organization?.plan === "free" && <p className="notice small">Free includes 3 sending domains across your organization. Add each domain here and verify its DNS records. No paid plan is required.</p>}
      <form className="card row" onSubmit={(e) => {
        e.preventDefault();
        const f = new FormData(e.currentTarget);
        void run(async () => {
          const d = await api<Domain>(`/projects/${pid}/domains`, { method: "POST", body: { name: f.get("name"), mail_from_subdomain: f.get("mailfrom") || "" } });
          router.push(`/app/${pid}/domains/${d.id}`);
        });
      }}>
        <div style={{ flex: 2, minWidth: 220 }}><label htmlFor="name">Domain</label><input id="name" name="name" placeholder="example.com or mail.example.com" required /></div>
        <div style={{ flex: 1, minWidth: 140 }}><label htmlFor="mailfrom">MAIL FROM subdomain</label><input id="mailfrom" name="mailfrom" placeholder="send" aria-describedby="mailfrom-help" /><small id="mailfrom-help">Choose a name unused by your mailbox or other sending services.</small></div>
        <button className="btn btn-primary" disabled={pending} style={{ alignSelf: "flex-end" }}>Add domain</button>
      </form>
      {error && <p className="error" role="alert">{error}</p>}
      <div className="table-scroll" tabIndex={0} role="region" aria-label="Sending domains">
      <table className="table" style={{ marginTop: 20 }}>
        <thead><tr><th>Domain</th><th>Status</th><th>Last checked</th></tr></thead>
        <tbody>
          {(data?.data ?? []).map((d) => (
            <tr key={d.id}>
              <td><Link href={`/app/${pid}/domains/${d.id}`}>{d.name}</Link></td>
              <td><Status value={d.status} /></td>
              <td className="small">{formatDate(d.hedvi?.last_checked_at)}</td>
            </tr>
          ))}
        </tbody>
      </table>
      </div>
      {data && data.data.length === 0 && <p className="muted">No domains yet. Test mode works without one.</p>}
    </>
  );
}
