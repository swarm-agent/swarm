// Purpose: swarm-fleet serves each machine's catalog tools with `machine` and
// no swarm_* tool: those were removed from Swarm Control, and an older machine
// that still lists one must not get it listed or forwarded (no pass-through,
// no alias). Access comes from the catalog actions a machine lists. Threat: a
// removed tool reaching a machine through the fleet, or the fleet reporting
// access from tool names that no longer exist. Boundary: createFleet's
// tools/list and tools/call with an injected transport (no tailnet, no
// machine), the narrowest layer that sees what crosses to a machine.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtempSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { createFleet, accessLevel, catalogActions } from '../server.mjs';

const catalogTool = (name, actions) => ({ name, description: name, inputSchema: { type: 'object', properties: { action: { type: 'string', enum: actions }, machine: { type: 'string' } }, required: ['action'] } });

test('the fleet lists and forwards catalog tools only', async t => {
  const dir = mkdtempSync(join(tmpdir(), 'fleet-catalog-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  const forwarded = [];
  const fleet = createFleet([{ name: 'one', token: 'fixture', url: new URL('https://one.example.ts.net:8444/mcp') }], {
    statePath: join(dir, 'state.json'),
    send: async (machine, msg) => {
      if (msg.method === 'initialize') return { result: {} };
      if (msg.method === 'tools/list') return { result: { tools: [{ name: 'swarm_status', inputSchema: { type: 'object', properties: {} } }, catalogTool('manage_fleet', ['status', 'activity'])] } };
      forwarded.push([machine.name, msg.params]);
      return { result: { content: [{ type: 'text', text: JSON.stringify({ tool: msg.params.name, action: msg.params.arguments?.action, state: 'ok', data: {} }) }] } };
    },
  });
  t.after(() => fleet.stop());
  const listed = (await fleet.handle({ id: 1, method: 'tools/list' })).result.tools;
  const names = listed.map(tool => tool.name);
  assert.ok(names.includes('manage_fleet'), `catalog tool missing: ${names}`);
  assert.ok(!names.includes('swarm_status'), `removed tool listed: ${names}`);
  assert.equal(listed.find(tool => tool.name === 'manage_fleet').inputSchema.properties.machine.description.includes('swarm_list_machines'), true);

  const refused = (await fleet.handle({ id: 2, method: 'tools/call', params: { name: 'swarm_status', arguments: { machine: 'one' } } })).result;
  assert.equal(refused.isError, true);
  assert.match(refused.content[0].text, /swarm_\* tools were removed/);
  assert.deepEqual(forwarded, []);

  const answered = (await fleet.handle({ id: 3, method: 'tools/call', params: { name: 'manage_fleet', arguments: { machine: 'one', action: 'status' } } })).result;
  assert.notEqual(answered.isError, true);
  assert.deepEqual(forwarded, [['one', { name: 'manage_fleet', arguments: { action: 'status' } }]]);
});

test('access comes from the catalog actions a machine lists', () => {
  assert.equal(accessLevel(null), 'none');
  assert.deepEqual(catalogActions([{ name: 'swarm_manage_worker' }, catalogTool('manage_workers', ['list', 'create'])]), ['manage_workers list', 'manage_workers create']);
  assert.equal(accessLevel(catalogActions([catalogTool('manage_workers', ['list', 'create'])])), 'full control');
  assert.equal(accessLevel(catalogActions([catalogTool('manage_sessions', ['get', 'create', 'resolve_permission'])])), 'read, write and approve');
  assert.equal(accessLevel(catalogActions([catalogTool('manage_jobs', ['get_job', 'give_job'])])), 'read and write');
  assert.equal(accessLevel(catalogActions([catalogTool('manage_projects', ['list', 'get'])])), 'read only');
  // A removed swarm_* tool name grants nothing.
  assert.equal(accessLevel(catalogActions([{ name: 'swarm_manage_worker' }, { name: 'swarm_resolve_permission' }])), 'read only');
});
