import Link from "next/link";
import { BuildStatus, SiteFooter, SiteHeader } from "@/components/Site";
import { dict } from "@/lib/i18n";
import { docs } from "./content";
import { ResponsiveNav } from "@/components/ResponsiveNav";

export default function DocsLayout({ children }: { children: React.ReactNode }) {
  const t = dict.en;
  return (
    <>
      <SiteHeader t={t} />
      <BuildStatus text={t.status} />
      <div className="container docs">
        <ResponsiveNav id="docs-navigation" label="Documentation" className="docs-navigation">
          <Link href="/docs"><strong>Overview</strong></Link>
          {docs.map((d) => <Link key={d.slug} href={`/docs/${d.slug}`}>{d.title}</Link>)}
        </ResponsiveNav>
        <article id="main">{children}</article>
      </div>
      <SiteFooter t={t} />
    </>
  );
}
