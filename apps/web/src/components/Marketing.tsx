import Link from "next/link";
import { CodeTabs } from "./CodeTabs";
import { sendSnippets } from "@/lib/snippets";
import { appLink } from "@/lib/navigation";
import { publicApiUrl } from "@/lib/public-site";
import type { Dict } from "@/lib/i18n";

export function Home({ t, signup }: { t: Dict; signup: string }) {
  const icons = ["◎", "↗", "≡"];
  return (
    <>
      <section className="hero hero-center">
        <div className="container">
          <span className="eyebrow">{t.status}</span>
          <h1>{t.hero.title}</h1>
          <p className="lead">{t.hero.lead}</p>
          <div className="row" style={{ marginTop: 28 }}>
            <Link className="btn btn-primary" href={appLink(signup)}>{t.hero.cta} →</Link>
            <Link className="btn" href="/docs/quickstart">{t.hero.secondary}</Link>
          </div>
          <div style={{ maxWidth: 760, margin: "64px auto 0", textAlign: "left" }}>
            <CodeTabs snippets={sendSnippets(publicApiUrl, "hv_test_…", "Acme <onboarding@example.com>", "you@example.com")} />
          </div>
        </div>
      </section>
      <section className="section">
        <div className="container grid grid-3">
          {t.pillars.map((p, i) => (
            <div className="card" key={p.title}>
              <span className="feature-icon" aria-hidden="true">{icons[i]}</span>
              <h3>{p.title}</h3>
              <p className="muted small" style={{ margin: 0 }}>{p.text}</p>
            </div>
          ))}
        </div>
      </section>
      <section className="section section-dark">
        <div className="container">
          <span className="eyebrow">Flow</span>
          <h2>{t.flowTitle}</h2>
          <ol className="steps">{t.flow.map((step) => <li key={step}>{step}</li>)}</ol>
        </div>
      </section>
      <section className="section">
        <div className="container grid grid-2" style={{ alignItems: "center", gap: 48 }}>
          <div>
            <span className="eyebrow">Resend SDKs</span>
            <h2>{t.codeTitle}</h2>
            <p className="muted">{t.codeNote}</p>
            <p><Link className="btn btn-small" href="/docs/compatibility">Compatibility matrix →</Link></p>
          </div>
          <pre className="code">{`# Node · resend@6.39.0
RESEND_BASE_URL=${publicApiUrl}

# Python · resend==2.54.0
RESEND_API_URL=${publicApiUrl}

RESEND_API_KEY=hv_live_…`}</pre>
        </div>
      </section>
      <section className="section">
        <div className="container narrow">
          <span className="eyebrow">Honest by default</span>
          <h2>{t.honestyTitle}</h2>
          <ul className="checks">{t.honesty.map((h) => <li key={h}>{h}</li>)}</ul>
          <p style={{ marginTop: 32 }}><Link className="btn btn-primary" href={appLink(signup)}>{t.hero.cta} →</Link></p>
        </div>
      </section>
    </>
  );
}

export function Pricing({ t, signup }: { t: Dict; signup: string }) {
  const p = t.pricing;
  return (
    <section className="hero hero-center">
      <div className="container">
        <span className="eyebrow">Pricing</span>
        <h1>{p.title}</h1>
        <p className="lead">{p.lead}</p>
        <div className="grid grid-3" style={{ marginTop: 40, textAlign: "left" }}>
          {[p.free, p.flex, p.pro].map((plan, i) => (
            <div className={`card ${i === 2 ? "card-dark" : ""}`} key={plan.name}>
              <h2>{plan.name}</h2>
              <div className="price">{plan.price}</div>
              <p className="small" style={{ minHeight: "1.5em" }}>{plan.per}</p>
              <ul className="checks">{plan.points.map((x) => <li key={x}>{x}</li>)}</ul>
              <p style={{ marginTop: 20 }}>
                <Link className={`btn ${i === 2 ? "btn-lime" : "btn-primary"}`} href={appLink(signup)}>{t.nav.start}</Link>
              </p>
            </div>
          ))}
        </div>
        <p style={{ marginTop: 28 }} className="muted">{p.larger}</p>
        <ul className="small muted" style={{ textAlign: "left", maxWidth: 760, margin: "0 auto" }}>{p.notes.map((n) => <li key={n}>{n}</li>)}</ul>
      </div>
    </section>
  );
}
