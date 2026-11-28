"use client";

import { usePathname } from "next/navigation";
import { useRef, useState, type ReactNode } from "react";

type Props = { id: string; label: string; className: string; header?: ReactNode; children: ReactNode };

// A route change remounts the disclosure, including browser back/forward.
export function ResponsiveNav(props: Props) {
  const pathname = usePathname();
  return <NavigationDisclosure key={pathname} {...props} />;
}

function NavigationDisclosure({ id, label, className, header, children }: Props) {
  const [open, setOpen] = useState(false);
  const toggle = useRef<HTMLButtonElement>(null);
  return <div className={`responsive-nav ${className}`} onKeyDown={(event) => {
    if (event.key === "Escape" && open) {
      event.preventDefault();
      setOpen(false);
      toggle.current?.focus();
    }
  }} onClick={(event) => {
    if (event.target instanceof Element && event.target.closest("a")) setOpen(false);
  }} onChange={(event) => {
    if (event.target instanceof HTMLSelectElement) setOpen(false);
  }}>
    <div className="navigation-header">
      {header}
      <button ref={toggle} className="btn navigation-toggle" type="button" aria-expanded={open}
        aria-controls={id} aria-label={`${open ? "Close" : "Open"} ${label.toLowerCase()} menu`}
        onClick={() => setOpen(!open)}>
        <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" aria-hidden="true">
          {open ? <path d="m6 6 12 12M6 18 18 6" /> : <path d="M4 6h16M4 12h16M4 18h16" />}
        </svg>
        {open ? "Close" : label === "Dashboard" || label === "Main" ? "Menu" : label}
      </button>
    </div>
    <nav id={id} className="navigation-panel" data-open={open} aria-label={label}>{children}</nav>
  </div>;
}
