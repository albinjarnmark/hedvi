// Public health observations only. No bindings, secrets or customer data.
const targets = [
  ["website", "https://hedvi.dev/"],
  ["dashboard", "https://app.hedvi.dev/login"],
  ["api", "https://api.hedvi.dev/readyz"],
];
const headers = {"Content-Type":"application/json", "Access-Control-Allow-Origin":"https://status.hedvi.dev", "Cache-Control":"public, max-age=60", "X-Content-Type-Options":"nosniff"};
let pending;

async function readBounded(response) {
  const reader = response.body?.getReader();
  if (!reader) throw new Error("Empty response");
  const chunks=[]; let bytes=0;
  try {
    while (bytes < 65536) {
      const part=await reader.read();
      if (part.done) break;
      chunks.push(part.value.subarray(0,65536-bytes)); bytes+=part.value.length;
    }
  } finally { await reader.cancel(); }
  const out=new Uint8Array(Math.min(bytes,65536)); let offset=0;
  for (const chunk of chunks) { out.set(chunk,offset); offset+=chunk.length; }
  return new TextDecoder().decode(out);
}

export async function observe(fetcher=fetch) {
  const components=await Promise.all(targets.map(async ([id,url]) => {
    for (let attempt=0;attempt<2;attempt++) {
      const started=Date.now();
      try {
        const response=await fetcher(url,{redirect:"manual",signal:AbortSignal.timeout(4000),headers:{"User-Agent":"Hedvi-Status/2.0","Cache-Control":"no-cache"}});
        const body=await readBounded(response);
        const valid=id==="api" ? JSON.parse(body).status==="ready" : response.headers.get("content-type")?.includes("text/html") && /<html[\s>]/i.test(body) && (id==="dashboard" ? body.includes('name="email"') && body.includes('type="password"') : body.includes("Email, without the overhead.") && body.includes("/signup"));
        if (response.status===200 && valid) return {id,status:"passing",duration_ms:Date.now()-started};
      } catch { /* Publish neither upstream bodies nor exception details. */ }
    }
    return {id,status:"failing"};
  }));
  return {checked_at:new Date().toISOString(),components};
}

export default {
  async fetch(request,env,ctx) {
    const url=new URL(request.url);
    if (url.pathname!=="/status.json") return new Response("Not found",{status:404});
    if (!["GET","HEAD"].includes(request.method)) return new Response("Method not allowed",{status:405,headers:{Allow:"GET, HEAD"}});
    const cacheKey=new Request(url.origin+"/status.json");
    const cache=caches.default;
    let response=await cache.match(cacheKey);
    if (!response) {
      if (!pending) pending=observe().finally(()=>{pending=undefined;});
      const snapshot=await pending;
      response=new Response(JSON.stringify(snapshot),{headers});
      ctx.waitUntil(cache.put(cacheKey,response.clone()));
    }
    return request.method==="HEAD" ? new Response(null,{status:response.status,headers:response.headers}) : response;
  }
};
