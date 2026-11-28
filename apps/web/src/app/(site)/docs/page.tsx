import { supportEmail } from "@/lib/public-site";
import Link from "next/link";
import { publicMetadata } from "@/lib/seo";
import { docs } from "./content";

export const metadata = publicMetadata("/docs");

export default function DocsIndex() {
  return (
    <>
      <h1>Documentation</h1>
      <p className="muted">Verify your account, send a test email, connect your domain and manage billing. These guides cover Hedvi&apos;s API and dashboard. Documentation is in English; Swedish support is available at <a href={`mailto:${supportEmail}`}>{supportEmail}</a>.</p>
      <div className="grid grid-2" style={{ marginTop: 24 }}>
        {docs.map((d) => (
          <Link key={d.slug} href={`/docs/${d.slug}`} className="card" style={{ textDecoration: "none" }}>
            <h3>{d.title}</h3>
          </Link>
        ))}
      </div>
    </>
  );
}
