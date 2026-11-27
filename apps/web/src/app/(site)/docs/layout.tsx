import Link from "next/link";
import { BuildStatus, SiteFooter, SiteHeader } from "@/components/Site";
import { dict } from "@/lib/i18n";
import { docs } from "./content";

export default function DocsLayout({ children }: { children: React.ReactNode }) {
  const t = dict.en;
  return (
    <>
      <SiteHeader t={t} />
      <BuildStatus text={t.status} />
      <div className="container docs">
        <nav aria-label="Documentation">
          <Link href="/docs"><strong>Overview</strong></Link>
          {docs.map((d) => <Link key={d.slug} href={`/docs/${d.slug}`}>{d.title}</Link>)}
        </nav>
        <article id="main">{children}</article>
      </div>
      <SiteFooter t={t} />
    </>
  );
}
