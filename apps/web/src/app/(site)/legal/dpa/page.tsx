import { publicMetadata } from "@/lib/seo";
import { LegalDocument } from "@/components/LegalDocument";
import document from "@/content/legal/dpa.json";

export const metadata = publicMetadata("/legal/dpa");
export default function Page() { return <LegalDocument document={document} />; }
