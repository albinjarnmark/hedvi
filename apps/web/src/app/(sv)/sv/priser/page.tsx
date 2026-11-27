import type { Metadata } from "next";
import { BuildStatus, SiteFooter, SiteHeader } from "@/components/Site";
import { Pricing } from "@/components/Marketing";
import { dict } from "@/lib/i18n";

export const metadata: Metadata = { title: "Priser" };

export default function Page() {
  const t = dict.sv;
  return (
    <>
      <SiteHeader t={t} base="/sv" />
      <BuildStatus text={t.status} />
      <main id="main"><Pricing t={t} signup="/signup?locale=sv" /></main>
      <SiteFooter t={t} />
    </>
  );
}
