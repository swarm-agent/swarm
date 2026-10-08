#!/usr/bin/env node
// Purpose: exclusively own fixture daemon state and supervise bootstrap, readiness,
// browser dispatch and disposal. No shell nesting, inherited credentials, shared
// state deletion, or success inferred from a ready daemon. Container-only.
import { spawn } from 'node:child_process'
import { mkdtempSync, mkdirSync, writeFileSync, rmSync, readFileSync, realpathSync, statSync } from 'node:fs'
import { resolve, dirname, join, isAbsolute } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { randomUUID } from 'node:crypto'
import { createServer } from 'node:net'
import { parseArgs, readModelSettings, validateFixtureLocation } from './run-new-task-smoke.mjs'

const source = resolve(dirname(fileURLToPath(import.meta.url)), '..')
export function parseWrapperArgs(args) {
  const flags = {}, smoke = []
  for (let i = 0; i < args.length; i++) {
    if (['--daemon-bin', '--bootstrap-bin', '--api-port', '--peer-port'].includes(args[i])) {
      const key = args[i], value = args[++i]
      if (!value || value.startsWith('--') || key in flags) throw new Error('wrapper_arguments')
      flags[key] = value
    } else { smoke.push(args[i]); if (args[i] !== '--isolated-no-provider-egress') smoke.push(args[++i]) }
  }
  if (smoke.includes('--owner')) throw new Error('owner_is_wrapper_generated')
  const owner = `new-task-smoke-${randomUUID()}`
  const options = parseArgs([...smoke, '--owner', owner])
  const ports = [Number(new URL(options.origin).port)]
  for (const key of ['--api-port', '--peer-port']) {
    if (!/^\d+$/.test(flags[key] ?? '') || Number(flags[key]) < 1024 || Number(flags[key]) > 65535) throw new Error('wrapper_port')
    ports.push(Number(flags[key]))
  }
  if (new Set(ports).size !== 3) throw new Error('wrapper_distinct_ports')
  for (const key of ['--daemon-bin', '--bootstrap-bin']) if (!isAbsolute(flags[key] ?? '')) throw new Error('wrapper_absolute_binary')
  return { ...options, owner, daemonBin: flags['--daemon-bin'], bootstrapBin: flags['--bootstrap-bin'], apiPort: ports[1], peerPort: ports[2] }
}

export async function unusedPort(port) {
  await new Promise((ok, fail) => {
    const server = createServer()
    server.once('error', () => fail(new Error('port_already_in_use')))
    server.listen(port, '127.0.0.1', () => server.close(ok))
  })
}

// Child output is private and capped, and never included in an exception.
export function ownedProcess(command, args, env, input) {
  const child = spawn(command, args, { env, cwd: source, detached: true, stdio: ['pipe', 'pipe', 'pipe'] })
  let bytes = 0, text = '', exceeded = false
  for (const stream of [child.stdout, child.stderr]) stream.on('data', chunk => {
    bytes += chunk.length
    if (bytes <= 65536) text += chunk.toString('utf8')
    else { exceeded = true; if (child.pid) { try { process.kill(-child.pid, 'SIGKILL') } catch {} } }
  })
  child.stdin.on('error', () => {})
  child.stdin.end(input)
  const result = new Promise(ok => {
    child.once('error', () => ok({ code: 1, text: '', exceeded: false }))
    child.once('close', code => ok({ code, text, exceeded }))
  })
  return { child, result }
}

export async function stopOwned(p) {
  if (!p) return
  if (p.child.pid) { try { process.kill(-p.child.pid, 'SIGTERM') } catch {} }
  let timer
  await Promise.race([p.result, new Promise(ok => { timer = setTimeout(ok, 3000) })])
  clearTimeout(timer)
  if (p.child.pid) { try { process.kill(-p.child.pid, 'SIGKILL') } catch {} }
  await p.result // reap before state disposal
}

export async function runWrapper(args, dependencies = {}) {
  const options = parseWrapperArgs(args)
  options.fixture = validateFixtureLocation(options.fixture, process.env.TMPDIR)
  const settings = readModelSettings(options.settingsFile)
  for (const bin of [options.daemonBin, options.bootstrapBin]) {
    if (realpathSync(bin) !== bin || !statSync(bin).isFile() || !(statSync(bin).mode & 0o111)) throw new Error('binary_unavailable')
  }
  (dependencies.validateDesktop ?? (() => { if (!statSync(join(source, 'web/dist/index.html')).isFile()) throw new Error('desktop_build_unavailable') }))()
  const root = mkdtempSync(join(realpathSync(process.env.TMPDIR), 'new-task-daemon-'))
  const processes = new Set()
  let interrupted = false, failure, passed = false, stage = 'bootstrap', timer
  const launch = dependencies.launch ?? ownedProcess
  const stop = dependencies.stop ?? stopOwned
  const signal = () => { interrupted = true; for (const p of processes) { if (p.child.pid) { try { process.kill(-p.child.pid, 'SIGTERM') } catch {} } } }
  process.once('SIGINT', signal); process.once('SIGTERM', signal)
  timer = setTimeout(signal, options.timeoutMs + 60000)
  const tracked = (...params) => { const p = launch(...params); processes.add(p); return p }
  async function bounded(p, ms) {
    let deadline
    try {
      return await Promise.race([p.result, new Promise((_, reject) => { deadline = setTimeout(() => { void stop(p); reject(new Error('step_deadline')) }, ms) })])
    } finally { clearTimeout(deadline) }
  }
  try {
    writeFileSync(join(root, 'owner'), options.owner, { mode: 0o600 })
    for (const dir of ['home', 'data', 'runtime', 'config', 'cache', 'logs']) mkdirSync(join(root, dir), { mode: 0o700 })
    const env = { PATH: process.env.PATH, HOME: join(root, 'home'), TMPDIR: process.env.TMPDIR }
    const boot = await bounded(tracked(options.bootstrapBin, ['--state-root', root, '--owner', options.owner], env, JSON.stringify(settings)), 15000)
    if (boot.code !== 0 || boot.exceeded || !/^SMOKE_BOOTSTRAP_OK$/m.test(boot.text)) throw new Error('bootstrap_receipt')
    if (interrupted) throw new Error('interrupted')
    stage = 'readiness'
    for (const port of [options.apiPort, options.peerPort, Number(new URL(options.origin).port)]) await (dependencies.unusedPort ?? unusedPort)(port)
    const startup = `swarm_name = New Task Smoke\nhost = 127.0.0.1\nport = ${options.apiPort}\ndesktop_port = ${new URL(options.origin).port}\npeer_transport_port = ${options.peerPort}\n`
    const daemon = tracked(options.daemonBin, ['--listen', `127.0.0.1:${options.apiPort}`, '--desktop-port', new URL(options.origin).port, '--data-dir', join(root, 'data/swarm'), '--db-path', join(root, 'db'), '--lock-path', join(root, 'runtime/swarmd.lock')], {
      ...env, STATE_DIRECTORY: join(root, 'data'), RUNTIME_DIRECTORY: join(root, 'runtime'), CONFIGURATION_DIRECTORY: join(root, 'config'), CACHE_DIRECTORY: join(root, 'cache'), LOGS_DIRECTORY: join(root, 'logs'), SWARM_CHILD_STARTUP_CONFIG: startup, SWARM_WEB_DIST_DIR: join(source, 'web/dist'), SWARM_DISABLE_MINT_REPORT: '1',
    })
    let daemonExited = false
    daemon.result.then(() => { daemonExited = true })
    const deadline = Date.now() + 25000
    let ready = false, status = 0
    while (!ready && Date.now() < deadline && !daemonExited && !interrupted) {
      try {
        const get = dependencies.fetch ?? fetch
        const api = await get(`http://127.0.0.1:${options.apiPort}/readyz`, { signal: AbortSignal.timeout(1500), redirect: 'error' })
        status = api.status; await api.body?.cancel()
        const desktop = await get(options.origin, { signal: AbortSignal.timeout(1500), redirect: 'error' })
        status = desktop.status; await desktop.body?.cancel()
        ready = api.status === 200 && desktop.status === 200
      } catch { status = 0 }
      if (!ready) await new Promise(ok => setTimeout(ok, 100)) // readiness, not workload
    }
    if (!ready || daemonExited || interrupted) throw new Error(`readiness_unavailable http=${status}`)
    console.log('ISOLATED_DAEMON_READY')
    stage = 'browser'
    const browserEnv = { ...env }
    for (const name of ['PLAYWRIGHT_BROWSERS_PATH', 'CHROMIUM_EXECUTABLE_PATH', 'LD_LIBRARY_PATH']) if (process.env[name]) browserEnv[name] = process.env[name]
    const result = await bounded(tracked(process.execPath, [join(source, 'scripts/run-new-task-smoke.mjs'), '--desktop-url', options.origin, '--fixture-repo', options.fixture, '--model-settings-file', options.settingsFile, '--owner', options.owner, '--isolated-no-provider-egress', '--timeout-ms', String(options.timeoutMs)], browserEnv), options.timeoutMs + 5000)
    for (const line of result.text.split('\n')) if (/^SMOKE_DIAGNOSTIC step=[a-z0-9_-]{1,60} error=[a-z0-9_-]{1,60}( http=\d{3})?$/.test(line)) console.error(line)
    if (result.code !== 0 || result.exceeded || !/^SMOKE_BROWSER_PASS tests=[1-9]\d*:/m.test(result.text) || daemonExited || interrupted) throw new Error('browser_receipt_missing_or_failed')
    passed = true
  } catch (error) { failure = error }
  finally {
    clearTimeout(timer)
    for (const p of [...processes].reverse()) {
      try { await stop(p) } catch { failure ??= new Error('process_reap_failed') }
    }
    // No API deletion: exact wrapper-generated state is the disposal boundary.
    try {
      if (realpathSync(root) !== root || readFileSync(join(root, 'owner'), 'utf8') !== options.owner) throw new Error()
      rmSync(root, { recursive: true })
    } catch { failure ??= new Error('owned_state_disposal_failed') }
    process.removeListener('SIGINT', signal); process.removeListener('SIGTERM', signal)
  }
  if (failure || interrupted || !passed) {
    const code = /^readiness_unavailable http=\d+$/.test(failure?.message ?? '') ? failure.message : /^[a-z_]+$/.test(failure?.message ?? '') ? failure.message : 'operation_failed'
    throw new Error(`step=${stage} error=${code}`)
  }
  console.log('SMOKE_ISOLATED_PASS tests=1 cleanup=disposed')
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  if (process.argv.slice(2).join(' ') === '--help') console.log('Usage: node scripts/run-new-task-smoke-isolated.mjs --daemon-bin ABS --bootstrap-bin ABS --desktop-url http://127.0.0.1:PORT/ --api-port PORT --peer-port PORT --fixture-repo ABS_TMPDIR_REPO --model-settings-file ABS_NON_SECRET_JSON --isolated-no-provider-egress [--timeout-ms 120000]')
  else runWrapper(process.argv.slice(2)).catch(error => {
    const safe = /^(?:step=[a-z_-]+ error=)?[a-z_-]+(?: http=\d+)?$/.test(error.message) ? error.message : 'preflight_invalid_or_unavailable'
    console.error('SMOKE_ISOLATED_FAILED ' + safe); process.exitCode = 1
  })
}
