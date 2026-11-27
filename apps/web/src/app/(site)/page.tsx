import { SiteFooter, SiteHeader } from "@/components/Site";
import { Home } from "@/components/Marketing";
import { dict } from "@/lib/i18n";

export default function Page() {
  const t = dict.en;
  return (
    <>
      <SiteHeader t={t} />
      <main id="main"><Home t={t} signup="/signup" /></main>
      <SiteFooter t={t} />
    </>
  );
}
