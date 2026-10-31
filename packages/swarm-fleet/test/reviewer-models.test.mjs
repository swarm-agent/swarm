// createFleet must list and apply reviewer assignments via the canonical role,
// through each machine's catalog (manage_setup models and set_agent_model,
// answered in the catalog envelope). An injected MCP transport isolates the
// fleet mapping and verifies actual call arguments, including rejection
// propagation rather than a false success.
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
  const envelope = (action, data) => text({ tool: 'manage_setup', action, state: 'ok', data });
  const catalogTool = (name, actions) => ({ name, inputSchema: { type: 'object', properties: { action: { type: 'string', enum: actions } } } });
  const fleet = createFleet([{ name: 'one', token: 'fixture' }, { name: 'two', token: 'fixture' }], {
    statePath: join(dir, 'state.json'),
    send: async (machine, msg) => {
      if (msg.method === 'tools/list') return { result: { tools: [catalogTool('manage_setup', ['models', 'set_agent_model']), catalogTool('manage_workers', ['list', 'create', 'update'])] } };
      if (msg.method === 'initialize') return { result: {} };
      if (msg.params.name === 'manage_setup' && msg.params.arguments?.action === 'models') return envelope('models', { roles: [{ role: 'system-reviewer', provider: 'test', model: 'review' }] });
      calls.push([machine.name, msg.params]);
      return machine.name === 'two' ? { result: { isError: true, content: [{ text: 'refused' }] } } : envelope('set_agent_model', {});
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
  for (const [, call] of calls) assert.deepEqual(call, { name: 'manage_setup', arguments: { action: 'set_agent_model', role: 'system-reviewer', provider: 'test', model: 'review', thinking: 'high' } });
  assert.equal(set.applied.find(r => r.machine === 'one').ok, true);
  assert.equal(set.applied.find(r => r.machine === 'two').ok, false);
});
