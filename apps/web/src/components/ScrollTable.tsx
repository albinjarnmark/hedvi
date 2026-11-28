import type { TableHTMLAttributes } from "react";

export function ScrollTable({ children, ...props }: TableHTMLAttributes<HTMLTableElement>) {
  return <div className="table-scroll" role="region" aria-label={props["aria-label"] ?? "Table — scroll horizontally for more columns"} tabIndex={0}><table className="table" {...props}>{children}</table></div>;
}
