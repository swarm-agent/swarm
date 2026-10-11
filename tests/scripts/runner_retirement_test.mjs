import assert from 'node:assert/strict'
import test from 'node:test'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
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

// Purpose: run-runner-test owns runner selection. Reject
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

// Purpose: supported selection still reaches the wrapper's normal boundary.
// run-runner-test argv forwarding remains
// intact. Command spies record argv only, never fabricate provider/PASS evidence.
test('explicit Task Program runner preserves direct argv forwarding', { timeout: 10000 }, t => {
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
