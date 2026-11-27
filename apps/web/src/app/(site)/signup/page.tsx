import { connection } from "next/server";
import type { Metadata } from "next";
import { Suspense } from "react";
import { SignupForm } from "@/components/AuthForms";
import { SiteHeader } from "@/components/Site";
import { dict } from "@/lib/i18n";

export const metadata: Metadata = { title: "Create account", robots: { index: false } };

export default async function Page() {
  await connection();
  return (
    <>
      <SiteHeader t={dict.en} />
      <main id="main" className="container narrow" style={{ maxWidth: 480, padding: "48px 24px" }}>
        <Suspense><SignupForm /></Suspense>
      </main>
    </>
  );
}
