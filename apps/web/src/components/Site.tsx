import Link from "next/link";
import { Logo } from "./Logo";
import type { Dict } from "@/lib/i18n";
import { appLink } from "@/lib/navigation";
import { sourceUrl } from "@/lib/public-site";
import { ResponsiveNav } from "./ResponsiveNav";

export function SiteHeader({ t }: { t: Dict }) {
  return (
    <header className="site-header">
      <a className="skip" href="#main">Skip to content</a>
      <div className="container">
        <Link href="/" aria-label="Hedvi home">
          <Logo />
        </Link>
        <ResponsiveNav id="site-navigation" label="Main" className="site-navigation">
          <Link href="/docs">{t.nav.docs}</Link>
          <Link href="/pricing">{t.nav.pricing}</Link>
          <Link href="/docs/switch-from-resend">{t.nav.switch}</Link>
          <Link href={appLink("/login")}>{t.nav.signin}</Link>
          <Link className="btn btn-primary btn-small" href={appLink("/signup")}>{t.nav.start}</Link>
        </ResponsiveNav>
      </div>
    </header>
  );
}

export function SiteFooter({ t }: { t: Dict }) {
  return (
    <footer className="site-footer">
      <div className="container row between">
        <div>
          <p>{t.footer.company}</p>
          <p className="small">{t.footer.contact}</p>
        </div>
        <div className="row small">
          <Link href="/docs">Docs</Link>
          <Link href="/legal/terms">Terms</Link>
          <Link href="/legal/privacy">Privacy</Link>
          <Link href="/legal/dpa">DPA</Link>
          <Link href="/legal/acceptable-use">Acceptable use</Link>
          <Link href="/legal/subprocessors">Service providers</Link>
          <Link href="/legal/cookies">Cookies</Link>
          <Link href="/docs/data-and-retention">Data &amp; retention</Link>
          <Link href="/docs/compatibility">Compatibility</Link>
          <a href="https://status.hedvi.dev">Status</a>
          <a href={sourceUrl}>Source · AGPL-3.0</a>
          <a href="https://x.com/Hedvidev" rel="noopener noreferrer">X / @Hedvidev</a>
        </div>
      </div>
    </footer>
  );
}
