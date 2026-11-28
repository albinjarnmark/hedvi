"use client";
import Link from "next/link";
import { useParams, usePathname, useRouter } from "next/navigation";
import { createContext, useContext, useEffect } from "react";
import { api } from "@/lib/api";
import { useLoad } from "@/lib/hooks";
import { Logo } from "./Logo";
import { sourceUrl } from "@/lib/public-site";
import { ResponsiveNav } from "./ResponsiveNav";

export type Me = {
  user: { id: string; email: string; name: string; locale: string; mfa_enabled: boolean; is_staff: boolean; session_mfa: boolean; legal_accepted: boolean };
  organizations: { id: string; name: string; role: string; plan: string; currency: string; projects: { id: string; name: string; sending_status: string }[] }[];
  environment: { aws_mode: string; env: string; api_url: string };
};

const MeContext = createContext<Me | null>(null);
export const useMe = () => useContext(MeContext)!;

export function useProjectId(): string {
  const p = useParams<{ project?: string }>();
  return p.project ?? "";
}

const nav = [
  { href: "", label: "Overview" },
  { href: "/emails", label: "Emails" },
  { href: "/test-inbox", label: "Test inbox" },
  { href: "/domains", label: "Domains" },
  { href: "/developers", label: "Developers" },
  { href: "/settings", label: "Settings" },
];

export function AppShell({ children }: { children: React.ReactNode }) {
  const { data: me, error } = useLoad<Me>("/me");
  const router = useRouter();
  const pathname = usePathname();
  const projectId = useProjectId();
  useEffect(() => {
    if (error?.status === 401) router.replace("/login");
    if (me && !me.user.legal_accepted) router.replace("/legal/accept");
  }, [error, router, me]);
  if (!me || !me.user.legal_accepted) {
    return <main className="main" aria-busy="true">{error && error.status !== 401 ? <p className="error">{error.message}</p> : <p className="muted">Loading…</p>}</main>;
  }
  const projects = me.organizations.flatMap((o) => o.projects.map((p) => ({ ...p, org: o.name })));
  const base = `/app/${projectId}`;
  return (
    <MeContext.Provider value={me}>
      <div className="shell">
        <a className="skip" href="#main">Skip to content</a>
        <aside className="sidebar">
          <ResponsiveNav id="dashboard-navigation" label="Dashboard" className="dashboard-navigation"
            header={<Link href="/app" aria-label="Hedvi dashboard" className="dashboard-logo"><Logo variant="cream" height={26} /></Link>}>
          <label htmlFor="project" className="tiny" style={{ padding: "0 4px" }}>Project</label>
          <select id="project" value={projectId} onChange={(e) => router.push(`/app/${e.target.value}`)}>
            {projects.map((p) => <option key={p.id} value={p.id}>{p.org} / {p.name}</option>)}
          </select>
          {projectId && nav.map((n) => {
            const href = base + n.href;
            const current = n.href === "" ? pathname === base : pathname.startsWith(href);
            return <Link key={n.label} href={href} aria-current={current ? "page" : undefined}>{n.label}</Link>;
          })}
          {me.user.is_staff && me.user.mfa_enabled && me.user.session_mfa && <Link href="/app/admin" aria-current={pathname.startsWith("/app/admin") ? "page" : undefined}>Owner console</Link>}
          <div className="spacer" />
          <a href="https://status.hedvi.dev" className="tiny">Service status</a>
          <a href={sourceUrl} className="tiny">Source · AGPL-3.0</a>
          {me.environment.aws_mode === "mock" && <p className="tiny" style={{ color: "var(--lime)", padding: "0 8px" }}>Local simulator: nothing leaves this machine.</p>}
          <p className="tiny" style={{ padding: "0 8px", color: "var(--text-3)" }}>{me.user.email}</p>
          <button className="btn btn-small" style={{ margin: "4px 4px 0" }}
            onClick={async () => { await api("/auth/logout", { method: "POST" }); router.push("/login"); }}>Sign out</button>
          </ResponsiveNav>
        </aside>
        <main className="main" id="main">{children}</main>
      </div>
    </MeContext.Provider>
  );
}
