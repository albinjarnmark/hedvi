import { publicMetadata } from "@/lib/seo";
import { LegalDocument } from "@/components/LegalDocument";
import document from "@/content/legal/acceptable-use.json";

export const metadata = publicMetadata("/legal/acceptable-use");
export default function Page() { return <LegalDocument document={document} />; }
