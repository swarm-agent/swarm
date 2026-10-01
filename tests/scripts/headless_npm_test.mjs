// Requirement: standalone npm packs install/import without source or Desktop.
// Threat: hidden web dependencies, stale dist, install-time container execution,
// missing declarations/license or accidental shipping of tests/private files.
// Authority: package manifests/build script, exports and CLI bin. This package
// integration check runs real npm/Node/tsc with engine sentinels, never a daemon.
import assert from 'node:assert/strict';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';

assert.ok(process.env.TMPDIR, 'TMPDIR must be provided');
assert.ok(process.argv[2], 'Pass an output directory for candidate tarballs');
const root = fileURLToPath(new URL('../..', import.meta.url));
const output = resolve(process.argv[2]);
mkdirSync(output, { recursive: true });
const scratch = mkdtempSync(join(process.env.TMPDIR, 'headless-npm-'));
const sentinel = join(scratch, 'engine-called');
const bin = join(scratch, 'bin'); mkdirSync(bin);
for (const engine of ['docker', 'podman']) writeFileSync(join(bin, engine), '#!/bin/sh\nprintf called > "$ENGINE_SENTINEL"\nexit 99\n', { mode: 0o755 });
const env = { ...process.env, PATH: `${bin}:${process.env.PATH}`, ENGINE_SENTINEL: sentinel, npm_config_audit: 'false', npm_config_fund: 'false', npm_config_ignore_scripts: 'false' };
function run(command, args, cwd) {
  const result = spawnSync(command, args, { cwd, env, encoding: 'utf8', timeout: 120000, maxBuffer: 2 * 1024 * 1024 });
  assert.equal(result.status, 0, `${command} failed: ${(result.stderr || result.stdout || String(result.error)).slice(-6000)}`);
  return result.stdout;
}
try {
  const sdk = join(scratch, 'sdk'); const cli = join(scratch, 'cli');
  cpSync(join(root, 'packages/sdk'), sdk, { recursive: true, filter: source => !/(?:^|\/)(node_modules|dist)(?:\/|$)/.test(source) });
  cpSync(join(root, 'packages/cli'), cli, { recursive: true, filter: source => !/(?:^|\/)node_modules(?:\/|$)/.test(source) });
  assert.ok(!existsSync(join(scratch, 'web')));
  run('npm', ['ci', '--ignore-scripts', '--no-audit', '--no-fund'], sdk);
  // Prove stale outputs are removed by prepack, not merely excluded by assertions.
  mkdirSync(join(sdk, 'dist')); writeFileSync(join(sdk, 'dist/stale.js'), 'throw Error("stale");');
  const packed = [];
  for (const [dir, kind] of [[sdk, 'sdk'], [cli, 'cli']]) {
    const manifest = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8'));
    for (const hook of ['preinstall', 'install', 'postinstall', 'prepare']) assert.equal(manifest.scripts?.[hook], undefined);
    assert.equal(Object.keys(manifest.dependencies || {}).length, 0);
    const stdout = run('npm', ['pack', '--json', '--pack-destination', output], dir);
    const start = stdout.indexOf('[\n');
    const receipt = JSON.parse(stdout.slice(start))[0];
    const paths = receipt.files.map(f => f.path);
    assert.ok(paths.includes('LICENSE')); assert.ok(paths.includes('README.md'));
    assert.ok(!paths.some(p => /node_modules|__tests__|\.env|stale\.js|\.map$/.test(p)));
    for (const p of paths) {
      const allowed = kind === 'sdk'
        ? /^(package\.json|README\.md|LICENSE|examples\/headless-session\.ts|dist\/.+\.(js|d\.ts))$/
        : /^(package\.json|README\.md|LICENSE|runtime-image\.json|bin\/swarm-headless\.js|src\/launcher\.js)$/;
      assert.match(p, allowed);
    }
    assert.ok(paths.includes(kind === 'sdk' ? 'dist/index.d.ts' : 'bin/swarm-headless.js'));
    const tarball = join(output, receipt.filename);
    packed.push({ tarball, sha256: createHash('sha256').update(readFileSync(tarball)).digest('hex'), files: paths.length });
  }
  const consumer = join(scratch, 'consumer'); mkdirSync(consumer);
  writeFileSync(join(consumer, 'package.json'), '{"private":true,"type":"module"}\n');
  // Ordinary install: do not disable lifecycle scripts, so the sentinel proves inertness.
  run('npm', ['install', '--offline', '--no-audit', '--no-fund', ...packed.map(p => p.tarball)], consumer);
  assert.ok(!existsSync(sentinel), 'installation invoked a container engine');
  writeFileSync(join(consumer, 'check.mjs'), `import assert from 'node:assert/strict';
import { SwarmClient, createSwarmClient } from '@swarm/sdk';
const client = new SwarmClient({ baseUrl: 'http://127.0.0.1:7783' });
assert.equal(typeof client.sessions.create, 'function');
assert.equal(typeof createSwarmClient, 'function');
`);
  run(process.execPath, ['check.mjs'], consumer);
  cpSync(join(consumer, 'node_modules/@swarm/sdk/examples/headless-session.ts'), join(consumer, 'headless-session.ts'));
  writeFileSync(join(consumer, 'check.ts'), `import { SwarmClient } from '@swarm/sdk';\nconst client: SwarmClient = new SwarmClient({ baseUrl: 'http://127.0.0.1:7783' });\nvoid client.sessions;\n`);
  run(process.execPath, [join(sdk, 'node_modules/typescript/bin/tsc'), '--noEmit', '--strict', '--target', 'ES2022', '--module', 'NodeNext', '--moduleResolution', 'NodeNext', '--typeRoots', join(sdk, 'node_modules/@types'), 'check.ts', 'headless-session.ts'], consumer);
  const launcher = join(consumer, 'node_modules/.bin/swarm-headless');
  assert.match(run(launcher, ['--help'], consumer), /start\|stop\|status\|setup/);
  assert.equal(run(launcher, ['--version'], consumer).trim(), JSON.parse(readFileSync(join(cli, 'package.json'))).version);
  assert.ok(!existsSync(sentinel), 'help/version invoked a container engine');
  assert.ok(!existsSync(join(consumer, 'node_modules/typescript')));
  console.log(JSON.stringify({ result: 'PASS', node: process.version, packed }, null, 2));
} finally {
  rmSync(scratch, { recursive: true, force: true });
}
