import "../../globals.css";

export default function LegacyLanguageLayout({ children }: { children: React.ReactNode }) {
  return <html lang="en"><body>{children}</body></html>;
}
