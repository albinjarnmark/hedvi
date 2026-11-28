import { describe, expect, it } from "vitest";
import { cloudflareZoneFile } from "./dns-import";
const domain="mail.example.com";
const ownership={record:"Ownership",type:"TXT",fqdn:`_hedvi-verification.${domain}`,value:"hedvi-domain=00000000-0000-4000-8000-000000000001"};
describe("Cloudflare DNS import",()=>{
 it("uses absolute names and enforces DNS-only DKIM",()=>{
  const file=cloudflareZoneFile(domain,[ownership,{record:"DKIM",type:"CNAME",fqdn:`selector._domainkey.${domain}`,value:"selector.dkim.amazonses.com"},{record:"SPF",type:"MX",fqdn:`send.${domain}`,value:"feedback-smtp.eu-north-1.amazonses.com",priority:10},{record:"SPF",type:"TXT",fqdn:`send.${domain}`,value:"v=spf1 include:amazonses.com ~all"}],`send.${domain}`);
  expect(file).toContain(`selector._domainkey.${domain}. 300 IN CNAME selector.dkim.amazonses.com. ; cf_tags=cf-proxied:false`);
  expect(file).toContain("IN MX 10 feedback-smtp.eu-north-1.amazonses.com.");
  expect(file).not.toContain("\nexample.com.");
 });
 it("exports the ownership-only first step",()=>expect(cloudflareZoneFile(domain,[ownership],`send.${domain}`)).toContain(ownership.value));
 it("rejects root mail changes, external names and injected records",()=>{
  for(const record of [{...ownership,fqdn:"example.com"},{...ownership,fqdn:"_hedvi-verification.attacker.test"},{...ownership,value:'bad"\n@ IN A 1.2.3.4'},{...ownership,type:"NS"}]) expect(()=>cloudflareZoneFile(domain,[record],`send.${domain}`)).toThrow();
 });
});
