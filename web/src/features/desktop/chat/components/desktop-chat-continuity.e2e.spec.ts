import assert from 'node:assert/strict'
import { spawn, type ChildProcess } from 'node:child_process'
import { once } from 'node:events'
import { mkdirSync, writeFileSync } from 'node:fs'
import { mkdtemp } from 'node:fs/promises'
import net from 'node:net'
import { join } from 'node:path'
import test from 'node:test'

import { chromium, type Browser, type Page } from 'playwright'

const ENABLED = process.env.SWARM_DESKTOP_CONTINUITY_E2E === '1'
const SSH_TARGET = (process.env.SWARM_PRIMARY_SSH || '').trim()
const DESKTOP_URL = (process.env.SWARM_DESKTOP_URL || '').replace(/\/+$/, '')
const REMOTE_DESKTOP_PORT = Number(process.env.SWARM_REMOTE_DESKTOP_PORT || 5555)
const WORKSPACE_SELECTOR = process.env.SWARM_E2E_WORKSPACE || 'swarm-go'
const PROVIDER = process.env.SWARM_PROVIDER || 'fireworks'
const MODEL = process.env.SWARM_MODEL || 'accounts/fireworks/models/kimi-k2p6'
const THINKING = process.env.SWARM_THINKING || 'low'
const AGENT_NAME = process.env.SWARM_AGENT_NAME || 'swarm'
const TIMEOUT_MS = Number(process.env.SWARM_CONTINUITY_TIMEOUT_MS || 180_000)

const TOOL_EVENT_PREFIXES = ['session.tool.', 'session.provider_tool_call.']

type JsonRecord = Record<string, unknown>

type MessageWire = {
  id?: string
  global_seq?: number
  role?: string
  content?: string
  metadata?: JsonRecord
}

type EventWire = {
  seq?: number
  event_type?: string
  payload?: unknown
}

type EpochWire = {
  epoch_id?: string
  parent_epoch_id?: string
  status?: string
}

type HydratedSessionWire = {
  current_execution_epoch?: EpochWire
  has_active_plan?: boolean
  active_plan?: JsonRecord
}

type WorkspaceWire = {
  path?: string
  workspace_name?: string
  workspace_binding_id?: string
}

type TestEvidence = {
  scenario: string
  sessionId: string
  markers: string[]
  assistantMessages: string[]
  eventTypes: string[]
  boundary?: { before: string; after: string; parent: string }
}

function fail(message: string): never {
  throw new Error(message)
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

function uniqueMarker(prefix: string): string {
  return `${prefix}_${Date.now()}_${Math.random().toString(36).slice(2, 9)}`.toUpperCase()
}

function basename(value: string): string {
  const normalized = value.trim().replace(/[\\/]+$/, '')
  const parts = normalized.split(/[\\/]/).filter(Boolean)
  return parts[parts.length - 1] || ''
}

function slugifySegment(value: string): string {
  return value.trim().toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'workspace'
}

function pathHash(value: string): string {
  let hash = 2166136261
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index)
    hash = Math.imul(hash, 16777619)
  }
  return (hash >>> 0).toString(36)
}

function workspaceName(workspace: WorkspaceWire): string {
  return String(workspace.workspace_name ?? '').trim() || basename(String(workspace.path ?? ''))
}

function workspaceSlugBase(workspace: WorkspaceWire): string {
  const base = slugifySegment(workspaceName(workspace))
  return base === 'swarm' ? 'swarm-workspace' : base
}

function resolveWorkspaceSlug(workspaces: WorkspaceWire[], workspace: WorkspaceWire): string {
  const counts = new Map<string, number>()
  for (const candidate of workspaces) {
    const base = workspaceSlugBase(candidate)
    counts.set(base, (counts.get(base) ?? 0) + 1)
  }
  const base = workspaceSlugBase(workspace)
  return (counts.get(base) ?? 0) > 1
    ? `${base}-${pathHash(String(workspace.path ?? '')).slice(0, 6)}`
    : base
}

function selectWorkspace(workspaces: WorkspaceWire[]): WorkspaceWire {
  const selector = WORKSPACE_SELECTOR.trim()
  const lower = selector.toLowerCase()
  const workspace = workspaces.find((candidate) => String(candidate.path ?? '').trim() === selector)
    ?? workspaces.find((candidate) => workspaceName(candidate).toLowerCase() === lower)
    ?? workspaces.find((candidate) => basename(String(candidate.path ?? '')).toLowerCase() === lower)
  if (!workspace) {
    fail(`workspace ${JSON.stringify(selector)} not found; candidates=${workspaces.map((candidate) => `${workspaceName(candidate)}=${candidate.path}`).join(', ')}`)
  }
  return workspace
}

async function freePort(): Promise<number> {
  const server = net.createServer()
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolve)
  })
  const address = server.address()
  assert(address && typeof address === 'object')
  const port = address.port
  await new Promise<void>((resolve) => server.close(() => resolve()))
  return port
}

async function waitForTCP(port: number, timeoutMs: number): Promise<void> {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    const connected = await new Promise<boolean>((resolve) => {
      const socket = net.connect({ host: '127.0.0.1', port })
      socket.once('connect', () => { socket.destroy(); resolve(true) })
      socket.once('error', () => { socket.destroy(); resolve(false) })
      socket.setTimeout(500, () => { socket.destroy(); resolve(false) })
    })
    if (connected) return
    await sleep(100)
  }
  fail(`timed out waiting for local tunnel port ${port}`)
}

async function openDesktopTarget(): Promise<{ appURL: string; tunnel: ChildProcess | null }> {
  if (DESKTOP_URL) return { appURL: DESKTOP_URL, tunnel: null }
  assert(SSH_TARGET, 'set SWARM_PRIMARY_SSH to the SSH target, or set SWARM_DESKTOP_URL for a direct Desktop connection')
  const port = await freePort()
  const tunnel = spawn('ssh', ['-N', '-L', `${port}:127.0.0.1:${REMOTE_DESKTOP_PORT}`, SSH_TARGET], {
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  let stderr = ''
  tunnel.stderr?.on('data', (chunk) => { stderr += String(chunk) })
  tunnel.once('exit', (code, signal) => {
    if (code && code !== 0) console.error(`[continuity-e2e] ssh tunnel exited code=${code}: ${stderr}`)
    else if (signal) console.error(`[continuity-e2e] ssh tunnel exited signal=${signal}`)
  })
  try {
    await waitForTCP(port, 15_000)
  } catch (error) {
    tunnel.kill('SIGTERM')
    throw error
  }
  return { appURL: `http://127.0.0.1:${port}`, tunnel }
}

async function closeTunnel(tunnel: ChildProcess | null): Promise<void> {
  if (!tunnel || tunnel.exitCode !== null || tunnel.signalCode !== null) return
  const exited = once(tunnel, 'exit').then(() => undefined)
  tunnel.kill('SIGTERM')
  await Promise.race([exited, sleep(2_000)])
  if (tunnel.exitCode === null && tunnel.signalCode === null) {
    tunnel.kill('SIGKILL')
    await Promise.race([exited, sleep(2_000)])
  }
}

async function browserJSON<T>(page: Page, route: string, init: { method?: string; body?: unknown } = {}): Promise<T> {
  return await page.evaluate(async ({ route: innerRoute, init: innerInit }) => {
    const response = await fetch(innerRoute, {
      credentials: 'include',
      method: innerInit.method || 'GET',
      headers: {
        Accept: 'application/json',
        ...(innerInit.body === undefined ? {} : { 'Content-Type': 'application/json' }),
      },
      body: innerInit.body === undefined ? undefined : JSON.stringify(innerInit.body),
    })
    const text = await response.text()
    let parsed: unknown = null
    try { parsed = text ? JSON.parse(text) : null } catch { parsed = { raw: text } }
    if (!response.ok) throw new Error(`${innerInit.method || 'GET'} ${innerRoute} HTTP ${response.status}: ${text.slice(0, 1600)}`)
    return parsed
  }, { route, init }) as T
}

async function initializePage(page: Page, appURL: string): Promise<void> {
  await page.goto(appURL, { waitUntil: 'domcontentloaded' })
  await browserJSON(page, '/v1/auth/desktop/session')
}

async function createSession(page: Page): Promise<{ sessionId: string; route: string }> {
  const [overview, topology] = await Promise.all([
    browserJSON<{ workspaces?: WorkspaceWire[] }>(page, '/v1/workspace/overview?workspace_limit=500&discover_limit=500'),
    browserJSON<{ runtimes?: JsonRecord[]; workspace_bindings?: JsonRecord[] }>(page, '/v1/swarm/topology'),
  ])
  const workspaces = Array.isArray(overview.workspaces) ? overview.workspaces : []
  const workspace = selectWorkspace(workspaces)
  const workspacePath = String(workspace.path ?? '').trim()
  assert(workspacePath, `selected workspace is missing path: ${JSON.stringify(workspace)}`)
  const runtime = (topology.runtimes ?? []).find((candidate) => String(candidate.relationship ?? '') === 'self')
    ?? (topology.runtimes ?? [])[0]
  const binding = (topology.workspace_bindings ?? []).find((candidate) => String(candidate.source_workspace_path ?? '') === workspacePath)
    ?? (topology.workspace_bindings ?? []).find((candidate) => String(candidate.state ?? '') === 'bound')
    ?? (topology.workspace_bindings ?? [])[0]
  const swarmId = String(runtime?.swarm_id ?? '').trim()
  const bindingId = String(binding?.workspace_binding_id ?? workspace.workspace_binding_id ?? '').trim()
  assert(swarmId && bindingId, `missing self runtime/workspace binding: runtime=${JSON.stringify(runtime)} binding=${JSON.stringify(binding)}`)

  const suffix = `${Date.now()}-${Math.random().toString(36).slice(2, 9)}`
  const created = await browserJSON<{ session?: { id?: string } }>(page, '/v3/sessions', {
    method: 'POST',
    body: {
      client_request_id: `desktop-continuity-create:${suffix}`,
      title: `Desktop continuity E2E ${suffix}`,
      workspace_path: workspacePath,
      workspace_name: workspaceName(workspace),
      workspace_binding_id: bindingId,
      swarm_id: swarmId,
      target_kind: 'host',
      target_relationship: 'self',
      host_workspace_path: workspacePath,
      runtime_workspace_path: String(binding?.destination_workspace_path ?? workspacePath),
      mode: 'auto',
      agent_name: AGENT_NAME,
      preference: { provider: PROVIDER, model: MODEL, thinking: THINKING },
      metadata: { desktop_chat_continuity_e2e: true },
    },
  })
  const sessionId = String(created.session?.id ?? '').trim()
  assert(sessionId, `session create response missing id: ${JSON.stringify(created)}`)
  const route = `/${encodeURIComponent(resolveWorkspaceSlug(workspaces, workspace))}/${encodeURIComponent(sessionId)}`
  return { sessionId, route }
}

async function openSession(page: Page, appURL: string, route: string): Promise<void> {
  await page.goto(`${appURL}${route}`, { waitUntil: 'domcontentloaded' })
  await page.getByTestId('desktop-chat-scroller').waitFor({ state: 'visible', timeout: 30_000 })
  await page.locator('textarea').first().waitFor({ state: 'visible', timeout: 30_000 })
}

async function messages(page: Page, sessionId: string): Promise<MessageWire[]> {
  const response = await browserJSON<{ messages?: MessageWire[] }>(page, `/v3/sessions/${encodeURIComponent(sessionId)}/messages?tail=true&limit=200`)
  return Array.isArray(response.messages) ? response.messages : []
}

async function events(page: Page, sessionId: string, afterSeq = 0): Promise<EventWire[]> {
  const response = await browserJSON<{ events?: EventWire[] }>(page, `/v3/sessions/${encodeURIComponent(sessionId)}/events?after_seq=${afterSeq}&limit=500`)
  return Array.isArray(response.events) ? response.events : []
}

async function currentEventSeq(page: Page, sessionId: string): Promise<number> {
  const response = await browserJSON<{ high_watermark_seq?: number }>(page, `/v3/sessions/${encodeURIComponent(sessionId)}/events?after_seq=0&limit=1`)
  return Number(response.high_watermark_seq ?? 0)
}

async function sendMessage(page: Page, sessionId: string, prompt: string): Promise<{ runId: string; eventSeqBefore: number }> {
  const eventSeqBefore = await currentEventSeq(page, sessionId)
  const responsePromise = page.waitForResponse((response) => {
    if (response.request().method() !== 'POST') return false
    try {
      return new URL(response.url()).pathname === `/v3/sessions/${sessionId}/messages`
    } catch {
      return false
    }
  }, { timeout: 30_000 })
  await page.locator('textarea').first().fill(prompt)
  await page.getByRole('button', { name: 'Send message' }).click()
  const response = await responsePromise
  const body = await response.json() as { run_intent?: { run_id?: string } }
  const runId = String(body.run_intent?.run_id ?? '').trim()
  assert(runId, `message response missing run_id: ${JSON.stringify(body)}`)
  return { runId, eventSeqBefore }
}

async function waitForAssistant(
  page: Page,
  sessionId: string,
  afterMessageSeq: number,
  predicate: (content: string) => boolean,
  label: string,
): Promise<MessageWire> {
  const deadline = Date.now() + TIMEOUT_MS
  let last: MessageWire[] = []
  while (Date.now() < deadline) {
    last = await messages(page, sessionId)
    const match = last.find((message) => message.role === 'assistant'
      && Number(message.global_seq ?? 0) > afterMessageSeq
      && predicate(String(message.content ?? '')))
    if (match) return match
    await sleep(400)
  }
  throw new Error(`timed out waiting for ${label}; tail=${JSON.stringify(last.slice(-12), null, 2)}`)
}

async function waitForRunEvent(
  page: Page,
  sessionId: string,
  runId: string,
  acceptedTypes: string[],
  label: string,
  payloadMarker = '',
): Promise<EventWire> {
  const deadline = Date.now() + TIMEOUT_MS
  let last: EventWire[] = []
  while (Date.now() < deadline) {
    last = await events(page, sessionId)
    const match = last.find((event) => {
      if (!acceptedTypes.includes(String(event.event_type ?? ''))) return false
      const text = JSON.stringify(event.payload ?? {})
      return text.includes(runId) && (!payloadMarker || text.includes(payloadMarker))
    })
    if (match) return match
    await sleep(300)
  }
  throw new Error(`timed out waiting for ${label}; events=${JSON.stringify(last.slice(-20), null, 2)}`)
}

async function assertToolFreeSince(page: Page, sessionId: string, afterEventSeq: number): Promise<string[]> {
  const observed = await events(page, sessionId, afterEventSeq)
  const eventTypes = observed.map((event) => String(event.event_type ?? '')).filter(Boolean)
  const toolEvents = eventTypes.filter((eventType) => TOOL_EVENT_PREFIXES.some((prefix) => eventType.startsWith(prefix)))
  assert.deepEqual(toolEvents, [], `AI turn emitted tool events: ${JSON.stringify(toolEvents)}`)
  assert.equal((await messages(page, sessionId)).some((message) => message.role === 'tool'), false, 'AI turn persisted a tool message')
  return eventTypes
}

async function hydratedSession(page: Page, sessionId: string): Promise<HydratedSessionWire> {
  const response = await browserJSON<{ session_views_by_id?: Record<string, HydratedSessionWire> }>(page, '/v3/sync/hydrate', {
    method: 'POST',
    body: {
      surface: 'desktop',
      session_ids: [sessionId],
      resources: {
        current_run_state: true,
        session_view: true,
        active_plan: true,
      },
    },
  })
  const view = response.session_views_by_id?.[sessionId]
  assert(view, `sync hydrate response missing session view for ${sessionId}: ${JSON.stringify(response)}`)
  return view
}

function activePlanState(hydrated: { active_plan?: JsonRecord }): { planId: string; status: string; checkpointStatus: string } {
  const plan = hydrated.active_plan ?? {}
  const document = (plan.document && typeof plan.document === 'object' ? plan.document : {}) as JsonRecord
  const execution = (document.execution_state && typeof document.execution_state === 'object' ? document.execution_state : {}) as JsonRecord
  const checkpoints = Array.isArray(document.checkpoints) ? document.checkpoints as JsonRecord[] : []
  return {
    planId: String(plan.id ?? ''),
    status: String(execution.status ?? ''),
    checkpointStatus: String(checkpoints[0]?.status ?? ''),
  }
}

async function waitForPausedCheckpoint(page: Page, sessionId: string): Promise<{ planId: string; status: string; checkpointStatus: string }> {
  const deadline = Date.now() + 30_000
  let last = { planId: '', status: '', checkpointStatus: '' }
  while (Date.now() < deadline) {
    last = activePlanState(await hydratedSession(page, sessionId))
    if (last.status === 'paused' && last.checkpointStatus === 'paused') return last
    await sleep(250)
  }
  throw new Error(`timed out waiting for paused checkpoint state: ${JSON.stringify(last)}`)
}

async function installFinalHandoffBoundaryFixture(page: Page, sessionId: string, marker: string): Promise<{ before: string; after: string; parent: string }> {
  const beforeHydrate = await hydratedSession(page, sessionId)
  const before = String(beforeHydrate.current_execution_epoch?.epoch_id ?? '').trim()
  assert(before, `session missing predecessor execution epoch: ${JSON.stringify(beforeHydrate)}`)

  const suffix = `${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
  const handoff = await browserJSON<{ run_intent?: { status?: string } }>(page, `/v3/sessions/${encodeURIComponent(sessionId)}/messages`, {
    method: 'POST',
    body: {
      client_request_id: `desktop-continuity-final-handoff:${suffix}`,
      message_id: `desktop-continuity-final-handoff-message:${suffix}`,
      run_id: `desktop-continuity-final-handoff-run:${suffix}`,
      role: 'system',
      content: `Final checkpoint handoff\n\n${marker}\n\nThe final boundary fixture is complete.`,
      metadata: {
        source: 'plan_execution_final_handoff',
        kind: 'plan_final_checkpoint_handoff',
        action: 'complete_checkpoint',
        checkpoint_id: 'continuity-fixture',
        next_action: 'await_review',
        final_handoff: {
          schema_version: 1,
          title: 'Continuity boundary fixture',
          overview: marker,
          details: { report: marker, result: 'done' },
        },
      },
      // A deliberately invalid dispatch authority persists the canonical
      // final-handoff packet without asking a provider to answer it.
      dispatch_authority: { runtime_swarm_id: 'continuity-fixture-no-provider-dispatch' },
    },
  })
  assert.equal(String(handoff.run_intent?.status ?? ''), 'dispatch_blocked', `handoff fixture unexpectedly dispatched a provider: ${JSON.stringify(handoff)}`)
  await page.getByTestId('desktop-v3-plan-final-handoff').waitFor({ state: 'visible', timeout: 30_000 })

  // Manual compact is the public canonical operation that seals the current
  // execution epoch and opens a successor. Because the handoff is the sealed
  // predecessor tail, provider context injects that packet into the successor.
  const compactRunId = `desktop-continuity-boundary:${suffix}`
  await browserJSON(page, `/v3/sessions/${encodeURIComponent(sessionId)}/compact`, {
    method: 'POST',
    body: {
      client_request_id: `desktop-continuity-boundary:${suffix}`,
      run_id: compactRunId,
      note: 'Create the test-owned fresh context boundary after the final handoff. Preserve the explicit markers.',
    },
  })

  const afterHydrate = await hydratedSession(page, sessionId)
  const after = String(afterHydrate.current_execution_epoch?.epoch_id ?? '').trim()
  const parent = String(afterHydrate.current_execution_epoch?.parent_epoch_id ?? '').trim()
  assert(after && after !== before, `final handoff fixture did not open a successor epoch: before=${before} after=${after}`)
  assert.equal(parent, before, `successor epoch parent=${parent}, want ${before}`)
  assert.equal(afterHydrate.has_active_plan, false, 'ordinary chat must remain available before any checkpoint is added')
  return { before, after, parent }
}

async function writeEvidence(evidenceDir: string, evidence: TestEvidence[]): Promise<void> {
  mkdirSync(evidenceDir, { recursive: true })
  writeFileSync(join(evidenceDir, 'desktop-chat-continuity-summary.json'), `${JSON.stringify({ ok: true, evidence }, null, 2)}\n`)
}

async function regularContinuityScenario(browser: Browser, appURL: string): Promise<TestEvidence> {
  const page = await browser.newPage({ viewport: { width: 1440, height: 960 } })
  try {
    await initializePage(page, appURL)
    const { sessionId, route } = await createSession(page)
    await openSession(page, appURL, route)
    const marker = uniqueMarker('regular_origin')
    const proof = `REGULAR_CONTINUITY_PROOF:${marker}`
    const baselineMessageSeq = Math.max(0, ...(await messages(page, sessionId)).map((message) => Number(message.global_seq ?? 0)))

    const acknowledgement = `REGULAR_ACK:${marker}`
    const first = await sendMessage(page, sessionId, `Remember the marker ${marker}. Reply with exactly ${acknowledgement}. Return text only; do not call tools or perform actions.`)
    const firstReply = await waitForAssistant(page, sessionId, baselineMessageSeq, (content) => content.trim() === acknowledgement, 'regular continuity acknowledgement')
    const second = await sendMessage(page, sessionId, `Prove this is the same continuing conversation by retrieving the marker from my first message. Reply with exactly ${proof}. Return text only; do not call tools or perform actions.`)
    const secondReply = await waitForAssistant(page, sessionId, Number(firstReply.global_seq ?? 0), (content) => content.trim() === proof, 'regular continuity proof')
    const eventTypes = await assertToolFreeSince(page, sessionId, Math.min(first.eventSeqBefore, second.eventSeqBefore))
    assert.equal((await hydratedSession(page, sessionId)).has_active_plan, false, 'regular conversation unexpectedly created a checkpoint')
    assert.equal(await page.getByTestId('desktop-tool-activity-card').count(), 0, 'regular continuity rendered unexpected tool activity')
    return {
      scenario: 'regular-continuity',
      sessionId,
      markers: [marker, proof],
      assistantMessages: [String(firstReply.content ?? ''), String(secondReply.content ?? '')],
      eventTypes,
    }
  } finally {
    await page.close()
  }
}

async function postHandoffContinuityScenario(browser: Browser, appURL: string): Promise<TestEvidence> {
  const page = await browser.newPage({ viewport: { width: 1440, height: 960 } })
  try {
    await initializePage(page, appURL)
    const { sessionId, route } = await createSession(page)
    await openSession(page, appURL, route)
    const origin = uniqueMarker('handoff_origin')
    const handoffMarker = uniqueMarker('final_handoff')
    const proof = `POST_HANDOFF_PROOF:${origin}:${handoffMarker}`
    const baselineMessageSeq = Math.max(0, ...(await messages(page, sessionId)).map((message) => Number(message.global_seq ?? 0)))

    const acknowledgement = `HANDOFF_ORIGIN_ACK:${origin}`
    const first = await sendMessage(page, sessionId, `Remember ${origin}. Reply with exactly ${acknowledgement}. Return text only; do not call tools or perform actions.`)
    const firstReply = await waitForAssistant(page, sessionId, baselineMessageSeq, (content) => content.trim() === acknowledgement, 'pre-handoff acknowledgement')
    const firstEventTypes = await assertToolFreeSince(page, sessionId, first.eventSeqBefore)
    const boundary = await installFinalHandoffBoundaryFixture(page, sessionId, `${handoffMarker}:${origin}`)
    const secondEventSeqBefore = await currentEventSeq(page, sessionId)
    await sendMessage(page, sessionId, `Continue normally before any checkpoint is added. Retrieve the original marker and the final-handoff marker from the handoff packet. Reply with exactly ${proof}. Return text only; do not call tools or perform actions.`)
    const secondReply = await waitForAssistant(page, sessionId, Number(firstReply.global_seq ?? 0), (content) => content.trim() === proof, 'post-handoff continuity proof')
    const secondEventTypes = await assertToolFreeSince(page, sessionId, secondEventSeqBefore)
    const eventTypes = [...firstEventTypes, ...secondEventTypes]
    assert.equal((await hydratedSession(page, sessionId)).has_active_plan, false, 'post-handoff ordinary chat unexpectedly added a checkpoint')
    assert.equal(await page.getByTestId('desktop-tool-activity-card').count(), 0, 'post-handoff continuity rendered unexpected tool activity')
    return {
      scenario: 'post-final-handoff-continuity',
      sessionId,
      markers: [origin, handoffMarker, proof],
      assistantMessages: [String(firstReply.content ?? ''), String(secondReply.content ?? '')],
      eventTypes,
      boundary,
    }
  } finally {
    await page.close()
  }
}

async function pausedRunContinuityScenario(browser: Browser, appURL: string): Promise<TestEvidence> {
  const page = await browser.newPage({ viewport: { width: 1440, height: 960 } })
  try {
    await initializePage(page, appURL)
    const { sessionId, route } = await createSession(page)
    await openSession(page, appURL, route)
    const origin = uniqueMarker('pause_origin')
    const streaming = uniqueMarker('pause_streaming')
    const resumed = uniqueMarker('pause_resumed')
    const proof = `PAUSE_CONTINUITY_PROOF:${origin}:${resumed}`
    const firstEventSeqBefore = await currentEventSeq(page, sessionId)
    const fixture = await browserJSON<{ plan_id?: string; run_intent?: { run_id?: string } }>(page, `/v3/sessions/${encodeURIComponent(sessionId)}/plan-mode/lifecycle/start-session-checkpoint`, {
      method: 'POST',
      body: {
        change_request: `Remember ${origin}. Start your text response with ${streaming}, then write a very long numbered explanation of conversation continuity so the response remains in progress until I pause it. This observation is not checkpoint completion. Return text only; do not call tools or perform actions.`,
        checkpoint_title: 'Paused continuity fixture',
        tasks: ['Keep writing text until the user pauses the run', 'Acknowledge a later continuity probe without completing the checkpoint'],
        acceptance_criteria: ['The resumed user message can prove continuity', 'The checkpoint remains in progress for a later explicit completion request'],
        notes: 'This is a live continuity fixture. The AI must only answer with text and must not complete the checkpoint.',
      },
    })
    const runId = String(fixture.run_intent?.run_id ?? '').trim()
    assert(runId, `checkpoint fixture response missing run_id: ${JSON.stringify(fixture)}`)
    await page.reload({ waitUntil: 'domcontentloaded' })
    await page.getByTestId('desktop-chat-scroller').waitFor({ state: 'visible', timeout: 30_000 })
    await waitForRunEvent(page, sessionId, runId, ['session.assistant.delta'], 'streaming assistant marker', streaming)
    const stopButton = page.getByRole('button', { name: 'Stop run' })
    await stopButton.waitFor({ state: 'visible', timeout: 30_000 })
    await stopButton.click()
    await waitForRunEvent(page, sessionId, runId, ['session.run.cancelled', 'session.run.canceled'], 'paused/cancelled run event')
    const paused = await waitForPausedCheckpoint(page, sessionId)
    assert(paused.planId, `paused checkpoint fixture missing plan_id: ${JSON.stringify(paused)}`)
    const firstEventTypes = await assertToolFreeSince(page, sessionId, firstEventSeqBefore)
    await page.getByRole('button', { name: 'Send message' }).waitFor({ state: 'visible', timeout: 30_000 })

    const beforeResumeMessages = await messages(page, sessionId)
    const beforeResumeSeq = Math.max(0, ...beforeResumeMessages.map((message) => Number(message.global_seq ?? 0)))
    const secondEventSeqBefore = await currentEventSeq(page, sessionId)
    await sendMessage(page, sessionId, `After the paused run, here is a new marker: ${resumed}. Prove you see this message and retained the original request by replying with exactly ${proof}. This is an observation inside the same still-in-progress checkpoint, not a completion request; leave the checkpoint in progress. Return text only; do not call tools or perform actions.`)
    const secondReply = await waitForAssistant(page, sessionId, beforeResumeSeq, (content) => content.trim() === proof, 'paused-run continuation proof')
    assert.equal((await messages(page, sessionId)).some((message) => message.role === 'user' && String(message.content ?? '').includes(resumed)), true, 'paused checkpoint did not persist the continuation message')
    const secondEventTypes = await assertToolFreeSince(page, sessionId, secondEventSeqBefore)
    const eventTypes = [...firstEventTypes, ...secondEventTypes]
    const resumedPlan = activePlanState(await hydratedSession(page, sessionId))
    assert.equal(resumedPlan.planId, paused.planId, 'paused message created a different checkpoint instead of continuing the same one')
    assert.equal(resumedPlan.status, 'in_progress', `paused checkpoint did not reactivate: ${JSON.stringify(resumedPlan)}`)
    assert.equal(await page.getByTestId('desktop-tool-activity-card').count(), 0, 'paused-run continuation rendered unexpected tool activity')
    return {
      scenario: 'paused-run-continuity',
      sessionId,
      markers: [origin, streaming, resumed, proof],
      assistantMessages: [String(secondReply.content ?? '')],
      eventTypes,
    }
  } finally {
    await page.close()
  }
}

test('remote desktop text-only conversation continuity', { skip: !ENABLED, timeout: Math.max(720_000, TIMEOUT_MS * 4) }, async (t) => {
  const tmpRoot = process.env.TMPDIR?.trim()
  assert(tmpRoot, 'SWARM_DESKTOP_CONTINUITY_E2E requires the run-provided TMPDIR')
  const evidenceDir = process.env.SWARM_E2E_EVIDENCE_DIR || await mkdtemp(join(tmpRoot, 'swarm-desktop-continuity-'))
  const target = await openDesktopTarget()
  let browser: Browser | null = null
  const evidence: TestEvidence[] = []
  try {
    browser = await chromium.launch({
      headless: process.env.SWARM_E2E_HEADFUL !== '1',
      executablePath: process.env.PLAYWRIGHT_BROWSER_EXECUTABLE || undefined,
    })
    const activeBrowser = browser
    await t.test('regular non-checkpoint chat recalls the first-message marker', async () => {
      evidence.push(await regularContinuityScenario(activeBrowser, target.appURL))
    })
    await t.test('chat continues across a final-handoff successor boundary before checkpoints', async () => {
      evidence.push(await postHandoffContinuityScenario(activeBrowser, target.appURL))
    })
    await t.test('a paused in-progress run accepts a new message and retains prior context', async () => {
      evidence.push(await pausedRunContinuityScenario(activeBrowser, target.appURL))
    })
    await writeEvidence(evidenceDir, evidence)
    console.log(`desktop chat continuity Playwright E2E evidence\n${JSON.stringify({ ok: true, appURL: target.appURL, ssh: DESKTOP_URL ? null : SSH_TARGET, evidenceDir, evidence }, null, 2)}`)
  } catch (error) {
    mkdirSync(evidenceDir, { recursive: true })
    writeFileSync(join(evidenceDir, 'desktop-chat-continuity-failure.json'), `${JSON.stringify({ ok: false, error: error instanceof Error ? error.message : String(error), evidence }, null, 2)}\n`)
    throw error
  } finally {
    await browser?.close().catch(() => undefined)
    await closeTunnel(target.tunnel)
  }
})
