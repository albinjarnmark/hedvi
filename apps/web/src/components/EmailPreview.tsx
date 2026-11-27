"use client";
import { useMemo, useState } from "react";
import { bytes } from "@/lib/api";
import { sandboxDoc } from "@/lib/email-preview";

export type Content = {
  html: string;
  text: string;
  headers: { name: string; value: string }[] | null;
  attachments: { index: number; filename: string; content_type: string; content_id: string; size: number }[];
  links: string[] | null;
  raw_headers: string;
};

export function EmailPreview({ content, attachmentHref }: { content: Content; attachmentHref: (i: number) => string }) {
  const [tab, setTab] = useState<"html" | "text" | "headers">(content.html ? "html" : "text");
  const [mobile, setMobile] = useState(false);
  const [remote, setRemote] = useState(false);
  const doc = useMemo(() => sandboxDoc(content.html, remote), [content.html, remote]);
  return (
    <div className="stack">
      <div className="row between">
        <div className="row" role="tablist" aria-label="Email content">
          {(["html", "text", "headers"] as const).map((t) => (
            <button key={t} role="tab" aria-selected={tab === t} className={`btn btn-small ${tab === t ? "btn-primary" : ""}`} onClick={() => setTab(t)}
              disabled={t === "html" && !content.html}>{t === "html" ? "HTML" : t === "text" ? "Text" : "Headers"}</button>
          ))}
        </div>
        {tab === "html" && (
          <div className="row small">
            <label className="row" style={{ fontWeight: 400, margin: 0 }}><input type="checkbox" checked={mobile} onChange={(e) => setMobile(e.target.checked)} /> Mobile width</label>
            <label className="row" style={{ fontWeight: 400, margin: 0 }}><input type="checkbox" checked={remote} onChange={(e) => setRemote(e.target.checked)} /> Load remote images</label>
          </div>
        )}
      </div>
      {tab === "html" && <iframe title="Email preview" className={`preview-frame ${mobile ? "mobile" : ""}`} sandbox="" srcDoc={doc} referrerPolicy="no-referrer" />}
      {tab === "text" && <pre className="code" style={{ whiteSpace: "pre-wrap" }}>{content.text || "(no text part)"}</pre>}
      {tab === "headers" && <pre className="code" style={{ whiteSpace: "pre-wrap" }}>{content.raw_headers}</pre>}
      {tab === "html" && !remote && <p className="tiny muted">Remote images and resource hints are blocked. Loading remote images can reveal your IP address and that you opened this preview.</p>}
      {(content.links?.length ?? 0) > 0 && (
        <details>
          <summary className="small"><strong>{content.links!.length} links</strong> (not opened by the preview)</summary>
          <ul className="small break">{content.links!.map((l) => <li key={l}><code>{l}</code></li>)}</ul>
        </details>
      )}
      {content.attachments.length > 0 && (
        <div>
          <h3>Attachments</h3>
          <ul className="small">
            {content.attachments.map((a) => (
              <li key={a.index}>
                <a href={attachmentHref(a.index)} download>{a.filename}</a> · {a.content_type} · {bytes(a.size)}{a.content_id ? ` · inline (cid:${a.content_id})` : ""}
              </li>
            ))}
          </ul>
          <p className="tiny muted">Attachments always download; they are never opened in the browser.</p>
        </div>
      )}
    </div>
  );
}
