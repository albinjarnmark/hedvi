import { publicMetadata } from "@/lib/seo";
import { LegalDocument } from "@/components/LegalDocument";
import document from "@/content/legal/terms.json";

export const metadata = publicMetadata("/legal/terms");
export default function Page() { return <LegalDocument document={document} />; }
