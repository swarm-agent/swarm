import assert from 'node:assert/strict';
import { test } from 'node:test';
import { SwarmRemoteNamespace } from '../remote.js';
import type { SwarmTransport } from '../transport.js';

// Purpose: the owner relay namespace must call only the fixed /v1/remote
// routes, send explicit booleans for the ceiling (never inherit truthy junk)
// and unwrap the daemon's {remote} envelope including pairing state.
// Boundary: SwarmRemoteNamespace; a transport fixture is the wire contract,
// not a live relay connection.
test('remote namespace uses fixed owner routes and unwraps pairing status', async () => {
  const calls: Array<[string, string, unknown]> = [];
  const remote = { configured: true, enabled: true, connected: false, allow_write: true, allow_approve: false, allow_manage: false,
    pending_consents: [], pairing_code: 'ABCD-EF23', pairing_expires_at: 1 };
  const transport = { request: async (path: string, options: { method: string; body?: unknown }) => {
    calls.push([options.method, path, options.body]);
    return { data: { ok: true, remote, consent: { code: 'WXYZ-2345' } } };
  } } as unknown as SwarmTransport;
  const ns = new SwarmRemoteNamespace(transport);
  assert.equal((await ns.status()).pairing_code, 'ABCD-EF23');
  await ns.init({ relay_url: 'https://relay.example', device_name: 'box', allow_write: true, allow_manage: 'yes' as unknown as boolean });
  await ns.enable();
  await ns.disable();
  await ns.reset();
  assert.equal((await ns.decideConsent('WXYZ-2345', true)).code, 'WXYZ-2345');
  await ns.decideConsent('WXYZ-2345', false, ['swarm:write']);
  assert.deepEqual(calls, [
    ['GET', '/v1/remote', undefined],
    ['POST', '/v1/remote/init', { relay_url: 'https://relay.example', device_name: 'box', allow_write: true, allow_approve: false, allow_manage: false }],
    ['POST', '/v1/remote/enable', {}],
    ['POST', '/v1/remote/disable', {}],
    ['POST', '/v1/remote/reset', {}],
    ['POST', '/v1/remote/consents/approve', { code: 'WXYZ-2345' }],
    ['POST', '/v1/remote/consents/deny', { code: 'WXYZ-2345' }],
  ]);
});
