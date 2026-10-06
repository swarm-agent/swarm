// Purpose: parseArgs/validateFixtureLocation are the live runner's safety
// boundary. Pure argument and filesystem tests are narrower than launching a
// browser: reject public URLs, ambiguous options and symlink/outside fixtures
// before any daemon mutation. No live workloads, credentials or benchmark mocks.
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, symlinkSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { parseArgs, validateFixtureLocation, readModelSettings } from './run-new-task-smoke.mjs'

const args = ['--desktop-url', 'http://127.0.0.1:5655/', '--fixture-repo', '/scratch/fixture', '--model-settings-file', '/scratch/settings.json', '--isolated-no-provider-egress']
test('explicit bounded loopback options accepted', () => {
  assert.deepEqual(parseArgs(args), { origin: 'http://127.0.0.1:5655', fixture: '/scratch/fixture', timeoutMs: 120000, settingsFile: '/scratch/settings.json' })
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
    const assignment = { provider: 'operator-provider', model: 'operator-model' }
    writeFileSync(settings, JSON.stringify({ swarm: { action: assignment, plan: assignment }, token: 'must-not-forward' }))
    assert.deepEqual(readModelSettings(settings), { swarm: { action: assignment, plan: assignment } })
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
