import { connection } from "next/server";
import type { Metadata } from "next";
import { CompleteAccountMail } from "@/components/AccountRecovery";
export const metadata: Metadata = { title: "Verify your email", robots: { index: false }, referrer: "no-referrer" };
export default async function Page() {
 await connection();
 return <main id="main" className="container narrow" style={{ maxWidth: 480, padding: "48px 24px" }}> <CompleteAccountMail signup /> </main>;
}
