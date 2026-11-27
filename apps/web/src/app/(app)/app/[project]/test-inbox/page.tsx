"use client";
import { useSearchParams } from "next/navigation";
import { Suspense, useState } from "react";
import { useMe, useProjectId } from "@/components/AppShell";
import { CodeTabs } from "@/components/CodeTabs";
import { sendSnippets } from "@/lib/snippets";
import { CopyButton } from "@/components/CopyButton";
import { EmailTable, type EmailSummary } from "@/components/EmailList";
import { api, bytes } from "@/lib/api";
import { useAction, useLoad } from "@/lib/hooks";

type Inbox = { data: EmailSummary[]; limits: { storage_used: number; storage_limit: number; today: number; daily_limit: number; retention_days: number } };

function OnboardingKey({ pid }: { pid: string }) {
  const welcome = useSearchParams().get("welcome") === "1";
  const storageKey = `hedvi:onboarding-key:${pid}`;
  const [key, setKey] = useState(() => (typeof window === "undefined" ? null : sessionStorage.getItem(storageKey)));
  const me = useMe();
  if (!welcome && !key) return null;
  const shown = key ?? "hv_test_…";
  return (
    <div className="card" style={{ marginBottom: 20 }}>
      <h2>Your test key</h2>
      {key ? (
        <>
          <p className="small">Copy it now — Hedvi only stores a hash and cannot show it again.</p>
          <div className="row"><div className="secret" style={{ flex: 1 }}>{key}</div><CopyButton value={key} /></div>
          <p><button className="linklike small" onClick={() => { sessionStorage.removeItem(storageKey); setKey(null); }}>I have saved it</button></p>
        </>
      ) : <p className="small">Create a key under Developers.</p>}
      <p style={{ marginTop: 12 }}>Send a request — it appears below within seconds:</p>
      <CodeTabs snippets={sendSnippets(me.environment.api_url, shown, "Acme <onboarding@example.com>", me.user.email)} />
    </div>
  );
}

export default function TestInboxPage() {
  const pid = useProjectId();
  const { data, reload } = useLoad<Inbox>(`/projects/${pid}/test-inbox`, 2000);
  const { pending, error, run } = useAction();
  return (
    <>
      <div className="page-head">
        <div><h1>Test inbox</h1><p className="muted small">Captured test emails. Nothing here was delivered to an external mailbox.</p></div>
        <div className="row">
          <button className="btn btn-primary" disabled={pending} onClick={() => run(async () => { await api(`/projects/${pid}/test-inbox/sample`, { method: "POST" }); await reload(); })}>Send a sample</button>
          <button className="btn btn-danger" disabled={pending || !data?.data.length} onClick={() => run(async () => {
            if (!confirm("Delete all test emails in this project?")) return;
            await api(`/projects/${pid}/test-inbox`, { method: "DELETE" });
            await reload();
          })}>Delete all</button>
        </div>
      </div>
      <Suspense><OnboardingKey pid={pid} /></Suspense>
      {error && <p className="error" role="alert">{error}</p>}
      {data && (
        <>
          <p className="small muted" aria-live="polite">
            {data.limits.today} of {data.limits.daily_limit} today · {bytes(data.limits.storage_used)} of {bytes(data.limits.storage_limit)} stored · kept {data.limits.retention_days} days
          </p>
          <EmailTable emails={data.data} hrefBase={`/app/${pid}/emails`} empty={<>No test emails yet. Use the example above or <strong>Send a sample</strong>.</>} />
        </>
      )}
    </>
  );
}
