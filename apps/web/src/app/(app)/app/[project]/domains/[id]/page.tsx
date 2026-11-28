"use client";
import { ScrollTable } from "@/components/ScrollTable";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";
import { useProjectId } from "@/components/AppShell";
import { CopyButton } from "@/components/CopyButton";
import { Status } from "@/components/Status";
import { api, formatDate } from "@/lib/api";
import { useAction, useLoad } from "@/lib/hooks";

type Record = { record: string; name: string; fqdn: string; type: string; value: string; priority?: number; status: string; purpose: string; found?: string[] };
type Domain = {
  id: string; name: string; status: string; records: Record[];
  hedvi: { analysis: { ns_provider: string; provider_tips?: string[]; root_spf?: string[]; dmarc?: string; findings: { level: string; code: string; message: string }[] };
    last_checked_at: string | null; next_check_at: string | null; mail_from_domain: string };
};

export default function DomainPage() {
  const pid = useProjectId();
  const { id } = useParams<{ id: string }>();
  const router = useRouter();
  const { data, reload, error } = useLoad<Domain>(`/projects/${pid}/domains/${id}`, 10000);
  const { pending, error: actionError, run } = useAction();
  if (error) return <p className="error">{error.message}</p>;
  if (!data) return <p className="muted">Loading…</p>;
  const a = data.hedvi.analysis;
  return (
    <>
      <p className="small"><Link href={`/app/${pid}/domains`}>← Domains</Link></p>
      <div className="page-head">
        <div><h1>{data.name}</h1><p className="small muted">Last checked {formatDate(data.hedvi.last_checked_at)} · next check {formatDate(data.hedvi.next_check_at)}</p></div>
        <div className="row">
          <Status value={data.status} />
          <button className="btn btn-primary btn-small" disabled={pending} onClick={() => run(async () => { await api(`/projects/${pid}/domains/${id}/verify`, { method: "POST" }); await reload(); })}>Check now</button>
          <button className="btn btn-danger btn-small" disabled={pending} onClick={() => run(async () => {
            if (!confirm(`Remove ${data.name}? Live sending from it stops.`)) return;
            await api(`/projects/${pid}/domains/${id}`, { method: "DELETE" });
            router.push(`/app/${pid}/domains`);
          })}>Remove</button>
        </div>
      </div>
      {actionError && <p className="error" role="alert">{actionError}</p>}
      <div className="card">
        <h2>DNS provider: {a.ns_provider || "Unidentified"}</h2>
        <p className="small muted">Detected from your nameservers as a hint — your records may be managed elsewhere.</p>
        {(a.provider_tips ?? []).map((tip) => <p key={tip} className="small">• {tip}</p>)}
      </div>
      <h2 style={{ marginTop: 24 }}>Records to publish</h2>
      <p className="small muted">First publish the ownership TXT record and choose Check now. New claims expire after 72 hours. After your project has live-sending approval, another check generates the DKIM and MAIL FROM records. Publish those and check again. Keep the ownership record in DNS.</p>
      <ScrollTable>
        <thead><tr><th>Type</th><th>Name (host)</th><th>Value</th><th>Status</th></tr></thead>
        <tbody>
          {data.records.map((r) => (
            <tr key={r.fqdn + r.type + r.value}>
              <td><strong>{r.type}</strong><div className="tiny muted">{r.record}</div></td>
              <td className="dns-value">{r.fqdn} <CopyButton value={r.fqdn} /><div className="tiny muted">Relative to the sending domain: {r.name}. If your DNS provider requires a relative name, remove only the actual DNS zone suffix.</div></td>
              <td className="dns-value">{r.priority !== undefined ? `${r.priority} ` : ""}{r.value} <CopyButton value={r.value} />
                <div className="tiny muted" style={{ fontFamily: "var(--font)" }}>{r.purpose}</div>
                {r.status !== "verified" && r.found && r.found.length > 0 && <div className="tiny error">Found instead: {r.found.join(", ")}</div>}
              </td>
              <td><Status value={r.status} /></td>
            </tr>
          ))}
        </tbody>
      </ScrollTable>
      <h2 style={{ marginTop: 24 }}>What we found in your DNS</h2>
      <div className="stack">
        {a.findings.map((f) => (
          <p key={f.code} className={`notice ${f.level === "error" ? "notice-bad" : f.level === "warning" ? "notice-warn" : ""}`}>{f.message}</p>
        ))}
        {a.root_spf && a.root_spf.length > 0 && <p className="small">Existing SPF on {data.name}: <code>{a.root_spf.join(" | ")}</code></p>}
        {a.dmarc && <p className="small">Existing DMARC: <code>{a.dmarc}</code></p>}
      </div>
    </>
  );
}
