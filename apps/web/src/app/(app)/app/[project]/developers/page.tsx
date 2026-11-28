"use client";
import { AgentSetup } from "@/components/AgentSetup";
import { ScrollTable } from "@/components/ScrollTable";
import { useState } from "react";
import { useMe, useProjectId } from "@/components/AppShell";
import { CodeTabs } from "@/components/CodeTabs";
import { sendSnippets } from "@/lib/snippets";
import { CopyButton } from "@/components/CopyButton";
import { Status } from "@/components/Status";
import { api, formatDate } from "@/lib/api";
import { useAction, useLoad } from "@/lib/hooks";

type Key = { id: string; name: string; mode: string; permission: string; domain: string | null; hint: string; created_at: string; last_used_at: string | null; revoked_at: string | null };
type Hook = { id: string; url: string; events: string[]; status: string; failed_7d: number; succeeded_7d: number };
type Delivery = { id: string; event_id: string; type: string; state: string; attempts: number; last_status: number | null; last_error: string; last_response: string; created_at: string; next_attempt_at: string };

function Secret({ value, onClose }: { value: string; onClose: () => void }) {
  return (
    <div className="notice notice-lime stack" role="status">
      <p><strong>Copy this now.</strong> It is shown once.</p>
      <div className="row"><div className="secret" style={{ flex: 1 }}>{value}</div><CopyButton value={value} /></div>
      <button className="linklike small" onClick={onClose}>Done</button>
    </div>
  );
}

function Deliveries({ pid, hook }: { pid: string; hook: Hook }) {
  const { data, reload } = useLoad<{ data: Delivery[] }>(`/projects/${pid}/webhooks/${hook.id}/deliveries`, 5000);
  const { run } = useAction();
  return (
    <ScrollTable>
      <thead><tr><th>Event</th><th>State</th><th>Last response</th><th></th></tr></thead>
      <tbody>
        {(data?.data ?? []).map((d) => (
          <tr key={d.id}>
            <td className="small"><code>{d.type}</code><div className="tiny muted mono">{d.event_id}</div><div className="tiny muted">{formatDate(d.created_at)}</div></td>
            <td><Status value={d.state === "pending" ? "retry_wait" : d.state === "failed" ? "failed" : d.state === "succeeded" ? "succeeded" : "queued"} label={d.state} /><div className="tiny muted">{d.attempts} attempts</div></td>
            <td className="small break">{d.last_status ?? "—"} {d.last_error}<div className="tiny muted">{d.last_response.slice(0, 160)}</div></td>
            <td><button className="btn btn-small" onClick={() => run(async () => { await api(`/projects/${pid}/webhooks/${hook.id}/deliveries/${d.id}/resend`, { method: "POST" }); await reload(); })}>Resend event</button></td>
          </tr>
        ))}
      </tbody>
    </ScrollTable>
  );
}

export default function DevelopersPage() {
  const pid = useProjectId();
  const me = useMe();
  const keys = useLoad<{ data: Key[] }>(`/projects/${pid}/api-keys`);
  const hooks = useLoad<{ data: Hook[]; event_types: string[] }>(`/projects/${pid}/webhooks`, 10000);
  const domains = useLoad<{ data: { id: string; name: string; status: string }[] }>(`/projects/${pid}/domains`);
  const [secret, setSecret] = useState<string | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const { pending, error, run } = useAction();
  return (
    <>
      <div className="page-head"><div><h1>Developers</h1><p className="muted small">API keys are scoped to this project and stored hashed.</p></div></div>
      {secret && <Secret value={secret} onClose={() => setSecret(null)} />}
      {error && <p className="error" role="alert">{error}</p>}
      <AgentSetup />
      <section className="card stack" style={{ marginTop: 20 }}>
        <h2>API keys</h2>
        <form className="row" onSubmit={(e) => {
          e.preventDefault();
          const f = new FormData(e.currentTarget);
          void run(async () => {
            const res = await api<{ key: string }>(`/projects/${pid}/api-keys`, { method: "POST",
              body: { name: f.get("name"), mode: f.get("mode"), permission: f.get("permission"), domain_id: f.get("domain") || null } });
            setSecret(res.key);
            await keys.reload();
          });
        }}>
          <div style={{ flex: 2, minWidth: 180 }}><label htmlFor="kname">Name</label><input id="kname" name="name" required placeholder="Production server" /></div>
          <div><label htmlFor="kmode">Mode</label><select id="kmode" name="mode"><option value="test">Test</option><option value="live">Live</option></select></div>
          <div><label htmlFor="kperm">Permission</label><select id="kperm" name="permission"><option value="sending_access">Sending only</option><option value="full_access">Full access</option></select></div>
          <div><label htmlFor="kdom">Domain</label><select id="kdom" name="domain"><option value="">Any verified</option>{(domains.data?.data ?? []).map((d) => <option key={d.id} value={d.id}>{d.name}</option>)}</select></div>
          <button className="btn btn-primary" disabled={pending} style={{ alignSelf: "flex-end" }}>Create key</button>
        </form>
        <ScrollTable>
          <thead><tr><th>Name</th><th>Key</th><th>Mode</th><th>Last used</th><th></th></tr></thead>
          <tbody>
            {(keys.data?.data ?? []).map((k) => (
              <tr key={k.id}>
                <td>{k.name}<div className="tiny muted">{k.permission}{k.domain ? ` · ${k.domain}` : ""}</div></td>
                <td className="mono small">{k.hint}</td>
                <td><Status value={k.mode === "live" ? "approved" : "test_only"} label={k.mode} /></td>
                <td className="small">{k.revoked_at ? `Revoked ${formatDate(k.revoked_at)}` : formatDate(k.last_used_at)}</td>
                <td>{!k.revoked_at && <button className="btn btn-small btn-danger" onClick={() => run(async () => {
                  if (!confirm(`Revoke "${k.name}"? Requests using it fail immediately.`)) return;
                  await api(`/projects/${pid}/api-keys/${k.id}/revoke`, { method: "POST" });
                  await keys.reload();
                })}>Revoke</button>}</td>
              </tr>
            ))}
          </tbody>
        </ScrollTable>
      </section>
      <section className="card stack" style={{ marginTop: 20 }}>
        <h2>Send with your SDK</h2>
        <CodeTabs snippets={sendSnippets(me.environment.api_url, "hv_live_…", "App <app@yourdomain.com>", "customer@example.com")} />
      </section>
      <section className="card stack" style={{ marginTop: 20 }}>
        <h2>Webhooks</h2>
        <p className="small muted">Signed with Standard Webhooks (compatible with <code>resend.webhooks.verify</code>). Delivered at least once; deduplicate on <code>webhook-id</code>.</p>
        <form className="row" onSubmit={(e) => {
          e.preventDefault();
          const f = new FormData(e.currentTarget);
          void run(async () => {
            const res = await api<{ signing_secret: string }>(`/projects/${pid}/webhooks`, { method: "POST", body: { url: f.get("url") } });
            setSecret(res.signing_secret);
            await hooks.reload();
          });
        }}>
          <div style={{ flex: 1, minWidth: 260 }}><label htmlFor="wurl">Endpoint URL (HTTPS)</label><input id="wurl" name="url" type="url" required placeholder="https://example.com/webhooks/hedvi" /></div>
          <button className="btn btn-primary" disabled={pending} style={{ alignSelf: "flex-end" }}>Add endpoint</button>
        </form>
        {(hooks.data?.data ?? []).map((h) => (
          <div key={h.id} className="card stack">
            <div className="row between">
              <div><strong className="break">{h.url}</strong><div className="tiny muted">{h.events.join(", ")}</div></div>
              <div className="row">
                <Status value={h.status === "enabled" ? "approved" : "paused"} label={h.status} />
                <span className="tiny muted">7 d: {h.succeeded_7d} ok · {h.failed_7d} failed</span>
              </div>
            </div>
            <div className="row">
              <button className="btn btn-small" onClick={() => run(async () => { await api(`/projects/${pid}/webhooks/${h.id}/test`, { method: "POST" }); setOpen(h.id); })}>Send test event</button>
              <button className="btn btn-small" onClick={() => setOpen(open === h.id ? null : h.id)}>{open === h.id ? "Hide deliveries" : "Deliveries"}</button>
              <button className="btn btn-small" onClick={() => run(async () => { const r = await api<{ signing_secret: string }>(`/projects/${pid}/webhooks/${h.id}/secret`, { method: "POST" }); setSecret(r.signing_secret); })}>Reveal secret</button>
              <button className="btn btn-small" onClick={() => run(async () => {
                if (!confirm("Rotate the signing secret? Update your receiver right away.")) return;
                const r = await api<{ signing_secret: string }>(`/projects/${pid}/webhooks/${h.id}/rotate-secret`, { method: "POST" }); setSecret(r.signing_secret);
              })}>Rotate secret</button>
              <button className="btn btn-small" onClick={() => run(async () => { await api(`/projects/${pid}/webhooks/${h.id}`, { method: "PATCH", body: { status: h.status === "enabled" ? "disabled" : "enabled" } }); await hooks.reload(); })}>{h.status === "enabled" ? "Disable" : "Enable"}</button>
              <button className="btn btn-small btn-danger" onClick={() => run(async () => { if (!confirm("Delete this endpoint?")) return; await api(`/projects/${pid}/webhooks/${h.id}`, { method: "DELETE" }); await hooks.reload(); })}>Delete</button>
            </div>
            {open === h.id && <Deliveries pid={pid} hook={h} />}
          </div>
        ))}
      </section>
    </>
  );
}
