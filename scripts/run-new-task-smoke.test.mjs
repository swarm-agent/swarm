// Purpose: parseArgs/validateFixtureLocation are the live runner's safety
// boundary. Pure argument and filesystem tests are narrower than launching a
// browser: reject public URLs, ambiguous options and symlink/outside fixtures
// before any daemon mutation. No live workloads, credentials or benchmark mocks.
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, symlinkSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { spawnSync } from 'node:child_process'
import { parseArgs, validateFixtureLocation, readModelSettings, dispatch, runCLI } from './run-new-task-smoke.mjs'

const owner = 'new-task-smoke-00000000-0000-4000-8000-000000000000'
const args = ['--desktop-url', 'http://127.0.0.1:5655/', '--fixture-repo', '/scratch/fixture', '--model-settings-file', '/scratch/settings.json', '--owner', owner, '--isolated-no-provider-egress']
const assignment = { provider: 'operator-provider', model: 'operator-model', thinking: 'operator-thinking' }
const settingsValue = { swarm: { action: assignment, plan: assignment }, system_agents: Object.fromEntries(['compact', 'finder', 'coder', 'designer', 'router'].map(slot => [slot, assignment])) }
test('explicit bounded loopback options accepted', () => {
  assert.deepEqual(parseArgs(args), { origin: 'http://127.0.0.1:5655', fixture: '/scratch/fixture', timeoutMs: 120000, settingsFile: '/scratch/settings.json', owner })
  assert.equal(parseArgs([...args, '--timeout-ms', '180000']).timeoutMs, 180000)
})
test('unsafe URLs, missing acknowledgement and ambiguous options rejected', () => {
  for (const url of ['http://example.invalid:5655/', 'http://0.0.0.0:5655/', 'https://127.0.0.1:5655/', 'http://127.0.0.1/', 'http://user:secret@127.0.0.1:5655/', 'http://127.0.0.1:5655/projects', 'http://127.0.0.1:5655/?token=secret']) {
    assert.throws(() => parseArgs([args[0], url, ...args.slice(2)]), /loopback/)
  }
  assert.throws(() => parseArgs(args.slice(0, -1)), /acknowledgement/)
  assert.throws(() => parseArgs([...args.slice(0, 4), '--isolated-no-provider-egress']), /model-settings/)
  assert.throws(() => parseArgs([...args.slice(0, 4), '--model-settings-file', 'relative', '--isolated-no-provider-egress']), /model-settings/)
  for (const extra of [['--desktop-url', 'http://127.0.0.1:5656/'], ['--unknown'], ['--timeout-ms'], ['--timeout-ms', '29999'], ['--timeout-ms', '180001'], ['--timeout-ms', '1e5'], ['--timeout-ms', 'NaN']]) assert.throws(() => parseArgs([...args, ...extra]))
  assert.throws(() => parseArgs(['--desktop-url', args[1], '--fixture-repo', 'relative', '--model-settings-file', '/scratch/settings.json', '--isolated-no-provider-egress']), /absolute/)
})
test('only a real disposable directory strictly under run TMPDIR accepted', () => {
  assert.ok(process.env.TMPDIR, 'parent must supply run TMPDIR')
  const scratch = mkdtempSync(join(process.env.TMPDIR, 'new-task-runner-validation-'))
  try {
    const settings = join(scratch, 'settings.json')
    writeFileSync(settings, JSON.stringify({ ...settingsValue, token: 'must-not-forward' }))
    assert.deepEqual(readModelSettings(settings), settingsValue)
    writeFileSync(settings, JSON.stringify({ swarm: { action: assignment } }))
    assert.throws(() => readModelSettings(settings), /action\/plan/)
    writeFileSync(settings, 'x'.repeat(8193))
    assert.throws(() => readModelSettings(settings), /bounded/)
    const fixture = join(scratch, 'fixture'); mkdirSync(fixture)
    assert.equal(validateFixtureLocation(fixture, scratch), fixture)
    assert.throws(() => validateFixtureLocation(scratch, scratch), /strictly/)
    assert.throws(() => validateFixtureLocation(fixture, undefined), /TMPDIR/)
    assert.throws(() => validateFixtureLocation(join(scratch, 'missing'), scratch), /unavailable/)
    const alias = join(scratch, 'alias'); symlinkSync(fixture, alias)
    assert.throws(() => validateFixtureLocation(alias, scratch), /symlink/)
    const sibling = join(scratch, 'sibling'); mkdirSync(sibling)
    assert.throws(() => validateFixtureLocation(sibling, fixture), /strictly/)
  } finally { rmSync(scratch, { recursive: true, force: true }) }
})

// Purpose: dispatch is the runner's actual process boundary, not just parseArgs.
// Protocol subprocesses are hermetic runner tests, NOT simulated agent workloads
// or browser evidence. A zero exit/readiness line, skip or missing IPC receipt
// must never be upgraded to an E2E pass; only a complete actual test may pass.
const protocolTest = `
import test from 'node:test';
const scenario = process.env.PROTOCOL_SCENARIO;
if (scenario === 'empty') { console.log('ISOLATED_DAEMON_READY'); process.exit(0); }
if (scenario === 'failed') { process.send({kind:'smoke-diagnostic',step:'patch_model_assignments',error:'http_status',status:404}); }
if (scenario === 'early') process.exit(3);
test('protocol only, not E2E', { skip: scenario === 'skip' }, () => {
  if (scenario === 'failed') throw new Error('secret-body-must-not-print');
  if (scenario === 'success' || scenario === 'skip') process.send({kind:'smoke-success'});
});
`
test('actual dispatcher accepts complete protocol and rejects fail, skip, zero and missing receipt', async () => {
  const scratch = mkdtempSync(join(process.env.TMPDIR, 'new-task-dispatch-'))
  try {
    const file = join(scratch, 'protocol.test.mjs'); writeFileSync(file, protocolTest)
    const invoke = scenario => {
      const cleanEnv = { ...process.env, PROTOCOL_SCENARIO: scenario }
      delete cleanEnv.NODE_TEST_CONTEXT
      return dispatch(process.execPath, ['--test', '--test-isolation=none', '--test-reporter=tap', file], { env: cleanEnv }, 5000)
    }
    assert.deepEqual(await invoke('success'), { success: true, tests: 1 })
    for (const scenario of ['empty', 'skip', 'no-receipt']) await assert.rejects(invoke(scenario), /receipt/)
    await assert.rejects(invoke('failed'), /step=patch_model_assignments error=http_status http=404/)
    await assert.rejects(invoke('early'), /child_failed/)
  } finally { rmSync(scratch, { recursive: true, force: true }) }
})

test('CLI dispatch is reached only after safety checks and propagates missing/failed receipt', async () => {
  const scratch = mkdtempSync(join(process.env.TMPDIR, 'new-task-cli-'))
  try {
    const fixture = join(scratch, 'fixture'); mkdirSync(fixture)
    const settings = join(scratch, 'settings.json'); writeFileSync(settings, JSON.stringify(settingsValue))
    const valid = ['--desktop-url', args[1], '--fixture-repo', fixture, '--model-settings-file', settings, '--owner', owner, '--isolated-no-provider-egress']
    let calls = 0
    const dependencies = { dispatch: async (command, argv, options, deadline) => {
      calls++
      assert.equal(command, process.execPath)
      assert.ok(argv.includes('e2e/new-task-smoke.test.mjs'))
      assert.equal(JSON.parse(options.env.SWARM_NEW_TASK_SMOKE_OPTIONS).owner, owner)
      assert.equal(deadline, 120000)
      return { success: true, tests: 1 }
    } }
    assert.deepEqual(await runCLI(valid, dependencies), { success: true, tests: 1 })
    assert.equal(calls, 1)
    await assert.rejects(runCLI([...valid, '--unknown'], dependencies))
    assert.equal(calls, 1)
    await assert.rejects(runCLI(valid, { dispatch: async () => ({ success: true, tests: 0 }) }), /receipt/)
    await assert.rejects(runCLI(valid, { dispatch: async () => { throw new Error('child_failed') } }), /child_failed/)
    const invalid = spawnSync(process.execPath, ['scripts/run-new-task-smoke.mjs', '--unknown'], { timeout: 5000, encoding: 'utf8', maxBuffer: 4096 })
    assert.equal(invalid.status, 1)
    assert.ok(!invalid.stdout.includes('SMOKE_BROWSER_PASS'))
  } finally { rmSync(scratch, { recursive: true, force: true }) }
})
