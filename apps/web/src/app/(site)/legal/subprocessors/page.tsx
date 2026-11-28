import { publicMetadata } from "@/lib/seo";
import { LegalDocument } from "@/components/LegalDocument";
import document from "@/content/legal/subprocessors.json";

export const metadata = publicMetadata("/legal/subprocessors");
export default function Page() { return <LegalDocument document={document} />; }
