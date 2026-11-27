"use client";
import { useId, useState } from "react";
import { CodeBlock } from "./CopyButton";

import type { Snippet } from "@/lib/snippets";

export function CodeTabs({ snippets }: { snippets: Snippet[] }) {
  const [active, setActive] = useState(0);
  const id = useId();
  return (
    <div className="code-tabs">
      <div role="tablist" aria-label="Code examples">
        {snippets.map((s, i) => (
          <button key={s.label} role="tab" id={`${id}-t${i}`} aria-controls={`${id}-p${i}`} aria-selected={i === active} onClick={() => setActive(i)}>
            {s.label}
          </button>
        ))}
      </div>
      {snippets.map((s, i) => (
        <div key={s.label} role="tabpanel" id={`${id}-p${i}`} aria-labelledby={`${id}-t${i}`} hidden={i !== active}>
          <CodeBlock code={s.code} />
        </div>
      ))}
    </div>
  );
}
