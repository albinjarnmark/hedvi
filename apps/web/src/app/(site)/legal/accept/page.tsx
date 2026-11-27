import { connection } from "next/server";
import type { Metadata } from "next";
import { AcceptLegalForm } from "@/components/AccountRecovery";
export const metadata: Metadata = { title: "Review the terms", robots: { index: false }, referrer: "no-referrer" };
export default async function Page() {
 await connection();
 return <main id="main" className="container narrow" style={{ maxWidth: 480, padding: "48px 24px" }}> <AcceptLegalForm /> </main>;
}
