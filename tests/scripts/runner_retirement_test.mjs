import assert from 'node:assert/strict'
import test from 'node:test'
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { spawnSync } from 'node:child_process'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { SCENARIOS, parseOptions, requiredAssertions } from '../../scripts/runners/orchestrator-pr.mjs'

const root = fileURLToPath(new URL('../../', import.meta.url))
function fixture(t) {
  assert.ok(process.env.TMPDIR, 'parent must supply run TMPDIR')
  const scratch = mkdtempSync(path.join(process.env.TMPDIR, 'runner-retirement-'))
  t.after(() => rmSync(scratch, { recursive: true, force: true }))
  const bin = path.join(scratch, 'bin'), marker = path.join(scratch, 'invoked')
  mkdirSync(bin)
  // CLI admission sentinels, not workloads or fabricated qualification receipts.
  for (const command of ['node', 'ssh', 'scp']) {
    writeFileSync(path.join(bin, command), '#!/usr/bin/env bash\nprintf "%s\\n" "$0" >>"$INVOCATION_MARKER"\nexit 99\n', { mode: 0o700 })
  }
  const env = {
    PATH: `${bin}:${process.env.PATH}`, TMPDIR: scratch,
    INVOCATION_MARKER: marker,
    SWARM_TESTBENCH_ENV_FILE: path.join(scratch, 'absent.env'),
  }
  return { scratch, marker, env }
}
function shell(script, args, env) {
  const result = spawnSync('bash', [path.isAbsolute(script) ? script : path.join(root, script), ...args], {
    cwd: root, env, encoding: 'utf8', timeout: 5000, maxBuffer: 65536,
  })
  assert.equal(result.error, undefined)
  assert.equal(result.signal, null)
  return result
}

// Purpose: run-runner-test and run-testbench-runner own runner selection. Reject
// implicit/retired/unknown/traversal requests before node, SSH, config or deployment.
// Bounded subprocess admission tests assert nonzero/no success/no side effects;
// no daemon, credentials, cloud calls, provider work or qualification is simulated.
test('runner launchers require explicit selection and reject retirement before execution', { timeout: 15000 }, t => {
  const { scratch, marker, env } = fixture(t)
  const cases = [
    ['scripts/run-runner-test.sh', ['http://127.0.0.1:1', 'configured'], /test-name is required/],
    ['scripts/run-runner-test.sh', ['ssh-alias', 'configured', '--timeout-ms', '30000'], /test-name is required/],
    ['scripts/run-runner-test.sh', ['ssh-alias', 'configured', 'basic-plan-auto'], /basic-plan-auto is retired/],
    ['scripts/run-runner-test.sh', ['http://127.0.0.1:1', 'configured', 'basic-plan-auto'], /basic-plan-auto is retired/],
    ['scripts/run-runner-test.sh', ['ssh-alias', 'configured', 'unknown-runner'], /runner not found/],
    ['scripts/run-runner-test.sh', ['ssh-alias', 'configured', '../runners/task-program-worktrees'], /unsupported characters/],
    ['scripts/run-testbench-runner.sh', [], /runner-name is required/],
    ['scripts/run-testbench-runner.sh', ['--timeout-ms', '30000'], /runner-name is required/],
    ['scripts/run-testbench-runner.sh', ['basic-plan-auto'], /basic-plan-auto is retired/],
    ['scripts/run-testbench-runner.sh', ['unknown-runner'], /runner not found/],
    ['scripts/run-testbench-runner.sh', ['../runners/task-program-worktrees'], /unsupported characters/],
  ]
  for (const [script, args, message] of cases) {
    const result = shell(script, args, env)
    assert.notEqual(result.status, 0)
    assert.equal(result.stdout, '')
    assert.match(result.stderr, message)
    assert.doesNotMatch(result.stderr, /missing .*env/)
    assert.equal(existsSync(marker), false, 'admission must precede all execution')
    assert.deepEqual(readdirSync(scratch), ['bin'], 'rejection must not emit evidence')
  }
})

// Purpose: launch pre-run's canonical manifest/default argv must not invoke the
// retired scenario or replace an explicit old request. Dry-run is the narrowest
// production boundary proving selection; it cannot qualify any suite as passed.
test('pre-run defaults exclude retirement and explicit old suites fail without evidence', { timeout: 15000 }, t => {
  const { scratch, marker, env } = fixture(t)
  const listed = shell('scripts/run-testbench-launch-prerun.sh', ['--list-suites'], env)
  assert.equal(listed.status, 0)
  assert.doesNotMatch(listed.stdout, /plan-auto/)
  for (const suite of ['task-program', 'task-routing', 'desktop', 'tui', 'provider-sync']) {
    assert.ok(listed.stdout.split('\n').includes(suite))
  }
  for (const option of ['--suite', '--skip-suite']) {
    for (const retired of ['plan-auto', 'basic-plan-auto']) {
      const denied = shell('scripts/run-testbench-launch-prerun.sh', [option, retired, '--dry-run'], env)
      assert.notEqual(denied.status, 0)
      assert.equal(denied.stdout, '')
      assert.match(denied.stderr, /is retired.*no replacement is selected/)
      assert.equal(existsSync(marker), false)
    }
  }
  const config = path.join(scratch, 'testbench.env'), installed = path.join(scratch, 'installed.py')
  copyFileSync(path.join(root, '.env.example'), config)
  writeFileSync(installed, '# dry-run argv fixture only; never executed\n')
  const dry = shell('scripts/run-testbench-launch-prerun.sh', [
    '--dry-run', '--installed-onboarding-runner', installed,
    '--candidate-archive', 'candidate.tar.gz', '--candidate-checksum', 'candidate.tar.gz.sha256',
  ], { ...env, SWARM_TESTBENCH_ENV_FILE: config })
  assert.equal(dry.status, 0, dry.stderr)
  assert.doesNotMatch(dry.stdout, /plan-auto/)
  assert.match(dry.stdout, /run-testbench-runner\.sh task-program-worktrees/)
  assert.match(dry.stdout, /run-testbench-runner\.sh task-routing/)
  assert.equal(existsSync(marker), false)
  assert.deepEqual(readdirSync(scratch).sort(), ['bin', 'installed.py', 'testbench.env'])
})

// Purpose: supported selection still reaches each wrapper's normal boundary.
// run-runner-test argv forwarding and testbench role-model/tunnel adapter remain
// intact. Command spies record argv only, never fabricate provider/PASS evidence.
test('explicit Task Program runner preserves direct argv and testbench model forwarding', { timeout: 10000 }, t => {
  const { scratch, env } = fixture(t)
  const recorded = path.join(scratch, 'argv.json')
  writeFileSync(path.join(scratch, 'bin', 'node'), `#!/usr/bin/env bash\nexec '${process.execPath}' -e 'require("node:fs").writeFileSync(process.env.ARGV_RECORD, JSON.stringify(process.argv.slice(1)))' -- "$@"\n`, { mode: 0o700 })
  const direct = shell('scripts/run-runner-test.sh', [
    'http://127.0.0.1:1', 'configured-provider', 'task-program-worktrees',
    '--coder-model', 'configured-coder', '--coder-thinking', 'off', '--timeout-ms', '30000',
  ], { ...env, ARGV_RECORD: recorded })
  assert.equal(direct.status, 0, direct.stderr)
  const argv = JSON.parse(readFileSync(recorded, 'utf8'))
  assert.equal(argv[0], path.join(root, 'scripts/runners/task-program-worktrees.mjs'))
  assert.deepEqual(argv.slice(1), ['--api-url', 'http://127.0.0.1:1', '--provider', 'configured-provider',
    '--timeout-ms', '30000', '--coder-model', 'configured-coder', '--coder-thinking', 'off'])

  const checkout = path.join(scratch, 'fixture'), scripts = path.join(checkout, 'scripts')
  mkdirSync(path.join(scripts, 'runners'), { recursive: true })
  for (const name of ['run-testbench-runner.sh', 'lib-testbench-e2e.sh']) {
    copyFileSync(path.join(root, 'scripts', name), path.join(scripts, name))
  }
  copyFileSync(path.join(root, 'scripts/runners/task-program-worktrees.mjs'), path.join(scripts, 'runners/task-program-worktrees.mjs'))
  const config = path.join(scratch, 'testbench.env')
  copyFileSync(path.join(root, '.env.example'), config)
  writeFileSync(path.join(scripts, 'testbench-e2e-tunnel.sh'), `#!/usr/bin/env bash\nexec '${process.execPath}' -e 'require("node:fs").writeFileSync(process.env.ARGV_RECORD, JSON.stringify(process.argv.slice(1)))' -- "$@"\n`, { mode: 0o700 })
  const managed = shell(path.join(scripts, 'run-testbench-runner.sh'), ['task-program-worktrees', '--timeout-ms', '30000'], {
    ...env, SWARM_TESTBENCH_ENV_FILE: config, ARGV_RECORD: recorded,
  })
  assert.equal(managed.status, 0, managed.stderr)
  const tunnel = JSON.parse(readFileSync(recorded, 'utf8'))
  assert.deepEqual(tunnel.slice(0, 5), ['run', path.join(scripts, 'run-runner-test.sh'), '__SWARM_DESKTOP_URL__', 'fireworks', 'task-program-worktrees'])
  const configured = Object.fromEntries(readFileSync(config, 'utf8').split('\n')
    .filter(line => /^SWARM_TESTBENCH_(ACTION|PLAN|CODER|DESIGNER)_(MODEL|THINKING)=/.test(line))
    .map(line => line.split('=')))
  for (const role of ['ACTION', 'PLAN', 'CODER', 'DESIGNER']) {
    for (const setting of ['MODEL', 'THINKING']) {
      const flag = `--${role.toLowerCase()}-${setting.toLowerCase()}`
      assert.equal(tunnel[tunnel.indexOf(flag) + 1], configured[`SWARM_TESTBENCH_${role}_${setting}`])
    }
  }
  assert.deepEqual(tunnel.slice(-2), ['--timeout-ms', '30000'])
})

// Purpose: retirement does not remove current session/Orchestrator/media
// admission or Task Program explicit-model rejection. parseOptions and the
// Task Program top-level validation are the narrowest pre-provider boundaries;
// these assertions do not claim a live agent/provider or browser pass.
test('current session and orchestrator-chat scenarios and Task Program model guard remain', { timeout: 10000 }, t => {
  const { scratch } = fixture(t)
  assert.deepEqual([...SCENARIOS].sort(), ['audio', 'image', 'orchestrator-chat', 'session-api', 'video'])
  for (const scenario of ['session-api', 'orchestrator-chat']) {
    const options = parseOptions([
      '--api-url', 'http://127.0.0.1:1', '--workspace-path', path.join(scratch, 'source'),
      '--scenario', scenario, '--timeout-ms', '30000', '--candidate-revision', 'a'.repeat(40),
      '--run-id', 'unit-retirement', '--output', path.join(scratch, `${scenario}.json`),
    ], { TMPDIR: scratch, SWARM_RUNNER_TOKEN: 'unit-admission-not-a-credential' })
    assert.equal(options.scenario, scenario)
    assert.ok(requiredAssertions(scenario).length > 0)
    assert.equal(existsSync(options.output), false, 'parsing must not manufacture evidence')
  }
  const noModel = spawnSync(process.execPath, [path.join(root, 'scripts/runners/task-program-worktrees.mjs'),
    '--api-url', 'http://127.0.0.1:1', '--provider', 'configured-provider', '--timeout-ms', '30000'], {
    cwd: root, env: { PATH: process.env.PATH, TMPDIR: scratch },
    timeout: 5000, maxBuffer: 8192, encoding: 'utf8',
  })
  assert.equal(noModel.error, undefined)
  assert.equal(noModel.signal, null)
  assert.notEqual(noModel.status, 0)
  assert.equal(noModel.stdout, '')
  assert.match(noModel.stderr, /--coder-model is required/)
})

// Purpose: retirement means deletion, not an empty successful stub or retained
// directly runnable obsolete workflow. Filesystem assertion is supplementary to
// executable admission/manifest tests above; parent must perform actual deletion.
test('obsolete runner is absent, not a successful stub', () => {
  assert.equal(existsSync(path.join(root, 'scripts/runners/basic-plan-auto.mjs')), false)
})
