import { connection } from "next/server";
import type { Metadata } from "next";
import { AppShell } from "@/components/AppShell";
import "../../globals.css";

export const metadata: Metadata = { title: { default: "Dashboard · Hedvi", template: "%s · Hedvi" }, robots: { index: false }, icons: { icon: "/favicon.svg" } };

export default async function AppLayout({ children }: { children: React.ReactNode }) {
  await connection();
  return (
    <html lang="en">
      <body><AppShell>{children}</AppShell></body>
    </html>
  );
}
