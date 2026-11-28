import test from 'node:test';
import assert from 'node:assert/strict';
import worker,{observe} from './probe-worker.mjs';
const reply=url=>new Response(url.endsWith('/readyz')?' {"status":"ready"} ':'<!doctype html><html lang="en">Email, without the overhead. /signup <input name="email"><input type="password">',{headers:{'content-type':url.endsWith('/readyz')?'application/json':'text/html'}});
test('probes only fixed public targets and never follows redirects',async()=>{
 const seen=[];const result=await observe(async(url,opts)=>{seen.push(url);assert.equal(opts.redirect,'manual');return reply(url);});
 assert.deepEqual(seen,['https://hedvi.dev/','https://app.hedvi.dev/login','https://api.hedvi.dev/readyz']);
 assert.ok(result.components.every(c=>c.status==='passing'));
 assert.ok(Number.isFinite(Date.parse(result.checked_at)));
});
test('retries failure and rejects incorrect readiness without publishing upstream details',async()=>{
 const counts={};const result=await observe(async url=>{counts[url]=(counts[url]||0)+1;return url.endsWith('/readyz')?new Response('{"status":"secret internal error"}'):reply(url);});
 assert.equal(counts['https://api.hedvi.dev/readyz'],2);
 assert.deepEqual(result.components[2],{id:'api',status:'failing'});
 assert.ok(!JSON.stringify(result).includes('secret'));
});
test('redirects and non-HTML login pages fail closed',async()=>{
 const result=await observe(async()=>new Response('login redirect',{status:302,headers:{location:'https://private.invalid/'}}));
 assert.ok(result.components.every(c=>c.status==='failing'));
});
test('arbitrary targets and mutation methods are rejected before any probe',async()=>{
 assert.equal((await worker.fetch(new Request('https://probe.invalid/other?url=https://private.invalid'))).status,404);
 assert.equal((await worker.fetch(new Request('https://probe.invalid/status.json',{method:'POST'}))).status,405);
});

test('maintenance HTML is not a working website or sign-in form',async()=>{
 const result=await observe(async()=>new Response('<html>Maintenance</html>',{headers:{'content-type':'text/html'}}));
 assert.ok(result.components.every(c=>c.status==='failing'));
});
