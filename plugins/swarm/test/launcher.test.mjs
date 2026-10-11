// Requirement: the MCP launcher writes nothing but MCP to stdout. It only
// executes a Swarm command that has the bridge: an older `swarm` is probed with
// `help` alone (any other word starts its terminal UI), swarmctl is found on
// PATH or in the release install's private bin directory, and an install with
// no bridge fails on stderr with the update command.
// Authority: bin/swarm-mcp. Fake `swarm`/`swarmctl` executables on an isolated
// PATH; this does not start or contact a real Swarm.
import test from 'node:test';
import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import { mkdtemp, mkdir, writeFile, readFile, rm, symlink } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
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
  return { put, run, system };
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
