import assert from 'node:assert/strict'
import { spawn, type ChildProcess } from 'node:child_process'
import { once } from 'node:events'
import net from 'node:net'
import test from 'node:test'

import { chromium, type Browser, type Page } from 'playwright'

const ENABLED = process.env.SWARM_DESKTOP_LAUNCH_E2E === '1'
const SSH_TARGET = (process.env.SWARM_PRIMARY_SSH || '').trim()
const DESKTOP_URL = (process.env.SWARM_DESKTOP_URL || '').replace(/\/+$/, '')
const PROVIDER = (process.env.SWARM_PROVIDER || '').trim().toLowerCase()
const WORKSPACE_SELECTOR = (process.env.SWARM_E2E_WORKSPACE || '').trim()
const REMOTE_DESKTOP_PORT = Number(process.env.SWARM_REMOTE_DESKTOP_PORT || 5555)
const TIMEOUT_MS = Number(process.env.SWARM_DESKTOP_LAUNCH_TIMEOUT_MS || 900_000)

const TERMINAL_INTENT_STATUSES = new Set(['completed', 'failed', 'cancelled', 'expired', 'interrupted'])
const FAILURE_PATTERN = /failed|cancelled|expired|interrupted/i

type JsonRecord = Record<string, unknown>
type Assignment = { provider: string; model: string; thinking: string; service_tier?: string }
type WorkspaceWire = { path?: string; workspace_name?: string; workspace_binding_id?: string }
type SessionWire = {
  id?: string
  mode?: string
  model_profile?: { action?: Assignment; plan?: Assignment }
  metadata?: JsonRecord
  worktree_enabled?: boolean
  worktree_root_path?: string
  worktree_branch?: string
}
type LaunchWire = {
  session_id?: string
  starting_mode?: string
  session?: SessionWire
  session_view?: { identity?: SessionWire }
}
type MessageWire = { role?: string; content?: string; global_seq?: number }
type EventWire = { event_type?: string; payload?: unknown }
type RunIntentWire = { run_id?: string; status?: string; checkpoint_id?: string }
type HydrateWire = {
  sessions_by_id?: Record<string, SessionWire>
  session_views_by_id?: Record<string, JsonRecord>
  messages_by_session?: Record<string, MessageWire[]>
  events_by_session?: Record<string, EventWire[]>
  run_intents_by_session?: Record<string, RunIntentWire[]>
  active_plans_by_session?: Record<string, JsonRecord>
}
type TestContext = {
  browser: Browser
  page: Page
  appURL: string
  tunnel: ChildProcess | null
  workspaceRoute: string
  authority: JsonRecord
  assignments: { action: Assignment; plan: Assignment }
  originalSettings: JsonRecord
  settingsChanged: boolean
}

type ScenarioEvidence = {
  name: string
  mode: 'auto' | 'plan'
  worktree: boolean
  providerVerified: boolean
  assistantModeVerified: boolean
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

function fail(message: string): never {
  throw new Error(message)
}

function objectPayload(value: unknown): JsonRecord {
  if (value && typeof value === 'object') return value as JsonRecord
  try { return JSON.parse(String(value || '{}')) as JsonRecord } catch { return {} }
}

function basename(value: string): string {
  const parts = value.trim().replace(/[\\/]+$/, '').split(/[\\/]/).filter(Boolean)
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

function workspaceSlug(workspaces: WorkspaceWire[], workspace: WorkspaceWire): string {
  const base = workspaceSlugBase(workspace)
  const duplicate = workspaces.filter((candidate) => workspaceSlugBase(candidate) === base).length > 1
  return duplicate ? `${base}-${pathHash(String(workspace.path ?? '')).slice(0, 6)}` : base
}

function selectWorkspace(workspaces: WorkspaceWire[]): WorkspaceWire {
  if (WORKSPACE_SELECTOR) {
    const lower = WORKSPACE_SELECTOR.toLowerCase()
    const selected = workspaces.find((candidate) => String(candidate.path ?? '').trim() === WORKSPACE_SELECTOR)
      ?? workspaces.find((candidate) => workspaceName(candidate).toLowerCase() === lower)
      ?? workspaces.find((candidate) => basename(String(candidate.path ?? '')).toLowerCase() === lower)
    assert(selected, `workspace selector did not match any saved workspace`)
    return selected
  }
  const selected = workspaces.find((candidate) => String(candidate.path ?? '').trim())
  assert(selected, 'Desktop target has no saved workspace')
  return selected
}

async function freePort(): Promise<number> {
  const server = net.createServer()
  await new Promise<void>((resolve, reject) => {
    server.once('error', reject)
    server.listen(0, '127.0.0.1', resolve)
  })
  const address = server.address()
  assert(address && typeof address === 'object')
  await new Promise<void>((resolve) => server.close(() => resolve()))
  return address.port
}

async function waitForTCP(port: number): Promise<void> {
  const deadline = Date.now() + 15_000
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
  fail('timed out waiting for the SSH Desktop tunnel')
}

async function openDesktopTarget(): Promise<{ appURL: string; tunnel: ChildProcess | null }> {
  if (DESKTOP_URL) return { appURL: DESKTOP_URL, tunnel: null }
  assert(SSH_TARGET, 'set SWARM_PRIMARY_SSH or SWARM_DESKTOP_URL')
  const port = await freePort()
  const tunnel = spawn('ssh', ['-N', '-L', `${port}:127.0.0.1:${REMOTE_DESKTOP_PORT}`, SSH_TARGET], {
    stdio: ['ignore', 'ignore', 'ignore'],
  })
  tunnel.once('exit', (code) => {
    if (code && code !== 0) console.error('[desktop-launch-e2e] SSH tunnel failed')
  })
  try {
    await waitForTCP(port)
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
  if (tunnel.exitCode === null && tunnel.signalCode === null) tunnel.kill('SIGKILL')
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
    let decoded: unknown = null
    try { decoded = text ? JSON.parse(text) : null } catch { decoded = { raw: text } }
    if (!response.ok) throw new Error(`${innerInit.method || 'GET'} ${innerRoute} HTTP ${response.status}: ${text.slice(0, 1200)}`)
    return decoded
  }, { route, init }) as T
}

function recommendationFor(records: JsonRecord[], roles: string[], label: string): Assignment {
  for (const record of records) {
    const recommendations = Array.isArray(record.recommendations) ? record.recommendations as JsonRecord[] : []
    const recommendation = recommendations.find((item) => roles.includes(String(item.role || '').trim().toLowerCase()))
    const model = String(record.model || '').trim()
    const thinking = String(recommendation?.thinking || record.default_thinking || '').trim().toLowerCase()
    if (!recommendation || !model || !thinking) continue
    const serving = String(recommendation.serving || '').trim().toLowerCase()
    return {
      provider: PROVIDER,
      model,
      thinking,
      ...(serving === 'fast' || serving === 'priority' ? { service_tier: 'priority' } : {}),
    }
  }
  fail(`provider catalog has no complete ${label} recommendation`)
}

async function hydrate(page: Page, sessionID: string): Promise<HydrateWire> {
  return await browserJSON<HydrateWire>(page, '/v3/sync/hydrate', {
    method: 'POST',
    body: {
      surface: 'desktop',
      session_ids: [sessionID],
      history: { mode: 'tail', max_messages_per_session: 200, max_events_per_session: 200, manifest_policy: 'manifest' },
      resources: {
        messages: true,
        events: true,
        run_intents: true,
        current_run_state: true,
        session_view: true,
        active_plan: true,
        plan_revisions: false,
        permission_summaries: true,
      },
      include_active: true,
    },
  })
}

async function waitForModeReply(page: Page, sessionID: string, marker: string): Promise<HydrateWire> {
  const deadline = Date.now() + TIMEOUT_MS
  let latest: HydrateWire = {}
  while (Date.now() < deadline) {
    latest = await hydrate(page, sessionID)
    const messages = latest.messages_by_session?.[sessionID] || []
    const intents = latest.run_intents_by_session?.[sessionID] || []
    const failures = intents.filter((intent) => FAILURE_PATTERN.test(String(intent.status || '')))
    assert.equal(failures.length, 0, `session run failed while waiting for ${marker}`)
    const assistant = messages.find((message) => message.role === 'assistant' && String(message.content || '').trim() === marker)
    if (assistant && intents.length > 0 && intents.every((intent) => TERMINAL_INTENT_STATUSES.has(String(intent.status || '').toLowerCase()))) return latest
    await sleep(750)
  }
  fail(`timed out waiting for AI mode verification ${marker}`)
}

function usageRecords(events: EventWire[]): JsonRecord[] {
  const records: JsonRecord[] = []
  for (const event of events) {
    const payload = objectPayload(event.payload)
    const usage = payload.turn_usage || payload.TurnUsage
    if (usage && typeof usage === 'object') records.push(usage as JsonRecord)
  }
  return records
}

async function allUsage(page: Page, sessionID: string, events: EventWire[]): Promise<JsonRecord[]> {
  const response = await browserJSON<{ turn_usage_records?: JsonRecord[] }>(page, `/v1/sessions/${encodeURIComponent(sessionID)}/usage?limit=100`)
  return [...usageRecords(events), ...(response.turn_usage_records || [])]
}

async function openNewSessionPage(context: TestContext): Promise<void> {
  await context.page.goto(`${context.appURL}${context.workspaceRoute}`, { waitUntil: 'domcontentloaded' })
  const pane = context.page.getByTestId('desktop-v3-new-session-pane')
  await pane.waitFor({ state: 'visible', timeout: 30_000 })
  const composer = pane.locator('textarea').first()
  await composer.waitFor({ state: 'visible', timeout: 30_000 })
  // A failed worktree start intentionally restores its routed draft and chips.
  // Prime the public bare /new form before every independent scenario so no
  // prior failure or mode selection can leak into the next launch assertion.
  await composer.fill('/new')
  await composer.press('Enter')
  await assert.doesNotReject(async () => {
    await composer.waitFor({ state: 'visible', timeout: 5_000 })
  })
}

async function submitSlash(context: TestContext, command: string, endpoint: '/v3/sessions:routed' | '/v3/sessions:background-router'): Promise<LaunchWire> {
  await openNewSessionPage(context)
  const responsePromise = context.page.waitForResponse((response) => {
    if (response.request().method() !== 'POST') return false
    try { return new URL(response.url()).pathname === endpoint } catch { return false }
  }, { timeout: 30_000 })
  const composer = context.page.getByTestId('desktop-v3-new-session-pane').locator('textarea').first()
  await composer.fill(command)
  await composer.press('Enter')
  const response = await responsePromise
  const responseText = await response.text()
  assert(response.ok(), `${endpoint} returned HTTP ${response.status()}: ${responseText.slice(0, 1200)}`)
  const launched = JSON.parse(responseText) as LaunchWire
  assert(launched.session_id, `${endpoint} returned no session_id`)
  if (endpoint === '/v3/sessions:routed') {
    await context.page.waitForURL((url) => url.pathname.endsWith(`/${launched.session_id}`), { timeout: 30_000 })
    await context.page.getByTestId('desktop-chat-scroller').waitFor({ state: 'visible', timeout: 30_000 })
  }
  return launched
}

function modePrompt(mode: 'auto' | 'plan', marker: string): string {
  return `Read only your injected runtime session mode. If it is ${mode}, reply exactly ${marker}; otherwise reply exactly MODE_MISMATCH. Return text only. Do not call tools, create a plan, or inspect files.`
}

async function verifySimpleLaunch(
  context: TestContext,
  name: string,
  commandPrefix: string,
  mode: 'auto' | 'plan',
  worktree: boolean,
): Promise<ScenarioEvidence> {
  const marker = `MODE_${mode.toUpperCase()}_${name.toUpperCase().replace(/[^A-Z0-9]+/g, '_')}_OK`
  const launched = await submitSlash(context, `${commandPrefix} ${modePrompt(mode, marker)}`, '/v3/sessions:routed')
  const sessionID = String(launched.session_id)
  assert.equal(launched.starting_mode, mode, `${name} started in the wrong mode`)
  const immediate = launched.session || {}
  assert.equal(Boolean(immediate.worktree_enabled), worktree, `${name} worktree intent was not preserved`)
  if (worktree) {
    assert(String(immediate.worktree_root_path || '').trim(), `${name} has no worktree root`)
    assert(String(immediate.worktree_branch || '').trim(), `${name} has no worktree branch`)
  }
  const settled = await waitForModeReply(context.page, sessionID, marker)
  const session = settled.sessions_by_id?.[sessionID] || immediate
  assert.equal(session.mode, mode, `${name} settled in the wrong mode`)
  assert.equal(Boolean(session.worktree_enabled), worktree, `${name} settled with the wrong worktree state`)
  const profile = mode === 'plan' ? session.model_profile?.plan : session.model_profile?.action
  const expected = mode === 'plan' ? context.assignments.plan : context.assignments.action
  assert.equal(profile?.provider, PROVIDER, `${name} used the wrong provider profile`)
  assert.equal(profile?.model, expected.model, `${name} used the wrong model profile`)
  const events = settled.events_by_session?.[sessionID] || []
  assert.equal(events.some((event) => FAILURE_PATTERN.test(String(event.event_type || ''))), false, `${name} emitted a failure event`)
  const usage = await allUsage(context.page, sessionID, events)
  assert(usage.some((record) => record.provider === PROVIDER && record.model === expected.model), `${name} has no matching runtime usage evidence`)
  const view = settled.session_views_by_id?.[sessionID] || {}
  assert.equal(Boolean(view.has_active_plan || view.active_plan), false, `${name} unexpectedly created a plan`)
  return { name, mode, worktree, providerVerified: true, assistantModeVerified: true }
}

async function verifyTaskLaunch(context: TestContext, mode: 'auto' | 'plan'): Promise<ScenarioEvidence> {
  const name = mode === 'plan' ? 'task-plan' : 'task'
  const marker = `MODE_${mode.toUpperCase()}_${name.toUpperCase().replace('-', '_')}_OK`
  const prefix = mode === 'plan' ? '/task plan' : '/task'
  const launched = await submitSlash(context, `${prefix} ${modePrompt(mode, marker)}`, '/v3/sessions:background-router')
  const sessionID = String(launched.session_id)
  assert.equal(launched.starting_mode, mode, `${name} started in the wrong mode`)
  const immediate = launched.session || {}
  assert.equal(immediate.worktree_enabled, true, `${name} did not create a managed worktree`)
  const settled = await waitForModeReply(context.page, sessionID, marker)
  const hydratedSession = settled.sessions_by_id?.[sessionID] || {}
  const session = {
    ...immediate,
    ...hydratedSession,
    metadata: { ...(immediate.metadata || {}), ...(hydratedSession.metadata || {}) },
    model_profile: hydratedSession.model_profile || immediate.model_profile,
  }
  assert.equal(session.mode, mode, `${name} settled in the wrong mode`)
  assert.equal(session.worktree_enabled, true, `${name} settled without a managed worktree`)
  assert.equal(session.metadata?.background_router_session, true, `${name} lacks background Router metadata`)
  assert.equal(session.metadata?.plan_mode_requested, mode === 'plan', `${name} persisted the wrong mode intent`)
  const profile = mode === 'plan' ? session.model_profile?.plan : session.model_profile?.action
  const expected = mode === 'plan' ? context.assignments.plan : context.assignments.action
  assert.equal(profile?.provider, PROVIDER, `${name} used the wrong provider profile`)
  assert.equal(profile?.model, expected.model, `${name} used the wrong model profile`)
  const events = settled.events_by_session?.[sessionID] || []
  const usage = await allUsage(context.page, sessionID, events)
  assert(usage.some((record) => record.provider === PROVIDER && record.model === expected.model), `${name} has no matching runtime usage evidence`)
  return { name, mode, worktree: true, providerVerified: true, assistantModeVerified: true }
}

function parsePermissionArguments(permission: JsonRecord): JsonRecord {
  const raw = permission.tool_arguments ?? permission.arguments ?? permission.payload ?? {}
  return objectPayload(raw)
}

async function waitForExitPlanPermission(page: Page, sessionID: string): Promise<JsonRecord> {
  const deadline = Date.now() + TIMEOUT_MS
  while (Date.now() < deadline) {
    const response = await browserJSON<{ permissions?: JsonRecord[] }>(page, `/v3/sessions/${encodeURIComponent(sessionID)}/permissions?status=pending&limit=50`)
    const permission = (response.permissions || []).find((item) => item.tool_name === 'exit_plan_mode')
    if (permission?.id) return permission
    await sleep(750)
  }
  fail('timed out waiting for the Plan agent to request exit_plan_mode')
}

async function waitForSessionMode(page: Page, sessionID: string, mode: string): Promise<void> {
  const deadline = Date.now() + TIMEOUT_MS
  while (Date.now() < deadline) {
    const response = await browserJSON<{ session?: SessionWire }>(page, `/v3/sessions/${encodeURIComponent(sessionID)}`)
    if (response.session?.mode === mode) return
    await sleep(750)
  }
  fail(`timed out waiting for session mode ${mode}`)
}

async function waitForCompletedTwoCheckpointPlan(page: Page, sessionID: string): Promise<{ plan: JsonRecord; snapshot: HydrateWire }> {
  const deadline = Date.now() + TIMEOUT_MS
  let latest: HydrateWire = {}
  while (Date.now() < deadline) {
    latest = await hydrate(page, sessionID)
    const plan = latest.active_plans_by_session?.[sessionID]
      ?? (latest.session_views_by_id?.[sessionID]?.active_plan as JsonRecord | undefined)
      ?? {}
    const document = objectPayload(plan.document)
    const checkpoints = Array.isArray(document.checkpoints) ? document.checkpoints as JsonRecord[] : []
    const intents = latest.run_intents_by_session?.[sessionID] || []
    const failed = intents.filter((intent) => FAILURE_PATTERN.test(String(intent.status || '')))
    assert.equal(failed.length, 0, 'two-checkpoint lifecycle contains a failed run')
    const checkpointIntents = intents.filter((intent) => String(intent.checkpoint_id || '').trim())
    if (checkpoints.length === 2
      && checkpoints.every((checkpoint) => checkpoint.status === 'completed')
      && checkpointIntents.length === 2
      && checkpointIntents.every((intent) => intent.status === 'completed')) {
      return { plan, snapshot: latest }
    }
    await sleep(1_000)
  }
  fail('timed out waiting for automatic completion of two checkpoints')
}

async function verifyPlanLifecycle(context: TestContext): Promise<ScenarioEvidence> {
  const planMarker = 'PLAN_MODE_VERIFIED_CANONICAL'
  const cp1Marker = 'AUTO_MODE_VERIFIED_CP1'
  const cp2Marker = 'AUTO_MODE_VERIFIED_CP2'
  const prompt = [
    'Read your injected runtime context and proceed only if the current session mode is plan.',
    `Create a structured plan titled ${planMarker} with exactly two ordered checkpoints, cp-1 and cp-2.`,
    'Each checkpoint must contain exactly one task.',
    `Checkpoint cp-1 must complete with result ${cp1Marker}.`,
    `Checkpoint cp-2 must complete with result ${cp2Marker}.`,
    'Use automatic checkpoint execution and submit the complete plan now with exit_plan_mode.',
    'Do not inspect or modify files; this verifies only Desktop plan lifecycle and automatic Plan-to-Auto model switching.',
  ].join(' ')
  const launched = await submitSlash(context, `/new plan ${prompt}`, '/v3/sessions:routed')
  const sessionID = String(launched.session_id)
  assert.equal(launched.starting_mode, 'plan')
  assert.equal(launched.session?.model_profile?.plan?.model, context.assignments.plan.model, 'Plan agent profile did not start with the recommended Plan model')
  assert.equal(launched.session?.model_profile?.action?.model, context.assignments.action.model, 'Auto agent profile was not snapshotted for checkpoint execution')

  const permission = await waitForExitPlanPermission(context.page, sessionID)
  const proposed = parsePermissionArguments(permission)
  const document = objectPayload(proposed.document ?? objectPayload(proposed.approved_arguments).document)
  const checkpoints = Array.isArray(document.checkpoints) ? document.checkpoints as JsonRecord[] : []
  assert.equal(checkpoints.length, 2, 'Plan agent did not propose exactly two checkpoints')
  assert(checkpoints.every((checkpoint) => Array.isArray(checkpoint.tasks) && checkpoint.tasks.length === 1), 'each proposed checkpoint must have exactly one task')
  assert(JSON.stringify(document).includes(planMarker), 'Plan agent did not preserve its Plan-mode verification marker')

  const resolveResult = await context.page.evaluate(async ({ sessionID: innerSessionID, permissionID }) => {
    const response = await fetch(`/v3/sessions/${encodeURIComponent(innerSessionID)}/permissions/${encodeURIComponent(permissionID)}/resolve`, {
      credentials: 'include',
      method: 'POST',
      headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
      body: JSON.stringify({
        action: 'allow_once',
        reason: 'Canonical Desktop launch suite automatic checkpoint acceptance',
        approved_arguments: {
          execution_granularity: 'checkpointed',
          continuation_policy: 'automatic',
          continue_automatically: true,
        },
      }),
    })
    return { ok: response.ok, status: response.status, text: await response.text() }
  }, { sessionID, permissionID: String(permission.id) })

  if (!resolveResult.ok) {
    // Approval is committed before its best-effort V3 notification mutation. A
    // late notification may report that the now-completed Plan run is no longer
    // mutable even though approval and automatic execution already succeeded.
    const resolvedPermissions = await browserJSON<{ permissions?: JsonRecord[] }>(context.page, `/v3/sessions/${encodeURIComponent(sessionID)}/permissions?status=all&limit=50`)
    const resolved = (resolvedPermissions.permissions || []).find((item) => item.id === permission.id)
    assert.equal(resolved?.status, 'approved', `exit_plan_mode resolve failed without committing approval: HTTP ${resolveResult.status}: ${resolveResult.text.slice(0, 1200)}`)
  }

  await waitForSessionMode(context.page, sessionID, 'auto')
  const completed = await waitForCompletedTwoCheckpointPlan(context.page, sessionID)
  const completedDocument = objectPayload(completed.plan.document)
  const completedText = JSON.stringify(completedDocument)
  assert(completedText.includes(cp1Marker), 'checkpoint cp-1 did not preserve its Auto-mode completion marker')
  assert(completedText.includes(cp2Marker), 'checkpoint cp-2 did not preserve its Auto-mode completion marker')
  const events = completed.snapshot.events_by_session?.[sessionID] || []
  assert.equal(events.some((event) => FAILURE_PATTERN.test(String(event.event_type || ''))), false, 'plan lifecycle emitted a failure event')
  const usage = await allUsage(context.page, sessionID, events)
  assert(usage.some((record) => record.provider === PROVIDER && record.model === context.assignments.plan.model), 'no Plan-agent runtime usage was recorded')
  const checkpointRunIDs = new Set((completed.snapshot.run_intents_by_session?.[sessionID] || [])
    .filter((intent) => intent.checkpoint_id)
    .map((intent) => String(intent.run_id || '')))
  const autoUsage = usage.filter((record) => record.provider === PROVIDER
    && record.model === context.assignments.action.model
    && [...checkpointRunIDs].some((runID) => String(record.run_id || '') === runID || String(record.run_id || '').startsWith(`${runID}/`)))
  assert(autoUsage.length >= 2, `expected Auto-agent usage for both checkpoints, found ${autoUsage.length}`)
  const pending = await browserJSON<{ permissions?: JsonRecord[] }>(context.page, `/v3/sessions/${encodeURIComponent(sessionID)}/permissions?status=pending&limit=50`)
  assert.equal((pending.permissions || []).length, 0, 'automatic checkpoint execution left a pending permission')
  return { name: 'two-checkpoint-plan-auto', mode: 'plan', worktree: false, providerVerified: true, assistantModeVerified: true }
}

async function setup(): Promise<TestContext> {
  assert(PROVIDER && /^[a-z0-9._-]+$/.test(PROVIDER), 'SWARM_PROVIDER is required and must be a provider id')
  assert(Number.isFinite(TIMEOUT_MS) && TIMEOUT_MS >= 30_000, 'SWARM_DESKTOP_LAUNCH_TIMEOUT_MS must be at least 30000')
  const target = await openDesktopTarget()
  const browser = await chromium.launch({
    headless: process.env.SWARM_E2E_HEADFUL !== '1',
    executablePath: process.env.PLAYWRIGHT_BROWSER_EXECUTABLE || undefined,
  })
  const page = await browser.newPage({ viewport: { width: 1440, height: 960 } })
  await page.goto(target.appURL, { waitUntil: 'domcontentloaded' })
  await browserJSON(page, '/v1/auth/desktop/session')

  const providers = await browserJSON<{ providers?: JsonRecord[] }>(page, '/v1/providers')
  const provider = (providers.providers || []).find((item) => String(item.id || '').trim().toLowerCase() === PROVIDER)
  assert(provider, 'requested provider is not registered')
  assert.notEqual(provider.runnable, false, `requested provider is not runnable: ${String(provider.message || '')}`)
  const catalog = await browserJSON<{ records?: JsonRecord[] }>(page, `/v1/model/catalog?provider=${encodeURIComponent(PROVIDER)}&limit=500`)
  const records = catalog.records || []
  const assignments = {
    plan: recommendationFor(records, ['plan'], 'Plan model'),
    action: recommendationFor(records, ['auto', 'main'], 'Auto model'),
  }
  const settings = await browserJSON<{ agent_model_settings?: Record<string, JsonRecord> }>(page, '/v1/agent-model-settings')
  const originalSettings = settings.agent_model_settings?.swarm
  assert(originalSettings, 'canonical Swarm agent model settings are missing')
  await browserJSON(page, '/v1/agent-model-settings', {
    method: 'PATCH',
    body: { swarm: { action: assignments.action, plan: assignments.plan } },
  })

  const [overview, topology] = await Promise.all([
    browserJSON<{ workspaces?: WorkspaceWire[] }>(page, '/v1/workspace/overview?workspace_limit=500&discover_limit=500'),
    browserJSON<{ runtimes?: JsonRecord[]; workspace_bindings?: JsonRecord[] }>(page, '/v1/swarm/topology'),
  ])
  const workspaces = overview.workspaces || []
  const workspace = selectWorkspace(workspaces)
  const workspacePath = String(workspace.path || '').trim()
  const runtime = (topology.runtimes || []).find((item) => item.relationship === 'self') || (topology.runtimes || [])[0]
  const binding = (topology.workspace_bindings || []).find((item) => item.source_workspace_path === workspacePath || item.destination_workspace_path === workspacePath)
    || (topology.workspace_bindings || []).find((item) => item.state === 'bound')
    || (topology.workspace_bindings || [])[0]
  assert(runtime?.swarm_id && binding?.workspace_binding_id, 'selected workspace has no runnable topology authority')
  const authority = {
    workspace_path: workspacePath,
    workspace_name: workspaceName(workspace),
    host_workspace_path: workspacePath,
    runtime_workspace_path: String(binding.destination_workspace_path || workspacePath),
    workspace_binding_id: binding.workspace_binding_id,
    swarm_id: runtime.swarm_id,
    target_kind: 'host',
    target_relationship: 'self',
  }
  return {
    browser,
    page,
    appURL: target.appURL,
    tunnel: target.tunnel,
    workspaceRoute: `/${encodeURIComponent(workspaceSlug(workspaces, workspace))}`,
    authority,
    assignments,
    originalSettings,
    settingsChanged: true,
  }
}

async function teardown(context: TestContext | null): Promise<void> {
  if (!context) return
  if (context.settingsChanged) {
    await browserJSON(context.page, '/v1/agent-model-settings', {
      method: 'PATCH',
      body: { swarm: context.originalSettings },
    }).catch((error) => console.error(`[desktop-launch-e2e] failed to restore model settings: ${String(error)}`))
  }
  await context.browser.close().catch(() => undefined)
  await closeTunnel(context.tunnel)
}

test('canonical remote Desktop launch suite', { skip: !ENABLED, timeout: Math.max(1_800_000, TIMEOUT_MS * 10) }, async (t) => {
  let context: TestContext | null = null
  const evidence: ScenarioEvidence[] = []
  try {
    context = await setup()
    const active = context
    await t.test('/new prompt starts a plain Auto session', async () => {
      evidence.push(await verifySimpleLaunch(active, 'new', '/new', 'auto', false))
    })
    await t.test('/new plan prompt starts a Plan session', async () => {
      evidence.push(await verifySimpleLaunch(active, 'new-plan', '/new plan', 'plan', false))
    })
    await t.test('/new worktree prompt starts a managed-worktree Auto session', async () => {
      evidence.push(await verifySimpleLaunch(active, 'new-worktree', '/new worktree', 'auto', true))
    })
    await t.test('/new wp prompt starts a managed-worktree Plan session', async () => {
      evidence.push(await verifySimpleLaunch(active, 'new-wp', '/new wp', 'plan', true))
    })
    await t.test('/task starts and completes an Auto Router worktree session', async () => {
      evidence.push(await verifyTaskLaunch(active, 'auto'))
    })
    await t.test('/task plan starts and completes a Plan Router worktree session', async () => {
      evidence.push(await verifyTaskLaunch(active, 'plan'))
    })
    await t.test('Plan exits to Auto and completes two checkpoints without another approval', async () => {
      evidence.push(await verifyPlanLifecycle(active))
    })
    assert.equal(evidence.length, 7)
    assert(evidence.every((item) => item.providerVerified && item.assistantModeVerified))
    console.log(`canonical Desktop launch suite PASS\n${JSON.stringify({
      result: 'PASS',
      provider: PROVIDER,
      scenarios: evidence,
      model_roles: {
        plan: context.assignments.plan.model,
        auto: context.assignments.action.model,
      },
    }, null, 2)}`)
  } finally {
    await teardown(context)
  }
})
