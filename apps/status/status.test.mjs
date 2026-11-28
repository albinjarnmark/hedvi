import test from "node:test";
import assert from "node:assert/strict";
import {classify,combineObservations,retainPublished} from "./status.mjs";
const now = Date.parse("2026-11-28T10:00:00Z");
const valid = () => ({checked_at: new Date(now).toISOString(), components: ["website","dashboard","api"].map(id => ({id,status:"passing"})), incident: {state:"none"}});
test("stale, absent, future and incomplete evidence never reports passing", () => {
  assert.equal(classify(null,now),"unknown");
  for (const delta of [-1200001,60001]) { const s=valid();s.checked_at=new Date(now+delta).toISOString();assert.equal(classify(s,now),"unknown"); }
  const s=valid();s.components[2].id="website";assert.equal(classify(s,now),"unknown");
  s.components=[];assert.equal(classify(s,now),"unknown");
  s.components=[null,null,null];assert.equal(classify(s,now),"unknown");
  s.components={};assert.equal(classify(s,now),"unknown");
});
test("provider observation and declared incident both affect overall state", () => {
  const s=valid();assert.equal(classify(s,now),"passing");
  s.components[2].status="failing";assert.equal(classify(s,now),"issue");
  s.components[2].status="passing";s.incident={state:"active",title:"Sending delay",message:"Investigating",updated_at:new Date(now).toISOString()};assert.equal(classify(s,now),"issue");
});

test("fresh independent observations replace stale checks while preserving incidents", () => {
 const saved=valid();saved.checked_at=new Date(now-3600000).toISOString();
 const live=valid();delete live.incident;
 assert.equal(classify(combineObservations(saved,live,now),now),"passing");
 saved.incident={state:"active",title:"Delivery delay",message:"Investigating",updated_at:new Date(now).toISOString()};
 assert.equal(classify(combineObservations(saved,live,now),now),"issue");
 assert.equal(classify(combineObservations(null,live,now),now),"unknown");
 assert.equal(classify(combineObservations(saved,null,now),now),"unknown");
 live.components[2].status="failing";
 assert.equal(classify(combineObservations(valid(),live,now),now),"issue");
});

test("latest valid check wins even when the older cached check passes", () => {
 const saved=valid();saved.components[2].status="failing";
 const live=valid();live.checked_at=new Date(now-50000).toISOString();
 assert.equal(classify(combineObservations(saved,live,now),now),"issue");
 saved.checked_at=new Date(now-55000).toISOString();
 assert.equal(classify(combineObservations(saved,live,now),now),"passing");
});
test("transient or malformed snapshot fetch cannot erase an existing incident", () => {
 const saved=valid();saved.incident={state:"active",title:"Delay",message:"Investigating",updated_at:new Date(now).toISOString()};
 for (const incoming of [null,{}, {...valid(),incident:{state:"unknown"}}]) {
  assert.equal(retainPublished(saved,incoming,now),saved);
  assert.equal(classify(combineObservations(retainPublished(saved,incoming,now),valid(),now),now),"issue");
 }
 assert.equal(retainPublished(saved,valid(),now).incident.state,"none");
});

test("failed refresh cannot replace newer known failure with older green evidence", () => {
 const saved=valid();saved.checked_at=new Date(now-300000).toISOString();
 const live=valid();live.components[2].status="failing";
 const previous=combineObservations(saved,live,now);
 assert.equal(classify(combineObservations(saved,null,now+60000,previous),now+60000),"issue");
 assert.equal(classify(combineObservations(saved,null,now+1200001,previous),now+1200001),"unknown");
 const recovered=valid();recovered.checked_at=new Date(now+60000).toISOString();
 assert.equal(classify(combineObservations(saved,recovered,now+60000,previous),now+60000),"passing");
});
