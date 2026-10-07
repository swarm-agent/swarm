import assert from 'node:assert/strict';
import { test } from 'node:test';
import { SwarmApiError } from '@swarm-agent/sdk';
import { operations } from '../operations.mjs';

const assignment = { provider: 'codex', model: 'm', thinking: 'low' };
function fakeSdk({ owner = true, credentials = 1, models = true, workspaces = ['/project/bot'], remote = {} } = {}) {
  const calls = [];
  let status = { configured: false, enabled: false, connected: false, allow_write: false, allow_approve: false, allow_manage: false, pending_consents: [], ...remote };
  const sdk = {
    calls,
    onboarding: { get: async () => ({ identity: { bootstrapped: owner }, heuristics: { credential_count: credentials } }) },
    settings: { agentModels: async () => ({ agent_model_settings: models ? { swarm: { action: assignment, plan: assignment },
      system_agents: { compact: assignment, finder: assignment, coder: assignment, designer: assignment, router: assignment } } :
      { swarm: { action: assignment, plan: {} }, system_agents: {} } }) },
    workspaces: { list: async () => workspaces.map(path => ({ path })) },
    remote: {
      status: async () => { calls.push('status'); return status; },
      init: async input => { calls.push(['init', input]); if (!input.relay_url.startsWith('https://')) throw new SwarmApiError('relay URL must use https', { status: 400 });
        status = { ...status, configured: true, relay_url: new URL(input.relay_url).origin, device_name: input.device_name }; return status; },
      enable: async () => { calls.push('enable'); status = { ...status, enabled: true, pairing_code: 'ABCD-EF23', pairing_expires_at: 9e12, public_key: 'k' }; return status; },
      disable: async () => { calls.push('disable'); return status; },
      reset: async () => { calls.push('reset'); return status; },
      decideConsent: async (code, approve) => { calls.push(['consent', code, approve]); if (code === 'GONE-0000') throw new SwarmApiError('not pending', { status: 400 }); return { code }; },
    },
  };
  return sdk;
}

// Purpose: the guide reports the next unfinished step from canonical state and
// never touches relay administration before an owner exists (it is owner-only).
// Boundary: operations().run('setup') with an SDK fixture; real daemon
// onboarding and relay behaviour are covered by the container/relay checks.
test('setup reports step completion and installer defaults', async () => {
  const none = fakeSdk({ owner: false });
  const before = await operations(none, '/project', { relayUrl: 'https://relay.example', deviceName: 'box' }).run('setup', {});
  assert.deepEqual(before.steps, { owner: false, provider: false, models: false, workspace: false, claude: false });
  assert.equal(before.remote, null);
  assert.deepEqual(before.defaults, { relay_url: 'https://relay.example', device_name: 'box' });
  assert.deepEqual(none.calls, []);

  const partial = await operations(fakeSdk({ models: false, workspaces: ['/elsewhere/repo'] })).run('setup', {});
  assert.deepEqual(partial.steps, { owner: true, provider: true, models: false, workspace: false, claude: false });
  const done = await operations(fakeSdk({ remote: { configured: true, enabled: true, connected: true } })).run('setup', {});
  assert.deepEqual(done.steps, { owner: true, provider: true, models: true, workspace: true, claude: true });
});

// Purpose: Connect initializes once with explicit boolean ceilings, enables,
// surfaces the pairing code, refuses silently switching relays, surfaces the
// daemon's key-free validation message, and never returns key material.
test('remote-connect initializes, enables and exposes pairing without keys', async () => {
  const sdk = fakeSdk();
  const ops = operations(sdk);
  const connected = await ops.run('remote-connect', { relay_url: 'https://relay.example/', device_name: 'box', allow_write: true, allow_manage: 'yes' });
  assert.equal(connected.pairing_code, 'ABCD-EF23');
  assert.equal('public_key' in connected, false);
  assert.deepEqual(sdk.calls[1], ['init', { relay_url: 'https://relay.example/', device_name: 'box', allow_write: true, allow_approve: false, allow_manage: false }]);
  await ops.run('remote-connect', { relay_url: 'https://relay.example', device_name: 'ignored' });
  assert.equal(sdk.calls.filter(c => c[0] === 'init').length, 1);
  await assert.rejects(ops.run('remote-connect', { relay_url: 'https://other.example', device_name: 'box' }), /set up for https:\/\/relay.example/);

  await assert.rejects(operations(fakeSdk()).run('remote-connect', { relay_url: 'http://relay.example', device_name: 'box' }), /relay URL must use https/);
  await assert.rejects(ops.run('remote-reset', {}), /Confirm/);
  await ops.run('remote-reset', { confirm: true });
  assert.ok(sdk.calls.includes('reset'));
});

test('remote-consent decides only the given code and reports stale requests', async () => {
  const sdk = fakeSdk();
  const ops = operations(sdk);
  await ops.run('remote-consent', { code: 'WXYZ-2345', approve: true });
  await ops.run('remote-consent', { code: 'WXYZ-2345', approve: 'true' });
  assert.deepEqual(sdk.calls.filter(c => c[0] === 'consent'), [['consent', 'WXYZ-2345', true], ['consent', 'WXYZ-2345', false]]);
  await assert.rejects(ops.run('remote-consent', { code: 'GONE-0000', approve: true }), /no longer pending/);
});
