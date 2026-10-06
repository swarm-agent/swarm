// Purpose: runWrapper owns daemon startup/disposal and must preserve a browser
// failure or readiness-only early exit, never report success for it. This narrow
// lifecycle unit test uses injected process receipts, not synthetic E2E/agents.
// Separate dispatcher tests execute real subprocesses. Live proof is parent-owned.
import test from 'node:test'
import assert from 'node:assert/strict'
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, existsSync } from 'node:fs'
import { join } from 'node:path'
import { parseWrapperArgs, runWrapper } from './run-new-task-smoke-isolated.mjs'

const base = ['--daemon-bin', '/scratch/swarmd', '--bootstrap-bin', '/scratch/bootstrap', '--desktop-url', 'http://127.0.0.1:15655/', '--api-port', '17881', '--peer-port', '17882', '--fixture-repo', '/scratch/fixture', '--model-settings-file', '/scratch/settings.json', '--isolated-no-provider-egress']
test('wrapper requires distinct loopback ports, absolute binaries and generated ownership', () => {
  const options = parseWrapperArgs(base)
  assert.equal(options.apiPort, 17881)
  assert.match(options.owner, /^new-task-smoke-/)
  for (const extra of [['--peer-port', '20000'], ['--owner', 'caller-owner'], ['--unknown'], ['--api-port', 'bad']]) assert.throws(() => parseWrapperArgs([...base, ...extra]))
  assert.throws(() => parseWrapperArgs(base.map(value => value === '17882' ? '17881' : value)), /distinct/)
  assert.throws(() => parseWrapperArgs(base.map(value => value === '/scratch/swarmd' ? 'relative' : value)), /absolute/)
})

test('supervisor dispatches browser and reaps/disposes its exact state on success or failure', async () => {
  const scratch = mkdtempSync(join(process.env.TMPDIR, 'new-task-wrapper-test-'))
  const fixture = join(scratch, 'fixture'); mkdirSync(fixture)
  const settings = join(scratch, 'settings.json')
  const assignment = { provider: 'operator-provider', model: 'operator-model', thinking: 'operator-thinking' }
  writeFileSync(settings, JSON.stringify({ swarm: { action: assignment, plan: assignment }, system_agents: Object.fromEntries(['compact', 'finder', 'coder', 'designer', 'router'].map(slot => [slot, assignment])) }))
  const binary = join(scratch, 'binary'); writeFileSync(binary, 'unit-test-only', { mode: 0o700 })
  const args = base.map(value => value === '/scratch/swarmd' || value === '/scratch/bootstrap' ? binary : value === '/scratch/fixture' ? fixture : value === '/scratch/settings.json' ? settings : value)
  try {
    for (const scenario of ['success', 'readiness-only', 'failure', 'daemon-exit', 'bootstrap-failure']) {
      const stopped = [], calls = []
      let state, daemonResolve
      const dependencies = {
        validateDesktop: () => {}, unusedPort: async () => {},
        fetch: async () => ({ status: 200, body: { cancel: async () => {} } }),
        launch: (command, argv, env, input) => {
          const step = calls.length; calls.push({ command, argv, env, input })
          if (step === 0) {
            state = argv[1]
            assert.ok(existsSync(join(state, 'owner')))
            assert.ok(!Object.hasOwn(env, 'OPENAI_API_KEY'))
            const data = JSON.parse(input)
            assert.deepEqual(data.swarm.action, assignment)
            return { child: {}, result: Promise.resolve({ code: scenario === 'bootstrap-failure' ? 1 : 0, text: 'SMOKE_BOOTSTRAP_OK\n' }) }
          }
          if (step === 1) {
            assert.equal(env.HOME, join(state, 'home'))
            return { child: {}, result: scenario === 'daemon-exit' ? Promise.resolve({ code: 1, text: '' }) : new Promise(ok => { daemonResolve = ok }) }
          }
          assert.equal(command, process.execPath)
          assert.ok(argv[0].endsWith('/run-new-task-smoke.mjs'))
          assert.ok(argv.includes('--owner'))
          return { child: {}, result: Promise.resolve({ code: scenario === 'failure' ? 1 : 0, text: scenario === 'success' ? 'SMOKE_BROWSER_PASS tests=1: unit-protocol-only\n' : 'ISOLATED_DAEMON_READY\n' }) }
        },
        stop: async p => { stopped.push(p); daemonResolve?.({ code: 0, text: '' }); await p.result },
      }
      if (scenario === 'success') await runWrapper(args, dependencies)
      else await assert.rejects(runWrapper(args, dependencies), /receipt|readiness_unavailable/)
      assert.equal(stopped.length, calls.length)
      assert.ok(!existsSync(state), 'exact generated state must be disposed even after failed setup')
      assert.ok(existsSync(fixture), 'supplied fixture is never deleted')
      assert.ok(existsSync(settings), 'non-secret operator input is never deleted')
      if (scenario === 'success' || scenario === 'failure' || scenario === 'readiness-only') assert.equal(calls.length, 3)
    }
  } finally { rmSync(scratch, { recursive: true, force: true }) }
})
