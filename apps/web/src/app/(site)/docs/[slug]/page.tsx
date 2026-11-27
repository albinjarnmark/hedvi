import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { docs, findDoc } from "../content";

export function generateStaticParams() {
  return docs.map((d) => ({ slug: d.slug }));
}

export async function generateMetadata({ params }: { params: Promise<{ slug: string }> }): Promise<Metadata> {
  const { slug } = await params;
  return { title: findDoc(slug)?.title ?? "Docs" };
}

export default async function DocPage({ params }: { params: Promise<{ slug: string }> }) {
  const { slug } = await params;
  const doc = findDoc(slug);
  if (!doc) notFound();
  return (
    <>
      <h1>{doc.title}</h1>
      {doc.body}
    </>
  );
}
