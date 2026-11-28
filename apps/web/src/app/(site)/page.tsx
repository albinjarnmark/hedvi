import type { Metadata } from "next";
import { SiteFooter, SiteHeader } from "@/components/Site";
import { Home } from "@/components/Marketing";
import { dict } from "@/lib/i18n";
import { publicMetadata, publicPages, siteOrigin } from "@/lib/seo";

const { title, description } = publicPages["/"];
const shareTitle = title + " · Hedvi";
export const metadata: Metadata = {
  ...publicMetadata("/"),
  openGraph: {
    type: "website", siteName: "Hedvi", url: siteOrigin, title: shareTitle, description,
    images: [{ url: "/hedvi-share.png", width: 1200, height: 630, alt: "Hedvi — Email, without the overhead." }],
  },
  twitter: { card: "summary_large_image", title: shareTitle, description, images: ["/hedvi-share.png"] },
};

const structuredData = {
  "@context": "https://schema.org",
  "@graph": [
    { "@type": "Organization", "@id": siteOrigin + "/#publisher", name: "StackX AB" },
    { "@type": "WebSite", "@id": siteOrigin + "/#website", name: "Hedvi", url: siteOrigin,
      description, inLanguage: "en", publisher: { "@id": siteOrigin + "/#publisher" } },
  ],
};

export default function Page() {
  const t = dict.en;
  return (
    <>
      <script type="application/ld+json" dangerouslySetInnerHTML={{ __html: JSON.stringify(structuredData).replace(/</g, "\\u003c") }} />
      <SiteHeader t={t} />
      <main id="main"><Home t={t} signup="/signup" /></main>
      <SiteFooter t={t} />
    </>
  );
}
