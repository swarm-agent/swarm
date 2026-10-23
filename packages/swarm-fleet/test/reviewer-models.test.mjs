// createFleet must list and apply reviewer assignments via the canonical role.
// An injected MCP transport isolates the fleet mapping and verifies actual call
// arguments, including rejection propagation rather than a false success.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtempSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { createFleet } from '../server.mjs';

test('fleet lists and sets reviewer models on each machine', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'fleet-reviewer-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const calls = [];
  const text = value => ({ result: { content: [{ type: 'text', text: JSON.stringify(value) }] } });
  const fleet = createFleet([{ name: 'one', token: 'fixture' }, { name: 'two', token: 'fixture' }], {
    statePath: join(dir, 'state.json'),
    send: async (machine, msg) => {
      if (msg.method === 'tools/list') return { result: { tools: [{ name: 'swarm_set_agent_model' }, { name: 'swarm_manage_worker' }] } };
      if (msg.method === 'initialize') return { result: {} };
      if (msg.params.name === 'swarm_list_models') return text({ roles: [{ role: 'system-reviewer', provider: 'test', model: 'review' }] });
      calls.push([machine.name, msg.params]);
      return machine.name === 'two' ? { result: { isError: true, content: [{ text: 'refused' }] } } : text({});
    },
  });
  t.after(() => fleet.stop());
  const call = async (name, args = {}) => JSON.parse((await fleet.handle({ id: 1, method: 'tools/call', params: { name, arguments: args } })).result.content[0].text);
  const listed = await call('swarm_fleet_models');
  assert.equal(listed.machines.length, 2);
  for (const machine of listed.machines) {
    assert.deepEqual(machine.roles.find(r => r.role === 'reviewer'), { role: 'reviewer', provider: 'test', model: 'review', thinking: '', state: 'unset' });
  }
  const set = await call('swarm_fleet_set_models', { models: { reviewer: { provider: 'test', model: 'review', thinking: 'high' } } });
  assert.equal(calls.length, 2);
  for (const [, call] of calls) assert.deepEqual(call, { name: 'swarm_set_agent_model', arguments: { role: 'system-reviewer', provider: 'test', model: 'review', thinking: 'high' } });
  assert.equal(set.applied.find(r => r.machine === 'one').ok, true);
  assert.equal(set.applied.find(r => r.machine === 'two').ok, false);
});
