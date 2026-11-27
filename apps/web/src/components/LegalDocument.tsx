import Link from "next/link";
import { SiteFooter, SiteHeader } from "@/components/Site";
import { dict } from "@/lib/i18n";

export type LegalContent = {
  id: string;
  title: string;
  version: string;
  publishedAt: string;
  introduction: string;
  sections: { heading: string; paragraphs: string[]; items?: string[]; links?: {title: string; url: string}[] }[];
  sources?: { title: string; url: string }[];
};

export function LegalDocument({ document, manifestVersion }: { document: LegalContent; manifestVersion?: string }) {
  const base = manifestVersion ? `/legal/versions/${manifestVersion}` : "/legal";
  return <>
    <SiteHeader t={dict.en} />
    <main id="main" className="container" style={{ maxWidth: 860, paddingBlock: 64 }}>
      <nav aria-label="Legal documents" className="row small" style={{ flexWrap: "wrap", marginBottom: 32 }}>
        <Link href={`${base}/terms`}>Terms</Link><Link href={`${base}/privacy`}>Privacy</Link>
        <Link href={`${base}/dpa`}>Data processing agreement</Link><Link href={`${base}/acceptable-use`}>Acceptable use</Link>
        <Link href={`${base}/subprocessors`}>Service providers</Link><Link href={`${base}/cookies`}>Cookies</Link>
      </nav>
      <h1>{document.title}</h1>
      <p className="muted">Version {document.version} · Published {document.publishedAt}{manifestVersion && <> · Document bundle {manifestVersion}</>}</p>
      <p>{document.introduction}</p>
      {document.sections.map((section, index) => <section key={section.heading} aria-labelledby={`legal-section-${index}`}>
        <h2 id={`legal-section-${index}`}>{section.heading}</h2>
        {section.paragraphs.map((paragraph, i) => <p key={i}>{paragraph}</p>)}
        {section.links && <p className="small">{section.links.map((link, i) => <span key={link.url}>{i > 0 && " · "}<a href={link.url} rel="noopener noreferrer">{link.title}</a></span>)}</p>}
        {section.items && <ul>{section.items.map(item => <li key={item}>{item}</li>)}</ul>}
      </section>)}
      {document.sources && <section><h2>Related information</h2><ul>{document.sources.map(source => <li key={source.url}><a href={source.url} rel="noopener noreferrer">{source.title}</a></li>)}</ul></section>}
      <p>Contact StackX AB: <a href="mailto:hej@stackx.se">hej@stackx.se</a></p>
    </main>
    <SiteFooter t={dict.en} />
  </>;
}
