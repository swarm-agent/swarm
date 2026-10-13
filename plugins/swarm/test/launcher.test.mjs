// Requirement: the MCP launcher writes nothing but MCP to stdout. It only
// executes a Swarm command that has the bridge: an older `swarm` is probed with
// `help` alone (any other word starts its terminal UI), swarmctl is found on
// PATH or in the release install's private bin directory, and an install with
// no bridge fails on stderr with the update command.
// Authority: bin/swarm-mcp. Fake `swarm`/`swarmctl` executables on an isolated
// PATH; this does not start or contact a real Swarm.
import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync, spawn } from 'node:child_process';
import { createServer } from 'node:http';
import { mkdtemp, mkdir, writeFile, readFile, rm, symlink } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const launcher = fileURLToPath(new URL('../bin/swarm-mcp', import.meta.url));
const update = 'curl -fsSL https://swarmagent.dev/install | sh -s -- --yes';

const newSwarm = `case "$1" in
  help) echo "  swarm mcp [--access full|write|read]   connect a local AI client" ;;
  mcp) echo "swarm-bridge $*"; IFS= read -r line; echo "$line" ;;
  *) echo "terminal-ui $*" ;;
esac`;
const oldSwarm = `case "$1" in
  help) echo "  swarm [main|dev] [run] [tui-args...]" ;;
  *) echo "terminal-ui $*" ;;
esac`;
const newCtl = `if [ "$1 $2" = "mcp --help" ]; then echo "swarmctl mcp: connect a local AI client"; exit 0; fi
if [ "$1" = mcp ]; then echo "ctl-bridge $0 $*"; exit 0; fi
echo usage; exit 2`;
const oldCtl = 'echo "Usage:"; echo "  swarmctl setup ..."; exit 2';

async function fixture(t) {
  const dir = await mkdtemp(join(tmpdir(), 'swarm-mcp-launcher-'));
  t.after(() => rm(dir, { recursive: true, force: true }));
  const bin = join(dir, 'bin');
  const system = join(dir, 'share-bin');
  await mkdir(bin);
  await mkdir(system);
  const timeout = spawnSync('sh', ['-c', 'command -v timeout'], { encoding: 'utf8' }).stdout.trim();
  if (timeout) await symlink(timeout, join(bin, 'timeout'));
  const log = join(dir, 'calls.log');
  const put = async (where, name, body) => {
    await writeFile(join(where === 'system' ? system : bin, name), `#!/bin/sh\necho "${name} $*" >> '${log}'\n${body}\n`, { mode: 0o755 });
  };
  const run = (env = {}) => {
    const result = spawnSync('/bin/sh', [launcher], {
      encoding: 'utf8',
      input: '{"jsonrpc":"2.0","id":1,"method":"initialize"}\n',
      timeout: 30000,
      env: { PATH: bin, SWARM_SYSTEM_BINARY_DIR: system, ...env },
    });
    return { ...result, calls: existsSync(log) ? readFile(log, 'utf8') : Promise.resolve('') };
  };
  return { put, run, system, log };
}

test('a launcher with the bridge is used directly', async t => {
  const f = await fixture(t);
  await f.put('bin', 'swarm', newSwarm);
  await f.put('bin', 'swarmctl', newCtl);
  const r = f.run();
  assert.equal(r.status, 0);
  assert.equal(r.stdout, 'swarm-bridge mcp\n{"jsonrpc":"2.0","id":1,"method":"initialize"}\n', 'the bridge receives the client stdin untouched by probes');
  assert.equal(await r.calls, 'swarm help\nswarm mcp\n');
});

test('an old launcher is never run with mcp; swarmctl on PATH takes over', async t => {
  const f = await fixture(t);
  await f.put('bin', 'swarm', oldSwarm);
  await f.put('bin', 'swarmctl', newCtl);
  const r = f.run();
  assert.equal(r.status, 0);
  assert.match(r.stdout, /^ctl-bridge \S+\/bin\/swarmctl mcp\n$/);
  assert.doesNotMatch(r.stdout, /terminal-ui/);
  assert.equal(await r.calls, 'swarm help\nswarmctl mcp --help\nswarmctl mcp\n');
});

test('a release install reaches swarmctl in its private bin directory', async t => {
  const f = await fixture(t);
  await f.put('bin', 'swarm', oldSwarm);
  await f.put('system', 'swarmctl', newCtl);
  const r = f.run();
  assert.equal(r.status, 0);
  assert.equal(r.stdout, `ctl-bridge ${join(f.system, 'swarmctl')} mcp\n`);
});

test('a socket override uses the private swarmctl without probing the launcher', async t => {
  const f = await fixture(t);
  await f.put('bin', 'swarm', newSwarm);
  await f.put('system', 'swarmctl', newCtl);
  const r = f.run({ SWARM_MCP_SOCKET: '/fixture/api.sock' });
  assert.equal(r.status, 0);
  assert.equal(r.stdout, `ctl-bridge ${join(f.system, 'swarmctl')} mcp --socket /fixture/api.sock\n`);
  assert.doesNotMatch(await r.calls, /^swarm /m);
});

test('an install without the bridge fails on stderr with the update command', async t => {
  const f = await fixture(t);
  await f.put('bin', 'swarm', oldSwarm);
  await f.put('bin', 'swarmctl', oldCtl);
  await f.put('system', 'swarmctl', oldCtl);
  const r = f.run();
  assert.notEqual(r.status, 0);
  assert.equal(r.stdout, '');
  assert.match(r.stderr, /too old for AI connections/);
  assert.ok(r.stderr.includes(update));
  assert.doesNotMatch(await r.calls, /^swarm (?!help$)/m);
});

test('an explicit binary without the bridge is refused', async t => {
  const f = await fixture(t);
  await f.put('bin', 'swarmctl', oldCtl);
  const r = f.run({ SWARM_MCP_BIN: 'swarmctl' });
  assert.notEqual(r.status, 0);
  assert.equal(r.stdout, '');
  assert.ok(r.stderr.includes(update));
});

test('no Swarm at all says how to install it, with nothing on stdout', async t => {
  const f = await fixture(t);
  const r = f.run();
  assert.notEqual(r.status, 0);
  assert.equal(r.stdout, '');
  assert.match(r.stderr, /no Swarm found/);
  assert.ok(r.stderr.includes(update));
});

// Requirement: Claude starts this launcher, so a connection saved with
// connect.mjs (a Swarm's HTTPS endpoint on the tailnet, Docker or a chosen
// binary) must reach the Node adapter that honors it, not be ignored for a
// local Swarm. Threats: a remote connection silently replaced by whatever local
// Swarm is on PATH; a launcher <-> adapter loop in native mode; a named but
// missing SWARM_MCP_CONFIG falling back to native instead of failing.
// Authority: bin/swarm-mcp (saved-connection hand-off) and bin/swarm-mcp.mjs
// (SWARM_MCP_NATIVE on native hand-back). Real launcher, real adapter on this
// test's Node and a loopback HTTP stand-in for Swarm's /mcp endpoint (plain
// HTTP is accepted only on loopback); no real Swarm is started or contacted.

async function saved(t, connection) {
  const f = await fixture(t);
  const home = await mkdtemp(join(tmpdir(), 'swarm-mcp-home-'));
  t.after(() => rm(home, { recursive: true, force: true }));
  if (connection) {
    await mkdir(join(home, 'swarm-mcp'), { recursive: true });
    await writeFile(join(home, 'swarm-mcp', 'connection.json'), JSON.stringify(connection), { mode: 0o600 });
  }
  return { ...f, env: { HOME: home, XDG_CONFIG_HOME: home }, home };
}

async function withNode(f) {
  await symlink(process.execPath, join(dirname(f.system), 'bin', 'node'));
  // The adapter hands native mode back through `sh`, as on any POSIX host.
  await symlink('/bin/sh', join(dirname(f.system), 'bin', 'sh'));
}

test('a saved tailnet-style HTTP connection is served by the adapter, not a local Swarm', async t => {
  const seen = [];
  const server = createServer((req, res) => {
    let body = '';
    req.on('data', c => { body += c; });
    req.on('end', () => {
      seen.push({ method: req.method, url: req.url, auth: req.headers.authorization ?? null, body });
      const id = JSON.parse(body).id;
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ jsonrpc: '2.0', id, result: { serverInfo: { name: 'stand-in' } } }));
    });
  });
  await new Promise(r => server.listen(0, '127.0.0.1', r));
  t.after(() => server.close());
  const f = await saved(t, { mode: 'http', url: `http://127.0.0.1:${server.address().port}/mcp` });
  await withNode(f);
  await f.put('bin', 'swarm', newSwarm);
  await f.put('bin', 'swarmctl', newCtl);
  const child = spawn('/bin/sh', [launcher], { env: { PATH: join(dirname(f.system), 'bin'), SWARM_SYSTEM_BINARY_DIR: f.system, ...f.env } });
  const out = [];
  child.stdout.on('data', c => out.push(c));
  child.stdin.write('{"jsonrpc":"2.0","id":7,"method":"initialize"}\n');
  const deadline = Date.now() + 20000;
  while (!Buffer.concat(out).toString().includes('\n') && Date.now() < deadline) await new Promise(r => setTimeout(r, 25));
  child.stdin.end();
  await new Promise(r => child.on('close', r));
  assert.deepEqual(JSON.parse(Buffer.concat(out).toString()), { jsonrpc: '2.0', id: 7, result: { serverInfo: { name: 'stand-in' } } });
  assert.equal(seen.length, 1);
  assert.deepEqual({ method: seen[0].method, url: seen[0].url, auth: seen[0].auth }, { method: 'POST', url: '/mcp', auth: null }, 'keyless: no Authorization header without a token file');
  assert.equal(existsSync(f.log) ? await readFile(f.log, 'utf8') : '', '', 'no local swarm or swarmctl was probed or started');
});

test('a saved native connection hands back to the launcher once, without looping', async t => {
  const f = await saved(t, { mode: 'native' });
  await withNode(f);
  await f.put('bin', 'swarm', newSwarm);
  const r = f.run({ ...f.env, PATH: join(dirname(f.system), 'bin') });
  assert.equal(r.status, 0, r.stderr);
  assert.equal(r.stdout, 'swarm-bridge mcp\n{"jsonrpc":"2.0","id":1,"method":"initialize"}\n');
  assert.equal(await r.calls, 'swarm help\nswarm mcp\n');
});

test('a named connection file that is missing fails instead of using a local Swarm', async t => {
  const f = await saved(t, null);
  await withNode(f);
  await f.put('bin', 'swarm', newSwarm);
  const r = f.run({ ...f.env, PATH: join(dirname(f.system), 'bin'), SWARM_MCP_CONFIG: join(f.home, 'missing.json') });
  assert.notEqual(r.status, 0);
  assert.equal(r.stdout, '');
  assert.match(r.stderr, /configuration\/key file is missing or invalid/);
  assert.equal(await r.calls, '', 'the local Swarm was not probed');
});

test('a saved connection without Node says so on stderr and probes nothing', async t => {
  const f = await saved(t, { mode: 'http', url: 'https://swarm.example/mcp' });
  await f.put('bin', 'swarm', newSwarm);
  const r = f.run(f.env);
  assert.notEqual(r.status, 0);
  assert.equal(r.stdout, '');
  assert.match(r.stderr, /saved Swarm connection .* needs Node 22\+/);
  assert.equal(await r.calls, '');
});
