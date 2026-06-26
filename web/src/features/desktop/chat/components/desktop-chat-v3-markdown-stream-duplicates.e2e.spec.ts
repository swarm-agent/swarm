import assert from 'node:assert/strict'
import { once } from 'node:events'
import { createServer, type Server } from 'node:http'
import test from 'node:test'
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { existsSync } from 'node:fs'

import { chromium, type Page } from 'playwright'

const SESSION_ID = 'session-playwright-v3-markdown-stream'
const RUN_ID = 'run-playwright-v3-markdown-stream'
const WORKSPACE_PATH = '/tmp/swarm-playwright-v3-markdown-stream'
const WORKSPACE_NAME = 'V3 Markdown Stream'
const WORKSPACE_SLUG = 'markdown-stream-vthree'
const SENTINEL = 'SENTINEL-LOREM-STREAM'
const OPAQUE_SNAPSHOT_CURSOR = 'v3c1.playwright_snapshot_payload.playwright_snapshot_signature'
const OPAQUE_RECONNECT_CURSOR = 'v3c1.playwright_reconnect_payload.playwright_reconnect_signature'

const MARKDOWN_CHUNKS = [
  '**bo',
  'ld** and `co',
  'de`\n\n- it',
  'em ',
  `${SENTINEL}`,
]
const MARKDOWN_CONTENT = MARKDOWN_CHUNKS.join('')

function writeJson(res: import('node:http').ServerResponse, status: number, response: unknown): void {
  res.writeHead(status, {
    'content-type': 'application/json',
    'cache-control': 'no-store',
  })
  res.end(JSON.stringify(response))
}

function lifecycle(active = true): Record<string, unknown> {
  return {
    session_id: SESSION_ID,
    run_id: RUN_ID,
    active,
    phase: active ? 'running' : 'completed',
    started_at: 1,
    updated_at: Date.now(),
    ended_at: active ? undefined : Date.now(),
    generation: 1,
  }
}

function sessionWire(active = true): Record<string, unknown> {
  return {
    id: SESSION_ID,
    session_api: 'v3',
    title: 'V3 markdown stream duplicate regression',
    workspace_path: WORKSPACE_PATH,
    workspace_name: WORKSPACE_NAME,
    mode: 'auto',
    metadata: {},
    last_event_seq: 1,
    projection_high_watermark_seq: 1,
    message_count: 1,
    created_at: 1,
    updated_at: Date.now(),
    lifecycle: lifecycle(active),
    preference: { provider: 'mock', model: 'v3-markdown-stream', thinking: '', updated_at: 0 },
  }
}

function userMessage(): Record<string, unknown> {
  return { id: 'msg-user', session_id: SESSION_ID, global_seq: 1, role: 'user', content: 'Stream lorem ipsum markdown.', created_at: 1 }
}

function v3Snapshot(messages: Record<string, unknown>[], active = true): Record<string, unknown> {
  return {
    session: sessionWire(active),
    projection: { session_id: SESSION_ID, last_event_seq: 1, projection_high_watermark_seq: 1, updated_at: Date.now() },
    messages,
    events: [],
    pending_permissions: [],
    usage_summary: null,
    preference: { provider: 'mock', model: 'v3-markdown-stream', thinking: '', updated_at: 0 },
    context_window: 128000,
    max_output_tokens: 4096,
    has_active_plan: false,
    active_plan: null,
    plan_revisions: [],
  }
}

function v3SyncSnapshot(messages: Record<string, unknown>[], active = true, cursor = OPAQUE_SNAPSHOT_CURSOR): Record<string, unknown> {
  return {
    rev: 1,
    snapshot_endpoint_cursor: cursor,
    sessions_by_id: { [SESSION_ID]: sessionWire(active) },
    session_order: [SESSION_ID],
    messages_by_session: { [SESSION_ID]: messages },
    current_run_intent_by_session: active ? { [SESSION_ID]: { session_id: SESSION_ID, run_id: RUN_ID, status: 'running', created_at: 1, updated_at: Date.now(), event_seq: 1 } } : {},
    subscriptions: [{
      protocol: 'v3.realtime',
      protocol_version: 1,
      kind: 'subscribe.session',
      session_id: SESSION_ID,
      subscription_id: `desktop:${SESSION_ID}`,
      endpoint_cursor: cursor,
    }],
  }
}

async function startMockBackend(): Promise<{ server: Server; port: number; requests: string[]; setMessages: (messages: Record<string, unknown>[], active?: boolean) => void }> {
  let sessionMessages: Record<string, unknown>[] = [userMessage()]
  let sessionActive = true
  const requests: string[] = []
  const server = createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    const path = url.pathname
    requests.push(path)

    if (path === '/v3/sessions:workset' || path === '/v3/sessions:discover') {
      return writeJson(res, 500, { error: `legacy desktop sync route forbidden in browser runtime test: ${path}` })
    }

    if (path === '/v1/auth/desktop/session') return writeJson(res, 200, { ok: true, user_id: 'user-playwright', account_scope_id: 'acct-playwright' })
    if (path === '/v1/vault') return writeJson(res, 200, { enabled: false, unlocked: true, unlock_required: false, storage_mode: 'memory' })
    if (path === '/v1/onboarding') {
      return writeJson(res, 200, {
        ok: true,
        needs_onboarding: false,
        config: { swarm_name: 'Playwright Swarm', mode: 'lan', port: 7781, desktop_port: 5555 },
        heuristics: { credential_count: 1, saved_workspace_count: 1, vault_configured: false },
      })
    }
    if (path === '/v1/swarm/state') {
      return writeJson(res, 200, { ok: true, state: { node: { swarm_id: 'swarm-playwright', transports: [] }, pairing: {}, current_group_id: '', groups: [] } })
    }
    if (path === '/v1/providers') return writeJson(res, 200, { providers: [{ id: 'mock', ready: true, runnable: true }] })
    if (path === '/v1/auth/credentials') {
      return writeJson(res, 200, { provider: '', query: '', total: 1, providers: ['mock'], records: [{ id: 'cred-mock', provider: 'mock', label: 'Mock', active: true }] })
    }
    if (path === '/v1/model') {
      return writeJson(res, 200, { preference: { provider: 'mock', model: 'v3-markdown-stream', thinking: '', service_tier: '', context_mode: '' }, context_window: 128000, max_output_tokens: 4096 })
    }
    if (path === '/v1/models/favorites') return writeJson(res, 200, { records: [{ provider: 'mock', model: 'v3-markdown-stream', label: 'Mock V3 Markdown Stream' }] })
    if (path === '/v1/model/catalog') return writeJson(res, 200, { records: [{ provider: 'mock', model: 'v3-markdown-stream', context_window: 128000 }] })
    if (path === '/v2/agents') {
      return writeJson(res, 200, { state: { active_primary: 'swarm', version: 1, profiles: [{ name: 'swarm', mode: 'primary', enabled: true, provider: 'mock', model: 'v3-markdown-stream', exit_plan_mode_enabled: true }], active_subagent: {} } })
    }
    if (path === '/v1/workspace/overview') {
      return writeJson(res, 200, {
        ok: true,
        current_workspace: { requested_path: WORKSPACE_PATH, resolved_path: WORKSPACE_PATH, workspace_name: WORKSPACE_NAME },
        workspaces: [{ path: WORKSPACE_PATH, workspace_name: WORKSPACE_NAME, theme_id: '', directories: [], is_git_repo: false, sort_index: 0, added_at: 1, updated_at: 1, last_selected_at: 1, active: true, worktree_enabled: false, sessions: [sessionWire(sessionActive)] }],
        directories: [],
      })
    }
    if (path === '/v3/sync/bootstrap' || path === '/v3/sync/hydrate') return writeJson(res, 200, v3SyncSnapshot(sessionMessages, sessionActive))
    if (path === '/v3/sessions:reconnect') return writeJson(res, 200, v3SyncSnapshot(sessionMessages, sessionActive, OPAQUE_RECONNECT_CURSOR))
    if (path === `/v3/sessions/${SESSION_ID}/preference`) return writeJson(res, 200, { preference: { provider: 'mock', model: 'v3-markdown-stream', thinking: '', updated_at: 0 }, context_window: 128000, max_output_tokens: 4096 })
    if (path === `/v3/sessions/${SESSION_ID}`) return writeJson(res, 200, v3Snapshot(sessionMessages, sessionActive))
    if (path === '/v1/notifications') {
      return writeJson(res, 200, { notifications: [], summary: { swarm_id: 'swarm-playwright', total_count: 0, unread_count: 0, active_count: 0, updated_at: 0 } })
    }

    writeJson(res, 200, { ok: true })
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert(address && typeof address === 'object')
  return {
    server,
    port: address.port,
    requests,
    setMessages: (messages: Record<string, unknown>[], active = false) => {
      sessionMessages = messages
      sessionActive = active
    },
  }
}

async function startVite(backendPort: number): Promise<{ vite: ChildProcessWithoutNullStreams; port: number }> {
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
    env: { ...process.env, SWARM_BACKEND_URL: `http://127.0.0.1:${backendPort}`, SWARM_DESKTOP_PORT: String(port) },
  })

  let output = ''
  vite.stdout.on('data', (chunk) => { output += String(chunk) })
  vite.stderr.on('data', (chunk) => { output += String(chunk) })

  const deadline = Date.now() + 30_000
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/`)
      if (response.ok) return { vite, port }
    } catch {
      // Wait for Vite to bind.
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

async function installBrowserStreamControls(page: Page): Promise<void> {
  await page.addInitScript(`(() => {
    const sessionId = ${JSON.stringify(SESSION_ID)};
    const NativeWebSocket = window.WebSocket;
    class MockWebSocket extends EventTarget {
      static CONNECTING = NativeWebSocket.CONNECTING;
      static OPEN = NativeWebSocket.OPEN;
      static CLOSING = NativeWebSocket.CLOSING;
      static CLOSED = NativeWebSocket.CLOSED;
      constructor(url) {
        super();
        this.url = String(url);
        this.protocol = '';
        this.extensions = '';
        this.bufferedAmount = 0;
        this.binaryType = 'blob';
        this.readyState = MockWebSocket.CONNECTING;
        this.onopen = null;
        this.onmessage = null;
        this.onclose = null;
        this.onerror = null;
        window.__mockSockets = [...(window.__mockSockets || []), this];
        window.setTimeout(() => {
          this.readyState = MockWebSocket.OPEN;
          const event = new Event('open');
          this.dispatchEvent(event);
          if (this.onopen) this.onopen(event);
        }, 0);
      }
      send(data) { window.__mockSocketSent = [...(window.__mockSocketSent || []), { url: this.url, data }]; }
      close() {
        if (this.readyState === MockWebSocket.CLOSED) return;
        this.readyState = MockWebSocket.CLOSED;
        const event = new CloseEvent('close');
        this.dispatchEvent(event);
        if (this.onclose) this.onclose(event);
      }
      __emit(payload) {
        const event = new MessageEvent('message', { data: JSON.stringify(payload) });
        this.dispatchEvent(event);
        if (this.onmessage) this.onmessage(event);
      }
    }
    window.WebSocket = MockWebSocket;
    window.__socketUrls = () => (window.__mockSockets || []).map((socket) => socket.url);
    window.__emitV3LivePatchEverywhere = (seq, patch) => {
      for (const socket of window.__mockSockets || []) {
        if (socket.readyState !== MockWebSocket.OPEN) continue;
        if (socket.url.includes('/v3/realtime/stream')) {
          socket.__emit({
            protocol: 'v3.realtime',
            protocol_version: 1,
            kind: 'live.patch',
            session_id: patch.session_id,
            live: patch,
          });
        }
      }
    };
    window.__emitV3EventEverywhere = (seq, eventType, payload) => {
      const body = { session_id: sessionId, ...payload };
      for (const socket of window.__mockSockets || []) {
        if (socket.readyState !== MockWebSocket.OPEN) continue;
        if (socket.url.endsWith('/ws')) {
          socket.__emit({
            type: 'event',
            event: {
              global_seq: seq,
              stream: 'session:' + sessionId,
              event_type: eventType,
              entity_id: sessionId,
              ts_unix_ms: Date.now(),
              payload: body,
            },
          });
        } else if (socket.url.includes('/v3/realtime/stream') || socket.url.includes('/v3/sessions/' + sessionId + '/stream')) {
          socket.__emit({
            protocol: 'v3.realtime',
            kind: 'event',
            ok: true,
            session_id: sessionId,
            endpoint_cursor: 'v3c1.playwright_event_' + seq + '.playwright_event_signature_' + seq,
            last_seq: seq,
            high_watermark_seq: seq,
            event: {
              id: 'v3evt_' + sessionId + '_' + String(seq).padStart(20, '0'),
              session_id: sessionId,
              seq,
              event_type: eventType,
              ts_unix_ms: Date.now(),
              payload: body,
            },
          });
        }
      }
    };
    window.__lastChatRowText = () => {
      const rows = Array.from(document.querySelectorAll('[data-testid="desktop-chat-row"]'));
      return rows.at(-1)?.textContent || '';
    };
  })()`)
}

function countOccurrences(haystack: string, needle: string): number {
  return haystack.split(needle).length - 1
}

test('desktop V3 markdown stream renders each delta once and finalizes to the same text', { timeout: 60_000 }, async () => {
  const backend = await startMockBackend()
  const app = await startVite(backend.port)
  const browser = await chromium.launch({ headless: true })

  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 960 } })
    await installBrowserStreamControls(page)
    const consoleLines: string[] = []
    page.on('console', (message) => consoleLines.push(`${message.type()}: ${message.text()}`))
    page.on('pageerror', (error) => consoleLines.push(`pageerror: ${error.message}`))
    await page.goto(`http://127.0.0.1:${app.port}/${WORKSPACE_SLUG}/${SESSION_ID}`)
    try {
      await page.getByTestId('desktop-chat-scroller').waitFor({ state: 'visible', timeout: 15_000 })
    } catch (error) {
      const bodyText = await page.locator('body').innerText({ timeout: 1_000 }).catch(() => '')
      throw new Error(`desktop chat scroller did not appear: ${error instanceof Error ? error.message : String(error)}\nconsole=${consoleLines.join('\n')}\nbody=${bodyText.slice(0, 4000)}`)
    }
    await page.waitForFunction(() => (window as any).__socketUrls?.().some((url: string) => url.endsWith('/ws')))
    await page.waitForFunction(() => (window as any).__socketUrls?.().some((url: string) => url.includes('/v3/realtime/stream')))
    await page.waitForTimeout(250)

    assert.ok(backend.requests.includes('/v3/sync/bootstrap'), `Desktop browser did not bootstrap through canonical sync API: ${backend.requests.join(',')}`)
    assert.ok(backend.requests.includes('/v3/sessions:reconnect'), `Desktop browser did not request realtime durable reconnect subscriptions: ${backend.requests.join(',')}`)
    assert.equal(backend.requests.some((path) => path === '/v3/sessions:workset' || path === '/v3/sessions:discover'), false, `Desktop browser hit legacy sync route: ${backend.requests.join(',')}`)
    const websocketUrls = await page.evaluate(() => (window as any).__socketUrls?.() || []) as string[]
    assert.ok(websocketUrls.some((url) => url.includes(`/v3/realtime/stream?endpoint_cursor=${encodeURIComponent(OPAQUE_RECONNECT_CURSOR)}`)), `Desktop V3 realtime stream did not use opaque reconnect cursor: ${websocketUrls.join(',')}`)

    let offset = 0
    for (let index = 0; index < MARKDOWN_CHUNKS.length; index += 1) {
      const delta = MARKDOWN_CHUNKS[index]
      const byteLength = new TextEncoder().encode(delta).byteLength
      await page.evaluate(({ seq, runId, delta, offsetStart, offsetEnd }) => {
        ;(window as any).__emitV3LivePatchEverywhere(seq, {
          session_id: SESSION_ID,
          run_id: runId,
          stream_id: `assistant:${runId}:step:1`,
          stream_kind: 'assistant_text',
          operation: 'append',
          step: 1,
          step_id: 'step-1',
          live_seq_start: seq - 1,
          live_seq_end: seq - 1,
          offset_start: offsetStart,
          offset_end: offsetEnd,
          text: delta,
          recorded_at: Date.now(),
        })
      }, { seq: 2 + index, runId: RUN_ID, delta, offsetStart: offset, offsetEnd: offset + byteLength })
      offset += byteLength
    }

    const liveText = await page.waitForFunction((sentinel) => {
      const text = (window as any).__lastChatRowText?.() || ''
      return text.includes(sentinel) ? text : null
    }, SENTINEL).then((handle) => handle.jsonValue() as Promise<string>)

    assert.equal(countOccurrences(liveText, SENTINEL), 1, `streaming markdown duplicated a delta before finalization\n${liveText}`)
    assert.ok(liveText.includes('bold'), `live markdown did not render awkward bold split: ${liveText}`)
    assert.ok(liveText.includes('code'), `live markdown did not render awkward code split: ${liveText}`)
    assert.equal((await page.evaluate(() => (window as any).__socketUrls?.() || []) as string[]).some((url) => url.includes(`/v3/sessions/${SESSION_ID}/stream`)), false, 'V3 desktop opened a second per-session stream in addition to global /ws')

    backend.setMessages([
      userMessage(),
      { id: 'msg-assistant-final', session_id: SESSION_ID, global_seq: 50, role: 'assistant', content: MARKDOWN_CONTENT, created_at: Date.now() },
    ], false)

    await page.evaluate(({ runId, sessionId, content }) => {
      ;(window as any).__emitV3EventEverywhere(50, 'session.assistant.completed', {
        run_id: runId,
        status: 'completed',
        message: { id: 'msg-assistant-final', session_id: sessionId, global_seq: 50, role: 'assistant', content, created_at: Date.now() },
        run_intent: { session_id: sessionId, run_id: runId, status: 'completed' },
      })
    }, { runId: RUN_ID, sessionId: SESSION_ID, content: MARKDOWN_CONTENT })

    const finalText = await page.waitForFunction((sentinel) => {
      const rows = Array.from(document.querySelectorAll('[data-testid="desktop-chat-row"]')) as HTMLElement[]
      const row = rows.at(-1)
      if (row?.getAttribute('data-render-item-type') !== 'message') return null
      const text = row.textContent || ''
      return text.includes(sentinel) ? text : null
    }, SENTINEL).then((handle) => handle.jsonValue() as Promise<string>)

    assert.equal(countOccurrences(finalText, SENTINEL), 1, `final markdown message should contain one canonical copy\n${finalText}`)
    assert.ok(finalText.includes('bold'), `final markdown did not match live bold content: ${finalText}`)
    assert.ok(finalText.includes('code'), `final markdown did not match live code content: ${finalText}`)
  } finally {
    await browser.close().catch(() => undefined)
    await stopVite(app.vite)
    await new Promise((resolve) => backend.server.close(resolve))
  }
})
