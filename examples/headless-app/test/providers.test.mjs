import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import { readFile } from 'node:fs/promises';
import { SwarmApiError } from '@swarm-agent/sdk';
import { operations, providerKeyRedact } from '../operations.mjs';

function fixture() {
  const calls = [];
  const providers = [{ id: 'exa', runs_agents: false, ready: true, runnable: false, auth_methods: [{ credential_type: 'api', description: 'Search the web' }] },
    ...['vertex', 'claudecode', 'codex'].map(id => ({ id, auth_methods: [{ credential_type: id === 'vertex' ? 'adc' : id === 'codex' ? 'oauth' : 'cli' }] }))];
  const credential = { id: 'one', provider: 'exa', label: 'Search', active: true, auth_type: 'api', connection: { connected: true, verified_at: 1000 }, last4: '1234' };
  const sdk = { onboarding: { get: async () => ({ identity: { bootstrapped: true } }) },
    settings: { providers: async () => providers, agentModels: async () => ({}) },
    workspaces: { list: async () => [] }, remote: { status: async () => ({}) }, permissions: { bypass: async () => false },
    auth: { credentials: { list: async () => ({ records: [credential] }),
      save: async input => { calls.push(['save', input]); return credential; },
      activate: async (...args) => { calls.push(['activate', ...args]); return credential; },
      delete: async (...args) => { calls.push(['delete', ...args]); return { ok: true, deleted: true }; } } } };
  return { sdk, calls, ops: operations(sdk) };
}

// Purpose: operations is the narrow BFF boundary for advertised credential types,
// exact provider/id mutation targets, owner bootstrap, and secret-free responses.
// Negative requests must not reach the fake SDK or expose partial key echoes.
test('provider operations preserve targets, reject invalid mutations and redact secrets', async () => {
  const { sdk, calls, ops } = fixture();
  assert.deepEqual((await ops.run('setup', {})).connected, ['exa']);
  assert.equal((await ops.run('setup', {})).steps.provider, false);
  assert.equal('last4' in (await ops.run('credentials', {})).records[0], false);
  await assert.rejects(ops.run('credential', { provider: 'exa', type: 'oauth', key: 'secret' }), /advertised/);
  await assert.rejects(ops.run('credential-delete', { provider: 'exa', id: 'one' }), /Confirm/);
  assert.deepEqual(calls, []);
  await ops.run('credential', { provider: 'exa', type: 'api', key: 'secret', id: 'one' });
  assert.deepEqual(calls[0], ['save', { provider: 'exa', type: 'api', api_key: 'secret', active: true, id: 'one', label: 'Headless app' }]);
  await ops.run('credential-activate', { provider: 'exa', id: 'one' });
  await ops.run('credential-delete', { provider: 'exa', id: 'one', confirm: true });
  assert.deepEqual(calls.slice(1), [['activate', 'exa', 'one'], ['delete', 'exa', 'one']]);
  sdk.auth.credentials.save = async () => { throw new SwarmApiError('Rejected secret and sk-partly****1234', { status: 400 }); };
  await assert.rejects(ops.run('credential', { provider: 'exa', type: 'api', key: 'secret' }), /Rejected \[redacted\] and \[redacted\]/);
  assert.equal(providerKeyRedact('x '.repeat(200)).length, 300);
  sdk.onboarding.get = async () => ({ identity: { bootstrapped: false } });
  await assert.rejects(ops.run('credential-activate', { provider: 'exa', id: 'one' }), /owner/);
  assert.equal(calls.length, 3);
});

// Purpose: execute the real shared renderer and its handlers through operations
// with a fake SDK. This proves Exa discovery, non-key connect actions, clearing
// secret inputs on failure, and confirmed mutations without a browser dependency.
test('shared Settings provider UI handles Exa, failed saves and credential actions', async () => {
  const { sdk, ops, calls } = fixture();
  class Element {
    constructor(tag) { this.tagName = tag; this.children = []; this.value = ''; this.dataset = {}; }
    addEventListener() {}
    append(...children) { this.children.push(...children); }
    replaceChildren(...children) { this.children = children; }
  }
  const elements = new Map();
  const get = id => { if (!elements.has(id)) elements.set(id, new Element('div')); return elements.get(id); };
  let confirmed = false;
  const context = vm.createContext({ document: { getElementById: get, createElement: tag => new Element(tag) },
    Option: class extends Element { constructor(label, value) { super('option'); this.textContent = label; this.value = value; } },
    window: { addEventListener() {}, confirm: () => confirmed }, invoke: (op, b) => ops.run(op, b) });
  vm.runInContext(await readFile(new URL('../public/app.js', import.meta.url), 'utf8'), context);
  vm.runInContext('call = (path, data) => invoke(data.op, data)', context);
  await vm.runInContext('loadProviders()', context);
  const rows = get('provider-rows').children;
  assert.equal(rows[0].children[0].textContent, 'Exa · web search for agents');
  for (const row of rows.slice(1, 4)) assert.equal(row.children.some(c => c.tagName === 'form'), false);
  assert.match(rows[1].children.find(c => c.tagName === 'code').textContent, /--provider vertex/);
  assert.match(rows[2].children.find(c => c.tagName === 'code').textContent, /install.sh claude-code/);
  assert.ok(rows[3].children.some(c => c.textContent === 'Sign in with ChatGPT'));
  const row = rows[0], form = row.children.find(c => c.tagName === 'form'), input = form.children[0].children[0];
  input.value = 'secret';
  sdk.auth.credentials.save = async input => { calls.push(['save', input]); throw new SwarmApiError('Rejected secret sk-partly****1234', { status: 400 }); };
  await form.onsubmit({ preventDefault() {} });
  assert.equal(input.value, '');
  assert.equal(row.children.at(-1).textContent, 'Rejected [redacted] [redacted]');
  assert.equal(calls[0][1].provider, 'exa'); assert.equal(calls[0][1].type, 'api'); assert.equal(calls[0][1].active, true);
  await row.children.find(c => c.textContent === 'Make active').onclick();
  const remove = row.children.find(c => c.textContent === 'Remove');
  await remove.onclick(); assert.equal(calls.length, 2);
  confirmed = true; await remove.onclick();
  assert.deepEqual(calls.slice(1), [['activate', 'exa', 'one'], ['delete', 'exa', 'one']]);
});
