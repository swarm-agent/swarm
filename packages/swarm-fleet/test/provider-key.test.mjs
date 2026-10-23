import test from 'node:test';
import assert from 'node:assert/strict';
import {createFleet, parseFleet, accessLevel} from '../server.mjs';

// Purpose: createFleet must combine only named gateway capabilities, never
// handle secrets, and notify before waiting within the same call. Deferred
// machine replies prove it cannot return early. Stub MCP machines isolate this
// orchestration boundary without a live provider or browser.
test('fleet provider key combines pages and reports per-machine outcomes', {timeout:5000}, async () => {
 const machines = parseFleet(JSON.stringify(['one','two'].map(name => ({name,url:`https://${name}.example.ts.net:8444/mcp`}))));
 const calls = [], notifications = [], releases = [];
 let allWaiting;
 const waiting = new Promise(resolve => { allWaiting = resolve; });
 const fleet = createFleet(machines,{notify: message => notifications.push(message),send:async (machine,msg) => {
  calls.push([machine.name,msg.params]);
  if (msg.params.name === 'swarm_list_providers') {
   assert.equal(notifications[0].method,'notifications/message');
   assert.ok(notifications[0].params.data.url);
   assert.deepEqual(msg.params.arguments,{provider:'openai',wait_seconds:300});
   await new Promise(resolve => { releases.push(resolve); if (releases.length === 2) allWaiting(); });
  }
  const value = msg.params.name === 'swarm_request_provider_key'
   ? {url:`https://${machine.name}.example.ts.net:8444/keys/${'A'.repeat(43)}`}
   : {providers:[{provider:'openai',key_request:{status:machine.name === 'one' ? 'saved' : 'failed'}}]};
  return {result:{content:[{type:'text',text:JSON.stringify(value)}]}};
 }});
 const call = async (name,args) => (await fleet.handle({id:1,method:'tools/call',params:{name,arguments:args,_meta:{progressToken:'key-request'}}})).result;
 const args = {provider:'openai',machines:['one','two']};
 let completed = false;
 const pending = call('swarm_fleet_request_provider_key',args).then(result => { completed = true; return result; });
 await waiting;
 assert.equal(completed,false);
 assert.equal(notifications[1].method,'notifications/progress');
 assert.equal(notifications[1].params.progressToken,'key-request');
 const url = new URL(notifications[0].params.data.url);
 assert.equal(url.origin,'https://one.example.ts.net:8444');
 assert.deepEqual(JSON.parse(Buffer.from(url.hash.slice(6),'base64url')), [`https://two.example.ts.net:8444/keys/${'A'.repeat(43)}`]);
 assert.equal(calls.length,4);
 releases[0]();
 await Promise.resolve();
 assert.equal(completed,false);
 releases[1]();
 const result = JSON.parse((await pending).content[0].text);
 assert.equal(result.url,url.href);
 assert.equal(result.next,undefined);
 assert.deepEqual(result.machines.map(m => [m.machine,m.providers[0].key_request.status]),[['one','saved'],['two','failed']]);
 assert.equal(calls[2][1].arguments.wait_seconds,300);
 assert.equal(accessLevel(['swarm_request_provider_key']),'read and write');
 const rejected = await call('swarm_fleet_request_provider_key',{...args,machines:['missing']});
 assert.equal(rejected.isError,true);
 assert.equal(calls.length,4);
 fleet.stop();
});

// Purpose: returned machine URLs are untrusted; createFleet must not make a
// browser exfiltration link or silently omit a failed named destination.
test('fleet refuses arbitrary key destinations and partial issuance', async () => {
 const fleet = createFleet(parseFleet('[{"name":"one","url":"https://one.example.ts.net:8444/mcp"}]'), {send:async () => ({result:{content:[{type:'text',text:JSON.stringify({url:`https://evil.example.ts.net:8444/keys/${'A'.repeat(43)}`})}]}})});
 const result = (await fleet.handle({id:1,method:'tools/call',params:{name:'swarm_fleet_request_provider_key',arguments:{provider:'openai',machines:['one']}}})).result;
 assert.equal(result.isError,true);
 assert.equal(JSON.parse(result.content[0].text).url,undefined);
 fleet.stop();
});
