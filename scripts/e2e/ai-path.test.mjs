// Unit tests for the ai-path e2e: argument parsing, receipt format and secret
// redaction. Hermetic: no daemon, no network. Run: node --test scripts/e2e/*.test.mjs

import assert from 'node:assert/strict'
import { spawnSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import test from 'node:test'
import { fileURLToPath } from 'node:url'

import { parseArgs, SCENARIOS, UsageError } from './lib/args.mjs'
import { removeRunDir, createRunDir } from './lib/daemon.mjs'
import { KEY_LINK_PATTERN, pageMessage } from './lib/key-page.mjs'
import { DIAGNOSTIC_PREFIX, Receipt, RECEIPT_SCHEMA, redact, shortDetail } from './lib/receipt.mjs'

const DIR = path.dirname(fileURLToPath(import.meta.url))
const base = ['--scenario', 'onboard', '--bin-dir', '/b', '--work-dir', '/w', '--output', '/o.json']
// Shaped like a Google key, but not one: assembled so no scanner sees a literal.
const FAKE_KEY = ['AIza', 'Sy', 'FAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKE12'].join('')

test('parseArgs: required flags and defaults', () => {
  const opts = parseArgs(base)
  assert.equal(opts.scenario, 'onboard')
  assert.equal(opts.binDir, '/b')
  assert.equal(opts.workDir, '/w')
  assert.equal(opts.output, '/o.json')
  assert.equal(opts.model, 'gemini-3.8-flash')
  assert.equal(opts.thinking, 'low')
  assert.equal(opts.timeoutMs, 240000)
  assert.equal(opts.keyFile, undefined)
  assert.equal(opts.loadKeyCmd, undefined)
})

test('parseArgs: every scenario and every option, both spellings', () => {
  for (const scenario of SCENARIOS) assert.equal(parseArgs(['--scenario', scenario, ...base.slice(2)]).scenario, scenario)
  const opts = parseArgs([...base, '--key-file', '/k', '--model=gemini-3.8-flash', '--thinking', 'high', '--timeout-ms=600000', '--sdk-pack', '/p.tgz'])
  assert.equal(opts.keyFile, '/k')
  assert.equal(opts.thinking, 'high')
  assert.equal(opts.timeoutMs, 600000)
  assert.equal(opts.sdkPack, '/p.tgz')
  const loader = parseArgs([...base, '--load-key-cmd', 'load-test-key {socket}'])
  assert.equal(loader.loadKeyCmd, 'load-test-key {socket}')
})

test('parseArgs: rejects bad input', () => {
  const bad = [
    [],
    base.slice(2),
    ['--scenario', 'nope', ...base.slice(2)],
    [...base, '--key-file', '/k', '--load-key-cmd', 'x {socket}'],
    [...base, '--load-key-cmd', 'no placeholder'],
    [...base, '--timeout-ms', '5'],
    [...base, '--timeout-ms', 'abc'],
    [...base, '--model', 'bad model'],
    [...base, '--bogus', '1'],
    [...base, '--output'],
    [...base, '--scenario', 'chat'],
  ]
  for (const argv of bad) assert.throws(() => parseArgs(argv), UsageError, JSON.stringify(argv))
  assert.equal(parseArgs(['--help']).help, true)
})

test('parseArgs: an unknown argument is not echoed in full (a mistyped secret stays out of logs)', () => {
  try {
    parseArgs([...base, `--api-key=${FAKE_KEY}`])
    assert.fail('expected UsageError')
  } catch (err) {
    assert.ok(err instanceof UsageError)
    assert.ok(!err.message.includes(FAKE_KEY))
  }
})

test('redact: registered secrets and token shapes', () => {
  // Token shapes are assembled at run time so the repository secret scan never
  // sees a key-shaped literal.
  const swk = ['swk', 'abcdefgh12345678'].join('_')
  const sk = ['sk', 'proj', 'abcdefghijklmnopqrst'].join('-')
  const apiKey = ['api_key', 'hunter2hunter2'].join('=')
  const text = `key ${FAKE_KEY} bearer Bearer abcdefghijklmnop ${swk} ${sk} link http://127.0.0.1:4000/keys/${'a'.repeat(43)} ${apiKey} custom-secret-value`
  const out = redact(text, ['custom-secret-value'])
  for (const leaked of [FAKE_KEY, 'abcdefghijklmnop', 'swk_abcdefgh', 'sk-proj-abc', 'a'.repeat(43), 'hunter2hunter2', 'custom-secret-value']) {
    assert.ok(!out.includes(leaked), `${leaked} leaked: ${out}`)
  }
  assert.match(out, /\/keys\/\[redacted\]/)
  assert.match(out, /Bearer \[redacted\]/)
  assert.equal(redact('plain words stay as they are'), 'plain words stay as they are')
})

test('shortDetail: one line, bounded, redacted', () => {
  const long = `line one\nline two ${'x'.repeat(1000)}`
  const out = shortDetail(long)
  assert.ok(out.length <= 300)
  assert.ok(!out.includes('\n'))
  assert.equal(shortDetail({ a: 1 }), '{"a":1}')
  assert.ok(!shortDetail(`error: ${FAKE_KEY}`).includes(FAKE_KEY))
})

test('Receipt: schema, fields and pass/fail', () => {
  let clock = 1000
  const receipt = new Receipt('chat', { now: () => clock })
  receipt.addSecret('my-secret-key-value')
  receipt.check('a.first', true, 'fine')
  clock = 3500
  receipt.daemonVersion = 'v1.2.3'
  let json = receipt.toJSON()
  assert.deepEqual(Object.keys(json).sort(), ['assertions', 'daemon_version', 'duration_ms', 'passed', 'scenario', 'schema'])
  assert.equal(json.schema, RECEIPT_SCHEMA)
  assert.equal(json.schema, 'swarm.ai-path-e2e.v1')
  assert.equal(json.scenario, 'chat')
  assert.equal(json.passed, true)
  assert.equal(json.duration_ms, 2500)
  assert.equal(json.daemon_version, 'v1.2.3')
  assert.deepEqual(json.assertions, [{ name: 'a.first', ok: true, detail: 'fine' }])
  assert.equal(receipt.diagnosticLine(), null)

  receipt.check('b.second', false, 'the server said my-secret-key-value was wrong')
  receipt.check('c.third', false, 'later failure')
  json = receipt.toJSON()
  assert.equal(json.passed, false)
  assert.ok(!JSON.stringify(json).includes('my-secret-key-value'))
  const line = receipt.diagnosticLine()
  assert.ok(line.startsWith(DIAGNOSTIC_PREFIX))
  assert.ok(!line.includes('\n'))
  const payload = JSON.parse(line.slice(DIAGNOSTIC_PREFIX.length))
  assert.deepEqual(Object.keys(payload), ['scenario', 'failed', 'detail'])
  assert.equal(payload.scenario, 'chat')
  assert.equal(payload.failed, 'b.second')
  assert.ok(!payload.detail.includes('my-secret-key-value'))
})

test('Receipt: no assertions is a failure', () => {
  const receipt = new Receipt('onboard')
  assert.equal(receipt.passed, false)
  assert.equal(JSON.parse(receipt.diagnosticLine().slice(DIAGNOSTIC_PREFIX.length)).failed, 'no_assertions')
})

test('key page helpers: link shape and status message', () => {
  assert.ok(KEY_LINK_PATTERN.test(`http://127.0.0.1:41234/keys/${'A'.repeat(43)}`))
  assert.ok(!KEY_LINK_PATTERN.test(`http://example.com/keys/${'A'.repeat(43)}`))
  assert.ok(!KEY_LINK_PATTERN.test(`http://127.0.0.1:41234/keys/short`))
  assert.deepEqual(pageMessage('<p class="ok" role="status">Saved. Swarm verified the key with google. You can close this page.</p>'), { kind: 'ok', text: 'Saved. Swarm verified the key with google. You can close this page.' })
  assert.equal(pageMessage('<p>nothing</p>'), null)
})

test('run dirs: unique, private, and only they can be removed', () => {
  const work = fs.mkdtempSync(path.join(os.tmpdir(), 'ai-path-unit-'))
  try {
    const a = createRunDir(work, 'chat')
    const b = createRunDir(work, 'chat')
    assert.notEqual(a, b)
    assert.match(path.basename(a), /^sw-cht-[A-Za-z0-9]{6}$/)
    assert.equal(fs.statSync(a).mode & 0o777, 0o700)
    for (const dir of ['cfg', 'data', 'cache', 'run', 'log', 'home']) assert.ok(fs.statSync(path.join(a, dir)).isDirectory())
    assert.throws(() => removeRunDir(work, work))
    assert.throws(() => removeRunDir(path.join(a, 'data'), work))
    assert.throws(() => removeRunDir('', work))
    removeRunDir(a, work)
    assert.ok(!fs.existsSync(a))
    assert.ok(fs.existsSync(b))
  } finally {
    fs.rmSync(work, { recursive: true, force: true })
  }
})

test('CLI: usage errors exit 2 without a receipt', () => {
  const result = spawnSync(process.execPath, [path.join(DIR, 'ai-path.mjs'), '--scenario', 'nope'], { encoding: 'utf8' })
  assert.equal(result.status, 2)
  assert.match(result.stderr, /--scenario|is required/)
})

test('CLI: a missing bin dir fails with a receipt and one diagnostic line', () => {
  const work = fs.mkdtempSync(path.join(os.tmpdir(), 'ai-path-unit-'))
  try {
    const output = path.join(work, 'receipt.json')
    const result = spawnSync(process.execPath, [path.join(DIR, 'ai-path.mjs'), '--scenario', 'onboard', '--bin-dir', path.join(work, 'missing'), '--work-dir', work, '--output', output], { encoding: 'utf8' })
    assert.equal(result.status, 1)
    const receipt = JSON.parse(fs.readFileSync(output, 'utf8'))
    assert.equal(receipt.schema, 'swarm.ai-path-e2e.v1')
    assert.equal(receipt.passed, false)
    const lines = result.stdout.split('\n').filter((l) => l.startsWith(DIAGNOSTIC_PREFIX))
    assert.equal(lines.length, 1)
    assert.match(lines[0], /--bin-dir is not a directory/)
  } finally {
    fs.rmSync(work, { recursive: true, force: true })
  }
})
