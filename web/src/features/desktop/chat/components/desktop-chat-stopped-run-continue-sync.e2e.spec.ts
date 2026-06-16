import assert from 'node:assert/strict'
import { once } from 'node:events'
import { createServer, type IncomingMessage, type Server } from 'node:http'
import test from 'node:test'
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { existsSync } from 'node:fs'

import { chromium, type Page } from 'playwright'

const SESSION_ID = 'session-playwright-stopped-run-continue'
const FIRST_RUN_ID = 'run-playwright-stopped-run-continue-1'
const SECOND_RUN_ID = 'run-playwright-stopped-run-continue-2'
const WORKSPACE_PATH = '/tmp/swarm-playwright-stopped-run-continue'
const WORKSPACE_NAME = 'Stopped Run Continue Sync'
const WORKSPACE_SLUG = 'stopped-run-continue-sync'
const OPAQUE_BOOTSTRAP_CURSOR = 'v3c1.stopped_continue_bootstrap_payload.stopped_continue_bootstrap_signature'
const OPAQUE_RECONNECT_CURSOR = 'v3c1.stopped_continue_reconnect_payload.stopped_continue_reconnect_signature'
const OPAQUE_POST_CURSOR = 'v3c1.stopped_continue_post_payload.stopped_continue_post_signature'

const OLD_REASONING_SENTINEL = 'SYNC-OLD-THINKING-STAYS'
const OLD_ASSISTANT_SENTINEL = 'SYNC-OLD-ASSISTANT-STAYS'
const NEXT_USER_SENTINEL = 'SYNC-NEXT-USER-CONTINUES'
const NEXT_REASONING_SENTINEL = 'SYNC-NEXT-THINKING-STREAMS'
const NEXT_ASSISTANT_SENTINEL = 'SYNC-NEXT-ASSISTANT-CONTINUES'

const NEXT_PROMPT = `Continue in this same conversation and keep ${NEXT_USER_SENTINEL} visible.`

function writeJson(res: import('node:http').ServerResponse, status: number, response: unknown): void {
  res.writeHead(status, {
    'content-type': 'application/json',
    'cache-control': 'no-store',
  })
  res.end(JSON.stringify(response))
}

async function readJsonBody(req: IncomingMessage): Promise<Record<string, unknown>> {
  const chunks: Buffer[] = []
  for await (const chunk of req) {
    chunks.push(Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk))
  }
  const body = Buffer.concat(chunks).toString('utf8').trim()
  if (!body) return {}
  return JSON.parse(body) as Record<string, unknown>
}

function lifecycle(active: boolean, runId = active ? SECOND_RUN_ID : FIRST_RUN_ID): Record<string, unknown> {
  return {
    session_id: SESSION_ID,
    run_id: runId,
    active,
    phase: active ? 'running' : 'completed',
    started_at: 1,
    updated_at: Date.now(),
    ended_at: active ? undefined : 2,
    stop_reason: active ? undefined : 'user_stop',
    generation: active ? 2 : 1,
  }
}

function runIntent(status: 'running' | 'completed', runId = status === 'running' ? SECOND_RUN_ID : FIRST_RUN_ID, eventSeq = status === 'running' ? 5 : 4): Record<string, unknown> {
  return {
    session_id: SESSION_ID,
    run_id: runId,
    status,
    created_at: 1,
    updated_at: Date.now(),
    event_seq: eventSeq,
  }
}

function sessionWire(active: boolean): Record<string, unknown> {
  return {
    id: SESSION_ID,
    session_api: 'v3',
    title: 'Stopped run continue sync regression',
    workspace_path: WORKSPACE_PATH,
    workspace_name: WORKSPACE_NAME,
    mode: 'auto',
    metadata: {},
    last_event_seq: active ? 5 : 4,
    projection_high_watermark_seq: active ? 5 : 4,
    message_count: active ? 4 : 3,
    created_at: 1,
    updated_at: Date.now(),
    lifecycle: lifecycle(active),
    run_intent: active ? runIntent('running') : null,
    preference: { provider: 'mock', model: 'stopped-run-continue-sync', thinking: 'low', updated_at: 0 },
  }
}

function initialMessages(): Record<string, unknown>[] {
  return [
    { id: 'msg-user-1', session_id: SESSION_ID, global_seq: 1, role: 'user', content: 'Please inspect the tree view render state.', created_at: 1 },
    { id: 'msg-reasoning-1', session_id: SESSION_ID, global_seq: 2, role: 'reasoning', content: `Thinking through the tree view sync path. ${OLD_REASONING_SENTINEL}`, created_at: 2 },
    { id: 'msg-assistant-1', session_id: SESSION_ID, global_seq: 3, role: 'assistant', content: `I found the stopped-run state and preserved the visible text. ${OLD_ASSISTANT_SENTINEL}`, created_at: 3 },
  ]
}

function v3SessionSnapshot(messages: Record<string, unknown>[], active: boolean): Record<string, unknown> {
  return {
    session: sessionWire(active),
    projection: { session_id: SESSION_ID, last_event_seq: active ? 5 : 4, projection_high_watermark_seq: active ? 5 : 4, updated_at: Date.now() },
    messages,
    events: [],
    pending_permissions: [],
    usage_summary: null,
    preference: { provider: 'mock', model: 'stopped-run-continue-sync', thinking: 'low', updated_at: 0 },
    context_window: 128000,
    max_output_tokens: 4096,
    has_active_plan: false,
    active_plan: null,
    plan_revisions: [],
  }
}

function v3SyncSnapshot(messages: Record<string, unknown>[], active: boolean, cursor = OPAQUE_BOOTSTRAP_CURSOR): Record<string, unknown> {
  return {
    rev: 1,
    snapshot_endpoint_cursor: cursor,
    sessions_by_id: { [SESSION_ID]: sessionWire(active) },
    session_order: [SESSION_ID],
    messages_by_session: { [SESSION_ID]: messages },
    current_run_intent_by_session: active ? { [SESSION_ID]: runIntent('running') } : {},
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

function messagesPage(messages: Record<string, unknown>[]): Record<string, unknown> {
  const seqs = messages.map((message) => typeof message.global_seq === 'number' ? message.global_seq : 0).filter((seq) => seq > 0)
  const newestSeq = seqs.length > 0 ? Math.max(...seqs) : 0
  const oldestSeq = seqs.length > 0 ? Math.min(...seqs) : 0
  return {
    messages,
    applied_seq: newestSeq,
    high_watermark: newestSeq,
    oldest_seq: oldestSeq,
    newest_seq: newestSeq,
    next_before_seq: oldestSeq,
    next_after_seq: newestSeq,
    has_more: false,
    has_more_older: false,
    has_more_newer: false,
  }
}

async function startMockBackend(): Promise<{ server: Server; port: number; requests: string[]; releaseMessagePost: () => void; setMessages: (messages: Record<string, unknown>[], active?: boolean) => void }> {
  let sessionMessages: Record<string, unknown>[] = initialMessages()
  let sessionActive = false
  let releaseMessagePost: (() => void) | null = null
  const requests: string[] = []
  const server = createServer((req, res) => {
    void (async () => {
      const url = new URL(req.url ?? '/', 'http://127.0.0.1')
      const path = url.pathname
      const method = req.method ?? 'GET'
      requests.push(`${method} ${path}`)

      if (path === '/v3/sessions:workset' || path === '/v3/sessions:discover') {
        return writeJson(res, 500, { error: `legacy desktop sync route forbidden in stopped-run continue test: ${path}` })
      }

      if (path === '/v1/auth/desktop/session') return writeJson(res, 200, { ok: true })
      if (path === '/v1/vault') return writeJson(res, 200, { enabled: false, unlocked: true, unlock_required: false, storage_mode: 'memory' })
      if (path === '/v1/onboarding') {
        return writeJson(res, 200, {
          ok: true,
          needs_onboarding: false,
          config: { swarm_name: 'Playwright Swarm', mode: 'lan', port: 7781, desktop_port: 5555 },
          heuristics: { credential_count: 1, saved_workspace_count: 1, vault_configured: false },
        })
      }
      if (path === '/v1/swarm/state') return writeJson(res, 200, { ok: true, state: { node: { swarm_id: 'swarm-playwright', transports: [] }, pairing: {}, current_group_id: '', groups: [] } })
      if (path === '/v1/providers') return writeJson(res, 200, { providers: [{ id: 'mock', ready: true, runnable: true }] })
      if (path === '/v1/auth/credentials') return writeJson(res, 200, { provider: '', query: '', total: 1, providers: ['mock'], records: [{ id: 'cred-mock', provider: 'mock', label: 'Mock', active: true }] })
      if (path === '/v1/model') return writeJson(res, 200, { preference: { provider: 'mock', model: 'stopped-run-continue-sync', thinking: 'low', service_tier: '', context_mode: '' }, context_window: 128000, max_output_tokens: 4096 })
      if (path === '/v1/models/favorites') return writeJson(res, 200, { records: [{ provider: 'mock', model: 'stopped-run-continue-sync', label: 'Mock Stopped Continue Sync' }] })
      if (path === '/v1/model/catalog') return writeJson(res, 200, { records: [{ provider: 'mock', model: 'stopped-run-continue-sync', context_window: 128000 }] })
      if (path === '/v2/agents') return writeJson(res, 200, { state: { active_primary: 'swarm', version: 1, profiles: [{ name: 'swarm', mode: 'primary', enabled: true, provider: 'mock', model: 'stopped-run-continue-sync', thinking: 'low', exit_plan_mode_enabled: true }], active_subagent: {} } })
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
      if (path === `/v3/sessions/${SESSION_ID}/preference`) return writeJson(res, 200, { preference: { provider: 'mock', model: 'stopped-run-continue-sync', thinking: 'low', updated_at: 0 }, context_window: 128000, max_output_tokens: 4096 })
      if (path === `/v3/sessions/${SESSION_ID}`) return writeJson(res, 200, v3SessionSnapshot(sessionMessages, sessionActive))
      if (path === `/v3/sessions/${SESSION_ID}/messages` && method === 'GET') return writeJson(res, 200, messagesPage(sessionMessages))
      if (path === `/v3/sessions/${SESSION_ID}/messages` && method === 'POST') {
        const body = await readJsonBody(req)
        const content = String(body.content ?? '').trim()
        await new Promise<void>((resolve) => { releaseMessagePost = resolve })
        const message = { id: 'msg-user-2', session_id: SESSION_ID, global_seq: 5, role: 'user', content, created_at: Date.now() }
        sessionMessages = [
          ...initialMessages(),
          message,
          { id: 'msg-reasoning-2', session_id: SESSION_ID, global_seq: 8, role: 'reasoning', content: NEXT_REASONING_SENTINEL, created_at: Date.now() },
          { id: 'msg-assistant-2', session_id: SESSION_ID, global_seq: 9, role: 'assistant', content: `Continuation response stayed synced. ${NEXT_ASSISTANT_SENTINEL}`, created_at: Date.now() },
        ]
        sessionActive = true
        return writeJson(res, 200, {
          ok: true,
          session: sessionWire(true),
          projection: { session_id: SESSION_ID, last_event_seq: 9, projection_high_watermark_seq: 9, updated_at: Date.now() },
          message,
          messages: sessionMessages,
          run_intent: runIntent('running', SECOND_RUN_ID, 5),
          realtime_outbox: { endpoint_seq: 5, endpoint_cursor: OPAQUE_POST_CURSOR, session_id: SESSION_ID },
        })
      }
      if (path === '/v1/notifications') return writeJson(res, 200, { notifications: [], summary: { swarm_id: 'swarm-playwright', total_count: 0, unread_count: 0, active_count: 0, updated_at: 0 } })

      writeJson(res, 200, { ok: true })
    })().catch((error) => {
      writeJson(res, 500, { error: error instanceof Error ? error.message : String(error) })
    })
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert(address && typeof address === 'object')
  return {
    server,
    port: address.port,
    requests,
    releaseMessagePost: () => {
      releaseMessagePost?.()
      releaseMessagePost = null
    },
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
    window.__chatText = () => Array.from(document.querySelectorAll('[data-testid="desktop-chat-row"]')).map((row) => row.textContent || '').join('\n---ROW---\n') || document.body.innerText || '';
    window.__chatRows = () => Array.from(document.querySelectorAll('[data-testid="desktop-chat-row"]')).map((row) => ({ type: row.getAttribute('data-render-item-type'), text: row.textContent || '' }));
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
            endpoint_cursor: 'v3c1.stopped_continue_event_' + seq + '.stopped_continue_event_signature_' + seq,
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
  })()`)
}

async function waitForChatText(page: Page, expected: string, timeout = 30_000): Promise<string> {
  const deadline = Date.now() + timeout
  let body = ''
  while (Date.now() < deadline) {
    body = await page.locator('body').innerText({ timeout: 1_000 }).catch(() => '')
    if (body.includes(expected)) return body
    await page.waitForTimeout(100)
  }
  const rows = await page.evaluate(() => (window as any).__chatRows?.() || []).catch(() => [])
  throw new Error(`timed out waiting for chat text ${expected}\nrows=${JSON.stringify(rows, null, 2)}\nbody=${body.slice(0, 4000)}`)
}

test('desktop V3 stopped run keeps thinking and prior text visible while continuing same conversation', { timeout: 60_000 }, async () => {
  const backend = await startMockBackend()
  const app = await startVite(backend.port)
  const browser = await chromium.launch({ headless: true })

  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 960 } })
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
    const initialText = await waitForChatText(page, OLD_REASONING_SENTINEL)
    assert.match(initialText, /Thinking/, `initial stopped conversation should render thinking label\n${initialText}`)
    assert.match(initialText, new RegExp(OLD_ASSISTANT_SENTINEL), `initial stopped conversation should render assistant text\n${initialText}`)
    assert.ok(backend.requests.includes('POST /v3/sync/bootstrap'), `Desktop browser did not bootstrap through canonical sync API: ${backend.requests.join(',')}`)
    assert.equal(backend.requests.some((entry) => entry.endsWith('/v3/sessions:workset') || entry.endsWith('/v3/sessions:discover')), false, `Desktop browser hit legacy sync route: ${backend.requests.join(',')}`)

    const composer = page.locator('textarea').first()
    await composer.fill(NEXT_PROMPT)
    await page.getByRole('button', { name: 'Send message' }).click()

    const pendingSendText = await waitForChatText(page, OLD_REASONING_SENTINEL)
    assert.match(pendingSendText, /Thinking/, `pending follow-up send removed the thinking label before backend response\n${pendingSendText}`)
    assert.match(pendingSendText, new RegExp(OLD_ASSISTANT_SENTINEL), `pending follow-up send removed prior assistant text before backend response\n${pendingSendText}`)

    backend.releaseMessagePost()

    const afterSendText = await waitForChatText(page, NEXT_USER_SENTINEL)
    assert.match(afterSendText, new RegExp(OLD_REASONING_SENTINEL), `sending follow-up removed the prior thinking row before refresh\n${afterSendText}`)
    assert.match(afterSendText, /Thinking/, `sending follow-up removed the thinking label before refresh\n${afterSendText}`)
    assert.match(afterSendText, new RegExp(OLD_ASSISTANT_SENTINEL), `sending follow-up removed prior assistant text before refresh\n${afterSendText}`)
    assert.match(afterSendText, new RegExp(NEXT_USER_SENTINEL), `follow-up user message was not rendered\n${afterSendText}`)

    const duringContinuationText = await waitForChatText(page, NEXT_REASONING_SENTINEL)
    assert.match(duringContinuationText, new RegExp(OLD_REASONING_SENTINEL), `continued sync removed prior thinking row\n${duringContinuationText}`)
    assert.match(duringContinuationText, new RegExp(OLD_ASSISTANT_SENTINEL), `continued sync removed prior assistant text\n${duringContinuationText}`)

    const finalText = await waitForChatText(page, NEXT_ASSISTANT_SENTINEL)
    for (const sentinel of [OLD_REASONING_SENTINEL, OLD_ASSISTANT_SENTINEL, NEXT_USER_SENTINEL, NEXT_REASONING_SENTINEL, NEXT_ASSISTANT_SENTINEL]) {
      assert.match(finalText, new RegExp(sentinel), `final same-conversation view lost ${sentinel}\n${finalText}`)
    }
  } finally {
    await browser.close().catch(() => undefined)
    await stopVite(app.vite)
    await new Promise((resolve) => backend.server.close(resolve))
  }
})
