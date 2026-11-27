"use client";
import Link from "next/link";
import { formatDate } from "@/lib/api";
import { Status } from "./Status";

export type EmailSummary = {
  id: string; mode: string; from: string; to: string[]; subject: string | null; last_event: string; created_at: string;
  content_available: boolean; attachments: number;
};

export function EmailTable({ emails, hrefBase, empty }: { emails: EmailSummary[]; hrefBase: string; empty: React.ReactNode }) {
  if (emails.length === 0) return <div className="card muted">{empty}</div>;
  return (
    <table className="table">
      <thead><tr><th>Status</th><th>To</th><th>Subject</th><th>Created</th></tr></thead>
      <tbody>
        {emails.map((e) => (
          <tr key={e.id}>
            <td><Status value={e.last_event} /></td>
            <td className="break">{e.to.join(", ")}</td>
            <td><Link href={`${hrefBase}/${e.id}`}>{e.subject ?? <em className="muted">content deleted</em>}</Link></td>
            <td className="nowrap small">{formatDate(e.created_at)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
