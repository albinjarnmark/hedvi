"use client";
import { useState } from "react";
import { cloudflareZoneFile, type SendingDNSRecord } from "@/lib/dns-import";

type ExportData = {name:string;records:SendingDNSRecord[];hedvi:{mail_from_domain:string;analysis:{mail_from_safety?:string}}};

export function CloudflareSetup({domain,records,prepareExport,onCheck,pending}:{domain:string;records:SendingDNSRecord[];prepareExport:()=>Promise<ExportData>;onCheck:()=>void;pending:boolean}) {
  const [open,setOpen]=useState(false),[downloaded,setDownloaded]=useState(false),[error,setError]=useState("");
  const [checking,setChecking]=useState(false);
  const ownershipOnly=records.length===1&&records[0]?.record==="Ownership";
  async function download() {
    setChecking(true);setError("");setDownloaded(false);
    try {
      const current=await prepareExport();
      const text=cloudflareZoneFile(current.name,current.records,current.hedvi.mail_from_domain,current.hedvi.analysis.mail_from_safety);
      const url=URL.createObjectURL(new Blob([text],{type:"text/plain;charset=utf-8"}));
      const a=document.createElement("a");a.href=url;a.download=`hedvi-${domain}-dns.txt`;a.click();
      setTimeout(()=>URL.revokeObjectURL(url),1000);setDownloaded(true);setError("");
    } catch (err) {setError(err instanceof Error ? err.message : "Could not check DNS. Try again.");}
    finally {setChecking(false);}
  }
  return <section className="card stack" aria-label="Cloudflare quick setup">
    <div className="row" style={{justifyContent:"space-between"}}><div><h2>Using Cloudflare?</h2><p className="small muted" style={{marginBottom:0}}>Import {ownershipOnly?"your ownership record":"all sending records"} at once.</p></div><button type="button" className="btn" aria-expanded={open} onClick={()=>setOpen(!open)}>{open?"Close setup":"Quick setup with Cloudflare"}</button></div>
    {open&&<div className="stack">
      <ol className="stack small">
        <li><button type="button" className="btn btn-primary btn-small" disabled={checking} onClick={download}>{checking?"Checking DNS…":"Download DNS records"}</button>{downloaded&&<span role="status"> File ready.</span>}</li>
        <li><a href="https://dash.cloudflare.com/?to=/:account/:zone/dns/records" target="_blank" rel="noreferrer">Open Cloudflare DNS ↗</a>. Choose the zone containing <strong>{domain}</strong>, then <strong>Import → Import DNS records</strong>.</li>
        <li>Choose the downloaded file and confirm the import. The file sets DKIM to <strong>DNS only</strong>. Keep existing DNS records. If Cloudflare reports a conflicting record, compare it with the table below before changing anything.</li>
      </ol>
      <p className="small muted">{ownershipOnly?"After import, check the domain to generate its DKIM and MAIL FROM records. Then download and import the updated file.":"This file contains only Hedvi's ownership, DKIM and sending subdomain records. The file does not include your root MX, SPF or DMARC records."}</p>
      {error&&<p role="alert" className="error">{error}</p>}
      <div><button type="button" className="btn btn-primary" disabled={pending} onClick={onCheck}>I imported the records — check domain</button></div>
    </div>}
  </section>;
}
