"use client";
import { useEffect, useRef, useState } from "react";
import QRCode from "qrcode";
import { useMe, type Me } from "./AppShell";
import { CopyButton } from "./CopyButton";
import { api } from "@/lib/api";
import { useAction } from "@/lib/hooks";

function AuthenticatorQR({ url }: { url: string }) {
  const canvas = useRef<HTMLCanvasElement>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    let active = true;
    const target = canvas.current;
    if (target) void QRCode.toCanvas(target, url, { width: 208, margin: 4, errorCorrectionLevel: "M" }).catch(() => { if (active) setFailed(true); });
    return () => { active = false; target?.getContext("2d")?.clearRect(0, 0, target.width, target.height); };
  }, [url]);
  return failed ? <p className="small">Use the app link or setup key below.</p> : <canvas ref={canvas} className="authenticator-qr" role="img" aria-label="Scan this QR code with your authenticator app" />;
}

export function MFASettings() {
  return <MFAForm user={useMe().user} />;
}

export function MFAForm({ user }: { user: Me["user"] }) {
  const [setup, setSetup] = useState<{ secret: string; otpauth_url: string } | null>(null);
  const [enabled, setEnabled] = useState(user.mfa_enabled);
  const [editing, setEditing] = useState(false);
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [message, setMessage] = useState("");
  // Only the current setup keeps this in memory; never browser storage or URLs.
  const setupPassword = useRef("");
  const mounted = useRef(true);
  const { pending, error, run, setError } = useAction();
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; setupPassword.current = ""; }; }, []);
  useEffect(() => {
    if (!setup) return;
    const timer = setTimeout(() => {
      setupPassword.current = ""; setSetup(null); setCode(""); setEditing(false);
      setMessage("Setup timed out. Start again when you are ready.");
    }, 10 * 60 * 1000);
    return () => clearTimeout(timer);
  }, [setup]);
  const clear = () => { setupPassword.current = ""; setPassword(""); setCode(""); setSetup(null); setEditing(false); setError(null); };
  if (!editing) return <div className="stack">
    <div className="row between"><strong>Authenticator app</strong><span className={`badge ${enabled ? "badge-ok" : "badge-neutral"}`}>{enabled ? "Enabled" : "Not enabled"}</span></div>
    <p className="small muted">{enabled ? "Your account is protected. Use the code from your authenticator app when signing in." : "Add an extra layer of security with Google Authenticator, 1Password, or another authenticator app."}</p>
    {message && <p className="small" role="status">{message}</p>}
    {enabled && user.is_staff ? <p className="small muted">Owner accounts keep two-factor authentication enabled.</p> : <button className={`btn ${enabled ? "btn-ghost" : "btn-primary"}`} type="button" onClick={() => { setMessage(""); setEditing(true); }}>{enabled ? "Turn off two-factor authentication…" : "Set up authenticator"}</button>}
  </div>;
  return <form className="stack" onSubmit={(event) => {
    event.preventDefault();
    void run(async () => {
      if (enabled) {
        await api("/me/mfa/disable", { method: "POST", body: { password, code } });
        clear(); setEnabled(false); setMessage("Two-factor authentication is now off.");
      } else if (setup) {
        await api("/me/mfa/enable", { method: "POST", body: { password: setupPassword.current, code } });
        clear(); setEnabled(true); setMessage("All set. Two-factor authentication is enabled.");
      } else {
        const result = await api<{ secret: string; otpauth_url: string }>("/me/mfa/setup", { method: "POST", body: { password } });
        if (!mounted.current) return;
        setupPassword.current = password; setPassword(""); setSetup(result);
      }
    });
  }}>
    <h3>{enabled ? "Turn off two-factor authentication" : setup ? "Add Hedvi to your authenticator" : "Confirm it is you"}</h3>
    {enabled ? <p className="small muted">Confirm this change with your password and the current code from your authenticator.</p> : !setup && <p className="small muted">Enter your password once to start. Then add Hedvi to your app and enter its six-digit code.</p>}
    {!setup && <div className="field"><label htmlFor="mfa-password">Current password</label><input id="mfa-password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" maxLength={256} autoFocus required /></div>}
    {setup && <>
      <p className="small muted">On your phone, tap the button below. On a computer, open Google Authenticator on your phone, tap <strong>+</strong>, then <strong>Scan a QR code</strong>.</p>
      <a className="btn btn-primary" href={setup.otpauth_url}>Open authenticator app</a>
      <AuthenticatorQR url={setup.otpauth_url} />
      <details><summary>Can&apos;t scan? Enter a setup key</summary><p className="small muted">In your app, choose Enter a setup key. Name it Hedvi and select Time based.</p><div className="secret">{setup.secret}</div><div className="row" style={{ marginTop: 10 }}><CopyButton value={setup.secret} label="Copy setup key" /></div></details>
      <p className="small">Enter the six-digit code shown for Hedvi to finish.</p>
    </>}
    {(setup || enabled) && <div className="field"><label htmlFor="mfa-code">6-digit code</label><input id="mfa-code" className="otp-input" value={code} onChange={(e) => setCode(e.target.value.replace(/\D/g, "").slice(0, 6))} inputMode="numeric" pattern="[0-9]{6}" maxLength={6} autoComplete="one-time-code" autoFocus required /></div>}
    {error && <p className="error" role="alert">{error}</p>}
    <div className="row"><button className={`btn ${enabled ? "btn-danger" : "btn-primary"}`} disabled={pending}>{pending ? "Please wait…" : enabled ? "Turn off" : setup ? "Verify and finish" : "Continue"}</button><button type="button" className="btn btn-ghost" disabled={pending} onClick={clear}>Cancel</button></div>
  </form>;
}
