import { publicMetadata } from "@/lib/seo";
import { LegalDocument } from "@/components/LegalDocument";
import document from "@/content/legal/privacy.json";

export const metadata = publicMetadata("/legal/privacy");
export default function Page() { return <LegalDocument document={document} />; }
