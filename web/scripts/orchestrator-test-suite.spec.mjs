import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import path from 'node:path'
import { spawnSync } from 'node:child_process'
import test from 'node:test'
import { fileURLToPath } from 'node:url'
import { ORCHESTRATOR_TEST_FILES, selectOrchestratorTests } from './orchestrator-test-suite.mjs'

const prefix = 'src/features/desktop/orchestrate/'
function fixture(t) {
  const root = mkdtempSync(path.join(process.env.TMPDIR || tmpdir(), 'orchestrator-selection-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  const directory = path.join(root, prefix)
  mkdirSync(directory, { recursive: true })
  for (const name of ['one.spec.ts', 'two.spec.tsx']) writeFileSync(path.join(directory, name), '')
  return { root, directory }
}

// Purpose: selectOrchestratorTests is the test-runner boundary, not app authority.
// This filesystem unit layer proves exact ordered .ts/.tsx selection and the real
// seed manifest's existence, without a provider, browser or broad test discovery.
test('explicit selection supports both extensions and preserves only reviewed files', { timeout: 5000 }, t => {
  const { root } = fixture(t)
  const files = [`${prefix}two.spec.tsx`, `${prefix}one.spec.ts`]
  assert.deepEqual(selectOrchestratorTests(root, files), files.map(file => `./${file}`))
  assert.deepEqual(files, [`${prefix}two.spec.tsx`, `${prefix}one.spec.ts`])
  const webRoot = fileURLToPath(new URL('../', import.meta.url))
  assert.deepEqual(selectOrchestratorTests(webRoot), ORCHESTRATOR_TEST_FILES.map(file => `./${file}`))
  assert.equal(ORCHESTRATOR_TEST_FILES.length, 7)
})

// Purpose: an empty/missing/partial manifest must fail before execution rather
// than pass an empty suite or run the surviving subset. selectOrchestratorTests
// validates the whole batch; this narrow layer observes rejection and no mutation.
test('empty, oversized, duplicate, missing and directory selections fail closed', { timeout: 5000 }, t => {
  const { root, directory } = fixture(t)
  mkdirSync(path.join(directory, 'directory.spec.ts'))
  const files = [`${prefix}one.spec.ts`, `${prefix}missing.spec.ts`]
  for (const selection of [[], null, Array(17).fill(files[0]), [files[0], files[0]], files, [`${prefix}directory.spec.ts`]]) {
    assert.throws(() => selectOrchestratorTests(root, selection))
  }
  assert.deepEqual(files, [`${prefix}one.spec.ts`, `${prefix}missing.spec.ts`])
  assert.deepEqual(selectOrchestratorTests(root, [files[0]]), [`./${files[0]}`])
})

// Purpose: deterministic Node selection must not admit browser/live/e2e files,
// globs, traversal, unrelated chat tests or symlink escapes. The selector's path
// and realpath checks are the narrowest boundary preventing accidental live runs.
test('browser/provider paths, discovery patterns and symlink redirects are rejected', { timeout: 5000 }, t => {
  const { root, directory } = fixture(t)
  for (const name of ['one.browser.spec.ts', 'one.live.spec.tsx', 'one.e2e.spec.ts']) writeFileSync(path.join(directory, name), '')
  for (const file of [
    `${prefix}one.browser.spec.ts`, `${prefix}one.live.spec.tsx`, `${prefix}one.e2e.spec.ts`,
    `${prefix}*.spec.ts`, `${prefix}../chat.spec.ts`, `${prefix}nested/one.spec.ts`,
    'src/features/desktop/chat/one.spec.ts', path.join(root, prefix, 'one.spec.ts'),
  ]) assert.throws(() => selectOrchestratorTests(root, [file]), /Not a deterministic/)
  const outside = path.join(root, 'outside.spec.ts')
  writeFileSync(outside, '')
  symlinkSync(outside, path.join(directory, 'escape.spec.ts'))
  symlinkSync(path.join(directory, 'one.spec.ts'), path.join(directory, 'alias.spec.ts'))
  for (const name of ['escape.spec.ts', 'alias.spec.ts']) {
    assert.throws(() => selectOrchestratorTests(root, [`${prefix}${name}`]), /symlink/)
  }
})

// Purpose: the CLI must reject user-supplied discovery/filters rather than turn
// the seed suite into a no-op or browser run. A bounded subprocess proves its
// observable nonzero exit without loading tsx or executing application tests.
test('runner rejects additional selectors before starting tests', { timeout: 5000 }, () => {
  const runner = fileURLToPath(new URL('./run-orchestrator-tests.mjs', import.meta.url))
  const result = spawnSync(process.execPath, [runner, '--test-name-pattern=missing'], {
    encoding: 'utf8', timeout: 3000, maxBuffer: 4096,
  })
  assert.equal(result.error, undefined)
  assert.equal(result.status, 1)
  assert.match(result.stderr, /accepts no extra selectors/)
  assert.equal(result.stdout, '')
})

// Purpose: the retained run-desktop-launch-test.sh compatibility entrypoint must
// report retirement with failure, never qualify an empty suite or contact a target.
// The shell boundary is the narrowest observable proof; only --help may succeed.
test('retired Desktop launch runner fails before any browser/provider work', { timeout: 5000 }, () => {
  const runner = fileURLToPath(new URL('../../scripts/run-desktop-launch-test.sh', import.meta.url))
  for (const args of [[], ['unused-target', 'unused-provider']]) {
    const result = spawnSync('bash', [runner, ...args], {
      encoding: 'utf8', timeout: 1000, maxBuffer: 4096,
    })
    assert.equal(result.error, undefined)
    assert.equal(result.status, 1)
    assert.match(result.stderr, /retired legacy chat journeys/)
    assert.equal(result.stdout, '')
  }
  const help = spawnSync('bash', [runner, '--help'], {
    encoding: 'utf8', timeout: 1000, maxBuffer: 4096,
  })
  assert.equal(help.error, undefined)
  assert.equal(help.status, 0)
  assert.match(help.stdout, /No replacement browser suite exists yet/)
})

// Purpose: PR composition must add the exact Orchestrator suite without replacing
// still-supported session API/auth/hydration/realtime coverage or adding paid
// journeys. package.json is the script-selection authority, not app security;
// this narrow manifest test asserts composition and retained explicit filenames.
test('PR frontend entrypoint preserves critical session API coverage', { timeout: 5000 }, async () => {
  const { readFileSync } = await import('node:fs')
  const pkg = JSON.parse(readFileSync(new URL('../package.json', import.meta.url), 'utf8'))
  assert.equal(pkg.scripts['test:pr'], 'pnpm run test:critical && pnpm run test:orchestrator:selection && pnpm run test:orchestrator')
  const tokens = pkg.scripts['test:critical'].split(/\s+/)
  for (const file of [
    './src/features/desktop/session-v3/new-session-flow.spec.ts',
    './src/features/desktop/session-v3/existing-session-flow.spec.ts',
    './src/features/desktop/session-v3/write-api.spec.ts',
    './src/features/desktop/realtime/local-session-auth.spec.ts',
    './src/features/desktop/state/desktop-v3-sync-api.spec.ts',
    './src/features/desktop/state/session-snapshot-hydration.spec.ts',
  ]) assert.ok(tokens.includes(file), `PR selection lost supported coverage: ${file}`)
  assert.ok(!tokens.some(token => /browser|e2e|runners|orchestrator-pr/.test(token)))
})
