import { publicMetadata } from "@/lib/seo";
import { LegalDocument } from "@/components/LegalDocument";
import document from "@/content/legal/cookies.json";

export const metadata = publicMetadata("/legal/cookies");
export default function Page() { return <LegalDocument document={document} />; }
