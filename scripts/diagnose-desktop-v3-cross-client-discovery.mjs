#!/usr/bin/env node
import { spawn } from 'node:child_process'
import fs from 'node:fs/promises'
import { createRequire } from 'node:module'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url))
const ROOT_DIR = path.resolve(SCRIPT_DIR, '..')
const WEB_PACKAGE_JSON = path.join(ROOT_DIR, 'web', 'package.json')

function usage() {
  console.log(`Usage: node scripts/diagnose-desktop-v3-cross-client-discovery.mjs [options]

Real two-client desktop V3 discovery diagnostic. It opens two Playwright browser
contexts against a live desktop (optionally through a configured SSH tunnel):

  Client A: navigates to the selected workspace and records only the V3 runtime
            path: /v3/sessions:reconnect and /v3/realtime/stream frames.
  Client B: creates a real V3 session in the same workspace, then optionally
            posts a Fireworks-backed message.

The diagnostic answers the critical fork:
  - Did Client A resume with a workset and auto_subscribe_sessions=true?
  - Did Client A receive workset.session.discovered for Client B's session?
  - Did Client A receive the session.created/event frames for that session?
  - Did Client A render the discovered session title?

Options:
  --ssh <target>                 Open SSH tunnel to remote desktop (or set SWARM_PRIMARY_SSH).
  --url <url>                    Desktop URL. Default: SWARM_DESKTOP_URL or local/tunnel URL.
  --remote-desktop-port <port>   Remote desktop port for --ssh. Default: 5555.
  --workspace <name|path>        Workspace to use. Default: swarm-go.
  --agent <name>                 Agent for Client B session. Default: swarm.
  --provider <provider>          Provider for Client B prompt. Default: fireworks.
  --model <model>                Model for Client B prompt. Default: accounts/fireworks/models/kimi-k2p6.
  --thinking <level>             Thinking setting. Default: low.
  --prompt <text>                Prompt Client B sends to create the session.
  --artifact-dir <path>          Evidence directory. Default: tmp/cross-client-v3/<timestamp>.
  --browser-executable <path>    Use a specific browser executable.
  --headful                      Show browser windows.
  --timeout-ms <ms>              Per-phase timeout. Default: 90000.
  --help                         Show this help.

Example remote run:
  node scripts/diagnose-desktop-v3-cross-client-discovery.mjs --ssh <target> --workspace swarm-go
`)
}

function fail(message) {
  throw new Error(message)
}

function requireValue(args, index, flag) {
  const value = args[index + 1]
  if (!value || value.startsWith('--')) fail(`${flag} requires a value`)
  return value
}

function parseArgs(argv) {
  const opts = {
    help: false,
    ssh: process.env.SWARM_PRIMARY_SSH || '',
    url: process.env.SWARM_DESKTOP_URL || '',
    remoteDesktopPort: Number(process.env.SWARM_REMOTE_DESKTOP_PORT || '') || 5555,
    workspace: process.env.SWARM_CROSS_CLIENT_WORKSPACE || 'swarm-go',
    agent: process.env.SWARM_AGENT_NAME || 'swarm',
    provider: process.env.SWARM_PROVIDER || 'fireworks',
    model: process.env.SWARM_MODEL || 'accounts/fireworks/models/kimi-k2p6',
    thinking: process.env.SWARM_THINKING || 'low',
    prompt: process.env.SWARM_CROSS_CLIENT_PROMPT || 'Cross-client V3 discovery test. Reply with exactly CROSS_CLIENT_V3_OK and do not call tools.',
    sendMessage: true,
    artifactDir: '',
    browserExecutable: process.env.PLAYWRIGHT_BROWSER_EXECUTABLE || '',
    headless: true,
    timeoutMs: Number(process.env.SWARM_CROSS_CLIENT_TIMEOUT_MS || '') || 90_000,
  }
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index]
    switch (arg) {
      case '--help':
      case '-h':
        opts.help = true
        break
      case '--ssh':
      case '--primary-ssh':
        opts.ssh = requireValue(argv, index, arg)
        index += 1
        break
      case '--url':
        opts.url = requireValue(argv, index, arg).replace(/\/+$/, '')
        index += 1
        break
      case '--remote-desktop-port':
        opts.remoteDesktopPort = Number(requireValue(argv, index, arg))
        if (!Number.isFinite(opts.remoteDesktopPort) || opts.remoteDesktopPort <= 0) fail(`${arg} must be a positive port`)
        index += 1
        break
      case '--workspace':
        opts.workspace = requireValue(argv, index, arg)
        index += 1
        break
      case '--agent':
      case '--agent-name':
        opts.agent = requireValue(argv, index, arg)
        index += 1
        break
      case '--provider':
        opts.provider = requireValue(argv, index, arg)
        index += 1
        break
      case '--model':
        opts.model = requireValue(argv, index, arg)
        index += 1
        break
      case '--thinking':
        opts.thinking = requireValue(argv, index, arg)
        index += 1
        break
      case '--prompt':
        opts.prompt = requireValue(argv, index, arg)
        index += 1
        break
      case '--no-message':
        fail('--no-message is intentionally unsupported: this diagnostic creates a real desktop session through the UI composer so route/swarm ownership matches production.')
      case '--artifact-dir':
      case '--evidence-dir':
        opts.artifactDir = requireValue(argv, index, arg)
        index += 1
        break
      case '--browser-executable':
        opts.browserExecutable = requireValue(argv, index, arg)
        index += 1
        break
      case '--headful':
        opts.headless = false
        break
      case '--timeout-ms':
        opts.timeoutMs = Number(requireValue(argv, index, arg))
        if (!Number.isFinite(opts.timeoutMs) || opts.timeoutMs <= 0) fail(`${arg} must be positive milliseconds`)
        index += 1
        break
      default:
        fail(`unknown argument: ${arg}`)
    }
  }
  if (!opts.artifactDir) {
    opts.artifactDir = path.join(ROOT_DIR, 'tmp', 'cross-client-v3', timestamp())
  }
  return opts
}

function timestamp() {
  return new Date().toISOString().replace(/[-:]/g, '').replace(/\.\d{3}Z$/, 'Z')
}

function loadPlaywright() {
  try {
    const requireFromWeb = createRequire(WEB_PACKAGE_JSON)
    return requireFromWeb('playwright')
  } catch (error) {
    fail(`Playwright is not installed for web package: ${error instanceof Error ? error.message : String(error)}`)
  }
}

async function getFreePort() {
  const server = net.createServer()
  await new Promise((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolve)
  })
  const address = server.address()
  if (!address || typeof address !== 'object') fail('failed to allocate local port')
  const port = address.port
  await new Promise((resolve) => server.close(resolve))
  return port
}

async function waitForTCP(port, timeoutMs) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const ok = await new Promise((resolve) => {
      const socket = net.connect({ host: '127.0.0.1', port })
      socket.once('connect', () => { socket.destroy(); resolve(true) })
      socket.once('error', () => { socket.destroy(); resolve(false) })
      socket.setTimeout(500, () => { socket.destroy(); resolve(false) })
    })
    if (ok) return
    await sleep(100)
  }
  fail(`timed out waiting for local TCP port ${port}`)
}

async function startSSHTunnel(opts) {
  if (!opts.ssh) return { url: opts.url || 'http://127.0.0.1:5555', close: async () => {} }
  const localPort = await getFreePort()
  const localForward = `${localPort}:127.0.0.1:${opts.remoteDesktopPort}`
  const child = spawn('ssh', ['-N', '-L', localForward, opts.ssh], { stdio: ['ignore', 'pipe', 'pipe'] })
  let stderr = ''
  child.stderr.on('data', (chunk) => { stderr += String(chunk) })
  child.once('exit', (code, signal) => {
    if (code !== null && code !== 0) console.error(`[cross-client-v3] ssh tunnel exited code=${code} stderr=${stderr}`)
    else if (signal) console.error(`[cross-client-v3] ssh tunnel exited signal=${signal}`)
  })
  await waitForTCP(localPort, 15_000)
  return {
    url: opts.url || `http://127.0.0.1:${localPort}`,
    close: async () => {
      if (child.exitCode !== null || child.signalCode !== null) return
      child.kill('SIGTERM')
      await Promise.race([
        new Promise((resolve) => child.once('exit', resolve)),
        sleep(2_000),
      ])
      if (child.exitCode === null && child.signalCode === null) child.kill('SIGKILL')
    },
  }
}

function slugifySegment(value) {
  return value.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'workspace'
}

function basename(value) {
  const normalized = String(value || '').trim().replace(/[\\/]+$/, '')
  const parts = normalized.split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] || ''
}

function pathHash(value) {
  let hash = 2166136261
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index)
    hash = Math.imul(hash, 16777619)
  }
  return (hash >>> 0).toString(36)
}

function workspaceName(workspace) {
  return String(workspace.workspace_name ?? workspace.workspaceName ?? '').trim() || basename(workspace.path)
}

function workspaceRouteSlugBase(workspace) {
  const base = slugifySegment(workspaceName(workspace))
  return base === 'swarm' ? 'swarm-workspace' : base
}

function resolveWorkspaceSlug(workspaces, workspace) {
  const counts = new Map()
  for (const item of workspaces) {
    const base = workspaceRouteSlugBase(item)
    counts.set(base, (counts.get(base) ?? 0) + 1)
  }
  const base = workspaceRouteSlugBase(workspace)
  return (counts.get(base) ?? 0) > 1 ? `${base}-${pathHash(String(workspace.path ?? '')).slice(0, 6)}` : base
}

function pickWorkspace(workspaces, selector) {
  const needle = selector.trim()
  const lower = needle.toLowerCase()
  const exact = workspaces.find((workspace) => String(workspace.path ?? '').trim() === needle)
    || workspaces.find((workspace) => workspaceName(workspace).toLowerCase() === lower)
    || workspaces.find((workspace) => basename(workspace.path).toLowerCase() === lower)
  if (exact) return exact
  fail(`workspace ${selector} not found. Candidates: ${workspaces.map((workspace) => `${workspaceName(workspace)}=${workspace.path}`).join(', ')}`)
}

async function installV3Recorder(page, label) {
  await page.addInitScript((clientLabel) => {
    const startedAt = Date.now()
    const records = []
    const redactURL = (value) => {
      try {
        const parsed = new URL(String(value), window.location.origin)
        if (parsed.pathname === '/v3/realtime/stream' && parsed.searchParams.has('endpoint_cursor')) {
          parsed.searchParams.set('endpoint_cursor', '<redacted>')
        }
        return `${parsed.pathname}${parsed.search}`
      } catch {
        return String(value)
      }
    }
    const pathOf = (value) => {
      try {
        const parsed = new URL(String(value), window.location.origin)
        return `${parsed.pathname}${parsed.search}`
      } catch {
        return String(value)
      }
    }
    const summarizeMessage = (message) => {
      const event = message && typeof message.event === 'object' ? message.event : null
      const payload = event && event.payload && typeof event.payload === 'object' ? event.payload : null
      const session = payload && payload.session && typeof payload.session === 'object' ? payload.session : null
      const worksets = Array.isArray(message?.worksets) ? message.worksets : []
      const subscriptions = Array.isArray(message?.subscriptions) ? message.subscriptions : []
      return {
        kind: String(message?.kind ?? message?.type ?? ''),
        event_type: String(message?.event_type ?? event?.event_type ?? payload?.event_type ?? ''),
        session_id: String(message?.session_id ?? event?.session_id ?? payload?.session_id ?? session?.id ?? ''),
        subscription_id: String(message?.subscription_id ?? ''),
        workset_id: String(message?.workset_id ?? ''),
        workset_subscription_id: String(message?.workset_subscription_id ?? ''),
        auto_subscribed: Boolean(message?.auto_subscribed),
        endpoint_cursor_present: Boolean(String(message?.endpoint_cursor ?? '').trim()),
        rev: typeof message?.rev === 'number' ? message.rev : undefined,
        last_seq: typeof message?.last_seq === 'number' ? message.last_seq : undefined,
        worksets_count: worksets.length,
        worksets_auto_subscribe_count: worksets.filter((workset) => Boolean(workset?.auto_subscribe_sessions)).length,
        subscriptions_count: subscriptions.length,
        session_title: typeof session?.title === 'string' ? session.title : undefined,
      }
    }
    const summarizeFetchBody = (body) => {
      if (!body || typeof body !== 'object') return null
      const worksets = Array.isArray(body.worksets) ? body.worksets : []
      const resumeWorksets = Array.isArray(body.realtime?.resume?.worksets) ? body.realtime.resume.worksets : []
      const session = body.session && typeof body.session === 'object' ? body.session : null
      return {
        ok: body.ok,
        error: typeof body.error === 'string' ? body.error : undefined,
        message: typeof body.message === 'string' ? body.message : undefined,
        session_id: String(session?.id ?? body.session_id ?? ''),
        title: String(session?.title ?? body.title ?? ''),
        endpoint_cursor_present: Boolean(String(body.endpoint_cursor ?? body.snapshot_endpoint_cursor ?? body.realtime?.resume?.endpoint_cursor ?? '').trim()),
        client_id_present: Boolean(String(body.client_id ?? '').trim()),
        workset_present: Boolean(body.workset && typeof body.workset === 'object'),
        workset_id: String(body.workset?.workset_id ?? body.workset_id ?? ''),
        workset_auto_subscribe: Boolean(body.workset?.auto_subscribe_sessions),
        worksets_count: worksets.length,
        worksets_auto_subscribe_count: worksets.filter((workset) => Boolean(workset?.auto_subscribe_sessions)).length,
        realtime_resume_worksets_count: resumeWorksets.length,
        realtime_resume_auto_subscribe_count: resumeWorksets.filter((workset) => Boolean(workset?.auto_subscribe_sessions)).length,
      }
    }
    const mark = (name, detail = {}) => {
      records.push({ client: clientLabel, name, t_ms: Date.now() - startedAt, epoch_ms: Date.now(), detail })
    }
    window.__crossClientV3 = { records, mark }

    const nativeFetch = window.fetch.bind(window)
    window.fetch = async (input, init) => {
      const url = typeof input === 'string' ? input : input && 'url' in input ? input.url : ''
      const method = String(init?.method || (input && typeof input !== 'string' && 'method' in input ? input.method : 'GET')).toUpperCase()
      const path = pathOf(url)
      const interesting = path.startsWith('/v3/sessions') || path === '/v1/auth/desktop/session' || path.startsWith('/v1/workspace/overview')
      let requestBody = null
      if (typeof init?.body === 'string') {
        try { requestBody = summarizeFetchBody(JSON.parse(init.body)) } catch {}
      }
      if (interesting) mark('fetch.request', { method, path, body: requestBody })
      const response = await nativeFetch(input, init)
      if (interesting) {
        response.clone().text().then((text) => {
          let parsed = null
          try { parsed = text ? JSON.parse(text) : null } catch { parsed = { raw: text.slice(0, 300) } }
          mark('fetch.response', { method, path, status: response.status, body: summarizeFetchBody(parsed) })
        }).catch((error) => mark('fetch.response.read_error', { method, path, error: String(error) }))
      }
      return response
    }

    const NativeWebSocket = window.WebSocket
    window.WebSocket = function V3RecordingWebSocket(url, protocols) {
      const socket = protocols === undefined ? new NativeWebSocket(url) : new NativeWebSocket(url, protocols)
      const isV3 = String(url).includes('/v3/realtime/stream')
      if (isV3) {
        mark('ws.constructed', { url: redactURL(url) })
        socket.addEventListener('open', () => mark('ws.open', { url: redactURL(url) }))
        socket.addEventListener('close', (event) => mark('ws.close', { code: event.code, reason: event.reason }))
        socket.addEventListener('error', () => mark('ws.error'))
        socket.addEventListener('message', (event) => {
          if (typeof event.data !== 'string') return
          try {
            mark('ws.message', summarizeMessage(JSON.parse(event.data)))
          } catch (error) {
            mark('ws.message.parse_error', { error: String(error) })
          }
        })
        try {
          const nativeSend = socket.send.bind(socket)
          socket.send = (data) => {
            if (typeof data === 'string') {
              try { mark('ws.send', summarizeMessage(JSON.parse(data))) }
              catch (error) { mark('ws.send.parse_error', { error: String(error) }) }
            }
            return nativeSend(data)
          }
        } catch (error) {
          mark('ws.send.patch_failed', { error: String(error) })
        }
      }
      return socket
    }
    window.WebSocket.prototype = NativeWebSocket.prototype
    Object.setPrototypeOf(window.WebSocket, NativeWebSocket)
  }, label)
}

async function browserJSON(page, route, init = {}) {
  return await page.evaluate(async ({ route: innerRoute, init: innerInit }) => {
    const response = await fetch(innerRoute, {
      credentials: 'include',
      method: innerInit.method || 'GET',
      headers: {
        Accept: 'application/json',
        ...(innerInit.body === undefined ? {} : { 'Content-Type': 'application/json' }),
        ...(innerInit.headers || {}),
      },
      body: innerInit.body === undefined ? undefined : JSON.stringify(innerInit.body),
    })
    const text = await response.text()
    let parsed = null
    try { parsed = text ? JSON.parse(text) : null } catch { parsed = { raw: text } }
    if (!response.ok) throw new Error(`${innerInit.method || 'GET'} ${innerRoute} HTTP ${response.status}: ${text.slice(0, 1200)}`)
    return parsed
  }, { route, init })
}

async function getRecords(page) {
  return await page.evaluate(() => window.__crossClientV3?.records ?? [])
}

async function waitForRecord(page, predicate, timeoutMs, label) {
  const deadline = Date.now() + timeoutMs
  let records = []
  while (Date.now() < deadline) {
    records = await getRecords(page)
    const found = records.find(predicate)
    if (found) return found
    await sleep(250)
  }
  throw new Error(`timed out waiting for ${label}. Last records: ${JSON.stringify(records.slice(-12), null, 2)}`)
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function writeNDJSON(file, records) {
  await fs.writeFile(file, `${records.map((record) => JSON.stringify(record)).join('\n')}\n`)
}

async function run() {
  const opts = parseArgs(process.argv.slice(2))
  if (opts.help) { usage(); return }
  await fs.mkdir(opts.artifactDir, { recursive: true })

  const playwright = loadPlaywright()
  const tunnel = await startSSHTunnel(opts)
  const appURL = tunnel.url.replace(/\/+$/, '')
  const browser = await playwright.chromium.launch({
    headless: opts.headless,
    executablePath: opts.browserExecutable || undefined,
  })
  let summary = {
    ok: false,
    artifactDir: opts.artifactDir,
    appURL,
    ssh: opts.ssh || null,
    workspaceSelector: opts.workspace,
    provider: opts.provider,
    model: opts.model,
  }

  const contextA = await browser.newContext({ viewport: { width: 1440, height: 1000 } })
  const contextB = await browser.newContext({ viewport: { width: 1440, height: 1000 } })
  const pageA = await contextA.newPage()
  const pageB = await contextB.newPage()

  try {
    await installV3Recorder(pageA, 'client-a-listener')
    await installV3Recorder(pageB, 'client-b-creator')

    await pageA.goto(appURL, { waitUntil: 'domcontentloaded' })
    await pageB.goto(appURL, { waitUntil: 'domcontentloaded' })
    await browserJSON(pageA, '/v1/auth/desktop/session')
    await browserJSON(pageB, '/v1/auth/desktop/session')

    const overview = await browserJSON(pageA, '/v1/workspace/overview?workspace_limit=500&discover_limit=500')
    const workspaces = Array.isArray(overview.workspaces) ? overview.workspaces : []
    const workspace = pickWorkspace(workspaces, opts.workspace)
    const workspacePath = String(workspace.path || '').trim()
    const workspaceTitle = workspaceName(workspace)
    if (!workspacePath) fail(`selected workspace is missing path: ${JSON.stringify(workspace)}`)
    const workspaceSlug = resolveWorkspaceSlug(workspaces, workspace)
    summary = { ...summary, workspacePath, workspaceName: workspaceTitle, workspaceSlug }

    await pageA.goto(`${appURL}/${encodeURIComponent(workspaceSlug)}`, { waitUntil: 'domcontentloaded' })
    await pageA.locator('[data-testid="desktop-workspace-sidebar"], body').first().waitFor({ state: 'visible', timeout: opts.timeoutMs })
    await pageB.goto(`${appURL}/${encodeURIComponent(workspaceSlug)}`, { waitUntil: 'domcontentloaded' })
    await pageB.locator('textarea').first().waitFor({ state: 'visible', timeout: opts.timeoutMs })

    const worksetReady = await waitForRecord(pageA, (record) => {
      if (record.name === 'ws.send' && record.detail?.kind === 'resume' && record.detail?.worksets_count > 0 && record.detail?.worksets_auto_subscribe_count > 0) return true
      if (record.name === 'fetch.response' && record.detail?.path === '/v3/sessions:reconnect') {
        const body = record.detail?.body || {}
        return (body.worksets_count > 0 && body.worksets_auto_subscribe_count > 0)
          || (body.realtime_resume_worksets_count > 0 && body.realtime_resume_auto_subscribe_count > 0)
      }
      return false
    }, opts.timeoutMs, 'Client A V3 workset resume with auto_subscribe_sessions=true')

    await pageB.locator('textarea').first().fill(opts.prompt)
    await pageB.getByRole('button', { name: 'Send message' }).click()

    const createResponse = await waitForRecord(pageB, (record) => record.name === 'fetch.response'
      && record.detail?.path === '/v3/sessions'
      && record.detail?.status === 200
      && record.detail?.body?.session_id, opts.timeoutMs, 'Client B successful /v3/sessions create through desktop UI')
    const sessionId = String(createResponse.detail.body.session_id || '').trim()
    if (!sessionId) fail(`Client B V3 UI create response missing session id: ${JSON.stringify(createResponse)}`)
    const title = String(createResponse.detail.body.title || '') || `session ${sessionId}`
    summary = { ...summary, createdSessionId: sessionId, createdTitle: title, clientAWorksetReady: worksetReady }

    const discovered = await waitForRecord(pageA, (record) => record.name === 'ws.message'
      && record.detail?.kind === 'workset.session.discovered'
      && record.detail?.session_id === sessionId, opts.timeoutMs, 'Client A workset.session.discovered for Client B session')

    const createdEvent = await waitForRecord(pageA, (record) => record.name === 'ws.message'
      && record.detail?.kind === 'event'
      && record.detail?.event_type === 'session.created'
      && record.detail?.session_id === sessionId, opts.timeoutMs, 'Client A session.created event for Client B session')

    let assistantFrame = null
    if (opts.sendMessage) {
      assistantFrame = await waitForRecord(pageA, (record) => record.name === 'ws.message'
        && record.detail?.kind === 'event'
        && record.detail?.session_id === sessionId
        && String(record.detail?.event_type || '').startsWith('session.assistant.'), opts.timeoutMs, 'Client A assistant frame for Client B session')
    }

    let rendered = false
    try {
      await pageA.getByText(title, { exact: false }).first().waitFor({ state: 'visible', timeout: 20_000 })
      rendered = true
    } catch {
      rendered = false
    }

    summary = {
      ...summary,
      ok: rendered,
      classification: rendered
        ? 'v3_delivery_and_frontend_render_ok'
        : 'v3_delivery_seen_but_frontend_render_missing',
      clientA: {
        worksetReady,
        discovered,
        createdEvent,
        assistantFrame,
        renderedTitle: rendered,
      },
    }

    if (!rendered) {
      throw new Error(`Client A received V3 discovery/session.created for ${sessionId}, but did not render title ${JSON.stringify(title)}`)
    }
  } catch (error) {
    summary = {
      ...summary,
      ok: false,
      error: error instanceof Error ? error.message : String(error),
    }
    await pageA.screenshot({ path: path.join(opts.artifactDir, 'client-a-failure.png'), fullPage: true }).catch(() => undefined)
    await pageB.screenshot({ path: path.join(opts.artifactDir, 'client-b-failure.png'), fullPage: true }).catch(() => undefined)
    throw error
  } finally {
    const [recordsA, recordsB] = await Promise.all([
      getRecords(pageA).catch(() => []),
      getRecords(pageB).catch(() => []),
    ])
    await writeNDJSON(path.join(opts.artifactDir, 'client-a-records.ndjson'), recordsA)
    await writeNDJSON(path.join(opts.artifactDir, 'client-b-records.ndjson'), recordsB)
    await fs.writeFile(path.join(opts.artifactDir, 'summary.json'), `${JSON.stringify(summary, null, 2)}\n`)
    console.log(`desktop V3 cross-client discovery evidence\n${JSON.stringify(summary, null, 2)}`)
    await browser.close().catch(() => undefined)
    await tunnel.close().catch(() => undefined)
  }
}

run().catch((error) => {
  console.error(error instanceof Error ? error.stack || error.message : String(error))
  process.exitCode = 1
})
