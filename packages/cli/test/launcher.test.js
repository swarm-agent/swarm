// Requirement: npm installs are inert; explicit launcher operations preserve state,
// require exact images/owned resources and never expose a public SDK listener.
// Threat: argv injection, tag drift, name collisions and recreation deleting data.
// Authority: parseArgs, selectedProject, engineRunner and runLauncher. An injected
// engine records exact argv/failures; this unit layer does NOT prove live containers.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir, homedir } from 'node:os';
import { parseArgs, runLauncher, selectedProject, engineRunner } from '../src/launcher.js';

const pin = { version: '0.1.0-headless.1', imageId: `sha256:${'a'.repeat(64)}`, platform: 'linux/amd64' };
const label = 'dev.swarm.headless';
const name = 'swarm-headless';
const volumes = ['config', 'data', 'cache', 'logs'];
function fixture({ current = null, collision = '', missing = '', fail = '', image = pin.imageId, orphaned = false, volumeProject = '/chosen/repository' } = {}) {
  const calls = [];
  const run = (args, options) => {
    calls.push({ args, options });
    if (fail && args.slice(0, 2).join(' ') === fail) throw Error('injected failure');
    const key = args.slice(0, 2).join(' ');
    if (key === 'container ls') return current ? name : '';
    if (key === 'container inspect') return JSON.stringify([current]);
    if (key === 'image inspect') return JSON.stringify([{ Id: image, Os: 'linux', Architecture: 'amd64' }]);
    if (key === 'network ls') return '';
    if (key === 'volume ls') return (current || collision || orphaned ? volumes.filter(v => v !== missing).map(v => `${name}-${v}`) : []).join('\n');
    if (key === 'volume inspect') return JSON.stringify([{ Labels: { [label]: args[2].endsWith(collision || '-never') ? 'someone-else' : name, [`${label}.project`]: volumeProject } }]);
    return '';
  };
  const io = { show() {}, project: () => '/chosen/repository' };
  return { calls, run, io };
}
function stopped() {
  return { Id: 'owned-container-id', Image: pin.imageId, State: { Running: false, Status: 'exited' }, Config: { Labels: { [label]: name, [`${label}.project`]: '/chosen/repository' } } };
}
const start = () => parseArgs(['start', '--project', '/chosen/repository']);

test('parsing rejects flags and credential argv before engine access', () => {
  for (const args of [ ['start', '--engine', 'sh'], ['start', '--name', '../x'], ['start', '--port', '0'], ['start', '--port', '7783;echo x'], ['stop', '--project', '/x'], ['start', '--privileged'], ['setup', '--', 'credential', '--api-key', 'do-not-echo'], ['setup', '--', 'status', '--socket', '/elsewhere'], ['setup', '--', 'workspace', '--path', '/'] ]) {
    assert.throws(() => parseArgs(args));
  }
  assert.throws(() => parseArgs(['setup', '--', 'credential', '--api-key', 'do-not-echo']), e => !e.message.includes('do-not-echo'));
  assert.equal(parseArgs(['--help']).command, 'help');
  assert.throws(() => engineRunner('docker', { DOCKER_HOST: 'tcp://remote:2375' }), /local/);
  assert.throws(() => engineRunner('podman', { CONTAINER_CONNECTION: 'remote' }), /local/);
});

test('start uses immutable image, isolated network, loopback and only selected mounts', () => {
  const f = fixture(); runLauncher(start(), pin, f.run, f.io);
  const run = f.calls.find(c => c.args[0] === 'run').args;
  assert.ok(run.includes(pin.imageId)); assert.ok(run.includes('--pull=never'));
  assert.equal(run[run.indexOf('--publish') + 1], '127.0.0.1:7783:7783');
  assert.equal(run.filter(a => a === '--mount').length, 5);
  assert.ok(run.includes('type=bind,src=/chosen/repository,dst=/project'));
  for (const v of volumes) assert.ok(run.some(a => a.startsWith(`type=volume,src=${name}-${v},`)));
  assert.ok(run.includes('ALL')); assert.ok(run.includes('no-new-privileges'));
  assert.equal(run.at(-1), '--container-sdk-port=7783');
  assert.ok(!run.includes('--privileged')); assert.ok(!run.some(a => a.includes('docker.sock')));
});

test('image mismatch and engine lookup failure cannot create or start anything', () => {
  for (const config of [{ image: `sha256:${'b'.repeat(64)}` }, { fail: 'container ls' }]) {
    const f = fixture(config); assert.throws(() => runLauncher(start(), pin, f.run, f.io));
    assert.ok(!f.calls.some(c => ['run', 'create', 'rm'].some(op => c.args.includes(op))));
  }
});

test('unmanaged containers are never stopped, executed or removed', () => {
  for (const command of ['stop', 'setup', 'start', 'status']) {
    const current = stopped(); current.Config.Labels[label] = 'foreign';
    const f = fixture({ current });
    const opts = command === 'start' ? start() : parseArgs(command === 'setup' ? ['setup', '--', 'status'] : [command]);
    assert.throws(() => runLauncher(opts, pin, f.run, f.io), /unmanaged/);
    assert.equal(f.calls.length, 2);
  }
});

test('stop preserves containers/volumes and recreation requires explicit stopped state', () => {
  const current = stopped(); current.State.Running = true;
  let f = fixture({ current }); runLauncher(parseArgs(['stop']), pin, f.run, f.io);
  assert.ok(f.calls.some(c => c.args[1] === 'stop'));
  assert.ok(!f.calls.some(c => c.args.includes('rm')));
  f = fixture({ current }); assert.throws(() => runLauncher({ ...start(), recreate: true }, pin, f.run, f.io), /Stop/);
  assert.equal(f.calls.length, 2);
  current.State.Running = false;
  f = fixture({ current }); assert.throws(() => runLauncher(start(), pin, f.run, f.io), /recreate/);
  f = fixture({ current }); runLauncher({ ...start(), recreate: true }, pin, f.run, f.io);
  const remove = f.calls.find(c => c.args[1] === 'rm').args;
  assert.deepEqual(remove, ['container', 'rm', 'owned-container-id']);
  assert.ok(!f.calls.some(c => c.args[0] === 'volume' && ['rm', 'create'].includes(c.args[1])));
  assert.ok(f.calls.some(c => c.args[0] === 'run'));
});

test('foreign or missing volumes and changed projects never remove a stopped instance', () => {
  for (const config of [{ collision: 'data' }, { missing: 'config' }]) {
    const f = fixture({ current: stopped(), ...config });
    assert.throws(() => runLauncher({ ...start(), recreate: true }, pin, f.run, f.io));
    assert.ok(!f.calls.some(c => c.args.includes('rm') || c.args[0] === 'run'));
    assert.ok(!f.calls.some(c => c.args[0] === 'volume' && c.args[1] === 'create'));
  }
  const current = stopped(); current.Config.Labels[`${label}.project`] = '/other';
  const f = fixture({ current }); assert.throws(() => runLauncher({ ...start(), recreate: true }, pin, f.run, f.io), /same project/);
  assert.equal(f.calls.length, 2);
});

test('failed replacement run retains state and never retries destructive operations', () => {
  const f = fixture({ current: stopped(), fail: 'run --detach' });
  assert.throws(() => runLauncher({ ...start(), recreate: true }, pin, f.run, f.io), /injected/);
  assert.equal(f.calls.filter(c => c.args[0] === 'run').length, 1);
  assert.ok(!f.calls.some(c => c.args[0] === 'volume' && c.args[1] !== 'ls' && c.args[1] !== 'inspect'));
});

test('setup passes only canonical swarmctl arguments, no tty; terminal secrets rejected', () => {
  const current = stopped(); current.State.Running = true;
  const opts = parseArgs(['setup', '--engine', 'podman', '--', 'credential', '--provider', 'chosen', '--api-key-stdin']);
  let f = fixture({ current }); runLauncher(opts, pin, f.run, f.io);
  assert.deepEqual(f.calls.at(-1), { args: ['exec', '-i', 'owned-container-id', 'swarmctl', 'setup', 'credential', '--provider', 'chosen', '--api-key-stdin'], options: { stream: true } });
  f = fixture({ current }); assert.throws(() => runLauncher(opts, pin, f.run, { ...f.io, stdinTTY: true }), /Redirect/);
  assert.equal(f.calls.length, 2);
  f = fixture({ current }); assert.throws(() => runLauncher(parseArgs(['setup', '--', 'sdk-token']), pin, f.run, { ...f.io, stdoutTTY: true }), /Redirect/);
  assert.equal(f.calls.length, 2);
});

test('project selection rejects home, root, linked worktrees, symlink metadata and mount injection', () => {
  const dir = mkdtempSync(join(tmpdir(), 'launcher-project-'));
  try {
    const normal = join(dir, 'normal'); mkdirSync(normal); mkdirSync(join(normal, '.git'));
    assert.equal(selectedProject(normal), normal);
    assert.throws(() => selectedProject('/'));
    assert.throws(() => selectedProject(homedir()));
    assert.throws(() => selectedProject('.'));
    const linked = join(dir, 'linked'); mkdirSync(linked); writeFileSync(join(linked, '.git'), 'gitdir: elsewhere');
    assert.throws(() => selectedProject(linked));
    const symbolic = join(dir, 'symbolic'); mkdirSync(symbolic); symlinkSync(join(normal, '.git'), join(symbolic, '.git'));
    assert.throws(() => selectedProject(symbolic));
    const comma = join(dir, 'comma,dst=/host'); mkdirSync(comma, { recursive: true });
    assert.throws(() => selectedProject(comma));
  } finally { rmSync(dir, { recursive: true, force: true }); }
});

test('orphaned state keeps its project binding and partial volume sets fail without mutation', () => {
  for (const config of [{ orphaned: true, volumeProject: '/other' }, { orphaned: true, missing: 'data' }]) {
    const f = fixture(config);
    assert.throws(() => runLauncher(start(), pin, f.run, f.io));
    assert.ok(!f.calls.some(c => c.args.includes('create') || c.args.includes('rm') || c.args[0] === 'run'));
  }
  const f = fixture({ orphaned: true });
  runLauncher(start(), pin, f.run, f.io);
  assert.ok(f.calls.some(c => c.args[0] === 'run'));
  assert.ok(!f.calls.some(c => c.args[0] === 'volume' && c.args[1] === 'create'));
});
