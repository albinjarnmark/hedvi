import type { Metadata } from "next";
import { LegalDocument } from "@/components/LegalDocument";
import document from "@/content/legal/dpa.json";

export const metadata: Metadata = { title: document.title };
export default function Page() { return <LegalDocument document={document} />; }
