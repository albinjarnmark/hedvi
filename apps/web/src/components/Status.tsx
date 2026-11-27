import { statusLabel, tone } from "@/lib/api";

export function Status({ value, label }: { value: string; label?: string }) {
  return <span className={`badge badge-${tone(value)}`}>{label ?? statusLabel[value] ?? value}</span>;
}
