import test from 'node:test';
import assert from 'node:assert/strict';
import {createFleet, parseFleet, accessLevel} from '../server.mjs';

// Purpose: createFleet must combine only named gateway capabilities, never
// handle secrets, and wait separately so the person receives the link before
// completion. Stub MCP machines isolate this orchestration boundary.
test('fleet provider key combines pages and reports per-machine outcomes', async () => {
 const machines = parseFleet(JSON.stringify(['one','two'].map(name => ({name,url:`https://${name}.example.ts.net:8444/mcp`}))));
 const calls = [];
 const fleet = createFleet(machines,{send:async (machine,msg) => {
  calls.push([machine.name,msg.params]);
  const value = msg.params.name === 'swarm_request_provider_key'
   ? {url:`https://${machine.name}.example.ts.net:8444/keys/${'A'.repeat(43)}`}
   : {providers:[{provider:'openai',key_request:{status:machine.name === 'one' ? 'saved' : 'failed'}}]};
  return {result:{content:[{type:'text',text:JSON.stringify(value)}]}};
 }});
 const call = async (name,args) => (await fleet.handle({id:1,method:'tools/call',params:{name,arguments:args}})).result;
 const args = {provider:'openai',machines:['one','two']};
 const result = JSON.parse((await call('swarm_fleet_request_provider_key',args)).content[0].text);
 const url = new URL(result.url);
 assert.equal(url.origin,'https://one.example.ts.net:8444');
 assert.deepEqual(JSON.parse(Buffer.from(url.hash.slice(6),'base64url')), [`https://two.example.ts.net:8444/keys/${'A'.repeat(43)}`]);
 assert.equal(calls.length,2);
 const waited = JSON.parse((await call(result.next.tool,result.next.arguments)).content[0].text);
 assert.deepEqual(waited.machines.map(m => [m.machine,m.providers[0].key_request.status]),[['one','saved'],['two','failed']]);
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
