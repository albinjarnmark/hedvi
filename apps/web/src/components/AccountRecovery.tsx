"use client";
import Link from "next/link";
import { MFAForm } from "./MFASettings";
import type { Me } from "./AppShell";
import { useRouter, useSearchParams } from "next/navigation";
import { useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { useAction, useLoad } from "@/lib/hooks";
import { consumeProofFragment, legalConsent, type LegalManifest } from "@/lib/account-proof";

export function RequestAccountMail({ signup = false }: { signup?: boolean }) {
  const params = useSearchParams();
  const sv = params.get("locale") === "sv";
  const [accepted, setAccepted] = useState(false);
  const { pending, error, run } = useAction();
  return <form className="card stack" onSubmit={async (event) => {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    const result = await run(() => api(signup ? "/auth/signup" : "/auth/password-reset/request", {
      method: "POST", body: { email: data.get("email"), locale: sv ? "sv" : "en" },
    }));
    if (result) setAccepted(true);
  }}>
    <h1 style={{ fontSize: "1.8rem" }}>{signup ? (sv ? "Skapa konto" : "Create your account") : (sv ? "Återställ lösenord" : "Reset your password")}</h1>
    {accepted ? <p role="status">{sv ? "Om adressen kan användas skickar vi ett meddelande med nästa steg. Kontrollera även skräpposten. Vänta en stund innan du försöker igen." : "If this address can be used, we will send a message with the next step. Check your spam folder too. Wait a little before trying again."}</p> : <>
      <p className="small muted">{signup ? (sv ? "Verifiera din e-postadress först. Därefter väljer du lösenord och får din testinkorg." : "Verify your email first. Then choose your password and get your test inbox.") : (sv ? "Vi skickar en länk för att välja ett nytt lösenord. Din autentiseringsapp behövs fortfarande om den är aktiverad." : "Request a link to choose a new password. Your authenticator is still required if enabled.")}</p>
      <div className="field"><label htmlFor="email">Email</label><input id="email" name="email" type="email" autoComplete="email" maxLength={254} required /></div>
      <button className="btn btn-primary" disabled={pending}>{sv ? "Skicka länk" : "Send link"}</button>
    </>}
    {error && <p className="error" role="alert">{error}</p>}
    <Link href="/login">{sv ? "Till inloggning" : "Back to sign in"}</Link>
  </form>;
}

function LegalAcknowledgement({ manifest }: { manifest: LegalManifest }) {
  const url = (id: string) => manifest.documents.find((doc) => doc.id === id)?.url ?? "/legal/accept";
  return <>
    {!manifest.adopted && <p className="small muted">These document versions are for local development and are not adopted for hosted use.</p>}
    <label className="small"><input name="legal" type="checkbox" required /> I agree to the <Link href={url("terms")} target="_blank" rel="noreferrer">Terms</Link>, <Link href={url("aup")} target="_blank" rel="noreferrer">Acceptable Use Policy</Link> and <Link href={url("dpa")} target="_blank" rel="noreferrer">DPA</Link>, and acknowledge receiving the <Link href={url("privacy")} target="_blank" rel="noreferrer">Privacy Notice</Link>, <Link href={url("subprocessors")} target="_blank" rel="noreferrer">provider list</Link> and <Link href={url("cookies")} target="_blank" rel="noreferrer">cookie notice</Link>. Version: {manifest.version}.</label>
  </>;
}

export function CompleteAccountMail({ signup = false }: { signup?: boolean }) {
  const router = useRouter();
  const sv = useSearchParams().get("locale") === "sv";
  const captured = useRef<string | null>(null);
  const [token, setToken] = useState<string | null>(null);
  const [done, setDone] = useState(false);
  const [passwordError, setPasswordError] = useState("");
  const { data: manifest, error: manifestError } = useLoad<LegalManifest>(signup ? "/auth/legal" : null);
  const { pending, error, run } = useAction();
  useEffect(() => {
    if (captured.current === null) captured.current = consumeProofFragment(window.location.href, (path) => window.history.replaceState(null, "", path));
    const value = captured.current;
    void Promise.resolve().then(() => setToken(value));
  }, []);
  if (done) return <section className="card stack"><h1>{sv ? "Lösenordet är ändrat" : "Password changed"}</h1><p>{sv ? "Alla tidigare sessioner har loggats ut. Logga in med ditt nya lösenord och din autentiseringsapp om den är aktiverad." : "All previous sessions have been signed out. Sign in with your new password and your authenticator if enabled."}</p><Link href="/login">{sv ? "Logga in" : "Sign in"}</Link></section>;
  if (token === null) return <p aria-busy="true">{sv ? "Öppnar länken…" : "Opening your link…"}</p>;
  if (!token) return <section className="card stack"><h1>Open your email link</h1><p>The link is missing or malformed. Request another link to continue.</p><Link href={signup ? "/signup" : "/forgot-password"}>Request a new link</Link><Link href="/login">Sign in if you already completed this step</Link></section>;
  return <form className="card stack" onSubmit={async (event) => {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    if (data.get("password") !== data.get("confirm-password")) {
      setPasswordError(sv ? "Lösenorden matchar inte." : "Passwords do not match.");
      event.currentTarget.querySelector<HTMLInputElement>("#confirm-password")?.focus();
      return;
    }
    setPasswordError("");
    if (signup && !manifest) return;
    const result = await run(() => api<{ project?: { id: string }; test_key?: { key: string } }>(signup ? "/auth/signup/complete" : "/auth/password-reset/complete", {
      method: "POST", body: {
        token, password: data.get("password"),
        ...(signup ? { name: data.get("name"), organization: data.get("organization"), consent: legalConsent(manifest!.version) } : {}),
      },
    }));
    if (!result) return;
    captured.current = ""; setToken("");
    if (signup && result.project && result.test_key) {
      sessionStorage.setItem(`hedvi:onboarding-key:${result.project.id}`, result.test_key.key);
      router.replace(`/app/${result.project.id}/test-inbox?welcome=1`);
    } else setDone(true);
  }}>
    <h1 style={{ fontSize: "1.8rem" }}>{signup ? (sv ? "Slutför ditt konto" : "Finish your account") : (sv ? "Välj ett nytt lösenord" : "Choose a new password")}</h1>
    <p className="small muted">{sv ? "Länken används först när du skickar formuläret. Den gäller i 30 minuter efter begäran." : "Only submitting this form uses the link. It expires 30 minutes after it was requested."}</p>
    {signup && <>
      <div className="field"><label htmlFor="name">{sv ? "Namn" : "Name"}</label><input id="name" name="name" autoComplete="name" maxLength={100} /></div>
      <div className="field"><label htmlFor="organization">{sv ? "Företag eller projekt" : "Company or project"}</label><input id="organization" name="organization" autoComplete="organization" maxLength={100} /></div>
    </>}
    <div className="field"><label htmlFor="password">{sv ? "Nytt lösenord (minst 12 tecken)" : "New password (at least 12 characters)"}</label><input id="password" name="password" type="password" minLength={12} maxLength={256} autoComplete="new-password" required /></div>
    <div className="field"><label htmlFor="confirm-password">{sv ? "Bekräfta lösenord" : "Confirm password"}</label><input id="confirm-password" name="confirm-password" type="password" minLength={12} maxLength={256} autoComplete="new-password" aria-invalid={!!passwordError} aria-describedby={passwordError ? "password-mismatch" : undefined} onChange={() => setPasswordError("")} required /></div>
    {passwordError && <p id="password-mismatch" className="error" role="alert">{passwordError}</p>}
    {signup && manifest && <LegalAcknowledgement manifest={manifest} />}
    {(error || manifestError) && <p className="error" role="alert">{error ?? manifestError?.message}</p>}
    <button className="btn btn-primary" disabled={pending || (signup && !manifest)}>{signup ? (sv ? "Skapa konto" : "Create account") : (sv ? "Ändra lösenord" : "Change password")}</button>
    <p className="small">Already completed this step? <Link href="/login">Sign in</Link>. Otherwise, <Link href={signup ? "/signup" : "/forgot-password"}>request a new link</Link>.</p>
  </form>;
}

export function AcceptLegalForm() {
  const router = useRouter();
  const { data: me } = useLoad<Me>("/me");
  const { data: manifest, error: loadError } = useLoad<LegalManifest>("/auth/legal");
  const { pending, error, run } = useAction();
  return <><form className="card stack" onSubmit={async (e) => {
    e.preventDefault(); if (!manifest) return;
    const result = await run(() => api("/auth/legal/accept", { method: "POST", body: legalConsent(manifest.version) }));
    if (result) router.replace("/app");
  }}>
    <h1>Review the current terms</h1>
    {manifest && <LegalAcknowledgement manifest={manifest} />}
    {(error || loadError) && <p className="error" role="alert">{error ?? loadError?.message}</p>}
    <button className="btn btn-primary" disabled={pending || !manifest}>Continue</button>
    <Link href="/forgot-password">Account recovery</Link>
    <button type="button" className="btn" onClick={async () => { await api("/auth/logout", { method: "POST" }); router.replace("/login"); }}>Sign out</button>
  </form>{me && <section className="card stack" style={{ marginTop: 24 }}><h2>Account security</h2><MFAForm user={me.user} /></section>}</>;
}
