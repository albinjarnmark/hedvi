import type { Metadata } from "next";
import "../../globals.css";

export const metadata: Metadata = {
  metadataBase: new URL("https://hedvi.dev"),
  title: { default: "Hedvi — Email, without the overhead.", template: "%s · Hedvi" },
  description: "Vi bygger ett enklare API för transaktionell e-post: testa tryggt, förstå leveransen, kontrollera kostnaden.",
  icons: { icon: "/favicon.svg", apple: "/icon-180.png" },
  alternates: { languages: { en: "/", sv: "/sv" } },
};

export default function SwedishLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="sv">
      <body>{children}</body>
    </html>
  );
}
