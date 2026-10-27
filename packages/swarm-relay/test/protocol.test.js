import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import { register } from 'node:module';
import test from 'node:test';
import { LOCAL_ONLY_TOOLS, SCOPES, SCOPE_READ, SCOPE_WRITE, TOOL_SCOPES, toolScope } from '../src/protocol.js';

register('./cloudflare-loader.js', import.meta.url);
const { Fleet } = await import('../src/fleet.js');

// Purpose: prevent permission drift from remote.toolScopes/localOnlyTools.
// This narrow map parity check complements the observable Fleet boundary tests below;
// it is not itself proof of daemon authorization or Cloudflare transport behavior.
test('relay scope and local-only maps match every daemon entry', async () => {
  const source = await readFile(new URL('../../../swarmd/internal/remote/service.go', import.meta.url), 'utf8');
  const scopeBlock = source.match(/var toolScopes = map\[string\]string\{([\s\S]*?)\n\}/)?.[1];
  assert.ok(scopeBlock);
  const daemonScopes = Object.fromEntries([...scopeBlock.matchAll(/"(swarm_\w+)":\s+Scope(Read|Write|Approve|Manage)/g)]
    .map(([, name, scope]) => [name, `swarm:${scope.toLowerCase()}`]));
  const { swarm_list_machines, swarm_pair_machine, swarm_remove_machine, ...deviceScopes } = TOOL_SCOPES;
  assert.deepEqual(deviceScopes, daemonScopes);
  assert.equal(swarm_list_machines, SCOPE_READ);
  assert.equal(swarm_pair_machine, 'swarm:manage');
  assert.equal(swarm_remove_machine, 'swarm:manage');
  const localBlock = source.match(/var localOnlyTools = map\[string\]bool\{([\s\S]*?)\n\}/)?.[1];
  assert.ok(localBlock);
  assert.deepEqual([...LOCAL_ONLY_TOOLS].sort(), [...localBlock.matchAll(/"(swarm_\w+)":\s+true/g)].map(([, name]) => name).sort());
  assert.equal(toolScope('swarm_unknown_tool'), SCOPE_WRITE);
});

function fleetFixture() {
  const fleet = Object.create(Fleet.prototype);
  const calls = [];
  fleet.devices = () => [{ deviceId: 'test-device' }];
  // Deliberately over-advertise: the relay must enforce its own scope/local-only
  // boundary even if a device returns tools the client must not see.
  fleet.forward = async (device, message, auth) => {
    calls.push({ device, message, auth });
    if (message.method === 'tools/list') {
      return { result: { tools: Object.keys(TOOL_SCOPES).filter((name) => !['swarm_list_machines', 'swarm_pair_machine', 'swarm_remove_machine'].includes(name))
        .map((name) => ({ name, inputSchema: { type: 'object', properties: {} } })) } };
    }
    return { result: { content: [{ type: 'text', text: 'defined' }] } };
  };
  return { fleet, calls };
}

// Purpose: Fleet.mcp/listTools/callTool must expose custom agent definitions to
// read+write clients, reject read-only calls without forwarding, and preserve
// local-only setup restrictions. A fake device transport isolates the real relay
// authorization boundary without needing credentials or a live Cloudflare worker.
test('write device lists and forwards define-agent; read device hides and rejects it', async () => {
  for (const scopes of [[SCOPE_READ], [SCOPE_READ, SCOPE_WRITE]]) {
    const { fleet, calls } = fleetFixture();
    const auth = { scope: scopes };
    const list = await fleet.mcp({ jsonrpc: '2.0', id: 1, method: 'tools/list' }, auth);
    const canWrite = scopes.includes(SCOPE_WRITE);
    assert.equal(list.result.tools.some((tool) => tool.name === 'swarm_define_agent'), canWrite);
    const before = calls.length;
    const response = await fleet.mcp({ jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'swarm_define_agent', arguments: { name: 'test-agent' } } }, auth);
    if (canWrite) {
      assert.deepEqual(response.result.content, [{ type: 'text', text: 'defined' }]);
      assert.equal(calls.length, before + 1);
      assert.equal(calls.at(-1).message.params.name, 'swarm_define_agent');
      assert.deepEqual(calls.at(-1).auth, auth);
    } else {
      assert.equal(response.result.isError, true);
      assert.match(response.result.content[0].text, /not authorized/);
      assert.equal(calls.length, before);
    }
  }
});

// Purpose: Fleet.listTools/callTool must fail closed for all daemon local-only
// setup tools even with manage scopes, with no unauthorized forwarding side effect.
test('local-only tools are hidden and never forwarded even with all scopes', async () => {
  const { fleet, calls } = fleetFixture();
  const list = await fleet.listTools(SCOPES);
  const before = calls.length;
  for (const name of LOCAL_ONLY_TOOLS) {
    assert.equal(list.some((tool) => tool.name === name), false, name);
    const result = await fleet.callTool({ name }, { scope: SCOPES }, SCOPES);
    assert.equal(result.isError, true, name);
    assert.match(result.content[0].text, /only available locally/);
    assert.equal(calls.length, before, name);
  }
  assert.equal(list.some((tool) => tool.name === 'swarm_define_agent'), true);
});
