import assert from 'node:assert/strict';
import { test } from 'node:test';
import { SwarmWorkersNamespace } from '../workers.js';
import type { SwarmTransport } from '../transport.js';

// Requirement: every control-plane method uses canonical worker HTTP paths and
// preserves CAS/idempotency pins. Threat: SDK helper bypass or silent malformed
// response acceptance. A deterministic transport double is the narrowest wire
// test; this is not a live daemon/provider test.
test('worker control wire contracts and negative envelopes', async () => {
  const calls: {path: string; method: string; body?: unknown}[] = [];
  let response: Record<string, unknown> = {};
  let failure: Error | undefined;
  const transport = { async request(path: string, options: {method: string; body?: unknown}) {
    calls.push({path, method: options.method, body: options.body});
    if (failure) throw failure;
    return {data: response, status: 200};
  }} as unknown as SwarmTransport;
  const control = new SwarmWorkersNamespace(transport).control;
  const target = {kind: 'ssh' as const, workspace_id: 'workspace-1', reference_id: 'connection-1', capacity: 1};
  const registration = {workspace_id:'workspace-1',name:'fixture',host:'host.example.invalid',user:'fixture',port:22,idempotency_key:'register-key'};
  const proposal = {worker_revision: 2, context_revision: 1, target, lifecycle: 'on_demand' as const, idempotency_key: 'proposal-key'};
  const context = {expected_revision: 0, text: 'knowledge', provenance: 'user'};
  const command = {expected_revision: 2, generation: 1, kind: 'stop' as const, idempotency_key: 'stop-key'};
  const job = {worker_revision: 2, deployment_revision: 2, context_revision: 1, idempotency_key: 'job-key', input: {prompt: 'review'}};
  const cases: {method: string; tail: string; field: string; array?: boolean; body?: unknown; invoke: () => Promise<unknown>}[] = [
    {method:'POST',tail:'ssh-targets',field:'target',body:registration,invoke:()=>control.registerSSHTarget('worker-1',registration)},
    {method:'POST',tail:'target-reference',field:'target',body:target,invoke:()=>control.resolveTarget('worker-1',target)},
    {method:'GET',tail:'context?revision=1',field:'context',invoke:()=>control.getContext('worker-1',1)},
    {method:'PUT',tail:'context',field:'context',body:context,invoke:()=>control.updateContext('worker-1',context)},
    {method:'POST',tail:'deployments',field:'deployment',body:proposal,invoke:()=>control.proposeDeployment('worker-1',proposal)},
    {method:'GET',tail:'deployments',field:'deployments',array:true,invoke:()=>control.listDeployments('worker-1')},
    {method:'GET',tail:'deployments/deployment-1',field:'deployment',invoke:()=>control.getDeployment('worker-1','deployment-1')},
    {method:'POST',tail:'deployments/deployment-1/approve',field:'deployment',body:{expected_revision:1,approval_digest:'digest'},invoke:()=>control.approveDeployment('worker-1','deployment-1',1,'digest')},
    {method:'POST',tail:'deployments/deployment-1/jobs',field:'run',body:job,invoke:()=>control.queueJob('worker-1','deployment-1',job)},
    {method:'POST',tail:'deployments/deployment-1/commands',field:'command',body:command,invoke:()=>control.command('worker-1','deployment-1',command)},
    {method:'GET',tail:'deployments/deployment-1/commands',field:'commands',array:true,invoke:()=>control.commands('worker-1','deployment-1')},
  ];
  for (const c of cases) {
    response = {[c.field]: c.array ? [] : {id:'fixture'}};
    await c.invoke();
    assert.deepEqual(calls.at(-1), {path:`/v3/workers/worker-1/${c.tail}`,method:c.method,body:c.body});
    response = {[c.field]: c.array ? {} : []};
    await assert.rejects(c.invoke, /Malformed worker control envelope/);
  }
  const before = calls.length;
  assert.throws(()=>control.getContext('../foreign'), /identity/);
  assert.throws(()=>control.approveDeployment('worker-1','deployment-1',0,'digest'), /revision/);
  assert.throws(()=>control.queueJob('worker-1','deployment-1',{...job,idempotency_key:''}), /idempotency/);
  assert.equal(calls.length,before);
  failure = new Error('503 remote worker execution unavailable');
  await assert.rejects(()=>control.command('worker-1','deployment-1',{...command,kind:'start'}), /503/);
});
