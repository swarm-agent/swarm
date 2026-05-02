import assert from 'node:assert/strict'
import { once } from 'node:events'
import { createServer, type Server } from 'node:http'
import test from 'node:test'
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { existsSync } from 'node:fs'

import { chromium, type Page } from 'playwright'

const SESSION_ID = 'session-playwright-streaming-jitter'
const RUN_ID = 'run-playwright-streaming-jitter'
const WORKSPACE_PATH = '/tmp/swarm-playwright-streaming-jitter'
const WORKSPACE_NAME = 'Streaming Jitter'
const WORKSPACE_SLUG = 'streaming-jitter'

const MARKDOWN_CONTENT = [
  'Here is a markdown-heavy streamed response that should not jump when it finalizes.',
  '',
  '## Summary',
  '',
  '- First item with **bold text** and `inline code`.',
  '- Second item spans enough text to wrap on desktop and exercise actual layout measurement.',
  '- Third item keeps the list alive while the final stored message replaces the live draft.',
  '',
  '```ts',
  'export function stableFinalFrame() {',
  "  return 'no flicker'",
  '}',
  '```',
  '',
  '> The final message should occupy the same visual position as the live stream.',
  '',
  'Final sentence after the blockquote so markdown parsing has realistic trailing content.',
].join('\n')

interface FrameSample {
  label: string
  top: number
  bottom: number
  height: number
  scrollTop: number
  scrollHeight: number
  clientHeight: number
  bottomGap: number
  transform: string
  itemType: string
}

function json(response: unknown): string {
  return JSON.stringify(response)
}

function writeJson(res: import('node:http').ServerResponse, status: number, response: unknown): void {
  res.writeHead(status, {
    'content-type': 'application/json',
    'cache-control': 'no-store',
  })
  res.end(json(response))
}

function activeLifecycle(): Record<string, unknown> {
  return {
    session_id: SESSION_ID,
    run_id: RUN_ID,
    active: true,
    phase: 'running',
    started_at: 1,
    updated_at: 1,
    generation: 1,
  }
}

function sessionWire(lifecycle: Record<string, unknown> | null = activeLifecycle()): Record<string, unknown> {
  return {
    id: SESSION_ID,
    title: 'Streaming jitter regression',
    workspace_path: WORKSPACE_PATH,
    workspace_name: WORKSPACE_NAME,
    mode: 'auto',
    metadata: {},
    message_count: 1,
    created_at: 1,
    updated_at: 1,
    lifecycle,
  }
}

async function startMockBackend(): Promise<{ server: Server; port: number }> {
  const server = createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    const path = url.pathname

    if (path === '/v1/auth/desktop/session') {
      writeJson(res, 200, { ok: true })
      return
    }
    if (path === '/v1/vault') {
      writeJson(res, 200, { enabled: false, unlocked: true, unlock_required: false, storage_mode: 'memory' })
      return
    }
    if (path === '/v1/onboarding') {
      writeJson(res, 200, {
        ok: true,
        needs_onboarding: false,
        config: { swarm_name: 'Playwright Swarm', mode: 'lan', port: 7781, desktop_port: 5555 },
        heuristics: { credential_count: 1, saved_workspace_count: 1, vault_configured: false },
      })
      return
    }
    if (path === '/v1/swarm/state') {
      writeJson(res, 200, {
        ok: true,
        state: {
          node: { swarm_id: 'swarm-playwright', transports: [] },
          pairing: {},
          current_group_id: '',
          groups: [],
        },
      })
      return
    }
    if (path === '/v1/providers') {
      writeJson(res, 200, { providers: [{ id: 'mock', ready: true, runnable: true }] })
      return
    }
    if (path === '/v1/auth/credentials') {
      writeJson(res, 200, {
        provider: '',
        query: '',
        total: 1,
        providers: ['mock'],
        records: [{ id: 'cred-mock', provider: 'mock', label: 'Mock', active: true }],
      })
      return
    }
    if (path === '/v1/model') {
      writeJson(res, 200, {
        preference: { provider: 'mock', model: 'streaming-jitter', thinking: '', service_tier: '', context_mode: '' },
        context_window: 128000,
        max_output_tokens: 4096,
      })
      return
    }
    if (path === '/v1/models/favorites') {
      writeJson(res, 200, { records: [{ provider: 'mock', model: 'streaming-jitter', label: 'Mock Streaming Jitter' }] })
      return
    }
    if (path === '/v1/model/catalog') {
      writeJson(res, 200, { records: [{ provider: 'mock', model: 'streaming-jitter', context_window: 128000 }] })
      return
    }
    if (path === '/v2/agents') {
      writeJson(res, 200, {
        state: {
          active_primary: 'swarm',
          version: 1,
          profiles: [{ name: 'swarm', mode: 'primary', enabled: true, provider: 'mock', model: 'streaming-jitter', exit_plan_mode_enabled: true }],
          active_subagent: {},
        },
      })
      return
    }
    if (path === '/v1/workspace/overview') {
      writeJson(res, 200, {
        ok: true,
        current_workspace: { requested_path: WORKSPACE_PATH, resolved_path: WORKSPACE_PATH, workspace_name: WORKSPACE_NAME },
        workspaces: [{ path: WORKSPACE_PATH, workspace_name: WORKSPACE_NAME, theme_id: '', directories: [], is_git_repo: false, sort_index: 0, added_at: 1, updated_at: 1, last_selected_at: 1, active: true, worktree_enabled: false, sessions: [sessionWire()] }],
        directories: [],
      })
      return
    }
    if (path === `/v1/sessions/${SESSION_ID}`) {
      writeJson(res, 200, { session: sessionWire() })
      return
    }
    if (path === `/v1/sessions/${SESSION_ID}/messages`) {
      writeJson(res, 200, { messages: [{ id: 'msg-user', session_id: SESSION_ID, global_seq: 1, role: 'user', content: 'Stream markdown please.', created_at: 1 }] })
      return
    }
    if (path === `/v1/sessions/${SESSION_ID}/preference`) {
      writeJson(res, 200, {
        preference: { provider: 'mock', model: 'streaming-jitter', thinking: '', service_tier: '', context_mode: '' },
        context_window: 128000,
        max_output_tokens: 4096,
      })
      return
    }
    if (path === `/v1/sessions/${SESSION_ID}/permissions`) {
      writeJson(res, 200, { permissions: [] })
      return
    }
    if (path === `/v1/sessions/${SESSION_ID}/usage`) {
      writeJson(res, 200, { usage_summary: null })
      return
    }
    if (path === '/v1/notifications') {
      writeJson(res, 200, { notifications: [], summary: { swarm_id: 'swarm-playwright', total_count: 0, unread_count: 0, active_count: 0, updated_at: 0 } })
      return
    }

    writeJson(res, 200, { ok: true })
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert(address && typeof address === 'object')
  return { server, port: address.port }
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
    env: {
      ...process.env,
      SWARM_BACKEND_URL: `http://127.0.0.1:${backendPort}`,
      SWARM_DESKTOP_PORT: String(port),
    },
  })

  let output = ''
  vite.stdout.on('data', (chunk) => { output += String(chunk) })
  vite.stderr.on('data', (chunk) => { output += String(chunk) })

  const deadline = Date.now() + 30_000
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/`)
      if (response.ok) {
        return { vite, port }
      }
    } catch {
      // Wait for Vite to bind.
    }
    await new Promise((resolve) => setTimeout(resolve, 100))
  }

  vite.kill('SIGTERM')
  throw new Error(`Vite did not start on port ${port}. Output:\n${output}`)
}

async function stopVite(vite: ChildProcessWithoutNullStreams): Promise<void> {
  if (vite.exitCode !== null || vite.signalCode !== null) {
    return
  }
  const exited = once(vite, 'exit').then(() => undefined)
  vite.kill('SIGTERM')
  await Promise.race([
    exited,
    new Promise<void>((resolve) => setTimeout(resolve, 2_000)),
  ])
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
      send(data) {
        window.__mockSocketSent = [...(window.__mockSocketSent || []), { url: this.url, data }];
      }
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
    window.__emitRunFrame = (payload) => {
      const sockets = window.__mockSockets || [];
      const socket = sockets.find((entry) => entry.url.includes('/v1/sessions/' + sessionId + '/run/stream')) || sockets.at(-1);
      if (!socket) throw new Error('run stream socket was not opened');
      socket.__emit(payload);
    };
    window.__sampleChatFrame = (label) => {
      const scroller = document.querySelector('[data-testid="desktop-chat-scroller"]');
      const rows = Array.from(document.querySelectorAll('[data-testid="desktop-chat-row"]'));
      const row = rows.at(-1) || null;
      if (!scroller || !row) return null;
      const rect = row.getBoundingClientRect();
      return {
        label,
        top: rect.top,
        bottom: rect.bottom,
        height: rect.height,
        scrollTop: scroller.scrollTop,
        scrollHeight: scroller.scrollHeight,
        clientHeight: scroller.clientHeight,
        bottomGap: scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight,
        transform: row.style.transform,
        itemType: row.getAttribute('data-render-item-type') || '',
      };
    };
  })()`)
}

async function waitForSample(page: Page): Promise<FrameSample> {
  const sample = await page.waitForFunction(() => (window as any).__sampleChatFrame?.('ready') ?? null)
  return await sample.jsonValue() as FrameSample
}

async function sampleAnimationFrames(page: Page, prefix: string, count: number): Promise<FrameSample[]> {
  return await page.evaluate(async ({ prefix, count }) => {
    const samples: any[] = []
    for (let index = 0; index < count; index += 1) {
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
      const sample = (window as any).__sampleChatFrame(`${prefix}:${index}`)
      if (sample) samples.push(sample)
    }
    return samples
  }, { prefix, count }) as FrameSample[]
}

function maxConsecutiveDelta(samples: FrameSample[], key: 'top' | 'bottom' | 'scrollTop' | 'height'): number {
  let max = 0
  for (let index = 1; index < samples.length; index += 1) {
    max = Math.max(max, Math.abs(samples[index][key] - samples[index - 1][key]))
  }
  return max
}

test('desktop streaming markdown finalizes without a last-frame position jitter', { timeout: 60_000 }, async () => {
  const backend = await startMockBackend()
  const app = await startVite(backend.port)
  const browser = await chromium.launch({ headless: true })

  try {
    const page = await browser.newPage({ viewport: { width: 1440, height: 960 } })
    await installBrowserStreamControls(page)
    await page.goto(`http://127.0.0.1:${app.port}/${WORKSPACE_SLUG}/${SESSION_ID}`)
    await page.getByTestId('desktop-chat-scroller').waitFor({ state: 'visible', timeout: 15_000 })

    await page.evaluate(({ runId, sessionId }) => {
      ;(window as any).__emitRunFrame({ type: 'run.accepted', run_id: runId, seq: 1, status: 'running' })
      ;(window as any).__emitRunFrame({ type: 'session.lifecycle.updated', run_id: runId, seq: 2, lifecycle: { session_id: sessionId, run_id: runId, active: true, phase: 'running', started_at: Date.now(), updated_at: Date.now(), generation: 1 } })
    }, { runId: RUN_ID, sessionId: SESSION_ID })

    const chunks = MARKDOWN_CONTENT.match(/[\s\S]{1,48}/g) ?? [MARKDOWN_CONTENT]
    for (let index = 0; index < chunks.length; index += 1) {
      await page.evaluate(({ runId, delta, seq }) => {
        ;(window as any).__emitRunFrame({ type: 'assistant.delta', run_id: runId, seq, delta })
      }, { runId: RUN_ID, delta: chunks[index], seq: 10 + index })
      if (index % 3 === 0) {
        await sampleAnimationFrames(page, 'streaming', 1)
      }
    }

    await waitForSample(page)
    await page.waitForFunction(() => {
      const rows = Array.from(document.querySelectorAll('[data-testid="desktop-chat-row"]')) as HTMLElement[]
      return rows.at(-1)?.getAttribute('data-render-item-type') === 'live-assistant'
    })
    const before = await sampleAnimationFrames(page, 'before-final', 6)

    await page.evaluate(({ runId, sessionId, content }) => {
      ;(window as any).__emitRunFrame({
        type: 'message.stored',
        run_id: runId,
        seq: 500,
        message: { id: 'msg-assistant-final', session_id: sessionId, global_seq: 2, role: 'assistant', content, created_at: Date.now() },
      })
      ;(window as any).__emitRunFrame({ type: 'turn.completed', run_id: runId, seq: 501 })
      ;(window as any).__emitRunFrame({ type: 'session.lifecycle.updated', run_id: runId, seq: 502, lifecycle: { session_id: sessionId, run_id: runId, active: false, phase: 'completed', started_at: Date.now() - 1000, ended_at: Date.now(), updated_at: Date.now(), generation: 1 } })
    }, { runId: RUN_ID, sessionId: SESSION_ID, content: MARKDOWN_CONTENT })

    const after = await sampleAnimationFrames(page, 'after-final', 18)
    const samples = [...before, ...after]
    const last = samples.at(-1)
    assert(last, 'expected frame samples')
    assert.equal(last.itemType, 'message', `expected final row to be stored message; samples=${JSON.stringify(samples, null, 2)}`)

    const topJump = maxConsecutiveDelta(samples, 'top')
    const bottomJump = maxConsecutiveDelta(samples, 'bottom')
    const scrollJump = maxConsecutiveDelta(after, 'scrollTop')
    const diagnostics = JSON.stringify(samples, null, 2)

    assert.ok(topJump <= 1, `last message top jittered by ${topJump}px\n${diagnostics}`)
    assert.ok(bottomJump <= 1, `last message bottom jittered by ${bottomJump}px\n${diagnostics}`)
    assert.ok(scrollJump <= 1, `scrollTop jittered after finalization by ${scrollJump}px\n${diagnostics}`)
    assert.ok(Math.abs(last.bottomGap) <= 2, `chat did not remain pinned to bottom\n${diagnostics}`)
  } finally {
    await browser.close().catch(() => undefined)
    await stopVite(app.vite)
    await new Promise((resolve) => backend.server.close(resolve))
  }
})
