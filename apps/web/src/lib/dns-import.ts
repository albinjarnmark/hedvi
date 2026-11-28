export type SendingDNSRecord = { record: string; fqdn: string; type: string; value: string; priority?: number; status?: string };
const dnsName = /^(?=.{1,253}$)(?:[a-z0-9_](?:[a-z0-9_-]{0,61}[a-z0-9_])?\.)+[a-z0-9-]+$/;

// Export only Hedvi's own records. Fully qualified names prevent a subdomain
// import from accidentally changing the parent zone's mail or website records.
export function cloudflareZoneFile(domain: string, records: SendingDNSRecord[], mailFromDomain: string, mailFromSafety?: string): string {
  if (!dnsName.test(domain) || records.length < 1 || records.length > 6) throw new Error("Invalid sending domain records.");
  if (records.some(r => r.record === "SPF") && mailFromSafety !== "clear") throw new Error("Check the MAIL FROM subdomain for existing records before importing.");
  const mailFromLabel = mailFromDomain.slice(0, -(`.${domain}`).length);
  if (mailFromDomain !== `${mailFromLabel}.${domain}` || !/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(mailFromLabel)) throw new Error("Invalid sending subdomain.");
  const lines = records.map(r => {
    if (!dnsName.test(r.fqdn) || !r.fqdn.endsWith(`.${domain}`)) throw new Error("DNS record is outside the sending domain.");
    let content: string;
    if (r.record === "Ownership" && r.type === "TXT" && r.fqdn === `_hedvi-verification.${domain}` && /^hedvi-domain=[0-9a-f-]{36}$/.test(r.value)) {
      content = `"${r.value}"`;
    } else if (r.record === "DKIM" && r.type === "CNAME") {
      const selector = r.fqdn.slice(0, -`._domainkey.${domain}`.length);
      if (!r.fqdn.endsWith(`._domainkey.${domain}`) || !/^[a-z0-9]{1,63}$/.test(selector) || r.value !== `${selector}.dkim.amazonses.com`) throw new Error("Invalid DKIM record.");
      content = `${r.value}.`;
    } else if (r.record === "SPF" && r.fqdn === mailFromDomain && r.type === "MX" && r.value === "feedback-smtp.eu-north-1.amazonses.com" && r.priority === 10) {
      content = `10 ${r.value}.`;
    } else if (r.record === "SPF" && r.fqdn === mailFromDomain && r.type === "TXT" && r.value === "v=spf1 include:amazonses.com ~all") {
      content = `"${r.value}"`;
    } else throw new Error("Unexpected DNS record.");
    return `${r.fqdn}. 300 IN ${r.type} ${content}${r.type === "CNAME" ? " ; cf_tags=cf-proxied:false" : ""}`;
  });
  return `; Hedvi sending records for ${domain}\n; Import into the existing Cloudflare zone. Keep DKIM DNS only.\n; Confirm the MAIL FROM host is dedicated to Hedvi before adding its MX and SPF.\n${lines.join("\n")}\n`;
}
