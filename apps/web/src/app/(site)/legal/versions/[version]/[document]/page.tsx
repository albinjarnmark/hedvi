import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { LegalDocument } from "@/components/LegalDocument";
import { legalVersions } from "@/content/legal/versions";

type Params = { version: string; document: string };
export const dynamicParams = false;
export function generateStaticParams(): Params[] {
  return Object.entries(legalVersions).flatMap(([version, docs]) => Object.keys(docs).map(document => ({ version, document })));
}
function archived({ version, document }: Params) {
  const docs = Object.hasOwn(legalVersions, version) ? legalVersions[version] : undefined;
  const content = docs && Object.hasOwn(docs, document) ? docs[document] : undefined;
  if (!content) notFound();
  return content;
}
export async function generateMetadata({ params }: { params: Promise<Params> }): Promise<Metadata> {
  const p = await params;
  return { title: `${archived(p).title} · ${p.version}` };
}
export default async function Page({ params }: { params: Promise<Params> }) {
  const p = await params;
  return <LegalDocument document={archived(p)} manifestVersion={p.version} />;
}
