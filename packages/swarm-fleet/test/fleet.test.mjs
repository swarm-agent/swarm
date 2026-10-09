import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createServer as createHTTP, request } from 'node:http';
import { createServer as createHTTPS } from 'node:https';
import { connect } from 'node:net';
import { execFileSync, spawn } from 'node:child_process';
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createFleet, machinesFromStatus, parseFleet } from '../server.mjs';

const KEY_A = 'swk_aaaaaaaaaaaaaaaaaaaa', KEY_B = 'swk_bbbbbbbbbbbbbbbbbbbb';
const READ_TOOLS = [{ name: 'swarm_list_sessions', inputSchema: { type: 'object', properties: {} } }, { name: 'swarm_get_session', inputSchema: { type: 'object', properties: { session_id: { type: 'string' } } } }];
const WRITE_TOOLS = [...READ_TOOLS, { name: 'swarm_start_session', inputSchema: { type: 'object', properties: { prompt: { type: 'string' } } } }];

// A stand-in for a machine's /mcp that checks its own key, like swarmd does.
function fakeMachine(handler) {
  const server = createHTTP((req, res) => {
    let body = ''; req.on('data', c => { body += c; });
    req.on('end', () => { const out = handler(JSON.parse(body), req.headers); res.writeHead(out.status ?? 200, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(out.body ?? out)); });
  });
  return new Promise(resolve => server.listen(0, '127.0.0.1', () => resolve({ server, url: `http://127.0.0.1:${server.address().port}/mcp` })));
}
const machineHandler = (key, tools, calls) => (msg, headers) => {
  if (headers.authorization !== `Bearer ${key}`) return { status: 401, body: { error: 'invalid scoped bearer token' } };
  if (msg.method === 'tools/list') return { jsonrpc: '2.0', id: msg.id, result: { tools } };
  calls.push(msg.params);
  if (!tools.some(t => t.name === msg.params.name)) return { jsonrpc: '2.0', id: msg.id, result: { content: [{ type: 'text', text: 'this key is limited to swarm:read' }], isError: true } };
  return { jsonrpc: '2.0', id: msg.id, result: { content: [{ type: 'text', text: `ok from ${key.slice(4, 5)}` }] } };
};

test('SWARM_FLEET is validated without echoing keys', () => {
  const good = parseFleet(JSON.stringify([{ name: 'box', url: 'https://box.tail1.ts.net:8444/mcp', token: KEY_A }]));
  assert.equal(good[0].url.port, '8444');
  assert.deepEqual(parseFleet(''), [], 'empty: machines come from the tailnet');
  assert.deepEqual(parseFleet('[]'), []);
  assert.equal(parseFleet(JSON.stringify([{ name: 'box', url: 'https://box.tail1.ts.net:8444/mcp' }]))[0].token, '', 'the key is optional');
  for (const bad of [
    'nope',
    JSON.stringify([{ name: 'Box', url: 'https://b/mcp', token: KEY_A }]),
    JSON.stringify([{ name: 'box', url: 'http://box.tail1.ts.net/mcp', token: KEY_A }]),
    JSON.stringify([{ name: 'box', url: 'https://user:pw@box/mcp', token: KEY_A }]),
    JSON.stringify([{ name: 'box', url: 'https://box/mcp', token: 'not-a-key-secret' }]),
    JSON.stringify([{ name: 'box', url: 'https://a/mcp', token: KEY_A }, { name: 'box', url: 'https://b/mcp', token: KEY_B }]),
  ]) {
    assert.throws(() => parseFleet(bad), e => !e.message.includes('secret') && !e.message.includes(KEY_A));
  }
});

test('one server lists every machine and routes each call with that machine\'s key', async () => {
  const callsA = [], callsB = [];
  const a = await fakeMachine(machineHandler(KEY_A, WRITE_TOOLS, callsA));
  const b = await fakeMachine(machineHandler(KEY_B, READ_TOOLS, callsB));
  const fleet = createFleet(parseFleet(JSON.stringify([
    { name: 'builder', url: a.url, token: KEY_A }, { name: 'prod', url: b.url, token: KEY_B },
    { name: 'gone', url: 'http://127.0.0.1:9/mcp', token: KEY_A },
  ])));
  try {
    const init = await fleet.handle({ jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-06-18' } });
    assert.equal(init.result.protocolVersion, '2025-06-18');
    const listed = (await fleet.handle({ jsonrpc: '2.0', id: 2, method: 'tools/list' })).result.tools;
    assert.deepEqual(listed.map(t => t.name), ['swarm_list_machines', 'swarm_fleet_status', 'swarm_list_sessions', 'swarm_get_session', 'swarm_start_session']);
    assert.ok(listed.slice(2).every(t => t.inputSchema.properties.machine), 'every forwarded tool takes `machine`');

    const machines = JSON.parse((await fleet.handle({ jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'swarm_list_machines' } })).result.content[0].text).machines;
    assert.deepEqual(machines.map(m => [m.machine, m.reachable, m.access]), [['builder', true, 'read and write'], ['prod', true, 'read only'], ['gone', false, 'none']]);
    assert.ok(!JSON.stringify(machines).includes(KEY_A), 'keys never appear in results');

    const call = (args, name = 'swarm_start_session') => fleet.handle({ jsonrpc: '2.0', id: 4, method: 'tools/call', params: { name, arguments: args } }).then(r => r.result);
    assert.equal((await call({ machine: 'builder', prompt: 'hi' })).content[0].text, 'ok from a');
    assert.deepEqual(callsA.at(-1), { name: 'swarm_start_session', arguments: { prompt: 'hi' } }, '`machine` is not forwarded');
    const refused = await call({ machine: 'prod', prompt: 'hi' });
    assert.equal(refused.isError, true, 'the machine, not the fleet, decides what its key allows');
    assert.match((await call({ prompt: 'hi' })).content[0].text, /Several machines/);
    assert.match((await call({ machine: 'gone' }, 'swarm_list_sessions')).content[0].text, /could not be reached/);
    assert.equal((await fleet.handle({ jsonrpc: '2.0', method: 'notifications/initialized' })), null);
  } finally { fleet.stop(); a.server.close(); b.server.close(); }
});

// The tailnet path: HTTPS to a machine name through tailscaled's CONNECT
// proxy, with real certificate checks, over the stdio transport.
test('stdio server reaches an https machine through a CONNECT proxy', async () => {
  const dir = mkdtempSync(join(tmpdir(), 'fleet-'));
  execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-subj', '/CN=box.tail1.ts.net',
    '-addext', 'subjectAltName=DNS:box.tail1.ts.net', '-keyout', join(dir, 'key.pem'), '-out', join(dir, 'cert.pem')], { stdio: 'ignore' });
  const calls = [];
  const machine = createHTTPS({ key: readFileSync(join(dir, 'key.pem')), cert: readFileSync(join(dir, 'cert.pem')) }, (req, res) => {
    let body = ''; req.on('data', c => { body += c; });
    req.on('end', () => { const out = machineHandler(KEY_A, READ_TOOLS, calls)(JSON.parse(body), req.headers); res.writeHead(out.status ?? 200, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(out.body ?? out)); });
  });
  await new Promise(r => machine.listen(0, '127.0.0.1', r));
  const port = machine.address().port;
  const tunnels = [];
  const proxy = createHTTP();
  proxy.on('connect', (req, socket, head) => {
    tunnels.push(req.url);
    if (req.url !== `box.tail1.ts.net:${port}`) { socket.end('HTTP/1.1 403 Forbidden\r\n\r\n'); return; }
    const upstream = connect(port, '127.0.0.1', () => { socket.write('HTTP/1.1 200 Connection Established\r\n\r\n'); upstream.write(head); upstream.pipe(socket); socket.pipe(upstream); });
    upstream.on('error', () => socket.destroy());
  });
  await new Promise(r => proxy.listen(0, '127.0.0.1', r));
  writeFileSync(join(dir, 'ca.pem'), readFileSync(join(dir, 'cert.pem')));
  const child = spawn(process.execPath, [fileURLToPath(new URL('../server.mjs', import.meta.url))], {
    env: { PATH: process.env.PATH, FLEET_DISCOVER: 'off', NODE_EXTRA_CA_CERTS: join(dir, 'ca.pem'), FLEET_PROXY: `http://127.0.0.1:${proxy.address().port}`,
      SWARM_FLEET: JSON.stringify([{ name: 'box', url: `https://box.tail1.ts.net:${port}/mcp`, token: KEY_A }]) },
    stdio: ['pipe', 'pipe', 'pipe'],
  });
  const replies = [];
  let buffer = '';
  child.stdout.on('data', c => { buffer += c; let i; while ((i = buffer.indexOf('\n')) >= 0) { replies.push(JSON.parse(buffer.slice(0, i))); buffer = buffer.slice(i + 1); } });
  const ask = async message => { child.stdin.write(JSON.stringify(message) + '\n'); const want = replies.length + 1; for (let i = 0; i < 200 && replies.length < want; i++) await new Promise(r => setTimeout(r, 25)); return replies.at(-1); };
  try {
    assert.equal((await ask({ jsonrpc: '2.0', id: 1, method: 'initialize', params: {} })).result.serverInfo.name, 'swarm-fleet');
    const tools = (await ask({ jsonrpc: '2.0', id: 2, method: 'tools/list' })).result.tools.map(t => t.name);
    assert.deepEqual(tools, ['swarm_list_machines', 'swarm_fleet_status', 'swarm_list_sessions', 'swarm_get_session']);
    const result = (await ask({ jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'swarm_list_sessions', arguments: {} } })).result;
    assert.equal(result.content[0].text, 'ok from a');
    assert.ok(tunnels.length >= 2 && tunnels.every(t => t === `box.tail1.ts.net:${port}`), 'every request went through the proxy by name');
  } finally { child.kill(); machine.close(); proxy.close(); }
});

// The tailnet decides: tagged peers become machines, keyed by nothing; the
// machine admits this device by its tailnet policy grant.
const STATUS = {
  Self: { DNSName: 'claude-web-1.tail1.ts.net.', Tags: ['tag:claude'], Online: true },
  Peer: {
    a: { DNSName: 'social.tail1.ts.net.', Tags: ['tag:swarm'], Online: true },
    b: { DNSName: 'vault.tail1.ts.net.', Tags: ['tag:swarm-protected'], Online: false },
    c: { DNSName: 'roy-laptop.tail1.ts.net.', Tags: [], Online: true },
    d: { DNSName: 'Social.tail1.ts.net.', Tags: ['tag:swarm'], Online: true },
    e: { DNSName: 'evil.example.com.', Tags: ['tag:swarm'], Online: true },
  },
};

test('tagged tailnet peers become machines; untagged and non-tailnet names do not', () => {
  const found = machinesFromStatus(STATUS, { tags: ['tag:swarm', 'tag:swarm-protected'] });
  assert.deepEqual(found.map(m => [m.name, m.url.href, m.token, m.source, m.online]), [
    ['social', 'https://social.tail1.ts.net:8444/mcp', '', 'tag:swarm', true],
    ['vault', 'https://vault.tail1.ts.net:8444/mcp', '', 'tag:swarm-protected', false],
  ]);
  assert.deepEqual(machinesFromStatus(STATUS).map(m => m.name), ['social'], 'default tag is tag:swarm');
});

test('discovered machines get no Authorization header; fleet status is one view', async () => {
  const seen = [];
  const handler = (msg, headers) => {
    seen.push(headers.authorization ?? null);
    if (msg.method === 'tools/list') return { jsonrpc: '2.0', id: msg.id, result: { tools: [...WRITE_TOOLS, { name: 'swarm_get_usage', inputSchema: { type: 'object', properties: {} } }] } };
    const text = msg.params.name === 'swarm_list_sessions'
      ? JSON.stringify({ sessions: [{ id: 's1', title: 'build', running: true, agent: 'swarm' }, { id: 's2', title: 'old' }] })
      : JSON.stringify({ period: 'today', summary: { total_cost_usd: 1.5 }, limits: { enabled: true, limit_exceeded: false } });
    return { jsonrpc: '2.0', id: msg.id, result: { content: [{ type: 'text', text }] } };
  };
  const box = await fakeMachine(handler);
  let calls = 0;
  const discover = async () => { calls++; return { machines: [
    { name: 'social', url: new URL(box.url), token: '', source: 'tag:swarm', online: true },
    { name: 'vault', url: new URL('http://127.0.0.1:9/mcp'), token: '', source: 'tag:swarm-protected', online: false },
  ] }; };
  const fleet = createFleet([], { discover });
  try {
    const listed = (await fleet.handle({ jsonrpc: '2.0', id: 1, method: 'tools/list' })).result.tools.map(t => t.name);
    assert.ok(listed.includes('swarm_start_session') && listed.includes('swarm_fleet_status'));
    const status = JSON.parse((await fleet.handle({ jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'swarm_fleet_status' } })).result.content[0].text);
    assert.deepEqual(status.machines.map(m => [m.machine, m.reachable, m.access]), [['social', true, 'read and write'], ['vault', false, 'none']]);
    assert.equal(status.machines[0].sessions_recent, 2);
    assert.deepEqual(status.machines[0].sessions_running, [{ id: 's1', title: 'build', agent: 'swarm' }]);
    assert.equal(status.machines[0].usage_today.total_cost_usd, 1.5);
    assert.equal(status.machines[1].error, 'offline on the tailnet');
    const run = (await fleet.handle({ jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'swarm_start_session', arguments: { machine: 'social', prompt: 'hi' } } })).result;
    assert.ok(!run.isError);
    assert.ok(seen.length >= 4 && seen.every(h => h === null), 'no credential is sent to a discovered machine');
    assert.ok(calls >= 2, 'status re-discovers the tailnet');
  } finally { fleet.stop(); box.server.close(); }
});

test('no machines: the fleet explains how to add them', async () => {
  const fleet = createFleet([], { discover: async () => ({ machines: [] }) });
  try {
    const out = JSON.parse((await fleet.handle({ jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name: 'swarm_list_machines' } })).result.content[0].text);
    assert.deepEqual(out.machines, []);
    assert.match(out.note, /tag:swarm/);
  } finally { fleet.stop(); }
});
