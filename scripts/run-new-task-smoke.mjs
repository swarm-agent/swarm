#!/usr/bin/env node
// Purpose: bound the opt-in live browser test, not the legacy launch suite.
// Argument validation is the narrowest boundary preventing accidental host/public
// attachment or non-disposable repository mutations before any browser/API work.
import { spawn } from 'node:child_process'
import { realpathSync, statSync, readFileSync } from 'node:fs'
import { isAbsolute, relative, resolve, dirname, sep } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { desktopOrigin } from './testbench-attach.mjs'

export function parseArgs(args) {
  const values = {}
  for (let i = 0; i < args.length; i++) {
    const key = args[i]
    if (!['--desktop-url', '--fixture-repo', '--timeout-ms', '--model-settings-file', '--isolated-no-provider-egress'].includes(key) || key in values) throw new Error('unknown or duplicate argument')
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
  return { origin, fixture, timeoutMs, settingsFile }
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
  const swarm = input?.agent_model_settings?.swarm ?? input?.swarm
  const clean = {}
  for (const slot of ['action', 'plan']) {
    const assignment = swarm?.[slot]
    if (!assignment || typeof assignment.provider !== 'string' || typeof assignment.model !== 'string' || !assignment.provider.trim() || !assignment.model.trim()) throw new Error('canonical Swarm action/plan assignments required')
    clean[slot] = {}
    for (const key of ['provider', 'model', 'thinking', 'service_tier', 'context_mode']) {
      const value = assignment[key]
      if (value !== undefined && (typeof value !== 'string' || value.length > 200 || /[\r\n\0]/.test(value))) throw new Error('invalid non-secret model assignment')
      if (value !== undefined) clean[slot][key] = value
    }
  }
  return { swarm: clean }
}

async function main() {
  if (process.argv.slice(2).join(' ') === '--help') {
    console.log('Usage: node scripts/run-new-task-smoke.mjs --desktop-url http://127.0.0.1:PORT/ --fixture-repo ABSOLUTE_TMPDIR_REPO --model-settings-file ABSOLUTE_NON_SECRET_JSON --isolated-no-provider-egress [--timeout-ms 120000]')
    return
  }
  const options = parseArgs(process.argv.slice(2))
  options.fixture = validateFixtureLocation(options.fixture, process.env.TMPDIR)
  readModelSettings(options.settingsFile)
  const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
  if (process.versions.node.split('.')[0] !== '24') throw new Error('Node 24 required; install the checked-in web prerequisites')
  const child = spawn(process.execPath, ['--test', '--test-isolation=none', '--test-reporter=tap', `--test-timeout=${options.timeoutMs - 10000}`, 'e2e/new-task-smoke.test.mjs'], {
    cwd: resolve(root, 'web'), detached: true, stdio: ['ignore', 'pipe', 'pipe', 'ipc'],
    env: { ...process.env, SWARM_NEW_TASK_SMOKE_OPTIONS: JSON.stringify(options) },
  })
  // Never print TAP failures, Playwright diagnostics, headers or daemon bodies.
  // Playwright launches Chromium in its own process group. The trusted test
  // reports that exact child PID over private IPC, never through log parsing.
  let browserPid
  child.on('message', message => {
    if (message?.kind === 'owned-chromium' && Number.isSafeInteger(message.pid) && message.pid > 1 && !browserPid) browserPid = message.pid
  })
  let timedOut = false, outputBytes = 0, outputExceeded = false, interrupted = false, stage = 'prerequisites'
  const kill = () => {
    if (browserPid) {
      try { process.kill(-browserPid, 'SIGKILL') } catch {}
      try { process.kill(browserPid, 'SIGKILL') } catch {}
    }
    try { process.kill(-child.pid, 'SIGKILL') } catch {}
  }
  const timer = setTimeout(() => { timedOut = true; kill() }, options.timeoutMs)
  const onSignal = () => { interrupted = true; kill(); process.exitCode = 1 }
  process.once('SIGINT', onSignal); process.once('SIGTERM', onSignal)
  for (const stream of [child.stdout, child.stderr]) stream.on('data', chunk => {
    outputBytes += chunk.length
    for (const name of ['readiness', 'fixture-setup', 'browser-form', 'task-response', 'reload', 'cleanup']) {
      if (chunk.toString('utf8').includes(`SMOKE_STAGE=${name}`)) stage = name
    }
    if (outputBytes > 65536) { outputExceeded = true; kill() }
  })
  const code = await new Promise(resolveExit => { child.once('error', () => resolveExit(1)); child.once('exit', code => resolveExit(code)) })
  clearTimeout(timer); kill()
  process.removeListener('SIGINT', onSignal); process.removeListener('SIGTERM', onSignal)
  if (interrupted || timedOut || outputExceeded || code !== 0) throw new Error(interrupted ? 'interrupted; dispose the owned daemon state' : timedOut ? 'hard deadline exceeded; dispose the owned daemon state' : outputExceeded ? 'bounded output exceeded; dispose the owned daemon state' : `browser smoke failed during ${stage} (diagnostics suppressed); dispose the owned daemon state`)
  console.log('PASS: real browser task creation, duplicate guard, pending approval, zero run intents, durable reload and owned cleanup. Not agent completion or full harness proof.')
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch(error => { console.error(`New Task smoke failed: ${error.message}`); process.exitCode = 1 })
}
