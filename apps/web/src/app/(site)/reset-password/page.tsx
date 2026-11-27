import { connection } from "next/server";
import type { Metadata } from "next";
import { CompleteAccountMail } from "@/components/AccountRecovery";
export const metadata: Metadata = { title: "Choose a new password", robots: { index: false }, referrer: "no-referrer" };
export default async function Page() {
 await connection();
 return <main id="main" className="container narrow" style={{ maxWidth: 480, padding: "48px 24px" }}> <CompleteAccountMail /> </main>;
}
