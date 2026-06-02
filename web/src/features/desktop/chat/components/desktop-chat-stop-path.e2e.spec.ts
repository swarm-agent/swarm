import assert from 'node:assert/strict'
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { once } from 'node:events'
import { existsSync, mkdirSync, writeFileSync } from 'node:fs'
import { mkdtemp } from 'node:fs/promises'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

import { chromium, type Page } from 'playwright'

const ENABLED = process.env.SWARM_DESKTOP_STOP_E2E === '1'
const BACKEND_URL = (process.env.SWARM_BACKEND_URL || 'http://127.0.0.1:7781').replace(/\/+$/, '')
const WORKSPACE_PATH = process.env.SWARM_E2E_WORKSPACE_PATH || process.cwd()
const WORKSPACE_NAME = process.env.SWARM_E2E_WORKSPACE_NAME || workspaceNameFromPath(WORKSPACE_PATH)
const PROVIDER = process.env.SWARM_PROVIDER || 'fireworks'
const MODEL = process.env.SWARM_MODEL || 'accounts/fireworks/models/kimi-k2p6'
const THINKING = process.env.SWARM_THINKING || 'low'
const AGENT_NAME = process.env.SWARM_AGENT_NAME || 'swarm'
const PROMPT = process.env.SWARM_E2E_PROMPT || 'Write a long detailed explanation of cancellation latency in distributed AI systems. Keep writing continuously until stopped.'
const STOP_TIMEOUT_MS = Number(process.env.SWARM_STOP_TIMEOUT_MS || 60_000)

type StopCheckpoint = {
  name: string
  epochMs: number
  relativeMs: number
  detail?: string
}

type SessionWire = {
  id?: string
  lifecycle?: {
    run_id?: string
    active?: boolean
    phase?: string
    started_at?: number
    ended_at?: number
    updated_at?: number
    stop_reason?: string
    error?: string
    owner_transport?: string
  } | null
}

type WorkspaceWire = {
  path?: string
  workspace_name?: string
}

function workspaceNameFromPath(value: string): string {
  const normalized = value.trim().replace(/[\\/]+$/, '')
  const parts = normalized.split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] || 'workspace'
}

function nowMs(): number {
  return Date.now()
}

function mark(checkpoints: StopCheckpoint[], startEpochMs: number, name: string, detail?: string): void {
  const epochMs = nowMs()
  checkpoints.push({ name, epochMs, relativeMs: epochMs - startEpochMs, detail })
}

function slugifySegment(value: string): string {
  return value.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'workspace'
}

function pathHash(path: string): string {
  let hash = 2166136261
  for (let index = 0; index < path.length; index += 1) {
    hash ^= path.charCodeAt(index)
    hash = Math.imul(hash, 16777619)
  }
  return (hash >>> 0).toString(36)
}

function workspaceRouteSlugBase(workspace: WorkspaceWire): string {
  const name = String(workspace.workspace_name ?? '').trim() || workspaceNameFromPath(String(workspace.path ?? ''))
  const base = slugifySegment(name)
  return base === 'swarm' ? 'swarm-workspace' : base
}

function resolveWorkspaceSlug(workspaces: WorkspaceWire[], workspacePath: string): string {
  const candidates = workspaces.length > 0 ? workspaces : [{ path: workspacePath, workspace_name: WORKSPACE_NAME }]
  const counts = new Map<string, number>()
  for (const workspace of candidates) {
    const base = workspaceRouteSlugBase(workspace)
    counts.set(base, (counts.get(base) ?? 0) + 1)
  }
  const target = candidates.find((workspace) => String(workspace.path ?? '').trim() === workspacePath.trim()) ?? candidates[0]
  const base = workspaceRouteSlugBase(target)
  return (counts.get(base) ?? 0) > 1 ? `${base}-${pathHash(String(target.path ?? '')).slice(0, 6)}` : base
}

async function startVite(backendURL: string): Promise<{ vite: ChildProcessWithoutNullStreams; port: number }> {
  const probe = createServer()
  probe.listen(0, '127.0.0.1')
  await once(probe, 'listening')
  const address = probe.address()
  assert(address && typeof address === 'object')
  const port = address.port
  await new Promise<void>((resolve) => probe.close(() => resolve()))

  const localNode = './node_modules/node/bin/node'
  const nodeBin = existsSync(localNode) ? localNode : process.execPath
  const vite = spawn(nodeBin, ['./scripts/vite-launcher.mjs', '--host', '127.0.0.1', '--port', String(port), '--strictPort'], {
    cwd: process.cwd(),
    env: { ...process.env, SWARM_BACKEND_URL: backendURL, SWARM_DESKTOP_PORT: String(port) },
  })

  let output = ''
  vite.stdout.on('data', (chunk) => { output += String(chunk) })
  vite.stderr.on('data', (chunk) => { output += String(chunk) })

  const deadline = nowMs() + 30_000
  while (nowMs() < deadline) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/`)
      if (response.ok) return { vite, port }
    } catch {
      // wait for Vite
    }
    await new Promise((resolve) => setTimeout(resolve, 100))
  }

  vite.kill('SIGTERM')
  throw new Error(`Vite did not start on port ${port}. Output:\n${output}`)
}

async function stopVite(vite: ChildProcessWithoutNullStreams): Promise<void> {
  if (vite.exitCode !== null || vite.signalCode !== null) return
  const exited = once(vite, 'exit').then(() => undefined)
  vite.kill('SIGTERM')
  await Promise.race([exited, new Promise<void>((resolve) => setTimeout(resolve, 2_000))])
  if (vite.exitCode === null && vite.signalCode === null) {
    vite.kill('SIGKILL')
    await exited.catch(() => undefined)
  }
}

async function apiJson<T>(page: Page, appURL: string, path: string, init: Parameters<typeof page.request.fetch>[1] = {}): Promise<T> {
  const response = await page.request.fetch(`${appURL}${path}`, {
    ...init,
    headers: {
      Accept: 'application/json',
      Origin: appURL,
      Referer: `${appURL}/`,
      'Sec-Fetch-Site': 'same-origin',
      ...(init.headers ?? {}),
    },
  })
  const text = await response.text()
  if (!response.ok()) {
    throw new Error(`${init.method ?? 'GET'} ${path} failed HTTP ${response.status()}: ${text}`)
  }
  return (text.trim() ? JSON.parse(text) : {}) as T
}

async function installStopPathInstrumentation(page: Page): Promise<void> {
  await page.addInitScript(`(() => {
    const startedAt = performance.now();
    const checkpoints = [];
    const mark = (name, detail) => checkpoints.push({ name, relativeMs: performance.now() - startedAt, epochMs: Date.now(), detail });
    window.__desktopStopE2E = { checkpoints, mark };
    document.addEventListener('click', (event) => {
      const target = event.target && event.target.closest ? event.target.closest('[aria-label="Stop run"], [aria-label="Send message"]') : null;
      if (target) mark(target.getAttribute('aria-label') === 'Stop run' ? 'user.stop.click.event' : 'user.send.click.event');
    }, true);
    const nativeFetch = window.fetch.bind(window);
    window.fetch = async (input, init) => {
      const url = typeof input === 'string' ? input : input && 'url' in input ? input.url : '';
      const method = String((init && init.method) || (input && typeof input !== 'string' && 'method' in input ? input.method : 'GET')).toUpperCase();
      const body = typeof init?.body === 'string' ? init.body : '';
      let type = '';
      try { type = body ? String(JSON.parse(body).type || '') : ''; } catch {}
      const isRunStreamPost = method === 'POST' && String(url).includes('/run/stream');
      const isStopPost = method === 'POST' && String(url).includes('/stop');
      const label = isStopPost ? 'client.stopSessionRun.request' : isRunStreamPost && type === 'run.start' ? 'client.startSessionRun.request' : '';
      if (label) mark(label + '.start');
      try {
        const response = await nativeFetch(input, init);
        if (label) mark(label + '.end', 'status=' + response.status);
        return response;
      } catch (error) {
        if (label) mark(label + '.error', error instanceof Error ? error.message : String(error));
        throw error;
      }
    };
    const NativeWebSocket = window.WebSocket;
    window.WebSocket = function(url, protocols) {
      const socket = protocols === undefined ? new NativeWebSocket(url) : new NativeWebSocket(url, protocols);
      if (String(url).includes('/run/stream')) {
        mark('client.runStream.websocket.constructed', String(url));
        socket.addEventListener('open', () => mark('client.runStream.websocket.open'));
        socket.addEventListener('message', (event) => {
          if (typeof event.data !== 'string') return;
          try {
            const payload = JSON.parse(event.data);
            if (payload.type === 'session.lifecycle.updated' && payload.lifecycle) {
              mark(payload.lifecycle.active === false ? 'client.websocket.lifecycle.inactive' : 'client.websocket.lifecycle.active', JSON.stringify(payload.lifecycle));
            } else if (payload.type === 'run.accepted' || payload.type === 'resume.accepted' || payload.type === 'run.stop.accepted') {
              mark('client.websocket.' + payload.type, event.data);
            }
          } catch {}
        });
        socket.addEventListener('close', () => mark('client.runStream.websocket.close'));
        socket.addEventListener('error', () => mark('client.runStream.websocket.error'));
      }
      return socket;
    };
    window.WebSocket.prototype = NativeWebSocket.prototype;
    Object.setPrototypeOf(window.WebSocket, NativeWebSocket);
  })()`)
}

async function browserCheckpoints(page: Page): Promise<StopCheckpoint[]> {
  return await page.evaluate(() => (window as unknown as { __desktopStopE2E?: { checkpoints: StopCheckpoint[] } }).__desktopStopE2E?.checkpoints ?? [])
}

async function createBackendSession(page: Page, appURL: string, checkpoints: StopCheckpoint[], startEpochMs: number): Promise<{ sessionId: string; slug: string }> {
  await apiJson(page, appURL, '/v1/auth/desktop/session')
  await apiJson(page, appURL, '/readyz')
  const workspaceOverview = await apiJson<{ workspaces?: WorkspaceWire[] }>(page, appURL, '/v1/workspace/overview?workspace_limit=200&discover_limit=200')
  const created = await apiJson<{ session?: SessionWire }>(page, appURL, '/v1/sessions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    data: {
      title: `Desktop stop Playwright E2E ${new Date().toISOString()}`,
      workspace_path: WORKSPACE_PATH,
      host_workspace_path: WORKSPACE_PATH,
      runtime_workspace_path: WORKSPACE_PATH,
      workspace_name: WORKSPACE_NAME,
      mode: 'auto',
      agent_name: AGENT_NAME,
      preference: { provider: PROVIDER, model: MODEL, thinking: THINKING },
      metadata: { desktop_stop_playwright_e2e: true },
    },
  })
  const sessionId = String(created.session?.id ?? '').trim()
  assert(sessionId, `session create response missing id: ${JSON.stringify(created)}`)
  const slug = resolveWorkspaceSlug(workspaceOverview.workspaces ?? [], WORKSPACE_PATH)
  mark(checkpoints, startEpochMs, 'backend.session.created', `session_id=${sessionId} slug=${slug}`)
  return { sessionId, slug }
}

async function fetchSession(page: Page, appURL: string, sessionId: string): Promise<SessionWire> {
  const response = await apiJson<{ session?: SessionWire }>(page, appURL, `/v1/sessions/${encodeURIComponent(sessionId)}`)
  return response.session ?? {}
}

async function waitForBackendLifecycle(
  page: Page,
  appURL: string,
  sessionId: string,
  checkpoints: StopCheckpoint[],
  startEpochMs: number,
  name: string,
  predicate: (session: SessionWire) => boolean,
  timeoutMs: number,
): Promise<SessionWire> {
  const deadline = nowMs() + timeoutMs
  let last: SessionWire = {}
  while (nowMs() < deadline) {
    last = await fetchSession(page, appURL, sessionId)
    if (predicate(last)) {
      mark(checkpoints, startEpochMs, name, JSON.stringify(last.lifecycle ?? null))
      return last
    }
    await new Promise((resolve) => setTimeout(resolve, 250))
  }
  throw new Error(`timed out waiting for ${name}; last lifecycle=${JSON.stringify(last.lifecycle ?? null)}`)
}

function requireCheckpoint(checkpoints: StopCheckpoint[], name: string): StopCheckpoint {
  const checkpoint = checkpoints.find((entry) => entry.name === name)
  assert(checkpoint, `missing checkpoint ${name}\n${JSON.stringify(checkpoints, null, 2)}`)
  return checkpoint
}

function writeEvidence(evidenceDir: string, summary: Record<string, unknown>, checkpoints: StopCheckpoint[]): void {
  mkdirSync(evidenceDir, { recursive: true })
  writeFileSync(join(evidenceDir, 'desktop-stop-playwright-summary.json'), `${JSON.stringify(summary, null, 2)}\n`)
  writeFileSync(join(evidenceDir, 'desktop-stop-playwright-checkpoints.json'), `${JSON.stringify(checkpoints, null, 2)}\n`)
}

test('desktop Stop button cancels a real provider run and backend lifecycle becomes inactive', { skip: !ENABLED, timeout: Math.max(120_000, STOP_TIMEOUT_MS + 60_000) }, async () => {
  const startEpochMs = nowMs()
  const checkpoints: StopCheckpoint[] = []
  const evidenceDir = process.env.SWARM_E2E_EVIDENCE_DIR || await mkdtemp(join(tmpdir(), 'swarm-desktop-stop-playwright-'))
  const app = await startVite(BACKEND_URL)
  const appURL = `http://127.0.0.1:${app.port}`
  const browser = await chromium.launch({ headless: process.env.SWARM_E2E_HEADFUL !== '1' })
  let summary: Record<string, unknown> = { ok: false, evidenceDir, backendURL: BACKEND_URL, provider: PROVIDER, model: MODEL }

  let page: Page | null = null

  try {
    page = await browser.newPage({ viewport: { width: 1440, height: 960 } })
    await installStopPathInstrumentation(page)
    const { sessionId, slug } = await createBackendSession(page, appURL, checkpoints, startEpochMs)
    summary = { ...summary, sessionId }

    await page.goto(`${appURL}/${encodeURIComponent(slug)}/${encodeURIComponent(sessionId)}`)
    await page.getByTestId('desktop-chat-scroller').waitFor({ state: 'visible', timeout: 30_000 })

    const composer = page.locator('textarea').first()
    await composer.fill(PROMPT)
    await page.getByRole('button', { name: 'Send message' }).click()

    const stopButton = page.getByRole('button', { name: 'Stop run' })
    await stopButton.waitFor({ state: 'visible', timeout: 30_000 })
    const activeSession = await waitForBackendLifecycle(
      page,
      appURL,
      sessionId,
      checkpoints,
      startEpochMs,
      'backend.lifecycle.active.observed',
      (candidate) => candidate.lifecycle?.active === true && Boolean(candidate.lifecycle.run_id),
      30_000,
    )
    const runId = String(activeSession.lifecycle?.run_id ?? '')
    assert(runId, `backend active lifecycle did not include run_id: ${JSON.stringify(activeSession.lifecycle)}`)

    await page.evaluate(() => {
      (window as unknown as { __desktopStopE2E: { mark: (name: string) => void } }).__desktopStopE2E.mark('test.before.stop.click')
    })
    await stopButton.click()

    const inactiveSession = await waitForBackendLifecycle(
      page,
      appURL,
      sessionId,
      checkpoints,
      startEpochMs,
      'backend.lifecycle.inactive.observed',
      (candidate) => candidate.lifecycle?.active === false && String(candidate.lifecycle.run_id ?? '') === runId,
      STOP_TIMEOUT_MS,
    )
    await page.waitForFunction(() => Boolean(document.querySelector('[aria-label="Send message"]')), undefined, { timeout: 30_000 })
    await page.evaluate(() => {
      (window as unknown as { __desktopStopE2E: { mark: (name: string) => void } }).__desktopStopE2E.mark('client.send.enabled.observed')
    })

    const diagnostics = [...checkpoints, ...await browserCheckpoints(page)]
      .sort((left, right) => left.epochMs - right.epochMs)
      .map((entry, index) => ({ ...entry, detail: entry.detail ?? `order=${index}` }))

    const click = requireCheckpoint(diagnostics, 'user.stop.click.event').epochMs
    const stopRequestStart = requireCheckpoint(diagnostics, 'client.stopSessionRun.request.start').epochMs
    const stopRequestEnd = requireCheckpoint(diagnostics, 'client.stopSessionRun.request.end').epochMs
    const backendInactive = requireCheckpoint(diagnostics, 'backend.lifecycle.inactive.observed').epochMs

    summary = {
      ok: true,
      evidenceDir,
      backendURL: BACKEND_URL,
      sessionId,
      runId,
      provider: PROVIDER,
      model: MODEL,
      lifecycle: inactiveSession.lifecycle ?? null,
      latency: {
        clickToStopRequestStartMs: stopRequestStart - click,
        stopRequestWallMs: stopRequestEnd - stopRequestStart,
        stopRequestEndToBackendInactiveMs: backendInactive - stopRequestEnd,
        clickToBackendInactiveMs: backendInactive - click,
      },
    }
    writeEvidence(evidenceDir, summary, diagnostics)
    console.log(`desktop stop Playwright E2E evidence\n${JSON.stringify(summary, null, 2)}`)

    assert.equal(inactiveSession.lifecycle?.active, false, `backend lifecycle stayed active: ${JSON.stringify(inactiveSession.lifecycle)}`)
  } catch (error) {
    const diagnostics = [...checkpoints, ...(page ? await browserCheckpoints(page).catch(() => []) : [])]
      .sort((left, right) => left.epochMs - right.epochMs)
      .map((entry, index) => ({ ...entry, detail: entry.detail ?? `order=${index}` }))
    if (page) {
      await page.screenshot({ path: join(evidenceDir, 'desktop-stop-playwright-failure.png'), fullPage: true }).catch(() => undefined)
    }
    summary = { ...summary, ok: false, error: error instanceof Error ? error.message : String(error) }
    writeEvidence(evidenceDir, summary, diagnostics)
    throw error
  } finally {
    await browser.close().catch(() => undefined)
    await stopVite(app.vite)
  }
})
