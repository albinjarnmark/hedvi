import type { Metadata } from "next";
import "../globals.css";

export const metadata: Metadata = {
  metadataBase: new URL("https://hedvi.dev"),
  title: { default: "Hedvi — Email, without the overhead.", template: "%s · Hedvi" },
  icons: { icon: "/favicon.svg", apple: "/icon-180.png" },
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
