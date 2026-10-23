import test from 'node:test';
import assert from 'node:assert/strict';
import {createFleet, parseFleet, accessLevel} from '../server.mjs';

// Purpose: createFleet must return named gateway capabilities before human input,
// not hide the link in notifications or deadlock on provider outcomes. A machine
// whose status never finishes isolates this orchestration boundary without a
// live provider or browser; unknown destinations must cause no machine calls.
test('fleet provider key returns pages without waiting or notifications', {timeout:5000}, async t => {
 const machines = parseFleet(JSON.stringify(['one','two'].map(name => ({name,url:`https://${name}.example.ts.net:8444/mcp`}))));
 const calls = [], notifications = [];
 const fleet = createFleet(machines,{notify: message => notifications.push(message),send:async (machine,msg) => {
  calls.push([machine.name,msg.params]);
  if (msg.params.name === 'swarm_list_providers') return new Promise(() => {});
  assert.equal(msg.params.name,'swarm_request_provider_key');
  assert.deepEqual(msg.params.arguments,{provider:'openai'});
  const value = {url:`https://${machine.name}.example.ts.net:8444/keys/${'A'.repeat(43)}`,expires_in_seconds:machine.name === 'one' ? 600 : 590};
  return {result:{content:[{type:'text',text:JSON.stringify(value)}]}};
 }});
 t.after(() => fleet.stop());
 const call = async (name,args) => (await fleet.handle({id:1,method:'tools/call',params:{name,arguments:args,_meta:{progressToken:'key-request'}}})).result;
 const args = {provider:'openai',machines:['one','two']};
 const response = await call('swarm_fleet_request_provider_key',args);
 assert.notEqual(response.isError,true);
 const result = JSON.parse(response.content[0].text);
 const url = new URL(result.url);
 assert.equal(url.origin,'https://one.example.ts.net:8444');
 assert.equal(url.pathname,`/keys/${'A'.repeat(43)}`);
 assert.deepEqual(JSON.parse(Buffer.from(url.hash.slice(6),'base64url')), [`https://two.example.ts.net:8444/keys/${'A'.repeat(43)}`]);
 assert.equal(result.tell_the_person,'Open this link on a device in your tailnet to save the provider key to these machines: one, two; check them against the hostnames on the page, then paste the key once, never into chat.');
 assert.equal(result.expires_in_seconds,590);
 assert.equal(result.next,'call swarm_list_providers with machine, provider and wait_seconds (at most 110) for each machine to wait for the outcome');
 assert.deepEqual(result.machines,[{machine:'one',status:'waiting'},{machine:'two',status:'waiting'}]);
 assert.deepEqual(calls.map(([name]) => name),['one','two']);
 assert.deepEqual(notifications,[]);
 assert.equal(accessLevel(['swarm_request_provider_key']),'read and write');
 const rejected = await call('swarm_fleet_request_provider_key',{...args,machines:['missing']});
 assert.equal(rejected.isError,true);
 assert.equal(calls.length,2);
 assert.deepEqual(notifications,[]);
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
