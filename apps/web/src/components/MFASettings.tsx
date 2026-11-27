"use client";
import { useState } from "react";
import { useMe, type Me } from "./AppShell";
import { api } from "@/lib/api";
import { useAction } from "@/lib/hooks";

export function MFASettings() {
  const me = useMe();
  return <MFAForm user={me.user} />;
}

export function MFAForm({ user }: { user: Me["user"] }) {
  const [setup, setSetup] = useState<{ secret: string; otpauth_url: string } | null>(null);
  const [enabled, setEnabled] = useState(user.mfa_enabled);
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const { pending, error, run } = useAction();
  const staff = user.is_staff;
  return <form className="stack" onSubmit={(e) => {
    e.preventDefault();
    void run(async () => {
      if (enabled) {
        if (!confirm("Turn off two-factor authentication for your account?")) return;
        await api("/me/mfa/disable", { method: "POST", body: { password, code } });
        setEnabled(false);
      } else if (setup) {
        await api("/me/mfa/enable", { method: "POST", body: { password, code } });
        setEnabled(true);
        setSetup(null);
      } else {
        setSetup(await api("/me/mfa/setup", { method: "POST", body: { password } }));
      }
      setPassword("");
      setCode("");
    });
  }}>
    <p>Two-factor authentication is <strong>{enabled ? "on" : "off"}</strong>.</p>
    {enabled && staff ? <p className="small muted">Operator accounts must keep two-factor authentication enabled.</p> : <>
      {setup && <>
        <p className="small">Add this key to your authenticator app, then confirm with your password and its six-digit code.</p>
        <div className="secret">{setup.secret}</div>
        <a className="small" href={setup.otpauth_url}>Open in authenticator app</a>
      </>}
      <div className="field"><label htmlFor="mfa-password">Current password</label><input id="mfa-password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" maxLength={256} required /></div>
      {(setup || enabled) && <div className="field"><label htmlFor="mfa-code">Authentication code</label><input id="mfa-code" value={code} onChange={(e) => setCode(e.target.value)} inputMode="numeric" pattern="[0-9]{6}" autoComplete="one-time-code" required /></div>}
      <button className={`btn ${enabled ? "btn-danger" : "btn-primary"}`} disabled={pending}>{enabled ? "Turn off two-factor authentication" : setup ? "Turn on two-factor authentication" : "Set up authenticator app"}</button>
      {setup && <button type="button" className="btn" disabled={pending} onClick={() => { setSetup(null); setPassword(""); setCode(""); }}>Cancel setup</button>}
    </>}
    {error && <p className="error" role="alert">{error}</p>}
  </form>;
}
