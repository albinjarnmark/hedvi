import { parseFragment, serialize, type DefaultTreeAdapterMap } from "parse5";

// Parse without a browser document: even parsing a malicious message must not
// fetch images, preconnect or resolve DNS. The iframe's sandbox and CSP remain
// independent enforcement layers; this removes resource hints CSP may miss.
const forbidden = new Set(["link", "meta", "base", "script", "noscript", "iframe", "frame", "frameset", "object", "embed", "applet", "template", "svg", "math"]);

export function sanitizedEmail(html: string): string {
  const tree = parseFragment(html);
  const pending: { node: DefaultTreeAdapterMap["parentNode"]; depth: number }[] = [{ node: tree, depth: 0 }];
  while (pending.length) {
    const { node, depth } = pending.pop()!;
    // Bound serialization depth for deliberately nested HTML as well.
    node.childNodes = depth >= 100 ? [] : node.childNodes.filter((child) => !("tagName" in child && forbidden.has(child.tagName.toLowerCase())));
    for (const child of node.childNodes) {
      if ("attrs" in child) {
        child.attrs = child.attrs.filter(({ name }) => !name.toLowerCase().startsWith("on") && !["srcdoc", "ping", "autofocus", "formaction"].includes(name.toLowerCase()));
      }
      if ("childNodes" in child) pending.push({ node: child, depth: depth + 1 });
    }
  }
  return serialize(tree);
}

export function sandboxDoc(html: string, remoteImages: boolean) {
  const csp = `default-src 'none'; style-src 'unsafe-inline'; img-src data: ${remoteImages ? "https:" : ""}; font-src data:; form-action 'none'; base-uri 'none'`;
  return `<!doctype html><html><head><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="${csp}"><meta http-equiv="x-dns-prefetch-control" content="off"></head><body>${sanitizedEmail(html)}</body></html>`;
}
