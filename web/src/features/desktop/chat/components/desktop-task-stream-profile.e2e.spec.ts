import assert from 'node:assert/strict'
import { spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { once } from 'node:events'
import { existsSync } from 'node:fs'
import { mkdir, writeFile } from 'node:fs/promises'
import { createServer, type Server } from 'node:http'
import { mkdtemp } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

import { chromium, type BrowserContext, type CDPSession, type Page } from 'playwright'

const ENABLED = process.env.SWARM_DESKTOP_TASK_STREAM_PROFILE === '1'
const SESSION_ID = 'session-playwright-v3-task-stream-profile'
const RUN_ID = 'run-playwright-v3-task-stream-profile'
const WORKSPACE_PATH = 'task-stream-profile-workspace'
const WORKSPACE_NAME = 'Task Stream Profile'
const WORKSPACE_SLUG = 'task-stream-profile'
const OPAQUE_SNAPSHOT_CURSOR = 'v3c1.task_stream_profile_snapshot.task_stream_profile_signature'
const OPAQUE_RECONNECT_CURSOR = 'v3c1.task_stream_profile_reconnect.task_stream_profile_signature'
const DEFAULT_DURATION_MS = 90_000
const DEFAULT_AGENT_COUNT = 20
const DEFAULT_TICK_MS = 250
const DEFAULT_PREVIEW_BYTES = 320

interface PerfMetric {
  name: string
  value: number
}

interface CpuProfileNode {
  id: number
  callFrame?: {
    functionName?: string
    url?: string
    lineNumber?: number
    columnNumber?: number
  }
}

interface CpuProfile {
  nodes?: CpuProfileNode[]
  samples?: number[]
  timeDeltas?: number[]
}

interface BrowserInstrumentationSnapshot {
  longTasks: Array<{ name: string; startTime: number; duration: number }>
  lagSamples: Array<{ at: number; lag: number }>
  mutationCount: number
  frameCount: number
}

interface ProfileSummary {
  ok: boolean
  evidenceDir: string
  durationMs: number
  agentCount: number
  tickMs: number
  emittedFrames: number
  expectedFrames: number
  topCpuSelfTime: Array<{ functionName: string; url: string; lineNumber: number; columnNumber: number; selfMs: number; samples: number }>
  cpuMetricsDelta: Record<string, number>
  longTaskSummary: {
    count: number
    totalMs: number
    maxMs: number
    over100Ms: number
    top: BrowserInstrumentationSnapshot['longTasks']
  }
  eventLoopLagSummary: {
    samples: number
    maxMs: number
    p95Ms: number
    over50Ms: number
  }
  domMutationCount: number
  animationFrameCount: number
  metricsSamples: Array<{ elapsedMs: number; metrics: Record<string, number> }>
  error?: string
}

function numberFromEnv(name: string, fallback: number): number {
  const value = Number(process.env[name] ?? '')
  return Number.isFinite(value) && value > 0 ? value : fallback
}

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
    title: 'V3 task stream CPU profile',
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
    preference: { provider: 'mock', model: 'task-stream-profile', thinking: '', updated_at: 0 },
  }
}

function userMessage(): Record<string, unknown> {
  return {
    id: 'msg-user-task-stream-profile',
    session_id: SESSION_ID,
    global_seq: 1,
    role: 'user',
    content: 'Profile task tool streaming with 20 subagents.',
    created_at: 1,
  }
}

function v3Snapshot(messages: Record<string, unknown>[], active = true): Record<string, unknown> {
  return {
    session: sessionWire(active),
    projection: { session_id: SESSION_ID, last_event_seq: 1, projection_high_watermark_seq: 1, updated_at: Date.now() },
    messages,
    events: [],
    pending_permissions: [],
    usage_summary: null,
    preference: { provider: 'mock', model: 'task-stream-profile', thinking: '', updated_at: 0 },
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
    scope_id: 'task-stream-profile-scope',
    selector: { kind: 'session_ids', session_ids: [SESSION_ID] },
    sync_scope: { surface: 'desktop', stream_kind: 'v3.sync.snapshot', selector_filter_hash: 'task-stream-profile', resource_set: 'messages,events,run_intents' },
    known_sessions: {},
    tombstones_by_session: {},
    replay_instructions: { stream_path: '/v3/sync/stream', transport: 'http_post', after_endpoint_cursor: cursor, bootstrap_required_on_cursor_error: true },
    projections_by_session: { [SESSION_ID]: { session_id: SESSION_ID, last_event_seq: 1, projection_high_watermark_seq: 1, updated_at: Date.now() } },
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

async function startMockBackend(): Promise<{ server: Server; port: number; requests: string[] }> {
  const sessionMessages = [userMessage()]
  const requests: string[] = []
  const server = createServer((req, res) => {
    const url = new URL(req.url ?? '/', 'http://127.0.0.1')
    const path = url.pathname
    requests.push(path)

    if (path === '/v3/sessions:workset' || path === '/v3/sessions:discover') {
      return writeJson(res, 500, { error: `legacy desktop sync route forbidden in task stream profile: ${path}` })
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
    if (path === '/v1/swarm/state') return writeJson(res, 200, { ok: true, state: { node: { swarm_id: 'swarm-playwright', transports: [] }, pairing: {}, current_group_id: '', groups: [] } })
    if (path === '/v1/providers') return writeJson(res, 200, { providers: [{ id: 'mock', ready: true, runnable: true }] })
    if (path === '/v1/auth/credentials') return writeJson(res, 200, { provider: '', query: '', total: 1, providers: ['mock'], records: [{ id: 'cred-mock', provider: 'mock', label: 'Mock', active: true }] })
    if (path === '/v1/model') return writeJson(res, 200, { preference: { provider: 'mock', model: 'task-stream-profile', thinking: '', service_tier: '', context_mode: '' }, context_window: 128000, max_output_tokens: 4096 })
    if (path === '/v1/models/favorites') return writeJson(res, 200, { records: [{ provider: 'mock', model: 'task-stream-profile', label: 'Mock Task Stream Profile' }] })
    if (path === '/v1/model/catalog') return writeJson(res, 200, { records: [{ provider: 'mock', model: 'task-stream-profile', context_window: 128000 }] })
    if (path === '/v2/agents') {
      return writeJson(res, 200, { state: { active_primary: 'swarm', version: 1, profiles: [{ name: 'swarm', mode: 'primary', enabled: true, provider: 'mock', model: 'task-stream-profile', exit_plan_mode_enabled: true }], active_subagent: {} } })
    }
    if (path === '/v1/workspace/overview') {
      return writeJson(res, 200, {
        ok: true,
        current_workspace: { requested_path: WORKSPACE_PATH, resolved_path: WORKSPACE_PATH, workspace_name: WORKSPACE_NAME },
        workspaces: [{ path: WORKSPACE_PATH, workspace_name: WORKSPACE_NAME, theme_id: '', directories: [], is_git_repo: false, sort_index: 0, added_at: 1, updated_at: 1, last_selected_at: 1, active: true, worktree_enabled: false, sessions: [sessionWire(true)] }],
        directories: [],
      })
    }
    if (path === '/v3/sync/bootstrap' || path === '/v3/sync/hydrate') return writeJson(res, 200, v3SyncSnapshot(sessionMessages, true))
    if (path === '/v3/sessions:reconnect') return writeJson(res, 200, v3SyncSnapshot(sessionMessages, true, OPAQUE_RECONNECT_CURSOR))
    if (path === `/v3/sessions/${SESSION_ID}/preference`) return writeJson(res, 200, { preference: { provider: 'mock', model: 'task-stream-profile', thinking: '', updated_at: 0 }, context_window: 128000, max_output_tokens: 4096 })
    if (path === `/v3/sessions/${SESSION_ID}`) return writeJson(res, 200, v3Snapshot(sessionMessages, true))
    if (path === '/v1/notifications') return writeJson(res, 200, { notifications: [], summary: { swarm_id: 'swarm-playwright', total_count: 0, unread_count: 0, active_count: 0, updated_at: 0 } })

    writeJson(res, 200, { ok: true })
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = server.address()
  assert(address && typeof address === 'object')
  return { server, port: address.port, requests }
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

async function installBrowserHarness(page: Page): Promise<void> {
  await page.addInitScript(`(() => {
    const sessionId = ${JSON.stringify(SESSION_ID)};
    window.__swarmTaskStreamProfile = {
      longTasks: [],
      lagSamples: [],
      mutationCount: 0,
      frameCount: 0,
    };
    try {
      const observer = new PerformanceObserver((list) => {
        for (const entry of list.getEntries()) {
          window.__swarmTaskStreamProfile.longTasks.push({
            name: entry.name,
            startTime: Math.round(entry.startTime),
            duration: Math.round(entry.duration),
          });
        }
      });
      observer.observe({ entryTypes: ['longtask'] });
    } catch {}
    try {
      let expected = performance.now() + 250;
      window.setInterval(() => {
        const now = performance.now();
        const lag = Math.max(0, now - expected);
        expected = now + 250;
        window.__swarmTaskStreamProfile.lagSamples.push({ at: Math.round(now), lag: Math.round(lag) });
      }, 250);
    } catch {}
    try {
      const tick = () => {
        window.__swarmTaskStreamProfile.frameCount += 1;
        window.requestAnimationFrame(tick);
      };
      window.requestAnimationFrame(tick);
    } catch {}
    try {
      const installMutationObserver = () => {
        const observer = new MutationObserver((records) => {
          window.__swarmTaskStreamProfile.mutationCount += records.length;
        });
        observer.observe(document.body || document.documentElement, { childList: true, subtree: true, characterData: true });
      };
      if (document.body) installMutationObserver();
      else document.addEventListener('DOMContentLoaded', installMutationObserver, { once: true });
    } catch {}

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
    window.__emitV3EventEverywhere = (seq, eventType, payload) => {
      const body = { session_id: sessionId, ...payload };
      for (const socket of window.__mockSockets || []) {
        if (socket.readyState !== MockWebSocket.OPEN) continue;
        if (socket.url.includes('/v3/realtime/stream') || socket.url.includes('/v3/sessions/' + sessionId + '/stream')) {
          socket.__emit({
            protocol: 'v3.realtime',
            protocol_version: 1,
            kind: 'event',
            ok: true,
            session_id: sessionId,
            endpoint_cursor: 'v3c1.task_stream_profile_event_' + seq + '.task_stream_profile_signature_' + seq,
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
    window.__startTaskStreamProfile = ({ agentCount, durationMs, tickMs, previewBytes }) => {
      const startedAt = performance.now();
      let seq = 2;
      let emitted = 0;
      const previewSeed = 'stream '.repeat(Math.max(1, Math.ceil(previewBytes / 7))).slice(0, previewBytes);
      const snapshot = (elapsedMs, terminal) => ({
        path_id: 'tool.task.stream.v1',
        tool: 'task',
        status: terminal ? 'completed' : 'running',
        launch_count: agentCount,
        launches: Array.from({ length: agentCount }, (_, index) => {
          const launchIndex = index + 1;
          const toolNames = ['read', 'search', 'bash', 'thinking', 'edit'];
          const toolName = toolNames[(Math.floor(elapsedMs / 1000) + index) % toolNames.length];
          return {
            launch_index: launchIndex,
            child_session_id: 'child-task-stream-profile-' + launchIndex,
            session_id: 'child-task-stream-profile-' + launchIndex,
            status: terminal ? 'completed' : 'running',
            resolved_agent_name: 'parallel',
            requested_subagent_type: 'parallel',
            assignment_label: 'agent ' + launchIndex,
            subagent_provider: 'mock',
            subagent_model: 'task-stream-profile',
            current_tool: toolName,
            tool_order: ['task', toolName],
            current_tool_started_at_ms: Date.now() - (elapsedMs % 5000),
            launch_started_at_ms: Date.now() - elapsedMs,
            current_tool_ms: elapsedMs % 5000,
            elapsed_ms: elapsedMs,
            current_preview_kind: toolName === 'thinking' ? 'reasoning' : 'tool',
            current_preview_text: previewSeed + ' #' + launchIndex + ' t=' + elapsedMs,
          };
        }),
      });
      window.__emitV3EventEverywhere(seq++, 'session.tool.started', {
        run_id: ${JSON.stringify(RUN_ID)},
        call_id: 'call-task-stream-profile',
        tool_instance_id: 'tool-instance-task-stream-profile',
        tool_name: 'task',
        status: 'running',
        output: JSON.stringify(snapshot(0, false)),
        recorded_at: Date.now(),
      });
      const timer = window.setInterval(() => {
        const elapsedMs = Math.min(durationMs, Math.round(performance.now() - startedAt));
        const terminal = elapsedMs >= durationMs;
        window.__emitV3EventEverywhere(seq++, terminal ? 'session.tool.completed' : 'session.tool.delta', {
          run_id: ${JSON.stringify(RUN_ID)},
          call_id: 'call-task-stream-profile',
          tool_instance_id: 'tool-instance-task-stream-profile',
          tool_name: 'task',
          status: terminal ? 'completed' : 'running',
          output: JSON.stringify(snapshot(elapsedMs, terminal)),
          duration_ms: elapsedMs,
          recorded_at: Date.now(),
        });
        emitted += 1;
        if (terminal) {
          window.clearInterval(timer);
          window.__taskStreamProfileDone = true;
          window.__taskStreamProfileEmitted = emitted;
        }
      }, tickMs);
      return { startedAt, expectedFrames: Math.ceil(durationMs / tickMs) };
    };
  })()`)
}

async function getMetrics(client: CDPSession): Promise<Record<string, number>> {
  const response = await client.send('Performance.getMetrics') as { metrics: PerfMetric[] }
  return Object.fromEntries(response.metrics.map((metric) => [metric.name, metric.value]))
}

function metricDelta(before: Record<string, number>, after: Record<string, number>): Record<string, number> {
  const keys = ['TaskDuration', 'ScriptDuration', 'LayoutDuration', 'RecalcStyleDuration', 'JSHeapUsedSize', 'Nodes', 'LayoutCount', 'RecalcStyleCount']
  return Object.fromEntries(keys.map((key) => [key, Number(((after[key] ?? 0) - (before[key] ?? 0)).toFixed(3))]))
}

function summarizeCpuProfile(profile: CpuProfile): ProfileSummary['topCpuSelfTime'] {
  const nodes = new Map((profile.nodes ?? []).map((node) => [node.id, node]))
  const samples = profile.samples ?? []
  const timeDeltas = profile.timeDeltas ?? []
  const byNode = new Map<number, { selfMs: number; samples: number }>()
  for (let index = 0; index < samples.length; index += 1) {
    const id = samples[index]
    const deltaUs = timeDeltas[index] ?? 0
    const current = byNode.get(id) ?? { selfMs: 0, samples: 0 }
    current.selfMs += deltaUs / 1000
    current.samples += 1
    byNode.set(id, current)
  }
  return [...byNode.entries()]
    .map(([id, value]) => {
      const frame = nodes.get(id)?.callFrame ?? {}
      return {
        functionName: frame.functionName || '(anonymous)',
        url: frame.url || '',
        lineNumber: typeof frame.lineNumber === 'number' ? frame.lineNumber + 1 : 0,
        columnNumber: typeof frame.columnNumber === 'number' ? frame.columnNumber + 1 : 0,
        selfMs: Number(value.selfMs.toFixed(2)),
        samples: value.samples,
      }
    })
    .filter((entry) => entry.functionName !== '(idle)' && entry.functionName !== '(program)')
    .sort((left, right) => right.selfMs - left.selfMs)
    .slice(0, 40)
}

function summarizeLongTasks(longTasks: BrowserInstrumentationSnapshot['longTasks']): ProfileSummary['longTaskSummary'] {
  const sorted = [...longTasks].sort((left, right) => right.duration - left.duration)
  return {
    count: longTasks.length,
    totalMs: longTasks.reduce((sum, task) => sum + task.duration, 0),
    maxMs: sorted[0]?.duration ?? 0,
    over100Ms: longTasks.filter((task) => task.duration >= 100).length,
    top: sorted.slice(0, 25),
  }
}

function percentile(values: number[], p: number): number {
  if (values.length === 0) return 0
  const sorted = [...values].sort((left, right) => left - right)
  const index = Math.min(sorted.length - 1, Math.max(0, Math.ceil((p / 100) * sorted.length) - 1))
  return sorted[index]
}

function summarizeLag(samples: BrowserInstrumentationSnapshot['lagSamples']): ProfileSummary['eventLoopLagSummary'] {
  const lags = samples.map((sample) => sample.lag)
  return {
    samples: samples.length,
    maxMs: Math.max(0, ...lags),
    p95Ms: percentile(lags, 95),
    over50Ms: lags.filter((lag) => lag >= 50).length,
  }
}

async function collectBrowserInstrumentation(page: Page): Promise<BrowserInstrumentationSnapshot> {
  return await page.evaluate(() => {
    const snapshot = (window as unknown as { __swarmTaskStreamProfile?: BrowserInstrumentationSnapshot }).__swarmTaskStreamProfile
    return snapshot ?? { longTasks: [], lagSamples: [], mutationCount: 0, frameCount: 0 }
  })
}

async function startMetricsSampler(client: CDPSession, durationMs: number): Promise<ProfileSummary['metricsSamples']> {
  const startedAt = Date.now()
  const samples: ProfileSummary['metricsSamples'] = []
  const intervalMs = Math.max(1000, Math.min(5000, Math.floor(durationMs / 18)))
  while (Date.now() - startedAt < durationMs) {
    samples.push({ elapsedMs: Date.now() - startedAt, metrics: await getMetrics(client) })
    await new Promise((resolve) => setTimeout(resolve, intervalMs))
  }
  samples.push({ elapsedMs: Date.now() - startedAt, metrics: await getMetrics(client) })
  return samples
}

async function profileScenario(context: BrowserContext, page: Page, appPort: number, evidenceDir: string): Promise<ProfileSummary> {
  const durationMs = numberFromEnv('SWARM_TASK_STREAM_PROFILE_DURATION_MS', DEFAULT_DURATION_MS)
  const agentCount = numberFromEnv('SWARM_TASK_STREAM_PROFILE_AGENTS', DEFAULT_AGENT_COUNT)
  const tickMs = numberFromEnv('SWARM_TASK_STREAM_PROFILE_TICK_MS', DEFAULT_TICK_MS)
  const previewBytes = numberFromEnv('SWARM_TASK_STREAM_PROFILE_PREVIEW_BYTES', DEFAULT_PREVIEW_BYTES)

  const client = await context.newCDPSession(page)
  await client.send('Performance.enable')
  await client.send('Profiler.enable')

  await page.goto(`http://127.0.0.1:${appPort}/${WORKSPACE_SLUG}/${SESSION_ID}`, { waitUntil: 'domcontentloaded', timeout: 60_000 })
  await page.getByTestId('desktop-v3-existing-conversation-pane').waitFor({ state: 'visible', timeout: 20_000 })
  await page.waitForFunction(() => (window as unknown as { __socketUrls?: () => string[] }).__socketUrls?.().some((url: string) => url.includes('/v3/realtime/stream')))
  await page.waitForTimeout(250)

  const beforeMetrics = await getMetrics(client)
  await client.send('Profiler.start')
  const metricsPromise = startMetricsSampler(client, durationMs)
  const { expectedFrames } = await page.evaluate((options) => {
    return (window as unknown as { __startTaskStreamProfile: (input: typeof options) => { startedAt: number; expectedFrames: number } }).__startTaskStreamProfile(options)
  }, { agentCount, durationMs, tickMs, previewBytes })

  await page.waitForFunction(() => (window as unknown as { __taskStreamProfileDone?: boolean }).__taskStreamProfileDone === true, undefined, { timeout: durationMs + 30_000 })
  await page.waitForTimeout(1000)

  const profileResponse = await client.send('Profiler.stop') as { profile: CpuProfile }
  const afterMetrics = await getMetrics(client)
  const metricsSamples = await metricsPromise
  const instrumentation = await collectBrowserInstrumentation(page)
  const emittedFrames = await page.evaluate(() => (window as unknown as { __taskStreamProfileEmitted?: number }).__taskStreamProfileEmitted ?? 0)

  await writeFile(join(evidenceDir, 'cpu-profile.cpuprofile'), JSON.stringify(profileResponse.profile, null, 2))
  await writeFile(join(evidenceDir, 'metrics-samples.json'), JSON.stringify(metricsSamples, null, 2))
  await writeFile(join(evidenceDir, 'browser-instrumentation.json'), JSON.stringify(instrumentation, null, 2))

  return {
    ok: true,
    evidenceDir,
    durationMs,
    agentCount,
    tickMs,
    emittedFrames,
    expectedFrames,
    topCpuSelfTime: summarizeCpuProfile(profileResponse.profile),
    cpuMetricsDelta: metricDelta(beforeMetrics, afterMetrics),
    longTaskSummary: summarizeLongTasks(instrumentation.longTasks),
    eventLoopLagSummary: summarizeLag(instrumentation.lagSamples),
    domMutationCount: instrumentation.mutationCount,
    animationFrameCount: instrumentation.frameCount,
    metricsSamples,
  }
}

test('Desktop task tool stream profile: 20 subagents stream for 90s and captures client CPU evidence', { skip: !ENABLED, timeout: numberFromEnv('SWARM_TASK_STREAM_PROFILE_DURATION_MS', DEFAULT_DURATION_MS) + 90_000 }, async () => {
  const evidenceDir = process.env.SWARM_TASK_STREAM_PROFILE_EVIDENCE_DIR || await mkdtemp(join(tmpdir(), 'swarm-desktop-task-stream-profile-'))
  await mkdir(evidenceDir, { recursive: true })

  const backend = await startMockBackend()
  const app = await startVite(backend.port)
  const browser = await chromium.launch({ headless: process.env.SWARM_E2E_HEADFUL !== '1', args: ['--disable-dev-shm-usage'] })
  const context = await browser.newContext({ viewport: { width: 1440, height: 1000 } })
  const page = await context.newPage()
  const consoleLines: string[] = []
  page.on('console', (message) => consoleLines.push(`${message.type()}: ${message.text()}`))
  page.on('pageerror', (error) => consoleLines.push(`pageerror: ${error.message}`))

  let summary: ProfileSummary | null = null
  try {
    await installBrowserHarness(page)
    summary = await profileScenario(context, page, app.port, evidenceDir)
    await writeFile(join(evidenceDir, 'summary.json'), JSON.stringify(summary, null, 2))
    await writeFile(join(evidenceDir, 'console.json'), JSON.stringify(consoleLines, null, 2))
    await writeFile(join(evidenceDir, 'backend-requests.json'), JSON.stringify(backend.requests, null, 2))
    await page.screenshot({ path: join(evidenceDir, 'final.png'), fullPage: true }).catch(() => undefined)

    console.log(`desktop task stream CPU profile evidence\n${JSON.stringify({
      ok: summary.ok,
      evidenceDir,
      durationMs: summary.durationMs,
      agentCount: summary.agentCount,
      emittedFrames: summary.emittedFrames,
      expectedFrames: summary.expectedFrames,
      cpuMetricsDelta: summary.cpuMetricsDelta,
      longTaskSummary: summary.longTaskSummary,
      eventLoopLagSummary: summary.eventLoopLagSummary,
      topCpuSelfTime: summary.topCpuSelfTime.slice(0, 15),
    }, null, 2)}`)

    assert.ok(summary.emittedFrames >= Math.max(1, summary.expectedFrames - 2), `stream emitted too few frames: ${summary.emittedFrames}/${summary.expectedFrames}`)
    assert.ok(summary.topCpuSelfTime.length > 0, 'CPU profiler did not report any active JavaScript samples')
  } catch (error) {
    summary = {
      ok: false,
      evidenceDir,
      durationMs: numberFromEnv('SWARM_TASK_STREAM_PROFILE_DURATION_MS', DEFAULT_DURATION_MS),
      agentCount: numberFromEnv('SWARM_TASK_STREAM_PROFILE_AGENTS', DEFAULT_AGENT_COUNT),
      tickMs: numberFromEnv('SWARM_TASK_STREAM_PROFILE_TICK_MS', DEFAULT_TICK_MS),
      emittedFrames: 0,
      expectedFrames: 0,
      topCpuSelfTime: [],
      cpuMetricsDelta: {},
      longTaskSummary: { count: 0, totalMs: 0, maxMs: 0, over100Ms: 0, top: [] },
      eventLoopLagSummary: { samples: 0, maxMs: 0, p95Ms: 0, over50Ms: 0 },
      domMutationCount: 0,
      animationFrameCount: 0,
      metricsSamples: [],
      error: error instanceof Error ? error.message : String(error),
    }
    await writeFile(join(evidenceDir, 'summary.json'), JSON.stringify(summary, null, 2))
    await writeFile(join(evidenceDir, 'console.json'), JSON.stringify(consoleLines, null, 2))
    await page.screenshot({ path: join(evidenceDir, 'failure.png'), fullPage: true }).catch(() => undefined)
    throw error
  } finally {
    await browser.close().catch(() => undefined)
    await stopVite(app.vite).catch(() => undefined)
    await new Promise<void>((resolve) => backend.server.close(() => resolve()))
  }
})
