import { connection } from "next/server";
import type { Metadata } from "next";
import { LoginForm } from "@/components/AuthForms";
import { SiteHeader } from "@/components/Site";
import { dict } from "@/lib/i18n";

export const metadata: Metadata = { title: "Sign in", robots: { index: false } };

export default async function Page() {
  await connection();
  return (
    <>
      <SiteHeader t={dict.en} />
      <main id="main" className="container narrow" style={{ maxWidth: 440, padding: "48px 24px" }}><LoginForm /></main>
    </>
  );
}
