#!/usr/bin/env node
// Purpose: fail closed before attaching to a real daemon, then supervise exactly
// one browser test. No API bodies, tokens or arbitrary test diagnostics escape.
import { spawn } from 'node:child_process'
import { realpathSync, statSync, readFileSync } from 'node:fs'
import { isAbsolute, relative, resolve, dirname, sep } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { desktopOrigin } from './testbench-attach.mjs'

export function parseArgs(args) {
  const values = {}
  for (let i = 0; i < args.length; i++) {
    const key = args[i]
    if (!['--desktop-url', '--fixture-repo', '--timeout-ms', '--model-settings-file', '--owner', '--isolated-no-provider-egress'].includes(key) || key in values) throw new Error('unknown or duplicate argument')
    if (key === '--isolated-no-provider-egress') { values[key] = true; continue }
    const value = args[++i]
    if (!value || value.startsWith('--')) throw new Error('argument value missing')
    values[key] = value
  }
  if (!values['--isolated-no-provider-egress']) throw new Error('explicit isolated, credential-free, provider-egress-denied acknowledgement required')
  let origin
  try { origin = desktopOrigin(values['--desktop-url']) } catch { throw new Error('explicit loopback Desktop root URL required') }
  const fixture = values['--fixture-repo']
  if (!fixture || !isAbsolute(fixture) || /[\r\n\0]/.test(fixture)) throw new Error('absolute disposable fixture repository required')
  const rawTimeout = values['--timeout-ms'] ?? '120000'
  const timeoutMs = Number(rawTimeout)
  if (!/^\d+$/.test(rawTimeout) || !Number.isSafeInteger(timeoutMs) || timeoutMs < 30000 || timeoutMs > 180000) throw new Error('timeout must be 30000..180000 milliseconds')
  const settingsFile = values['--model-settings-file']
  if (!settingsFile || !isAbsolute(settingsFile)) throw new Error('absolute non-secret canonical model-settings file required')
  const owner = values['--owner']
  if (!/^new-task-smoke-[a-f0-9-]{36}$/.test(owner ?? '')) throw new Error('exact wrapper-owned fixture identity required')
  return { origin, fixture, timeoutMs, settingsFile, owner }
}

export function validateFixtureLocation(fixture, tmpdir) {
  if (!tmpdir || !isAbsolute(tmpdir)) throw new Error('run-provided absolute TMPDIR required')
  let root, path
  try { root = realpathSync(tmpdir); path = realpathSync(fixture) } catch { throw new Error('TMPDIR or fixture unavailable') }
  const rel = relative(root, path)
  if (!rel || rel === '..' || rel.startsWith(`..${sep}`) || isAbsolute(rel) || path !== resolve(fixture) || !statSync(path).isDirectory()) throw new Error('fixture must be a real directory strictly inside TMPDIR, without symlink aliases')
  return path
}

export function readModelSettings(path) {
  let input
  try {
    if (!statSync(path).isFile() || statSync(path).size > 8192) throw new Error()
    input = JSON.parse(readFileSync(path, 'utf8'))
  } catch { throw new Error('bounded non-secret model-settings JSON unavailable') }
  input = input?.agent_model_settings ?? input
  const clean = { swarm: {}, system_agents: {} }
  for (const [group, slot] of [['swarm', 'action'], ['swarm', 'plan'], ...['compact', 'finder', 'coder', 'designer', 'router'].map(slot => ['system_agents', slot])]) {
    const assignment = input?.[group]?.[slot]
    if (!assignment || ['provider', 'model', 'thinking'].some(key => typeof assignment[key] !== 'string' || !assignment[key].trim())) throw new Error('canonical action/plan and all system-agent assignments with thinking required')
    clean[group][slot] = {}
    for (const key of ['provider', 'model', 'thinking', 'service_tier', 'context_mode']) {
      const value = assignment[key]
      if (value !== undefined && (typeof value !== 'string' || value.length > 200 || /[\r\n\0]/.test(value))) throw new Error('invalid non-secret model assignment')
      if (value !== undefined) clean[group][slot][key] = value
    }
  }
  return clean
}

// Purpose: actual subprocess dispatch must reject zero tests, early exit, skipped
// tests and forged readiness-only success. Hermetic protocol tests exercise this
// boundary; they are not browser/E2E evidence.
export async function dispatch(command, args, options, timeoutMs) {
  const child = spawn(command, args, { ...options, detached: true, stdio: ['ignore', 'pipe', 'pipe', 'ipc'] })
  let browserPid, success = false, diagnostic = '', lastStep = 'prerequisites'
  child.on('message', message => {
    if (message?.kind === 'smoke-success') success = true
    if (message?.kind === 'smoke-diagnostic' && /^[a-z0-9_-]{1,60}$/.test(message.step) && /^[a-z0-9_-]{1,60}$/.test(message.error)) {
      const status = Number.isInteger(message.status) && message.status >= 100 && message.status <= 599 ? ` http=${message.status}` : ''
      lastStep = message.step
      const line = `step=${message.step} error=${message.error}${status}`
      if (message.error !== 'none' && !diagnostic) diagnostic = line
      console.error('SMOKE_DIAGNOSTIC ' + line)
    }
    if (message?.kind === 'owned-chromium' && Number.isSafeInteger(message.pid) && message.pid > 1 && !browserPid) browserPid = message.pid
  })
  let timedOut = false, outputBytes = 0, outputExceeded = false, interrupted = false, tap = ''
  const kill = () => {
    if (browserPid) {
      try { process.kill(-browserPid, 'SIGKILL') } catch {}
      try { process.kill(browserPid, 'SIGKILL') } catch {}
    }
    if (child.pid) { try { process.kill(-child.pid, 'SIGKILL') } catch {} }
  }
  const timer = setTimeout(() => { timedOut = true; kill() }, timeoutMs)
  const onSignal = () => { interrupted = true; kill() }
  process.once('SIGINT', onSignal); process.once('SIGTERM', onSignal)
  for (const stream of [child.stdout, child.stderr]) stream.on('data', chunk => {
    outputBytes += chunk.length
    if (outputBytes <= 65536) tap += chunk.toString('utf8')
    else { outputExceeded = true; kill() }
  })
  const code = await new Promise(resolveExit => { child.once('error', () => resolveExit(1)); child.once('close', code => resolveExit(code)) })
  clearTimeout(timer); kill()
  process.removeListener('SIGINT', onSignal); process.removeListener('SIGTERM', onSignal)
  if (interrupted || timedOut || outputExceeded || code !== 0) throw new Error(interrupted ? 'interrupted' : timedOut ? 'deadline' : outputExceeded ? 'output_bound' : `child_failed ${diagnostic || `step=${lastStep} error=no_safe_diagnostic`}`)
  const count = name => Number(tap.match(new RegExp(`^# ${name} (\\d+)$`, 'm'))?.[1] ?? -1)
  const tests = count('tests')
  if (!success || tests < 1 || count('pass') !== tests || count('fail') !== 0 || count('cancelled') !== 0 || count('skipped') !== 0 || count('todo') !== 0) throw new Error('test_receipt_missing_or_incomplete')
  return { success, tests }
}

export async function runCLI(args, dependencies = {}) {
  if (args.join(' ') === '--help') {
    console.log('Usage: node scripts/run-new-task-smoke.mjs --desktop-url http://127.0.0.1:PORT/ --fixture-repo ABSOLUTE_TMPDIR_REPO --model-settings-file ABSOLUTE_NON_SECRET_JSON --owner WRAPPER_OWNER --isolated-no-provider-egress [--timeout-ms 120000]')
    return
  }
  const options = parseArgs(args)
  options.fixture = validateFixtureLocation(options.fixture, process.env.TMPDIR)
  readModelSettings(options.settingsFile)
  const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
  if (process.versions.node.split('.')[0] !== '24') throw new Error('Node 24 required')
  const result = await (dependencies.dispatch ?? dispatch)(process.execPath, ['--test', '--test-isolation=none', '--test-reporter=tap', `--test-timeout=${options.timeoutMs - 10000}`, 'e2e/new-task-smoke.test.mjs'], {
    cwd: resolve(root, 'web'), env: { ...process.env, SWARM_NEW_TASK_SMOKE_OPTIONS: JSON.stringify(options) },
  }, options.timeoutMs)
  if (!result.success || result.tests < 1) throw new Error('test_receipt_missing')
  console.log(`SMOKE_BROWSER_PASS tests=${result.tests}: creation, duplicate guard, pending approval, zero run intents and reload. Not agent completion.`)
  return result
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  runCLI(process.argv.slice(2)).catch(error => { console.error(`New Task smoke failed: ${error.message}`); process.exitCode = 1 })
}
