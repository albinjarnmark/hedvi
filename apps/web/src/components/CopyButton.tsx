"use client";
import { useState } from "react";

export function CopyButton({ value, label = "Copy" }: { value: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      className="btn btn-small btn-lime"
      onClick={async () => {
        await navigator.clipboard.writeText(value);
        setDone(true);
        setTimeout(() => setDone(false), 1500);
      }}
      aria-live="polite"
    >
      {done ? "Copied" : label}
    </button>
  );
}

export function CodeBlock({ code }: { code: string }) {
  return (
    <div className="copy-wrap">
      <div className="code-toolbar"><CopyButton value={code} /></div>
      <pre className="code" tabIndex={0} aria-label="Code example">
        <code>{code}</code>
      </pre>
    </div>
  );
}
