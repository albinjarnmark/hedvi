"use client";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useState } from "react";
import { api, ApiError } from "@/lib/api";
import { RequestAccountMail } from "./AccountRecovery";
import { useAction } from "@/lib/hooks";

export function LoginForm() {
  const router = useRouter();
  const [mfa, setMfa] = useState(false);
  const { pending, error, run } = useAction();
  return (
    <form
      className="card stack"
      onSubmit={async (e) => {
        e.preventDefault();
        const f = new FormData(e.currentTarget);
        await run(async () => {
          try {
            await api("/auth/login", { method: "POST", body: { email: f.get("email"), password: f.get("password"), totp_code: f.get("totp") ?? "", remember_me: f.get("remember_me") === "on" } });
            router.push("/app");
          } catch (err) {
            if (err instanceof ApiError && err.body.mfa_required) {
              setMfa(true);
              throw new Error("Enter the 6-digit code from your authenticator app.");
            }
            throw err;
          }
        });
      }}
    >
      <h1 style={{ fontSize: "1.8rem" }}>Sign in</h1>
      <div className="field"><label htmlFor="email">Email</label><input id="email" name="email" type="email" autoComplete="email" required /></div>
      <div className="field"><label htmlFor="password">Password</label><input id="password" name="password" type="password" autoComplete="current-password" maxLength={256} required /></div>
      {mfa && <div className="field"><label htmlFor="totp">Authentication code</label><input id="totp" name="totp" inputMode="numeric" autoComplete="one-time-code" pattern="[0-9]{6}" required /></div>}
      <label className="check-label"><input type="checkbox" name="remember_me" aria-describedby="remember-help" /><span>Remember me</span></label>
      <p id="remember-help" className="tiny muted">Stay signed in for 30 days on this device. Use this only on a device you trust.</p>
      {error && <p className="error" role="alert">{error}</p>}
      <button className="btn btn-primary" disabled={pending}>Sign in</button>
      <p className="small"><Link href="/forgot-password">Forgot your password or need to verify your email?</Link></p>
      <p className="small">New to Hedvi? <Link href="/signup">Create an account</Link></p>
      <p className="tiny muted">We use a secure cookie to keep you signed in. No advertising or analytics cookies. <Link href="/legal/cookies">About cookies</Link></p>
    </form>
  );
}

export function SignupForm() { return <RequestAccountMail signup />; }
