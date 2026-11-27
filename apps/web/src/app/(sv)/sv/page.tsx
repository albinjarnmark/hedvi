import { SiteFooter, SiteHeader } from "@/components/Site";
import { Home } from "@/components/Marketing";
import { dict } from "@/lib/i18n";

export default function Page() {
  const t = dict.sv;
  return (
    <>
      <SiteHeader t={t} base="/sv" />
      <main id="main"><Home t={t} signup="/signup?locale=sv" /></main>
      <SiteFooter t={t} />
    </>
  );
}
