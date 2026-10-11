import assert from 'node:assert/strict';
import { chmodSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import net from 'node:net';
import path from 'node:path';
import { test } from 'node:test';
import { SwarmClient } from '../client.js';
import {
  isUsableSwarmSocket,
  resolveSwarmConnection,
  SWARM_DEFAULT_API_URL,
  SWARM_OWNER_SOCKET_PATH,
} from '../local-connection.js';

function listen(socketPath: string): Promise<net.Server> {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.once('error', reject);
    server.listen(socketPath, () => resolve(server));
  });
}

function close(server: net.Server): Promise<void> {
  return new Promise((resolve) => server.close(() => resolve()));
}

const never = () => false;
const always = () => true;

function scratch(t: { after: (fn: () => void) => void }): string {
  const base = process.env.TMPDIR;
  assert.ok(base, 'TMPDIR is required for socket scratch');
  const dir = mkdtempSync(path.join(base, 'swarm-conn-'));
  t.after(() => rmSync(dir, { recursive: true, force: true }));
  return dir;
}

test('default connection constants match the daemon contract', () => {
  assert.equal(SWARM_DEFAULT_API_URL, 'http://127.0.0.1:7781');
  assert.equal(SWARM_OWNER_SOCKET_PATH, '/var/lib/swarmd/local-transport/api.sock');
});

test('resolveSwarmConnection: falls back to the loopback API when no socket is usable', () => {
  const probed: string[] = [];
  const conn = resolveSwarmConnection({}, { env: {}, isUsableSocket: (p) => { probed.push(p); return false; } });
  assert.deepEqual(conn, { baseUrl: 'http://127.0.0.1:7781' });
  assert.deepEqual(probed, [SWARM_OWNER_SOCKET_PATH]);
});

test('resolveSwarmConnection: uses the owner socket when it is usable', () => {
  const conn = resolveSwarmConnection({}, { env: {}, ownerSocketPath: '/x/api.sock', isUsableSocket: (p) => p === '/x/api.sock' });
  assert.deepEqual(conn, { baseUrl: 'http://127.0.0.1:7781', socketPath: '/x/api.sock' });
});

test('resolveSwarmConnection: SWARM_SOCKET_PATH wins over the owner socket and is not probed', () => {
  let probed = false;
  const conn = resolveSwarmConnection({}, { env: { SWARM_SOCKET_PATH: '/env/api.sock' }, isUsableSocket: () => { probed = true; return true; } });
  assert.deepEqual(conn, { baseUrl: 'http://127.0.0.1:7781', socketPath: '/env/api.sock' });
  assert.equal(probed, false);
});

test('resolveSwarmConnection: explicit settings and URL environment skip socket discovery', () => {
  assert.deepEqual(
    resolveSwarmConnection({ baseUrl: 'http://127.0.0.1:9000/' }, { env: { SWARM_SOCKET_PATH: '/env/api.sock' }, isUsableSocket: always }),
    { baseUrl: 'http://127.0.0.1:9000' },
  );
  assert.deepEqual(
    resolveSwarmConnection({ socketPath: '/cfg/api.sock' }, { env: { SWARM_SOCKET_PATH: '/env/api.sock' }, isUsableSocket: never }),
    { baseUrl: 'http://127.0.0.1:7781', socketPath: '/cfg/api.sock' },
  );
  assert.deepEqual(
    resolveSwarmConnection({}, { env: { SWARM_API_URL: 'http://127.0.0.1:7999' }, isUsableSocket: always }),
    { baseUrl: 'http://127.0.0.1:7999' },
  );
  assert.deepEqual(
    resolveSwarmConnection({}, { env: { SWARM_DESKTOP_URL: 'http://127.0.0.1:5555' }, isUsableSocket: always }),
    { baseUrl: 'http://127.0.0.1:5555' },
  );
});

test('isUsableSwarmSocket: accepts a live socket, rejects missing, regular and unreachable paths', async (t) => {
  const dir = scratch(t);
  const sock = path.join(dir, 'api.sock');
  const server = await listen(sock);
  const locked = path.join(dir, 'locked');
  mkdirSync(locked);
  const hidden = path.join(locked, 'api.sock');
  const inner = await listen(hidden);
  try {
    assert.equal(isUsableSwarmSocket(sock), true);
    assert.equal(isUsableSwarmSocket(path.join(dir, 'missing.sock')), false);
    const file = path.join(dir, 'plain');
    writeFileSync(file, '');
    assert.equal(isUsableSwarmSocket(file), false);
    if (process.getuid?.() !== 0) {
      chmodSync(locked, 0o000);
      assert.equal(isUsableSwarmSocket(hidden), false);
    }
  } finally {
    chmodSync(locked, 0o700);
    await close(inner);
    await close(server);
  }
});

test('SwarmClient: discovers a usable owner socket only without explicit settings', async (t) => {
  const dir = scratch(t);
  const sock = path.join(dir, 'api.sock');
  const server = await listen(sock);
  const keys = ['SWARM_SOCKET_PATH', 'SWARM_API_URL', 'SWARM_DESKTOP_URL'] as const;
  const saved = keys.map((k) => process.env[k]);
  try {
    assert.deepEqual(resolveSwarmConnection({}, { env: {}, ownerSocketPath: sock }), { baseUrl: SWARM_DEFAULT_API_URL, socketPath: sock });

    delete process.env.SWARM_API_URL;
    delete process.env.SWARM_DESKTOP_URL;
    process.env.SWARM_SOCKET_PATH = sock;
    const fromEnv = new SwarmClient();
    assert.equal(fromEnv.config.socketPath, sock);
    assert.equal(fromEnv.config.baseUrl, SWARM_DEFAULT_API_URL);

    const explicit = new SwarmClient({ baseUrl: 'http://127.0.0.1:9000' });
    assert.equal(explicit.config.socketPath, undefined);
    assert.equal(explicit.config.baseUrl, 'http://127.0.0.1:9000');
  } finally {
    keys.forEach((k, i) => { if (saved[i] === undefined) delete process.env[k]; else process.env[k] = saved[i]; });
    await close(server);
  }
});

test('browser entry and connection probe stay free of static Node imports', () => {
  const src = path.resolve(import.meta.dirname, '..');
  const probe = readFileSync(path.join(src, 'local-connection.ts'), 'utf8');
  assert.doesNotMatch(probe, /^\s*import\s/m, 'local-connection.ts must not import modules statically');

  const seen = new Set<string>();
  const visit = (file: string) => {
    if (seen.has(file)) return;
    seen.add(file);
    const text = readFileSync(file, 'utf8');
    for (const match of text.matchAll(/^\s*(import|export)\s+(?!type\b)[^;]*?from\s+'([^']+)'/gm)) {
      const spec = match[2];
      assert.ok(!spec.startsWith('node:') && spec !== 'ws', `browser graph imports ${spec} from ${path.basename(file)}`);
      if (spec.startsWith('.')) visit(path.resolve(path.dirname(file), spec.replace(/\.js$/, '.ts')));
    }
  };
  visit(path.join(src, 'browser.ts'));
  assert.ok(!seen.has(path.join(src, 'local-connection.ts')));
  assert.ok(!seen.has(path.join(src, 'client.ts')));
});
