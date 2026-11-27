import type { Metadata } from "next";
import { BuildStatus, SiteFooter, SiteHeader } from "@/components/Site";
import { Pricing } from "@/components/Marketing";
import { dict } from "@/lib/i18n";

export const metadata: Metadata = { title: "Pricing" };

export default function Page() {
  const t = dict.en;
  return (
    <>
      <SiteHeader t={t} />
      <BuildStatus text={t.status} />
      <main id="main"><Pricing t={t} signup="/signup" /></main>
      <SiteFooter t={t} />
    </>
  );
}
