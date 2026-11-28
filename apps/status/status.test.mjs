import test from "node:test";
import assert from "node:assert/strict";
import {classify} from "./status.mjs";
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
