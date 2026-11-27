export type LegalManifest = {
  version: string;
  adopted: boolean;
  documents: { id: string; version: string; url: string; sha256: string; acknowledgement: string }[];
};

// Consume only the URL fragment. Tokens are never query parameters, cookies,
// local/session storage, analytics or requests made when the page opens.
export function consumeProofFragment(href: string, replace: (path: string) => void): string {
  const url = new URL(href);
  replace(url.pathname + url.search);
  const values = new URLSearchParams(url.hash.slice(1));
  const token = values.get("token") ?? "";
  return values.size === 1 && /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}\.[a-z2-7]{52}$/.test(token) ? token : "";
}

export function legalConsent(version: string) {
  return { version, terms: true, aup: true, dpa:true, privacy_notice: true, subprocessors_notice:true, cookies_notice:true };
}
