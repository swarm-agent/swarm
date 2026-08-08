#!/usr/bin/env node
import { spawn } from 'node:child_process'
import fs from 'node:fs/promises'
import { createRequire } from 'node:module'
import net from 'node:net'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const WEB_PACKAGE = path.join(ROOT, 'web', 'package.json')
const DEFAULT_PROMPT = 'Do not call tools. Think briefly about why 17 is prime, then reply with exactly STREAM_ORDER_FINAL.'

function fail(message) { throw new Error(message) }
function value(args, index, flag) {
  const result = args[index + 1]
  if (!result || result.startsWith('--')) fail(`${flag} requires a value`)
  return result
}
function stamp() { return new Date().toISOString().replace(/[-:]/g, '').replace(/\.\d{3}Z$/, 'Z') }
function sleep(ms) { return new Promise((resolve) => setTimeout(resolve, ms)) }
function usage() {
  console.log(`Usage: node scripts/diagnose-desktop-v3-stream-order.mjs [options]

Runs a real Desktop V3 AI turn and captures redacted realtime arrival order plus
sampled desktop-chat-row DOM order. Evidence defaults to tmp/desktop-v3-stream-order/.

  --ssh <target>                 SSH tunnel target (or set SWARM_PRIMARY_SSH)
  --url <url>                    Direct Desktop URL instead of the tunnel URL
  --remote-desktop-port <port>   Remote Desktop port (default: 5555)
  --workspace <name|path>        Workspace selector (default: swarm-go)
  --provider <provider>          Explicit provider (default: fireworks)
  --model <model>                Explicit model
  --thinking <level>             Explicit thinking level (default: low)
  --prompt <text>                Real no-tools prompt
  --artifact-dir <path>          Evidence directory under tmp/
  --browser-executable <path>    Playwright Chromium executable
  --headful                      Show the browser
  --timeout-ms <ms>              Completion timeout (default: 180000)
  --help                         Show help`)
}
function parseArgs(argv) {
  const opts = {
    ssh: process.env.SWARM_PRIMARY_SSH || '', url: process.env.SWARM_DESKTOP_URL || '',
    remotePort: Number(process.env.SWARM_REMOTE_DESKTOP_PORT || '') || 5555,
    workspace: process.env.SWARM_STREAM_ORDER_WORKSPACE || 'swarm-go',
    provider: process.env.SWARM_PROVIDER || 'fireworks',
    model: process.env.SWARM_MODEL || 'accounts/fireworks/models/kimi-k2p6',
    thinking: process.env.SWARM_THINKING || 'low', prompt: process.env.SWARM_STREAM_ORDER_PROMPT || DEFAULT_PROMPT,
    artifactDir: '', browserExecutable: process.env.PLAYWRIGHT_BROWSER_EXECUTABLE || '',
    headless: true, timeoutMs: Number(process.env.SWARM_STREAM_ORDER_TIMEOUT_MS || '') || 180_000, help: false,
  }
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i]
    if (arg === '--help' || arg === '-h') opts.help = true
    else if (arg === '--headful') opts.headless = false
    else if (['--ssh', '--primary-ssh'].includes(arg)) { opts.ssh = value(argv, i, arg); i += 1 }
    else if (arg === '--url') { opts.url = value(argv, i, arg).replace(/\/+$/, ''); i += 1 }
    else if (arg === '--remote-desktop-port') { opts.remotePort = Number(value(argv, i, arg)); i += 1 }
    else if (arg === '--workspace') { opts.workspace = value(argv, i, arg); i += 1 }
    else if (arg === '--provider') { opts.provider = value(argv, i, arg); i += 1 }
    else if (arg === '--model') { opts.model = value(argv, i, arg); i += 1 }
    else if (arg === '--thinking') { opts.thinking = value(argv, i, arg); i += 1 }
    else if (arg === '--prompt') { opts.prompt = value(argv, i, arg); i += 1 }
    else if (['--artifact-dir', '--evidence-dir'].includes(arg)) { opts.artifactDir = value(argv, i, arg); i += 1 }
    else if (arg === '--browser-executable') { opts.browserExecutable = value(argv, i, arg); i += 1 }
    else if (arg === '--timeout-ms') { opts.timeoutMs = Number(value(argv, i, arg)); i += 1 }
    else fail(`unknown argument: ${arg}`)
  }
  if (!opts.ssh && !opts.url) fail('pass --ssh/set SWARM_PRIMARY_SSH, or pass --url/set SWARM_DESKTOP_URL')
  if (!Number.isFinite(opts.remotePort) || opts.remotePort <= 0) fail('--remote-desktop-port must be positive')
  if (!Number.isFinite(opts.timeoutMs) || opts.timeoutMs <= 0) fail('--timeout-ms must be positive')
  if (!opts.artifactDir) opts.artifactDir = path.join(ROOT, 'tmp', 'desktop-v3-stream-order', stamp())
  return opts
}
function playwright() {
  try { return createRequire(WEB_PACKAGE)('playwright') }
  catch (error) { fail(`Playwright is unavailable in web/: ${error instanceof Error ? error.message : String(error)}`) }
}
async function freePort() {
  const server = net.createServer()
  await new Promise((resolve, reject) => { server.once('error', reject); server.listen(0, '127.0.0.1', resolve) })
  const address = server.address()
  if (!address || typeof address !== 'object') fail('failed to allocate tunnel port')
  await new Promise((resolve) => server.close(resolve))
  return address.port
}
async function startTunnel(opts) {
  if (!opts.ssh) return { url: opts.url || 'http://127.0.0.1:5555', close: async () => {} }
  const port = await freePort()
  const child = spawn('ssh', ['-o', 'ExitOnForwardFailure=yes', '-N', '-L', `${port}:127.0.0.1:${opts.remotePort}`, opts.ssh], { stdio: ['ignore', 'ignore', 'pipe'] })
  let stderr = ''
  child.stderr.on('data', (chunk) => { stderr += String(chunk) })
  const deadline = Date.now() + 15_000
  while (Date.now() < deadline) {
    if (child.exitCode !== null) {
      const error = `SSH tunnel failed: ${stderr.trim() || `exit ${child.exitCode}`}`
      await writeSetupFailure(opts, error)
      fail(error)
    }
    const open = await new Promise((resolve) => {
      const socket = net.connect({ host: '127.0.0.1', port })
      socket.once('connect', () => { socket.destroy(); resolve(true) })
      socket.once('error', () => { socket.destroy(); resolve(false) })
      socket.setTimeout(300, () => { socket.destroy(); resolve(false) })
    })
    if (open) return { url: opts.url || `http://127.0.0.1:${port}`, close: async () => { child.kill('SIGTERM') } }
    await sleep(100)
  }
  child.kill('SIGTERM')
  const error = `timed out opening SSH tunnel${stderr.trim() ? `: ${stderr.trim()}` : ''}`
  await writeSetupFailure(opts, error)
  fail(error)
}
async function writeSetupFailure(opts, error) {
  const summary = {
    ok: false,
    classification: 'external_ssh_access_failure',
    ssh: opts.ssh || null,
    workspace: opts.workspace,
    preference: { provider: opts.provider, model: opts.model, thinking: opts.thinking },
    error,
  }
  await fs.writeFile(path.join(opts.artifactDir, 'summary.json'), `${JSON.stringify(summary, null, 2)}\n`)
}
function basename(value) { return String(value || '').replace(/[\\/]+$/, '').split(/[\\/]/).filter(Boolean).at(-1) || '' }
function workspaceName(item) { return String(item.workspace_name ?? item.workspaceName ?? '').trim() || basename(item.path) }
function slug(value) { return value.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'workspace' }
function hash(value) { let result = 2166136261; for (const char of value) { result ^= char.charCodeAt(0); result = Math.imul(result, 16777619) }; return (result >>> 0).toString(36) }
function selectWorkspace(items, selector) {
  const lower = selector.toLowerCase()
  const selected = items.find((item) => String(item.path || '') === selector)
    || items.find((item) => workspaceName(item).toLowerCase() === lower || basename(item.path).toLowerCase() === lower)
  if (!selected) fail(`workspace ${selector} not found`)
  const base = slug(workspaceName(selected)) === 'swarm' ? 'swarm-workspace' : slug(workspaceName(selected))
  const duplicates = items.filter((item) => (slug(workspaceName(item)) === 'swarm' ? 'swarm-workspace' : slug(workspaceName(item))) === base).length
  return { selected, routeSlug: duplicates > 1 ? `${base}-${hash(String(selected.path || '')).slice(0, 6)}` : base }
}
async function installRecorder(page, preference) {
  await page.addInitScript((explicitPreference) => {
    const startedAt = Date.now()
    const records = []
    const mark = (name, detail = {}) => records.push({ t_ms: Date.now() - startedAt, name, detail })
    const preview = (value) => typeof value === 'string' ? value.replace(/\s+/g, ' ').trim().slice(0, 160) : undefined
    const summarizeFrame = (frame) => {
      const event = frame?.event && typeof frame.event === 'object' ? frame.event : null
      const payload = event?.payload && typeof event.payload === 'object' ? event.payload : (frame?.payload && typeof frame.payload === 'object' ? frame.payload : null)
      const message = payload?.message && typeof payload.message === 'object' ? payload.message : null
      return Object.fromEntries(Object.entries({
        kind: frame?.kind || frame?.type, event_type: frame?.event_type || event?.event_type || payload?.event_type,
        session_id: frame?.session_id || event?.session_id || payload?.session_id,
        event_seq: event?.seq, stream_kind: frame?.stream_kind || payload?.stream_kind,
        stream_id: frame?.stream_id || payload?.stream_id, step: frame?.step ?? payload?.step,
        step_id: frame?.step_id || payload?.step_id, live_seq_start: frame?.live_seq_start,
        live_seq_end: frame?.live_seq_end, text_preview: preview(frame?.text || payload?.delta || message?.content),
        message_role: message?.role, endpoint_cursor_present: Boolean(frame?.endpoint_cursor),
      }).filter(([, item]) => item !== undefined && item !== null && item !== ''))
    }
    window.__streamOrderEvidence = { records, mark }
    const nativeFetch = window.fetch.bind(window)
    window.fetch = async (input, init = {}) => {
      const rawURL = typeof input === 'string' ? input : input?.url || ''
      const parsedURL = new URL(rawURL, location.origin)
      const method = String(init.method || (typeof input !== 'string' ? input?.method : '') || 'GET').toUpperCase()
      let nextInit = init
      if (method === 'POST' && parsedURL.pathname === '/v3/sessions' && typeof init.body === 'string') {
        const body = JSON.parse(init.body)
        body.preference = explicitPreference
        nextInit = { ...init, body: JSON.stringify(body) }
        mark('session.preference.enforced', explicitPreference)
      }
      const response = await nativeFetch(input, nextInit)
      if (parsedURL.pathname === '/v3/sessions' || /\/v3\/sessions\/[^/]+\/messages$/.test(parsedURL.pathname)) {
        response.clone().json().then((body) => mark('fetch.response', {
          method, path: parsedURL.pathname, status: response.status,
          session_id: body?.session?.id || body?.session_id,
          run_id: body?.run_intent?.run_id || body?.run_id,
        })).catch(() => undefined)
      }
      return response
    }
    const NativeWebSocket = window.WebSocket
    window.WebSocket = function RecordingWebSocket(url, protocols) {
      const socket = protocols === undefined ? new NativeWebSocket(url) : new NativeWebSocket(url, protocols)
      if (String(url).includes('/v3/realtime/stream')) {
        mark('ws.constructed', { path: '/v3/realtime/stream' })
        socket.addEventListener('message', (message) => {
          if (typeof message.data !== 'string') return
          try { mark('ws.message', summarizeFrame(JSON.parse(message.data))) }
          catch { mark('ws.message.parse_error') }
        })
      }
      return socket
    }
    window.WebSocket.prototype = NativeWebSocket.prototype
    Object.setPrototypeOf(window.WebSocket, NativeWebSocket)
  }, preference)
}
async function browserJSON(page, route) {
  return page.evaluate(async (innerRoute) => {
    const response = await fetch(innerRoute, { credentials: 'include', headers: { Accept: 'application/json' } })
    const body = await response.json()
    if (!response.ok) throw new Error(`GET ${innerRoute} HTTP ${response.status}`)
    return body
  }, route)
}
async function readRecords(page) { return page.evaluate(() => window.__streamOrderEvidence?.records ?? []) }
async function sampleRows(page) {
  return page.evaluate(() => Array.from(document.querySelectorAll('[data-testid="desktop-chat-row"]')).map((row, index) => ({
    index, type: row.getAttribute('data-render-item-type') || '', key: row.getAttribute('data-render-item-key') || '',
    text: String(row.textContent || '').replace(/\s+/g, ' ').trim().slice(0, 240),
  })))
}
async function writeNDJSON(file, values) { await fs.writeFile(file, values.map((item) => JSON.stringify(item)).join('\n') + (values.length ? '\n' : '')) }

async function run() {
  const opts = parseArgs(process.argv.slice(2))
  if (opts.help) { usage(); return }
  await fs.mkdir(opts.artifactDir, { recursive: true })
  const tunnel = await startTunnel(opts)
  const browser = await playwright().chromium.launch({ headless: opts.headless, executablePath: opts.browserExecutable || undefined })
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } })
  const page = await context.newPage()
  const preference = { provider: opts.provider, model: opts.model, thinking: opts.thinking }
  const samples = []
  let summary = { ok: false, ssh: opts.ssh || null, workspace: opts.workspace, preference, artifact_dir: opts.artifactDir }
  try {
    await installRecorder(page, preference)
    const appURL = tunnel.url.replace(/\/+$/, '')
    await page.goto(appURL, { waitUntil: 'domcontentloaded' })
    await browserJSON(page, '/v1/auth/desktop/session')
    const overview = await browserJSON(page, '/v1/workspace/overview?workspace_limit=500&discover_limit=500')
    const { selected, routeSlug } = selectWorkspace(Array.isArray(overview.workspaces) ? overview.workspaces : [], opts.workspace)
    await page.goto(`${appURL}/${encodeURIComponent(routeSlug)}`, { waitUntil: 'domcontentloaded' })
    const composer = page.locator('textarea').first()
    await composer.waitFor({ state: 'visible', timeout: opts.timeoutMs })
    await composer.fill(opts.prompt)
    await page.getByRole('button', { name: 'Send message' }).click()
    const deadline = Date.now() + opts.timeoutMs
    let sessionId = ''
    let runId = ''
    let finalSeen = false
    while (Date.now() < deadline) {
      const records = await readRecords(page)
      const created = records.find((record) => record.name === 'fetch.response' && record.detail?.path === '/v3/sessions' && record.detail?.status >= 200 && record.detail?.status < 300)
      sessionId ||= String(created?.detail?.session_id || '')
      const message = records.find((record) => /\/messages$/.test(String(record.detail?.path || '')) && record.detail?.run_id)
      runId ||= String(message?.detail?.run_id || '')
      const rows = await sampleRows(page)
      const signature = JSON.stringify(rows)
      if (samples.at(-1)?.signature !== signature) samples.push({ t_ms: records.at(-1)?.t_ms ?? 0, signature, rows })
      finalSeen = rows.some((row) => row.text.includes('STREAM_ORDER_FINAL'))
      const terminal = records.some((record) => record.name === 'ws.message' && ['session.assistant.completed', 'session.run.completed', 'session.run.failed'].includes(record.detail?.event_type))
      if (sessionId && finalSeen && terminal) break
      await sleep(100)
    }
    const records = await readRecords(page)
    const relevantFrames = records.filter((record) => record.name === 'ws.message' && (
      record.detail?.stream_kind === 'assistant_text'
      || String(record.detail?.event_type || '').includes('reasoning')
      || String(record.detail?.event_type || '').includes('assistant')
      || String(record.detail?.event_type || '').includes('run.')
    ))
    const lastRows = samples.at(-1)?.rows ?? []
    const reasoningIndex = lastRows.findIndex((row) => row.type === 'live-reasoning' || /Thinking/.test(row.text))
    const answerIndex = lastRows.findIndex((row) => row.text.includes('STREAM_ORDER_FINAL'))
    summary = {
      ...summary, ok: Boolean(sessionId && finalSeen), session_id: sessionId, run_id: runId,
      workspace_name: workspaceName(selected), frame_count: relevantFrames.length, dom_sample_count: samples.length,
      final_dom_order: lastRows, reasoning_index: reasoningIndex, answer_index: answerIndex,
      classification: reasoningIndex >= 0 && answerIndex >= 0
        ? (reasoningIndex < answerIndex ? 'reasoning_before_answer' : 'answer_before_reasoning')
        : 'insufficient_final_dom_rows',
    }
    if (!summary.ok) fail(`real Desktop turn did not produce STREAM_ORDER_FINAL within ${opts.timeoutMs}ms`)
  } catch (error) {
    summary = { ...summary, ok: false, error: error instanceof Error ? error.message : String(error) }
    await page.screenshot({ path: path.join(opts.artifactDir, 'failure.png'), fullPage: true }).catch(() => undefined)
    throw error
  } finally {
    const records = await readRecords(page).catch(() => [])
    await writeNDJSON(path.join(opts.artifactDir, 'realtime-records.ndjson'), records.filter((record) => record.name === 'ws.message'))
    await writeNDJSON(path.join(opts.artifactDir, 'dom-samples.ndjson'), samples.map(({ signature: _signature, ...sample }) => sample))
    await fs.writeFile(path.join(opts.artifactDir, 'summary.json'), `${JSON.stringify(summary, null, 2)}\n`)
    console.log(`Desktop V3 stream-order evidence\n${JSON.stringify(summary, null, 2)}`)
    await browser.close().catch(() => undefined)
    await tunnel.close().catch(() => undefined)
  }
}
run().catch((error) => { console.error(error instanceof Error ? error.stack || error.message : String(error)); process.exitCode = 1 })
