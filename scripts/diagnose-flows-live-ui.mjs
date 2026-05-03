#!/usr/bin/env node
import fs from 'node:fs/promises'
import { createRequire } from 'node:module'
import os from 'node:os'
import path from 'node:path'
import { performance } from 'node:perf_hooks'
import { fileURLToPath } from 'node:url'

const SCRIPT_DIR = path.dirname(fileURLToPath(import.meta.url))
const ROOT_DIR = path.resolve(SCRIPT_DIR, '..')
const WEB_PACKAGE_JSON = path.join(ROOT_DIR, 'web', 'package.json')

const selectors = {
  page: '[data-testid="flows-settings-page"]',
  addOpen: '[data-testid="flows-add-open"]',
  addModal: '[data-testid="flows-add-modal"]',
  addName: '[data-testid="flows-add-name"]',
  addAgent: '[data-testid="flows-add-agent"]',
  addTarget: '[data-testid="flows-add-target"]',
  addWorkspace: '[data-testid="flows-add-workspace"]',
  addCadence: '[data-testid="flows-add-cadence"]',
  addTask: '[data-testid="flows-add-task"]',
  addSubmit: '[data-testid="flows-add-submit"]',
  detail: '[data-testid="flows-detail"]',
  runNow: '[data-testid="flows-detail-run-now"]',
  recentRuns: '[data-testid="flows-recent-runs"]',
  desktopSidebar: '[data-testid="desktop-workspace-sidebar"], aside',
  table: '[data-testid="flows-table"]',
  row: '[data-testid="flows-row"]',
  error: '[data-testid="flows-error"]',
  vaultPassword: '[data-testid="desktop-vault-password"]',
  vaultUnlock: '[data-testid="desktop-vault-unlock"]',
}

function usage() {
  console.log(`Usage: node scripts/diagnose-flows-live-ui.mjs [options]

Live Flows smoke harness. It drives the real desktop UI with Playwright, uses
same-origin browser fetches for backend assertions, and writes a diagnostic
summary that shows exactly which phase failed. It targets the canonical `/v3/flows`
API only.

Default phase:
  host                         UI create local/self Flow, Run now, verify session/history, then API scheduled smoke.

Target phases:
  --phase host                 Local controller/self target via UI + API checks.
  --phase container            Local container child target via API checks; requires an online /v1/swarm/targets kind=local child.
  --phase ssh                  SSH/remote child target via API checks; requires an online /v1/swarm/targets kind=remote child.
  --phase target               Generic API target smoke; provide --target-kind/--target-name/--target-swarm-id.

Live instance:
  --url <url>                  Desktop URL. Default: SWARM_DESKTOP_URL or http://127.0.0.1:<desktop_port>.
  --config <path>              swarm.conf for desktop_port discovery. Default: XDG config.

Flow settings:
  --agent <name>               Saved agent profile name on target. Default: memory.
  --agent-kind <kind>          Saved profile mode (primary, subagent, background). Default: background.
  --workspace <path|.>         Workspace sent to Flow create. Default: .
  --target-kind <kind>         self, local, remote, or another target kind. Default depends on phase.
  --target-name <name>         Match target display name for API target phases.
  --target-swarm-id <id>       Match exact target swarm_id for API target phases.
  --target-deployment-id <id>  Match exact deployment_id for API target phases.
  --prompt <text>              Prompt used by run-now smoke.
  --schedule-prompt <text>     Prompt used by scheduled smoke.
  --schedule-delay-minutes <n> Schedule API smoke at the next UTC HH:MM after this delay. Default: 1.
  --no-run-now                 Skip run-now execution.
  --no-schedule                Skip scheduled execution.
  --keep-flows                 Do not delete created diagnostic flows.

Browser/artifacts:
  --artifact-dir <path>        Default: tmp/flows-smoke-diagnostics/<timestamp>.
  --browser-executable <path>  Use a system browser executable.
  --desktop-vault-password-env <env>
                               Unlock the live desktop vault first when needed.
  --headful                    Show browser window.
  --timeout-ms <ms>            Overall timeout. Default: 300000.
  --run-timeout-ms <ms>        Run-now poll timeout. Default: 180000.
  --schedule-timeout-ms <ms>   Scheduled poll timeout. Default: 180000.
  --help                       Show this help.

Time granularity:
  Backend schedules accept HH:MM (minute granularity). The target scheduler ticks every ~15s.
  The current Add Flow UI exposes 30-minute time options, so this harness uses the API for near-term scheduled smoke.`)
}

function fail(message) {
  throw new Error(message)
}

function requireValue(args, index, flag) {
  const value = args[index + 1]
  if (!value || value.startsWith('--')) {
    fail(`${flag} requires a value`)
  }
  return value
}

function parseNumber(value, flag) {
  const parsed = Number(value)
  if (!Number.isFinite(parsed) || parsed <= 0) {
    fail(`${flag} must be a positive number`)
  }
  return parsed
}

function parseArgs(argv) {
  const opts = {
    help: false,
    phase: process.env.SWARM_FLOW_SMOKE_PHASE || 'host',
    url: process.env.SWARM_DESKTOP_URL || '',
    configPath: process.env.SWARM_CONFIG || '',
    agent: process.env.SWARM_FLOW_AGENT || 'memory',
    agentKind: process.env.SWARM_FLOW_AGENT_KIND || 'background',
    workspace: process.env.SWARM_FLOW_WORKSPACE || '.',
    targetKind: process.env.SWARM_FLOW_TARGET_KIND || '',
    targetName: process.env.SWARM_FLOW_TARGET_NAME || '',
    targetSwarmID: process.env.SWARM_FLOW_TARGET_SWARM_ID || '',
    targetDeploymentID: process.env.SWARM_FLOW_TARGET_DEPLOYMENT_ID || '',
    prompt: process.env.SWARM_FLOW_PROMPT || 'Flow smoke: reply with exactly "flow smoke ok". Do not modify files.',
    schedulePrompt: process.env.SWARM_FLOW_SCHEDULE_PROMPT || 'Scheduled Flow smoke: reply with exactly "scheduled flow smoke ok". Do not modify files.',
    scheduleDelayMinutes: Number(process.env.SWARM_FLOW_SCHEDULE_DELAY_MINUTES || '') || 1,
    runNow: true,
    schedule: true,
    keepFlows: false,
    artifactDir: '',
    browserExecutable: process.env.PLAYWRIGHT_BROWSER_EXECUTABLE || '',
    desktopVaultPasswordEnv: process.env.SWARM_DESKTOP_VAULT_PASSWORD_ENV || '',
    headless: true,
    timeoutMs: Number(process.env.SWARM_FLOW_TIMEOUT_MS || '') || 300000,
    runTimeoutMs: Number(process.env.SWARM_FLOW_RUN_TIMEOUT_MS || '') || 180000,
    scheduleTimeoutMs: Number(process.env.SWARM_FLOW_SCHEDULE_TIMEOUT_MS || '') || 180000,
  }

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index]
    switch (arg) {
      case '--help':
      case '-h':
        opts.help = true
        break
      case '--phase':
        opts.phase = requireValue(argv, index, arg)
        index += 1
        break
      case '--url':
        opts.url = requireValue(argv, index, arg)
        index += 1
        break
      case '--config':
        opts.configPath = requireValue(argv, index, arg)
        index += 1
        break
      case '--agent':
        opts.agent = requireValue(argv, index, arg)
        index += 1
        break
      case '--agent-kind':
        opts.agentKind = requireValue(argv, index, arg)
        index += 1
        break
      case '--workspace':
        opts.workspace = requireValue(argv, index, arg)
        index += 1
        break
      case '--target-kind':
        opts.targetKind = requireValue(argv, index, arg)
        index += 1
        break
      case '--target-name':
        opts.targetName = requireValue(argv, index, arg)
        index += 1
        break
      case '--target-swarm-id':
        opts.targetSwarmID = requireValue(argv, index, arg)
        index += 1
        break
      case '--target-deployment-id':
        opts.targetDeploymentID = requireValue(argv, index, arg)
        index += 1
        break
      case '--prompt':
        opts.prompt = requireValue(argv, index, arg)
        index += 1
        break
      case '--schedule-prompt':
        opts.schedulePrompt = requireValue(argv, index, arg)
        index += 1
        break
      case '--schedule-delay-minutes':
        opts.scheduleDelayMinutes = parseNumber(requireValue(argv, index, arg), arg)
        index += 1
        break
      case '--no-run-now':
        opts.runNow = false
        break
      case '--no-schedule':
        opts.schedule = false
        break
      case '--keep-flows':
        opts.keepFlows = true
        break
      case '--artifact-dir':
        opts.artifactDir = requireValue(argv, index, arg)
        index += 1
        break
      case '--browser-executable':
        opts.browserExecutable = requireValue(argv, index, arg)
        index += 1
        break
      case '--desktop-vault-password-env':
        opts.desktopVaultPasswordEnv = requireValue(argv, index, arg)
        index += 1
        break
      case '--headful':
        opts.headless = false
        break
      case '--timeout-ms':
        opts.timeoutMs = parseNumber(requireValue(argv, index, arg), arg)
        index += 1
        break
      case '--run-timeout-ms':
        opts.runTimeoutMs = parseNumber(requireValue(argv, index, arg), arg)
        index += 1
        break
      case '--schedule-timeout-ms':
        opts.scheduleTimeoutMs = parseNumber(requireValue(argv, index, arg), arg)
        index += 1
        break
      default:
        fail(`unknown argument: ${arg}`)
    }
  }

  opts.phase = String(opts.phase || '').trim().toLowerCase()
  if (!['host', 'container', 'ssh', 'target'].includes(opts.phase)) {
    fail('--phase must be host, container, ssh, or target')
  }
  opts.agent = String(opts.agent || '').trim() || 'memory'
  opts.agentKind = normalizeAgentKind(opts.agentKind)
  opts.workspace = String(opts.workspace || '').trim() || '.'
  opts.targetKind = String(opts.targetKind || '').trim().toLowerCase()
  opts.targetName = String(opts.targetName || '').trim()
  opts.targetSwarmID = String(opts.targetSwarmID || '').trim()
  opts.targetDeploymentID = String(opts.targetDeploymentID || '').trim()
  if (opts.scheduleDelayMinutes < 1) {
    opts.scheduleDelayMinutes = 1
  }
  return opts
}

function normalizeAgentKind(value) {
  const normalized = String(value || '').trim().toLowerCase()
  if (normalized === 'primary') {
    return 'agent'
  }
  if (['agent', 'subagent', 'background'].includes(normalized)) {
    return normalized
  }
  fail('--agent-kind must be agent/primary, subagent, or background')
}

function timestamp() {
  return new Date().toISOString().replace(/[-:]/g, '').replace(/\.\d{3}Z$/, 'Z')
}

function defaultArtifactDir() {
  return path.join(ROOT_DIR, 'tmp', 'flows-smoke-diagnostics', timestamp())
}

function defaultConfigPath() {
  const configHome = process.env.XDG_CONFIG_HOME || path.join(os.homedir(), '.config')
  return path.join(configHome, 'swarm', 'swarm.conf')
}

function parseConfig(text) {
  const out = new Map()
  for (const rawLine of text.split(/\r?\n/)) {
    const trimmed = rawLine.trim()
    if (!trimmed || trimmed.startsWith('#')) {
      continue
    }
    const equals = trimmed.indexOf('=')
    if (equals < 0) {
      continue
    }
    const key = trimmed.slice(0, equals).trim()
    const value = trimmed.slice(equals + 1).replace(/[ \t]+#.*$/, '').trim()
    if (key) {
      out.set(key, value)
    }
  }
  return out
}

async function resolveDesktopURL(opts) {
  if (opts.url.trim()) {
    return opts.url.trim().replace(/\/+$/, '')
  }
  const configPath = opts.configPath.trim() || defaultConfigPath()
  let desktopPort = '5555'
  try {
    const config = parseConfig(await fs.readFile(configPath, 'utf8'))
    desktopPort = config.get('desktop_port') || desktopPort
  } catch {
    // Missing config is allowed; the default URL remains explicit.
  }
  return `http://127.0.0.1:${desktopPort}`
}

function loadPlaywright() {
  try {
    const requireFromWeb = createRequire(WEB_PACKAGE_JSON)
    return requireFromWeb('playwright')
  } catch (error) {
    fail(`Playwright is not installed for the web package: ${error instanceof Error ? error.message : String(error)}`)
  }
}

function requestPath(url) {
  try {
    const parsed = new URL(url)
    return `${parsed.pathname}${parsed.search}`
  } catch {
    return url
  }
}

function isInterestingURL(url) {
  const pathAndQuery = requestPath(url)
  return pathAndQuery.startsWith('/v3/flows')
    || pathAndQuery.startsWith('/v1/sessions')
    || pathAndQuery.startsWith('/v1/swarm/targets')
    || pathAndQuery.startsWith('/v1/auth/desktop/session')
    || pathAndQuery === '/readyz'
    || pathAndQuery === '/healthz'
}

function createRecorder(summary) {
  return {
    start(name, details = {}) {
      const started = performance.now()
      console.log(`STEP ${name} start`)
      return (extra = {}) => {
        const duration = Math.round(performance.now() - started)
        summary.timings_ms[name] = duration
        summary.steps.push({ name, duration_ms: duration, ...details, ...extra })
        console.log(`TIMING ${name}=${duration}ms`)
      }
    },
  }
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

async function writeJSON(filePath, value) {
  await fs.writeFile(filePath, `${JSON.stringify(value, null, 2)}\n`, 'utf8')
}

async function maybeScreenshot(page, artifactDir, name, summary) {
  const filePath = path.join(artifactDir, `${name}.png`)
  try {
    await page.screenshot({ path: filePath, fullPage: true })
    summary.artifacts.push(path.relative(ROOT_DIR, filePath))
  } catch (error) {
    summary.notes.push(`screenshot ${name} failed: ${error instanceof Error ? error.message : String(error)}`)
  }
}

function compactPayload(payload) {
  if (!payload || typeof payload !== 'object') {
    return payload ?? null
  }
  const out = {}
  for (const [key, value] of Object.entries(payload)) {
    if (key === 'flow') {
      out.flow = summarizeFlowDetail(value)
    } else if (key === 'flows' && Array.isArray(value)) {
      out.flows = value.map(summarizeFlowSummary)
      out.flow_count = value.length
    } else if (key === 'history' && Array.isArray(value)) {
      out.history = value.map(summarizeRun)
      out.history_count = value.length
    } else if (key === 'sessions' && Array.isArray(value)) {
      out.sessions = value.slice(0, 20).map(summarizeSession)
      out.session_count = value.length
    } else if (key === 'targets' && Array.isArray(value)) {
      out.targets = value.map(summarizeTarget)
      out.target_count = value.length
    } else if (key === 'result') {
      out.result = summarizeDeliverResult(value)
    } else if (key === 'run') {
      out.run = value
    } else if (key !== 'ok') {
      out[key] = value
    }
  }
  if ('ok' in payload) {
    out.ok = Boolean(payload.ok)
  }
  return out
}

function summarizeFlowDetail(value) {
  if (!value || typeof value !== 'object') {
    return null
  }
  return {
    definition: summarizeDefinition(value.definition),
    target_detail: summarizeTarget(value.target_detail),
    workspace_detail: summarizeWorkspace(value.workspace_detail),
    agent_detail: summarizeAgentDetail(value.agent_detail),
    assignment_statuses: Array.isArray(value.assignment_statuses) ? value.assignment_statuses.map(summarizeAssignmentStatus) : [],
    outbox: Array.isArray(value.outbox) ? value.outbox.map(summarizeOutbox) : [],
    history: Array.isArray(value.history) ? value.history.map(summarizeRun) : [],
    history_count: Number(value.history_count || 0),
    last_run: summarizeRun(value.last_run),
  }
}

function summarizeFlowSummary(value) {
  if (!value || typeof value !== 'object') {
    return null
  }
  return {
    definition: summarizeDefinition(value.definition),
    target_detail: summarizeTarget(value.target_detail),
    workspace_detail: summarizeWorkspace(value.workspace_detail),
    agent_detail: summarizeAgentDetail(value.agent_detail),
    assignment_statuses: Array.isArray(value.assignment_statuses) ? value.assignment_statuses.map(summarizeAssignmentStatus) : [],
    last_run: summarizeRun(value.last_run),
    history_count: Number(value.history_count || 0),
  }
}

function summarizeDefinition(value) {
  if (!value || typeof value !== 'object') {
    return null
  }
  return {
    flow_id: String(value.flow_id || ''),
    revision: Number(value.revision || 0),
    name: String(value.name || ''),
    enabled: Boolean(value.enabled),
    target: value.target || null,
    agent: value.agent || null,
    workspace: value.workspace || null,
    schedule: value.schedule || null,
    next_due_at: String(value.next_due_at || ''),
    created_at: String(value.created_at || ''),
    updated_at: String(value.updated_at || ''),
    deleted_at: String(value.deleted_at || ''),
  }
}

function summarizeAssignmentStatus(value) {
  if (!value || typeof value !== 'object') {
    return null
  }
  return {
    flow_id: String(value.flow_id || ''),
    target_swarm_id: String(value.target_swarm_id || ''),
    command_id: String(value.command_id || ''),
    desired_revision: Number(value.desired_revision || 0),
    accepted_revision: Number(value.accepted_revision || 0),
    status: String(value.status || ''),
    pending_sync: Boolean(value.pending_sync),
    reason: String(value.reason || ''),
  }
}

function summarizeOutbox(value) {
  if (!value || typeof value !== 'object') {
    return null
  }
  return {
    command_id: String(value.command_id || ''),
    flow_id: String(value.flow_id || ''),
    revision: Number(value.revision || 0),
    target_swarm_id: String(value.target_swarm_id || ''),
    status: String(value.status || ''),
    attempt_count: Number(value.attempt_count || 0),
    last_error: String(value.last_error || ''),
  }
}

function summarizeDeliverResult(value) {
  if (!value || typeof value !== 'object') {
    return null
  }
  return {
    outbox: summarizeOutbox(value.outbox),
    assignment_state: summarizeAssignmentStatus(value.assignment_state),
    ack: value.ack || null,
    delivered: Boolean(value.delivered),
    pending_sync: Boolean(value.pending_sync),
  }
}

function summarizeRun(value) {
  if (!value || typeof value !== 'object') {
    return null
  }
  return {
    run_id: String(value.run_id || ''),
    flow_id: String(value.flow_id || ''),
    revision: Number(value.revision || 0),
    scheduled_at: String(value.scheduled_at || ''),
    started_at: String(value.started_at || ''),
    finished_at: String(value.finished_at || ''),
    status: String(value.status || ''),
    summary: String(value.summary || ''),
    session_id: String(value.session_id || ''),
    target_swarm_id: String(value.target_swarm_id || ''),
    reported_at: String(value.reported_at || ''),
    report_error: String(value.report_error || ''),
  }
}

function summarizeSession(value) {
  if (!value || typeof value !== 'object') {
    return null
  }
  const metadata = value.metadata && typeof value.metadata === 'object' ? value.metadata : {}
  return {
    id: String(value.id || ''),
    title: String(value.title || ''),
    workspace_path: String(value.workspace_path || ''),
    mode: String(value.mode || ''),
    flow_id: String(metadata.flow_id || ''),
    flow_revision: metadata.flow_revision,
    lifecycle: value.lifecycle ? {
      active: Boolean(value.lifecycle.active),
      phase: String(value.lifecycle.phase || ''),
      run_id: String(value.lifecycle.run_id || ''),
      error: String(value.lifecycle.error || ''),
      owner_transport: String(value.lifecycle.owner_transport || ''),
    } : null,
  }
}

function summarizeTarget(value) {
  if (!value || typeof value !== 'object') {
    return null
  }
  return {
    swarm_id: String(value.swarm_id || ''),
    name: String(value.name || ''),
    relationship: String(value.relationship || ''),
    kind: String(value.kind || ''),
    deployment_id: String(value.deployment_id || ''),
    attach_status: String(value.attach_status || ''),
    online: Boolean(value.online),
    selectable: Boolean(value.selectable),
    current: Boolean(value.current),
    backend_url_present: Boolean(String(value.backend_url || '').trim()),
    last_error: String(value.last_error || ''),
  }
}

function summarizeWorkspace(value) {
  if (!value || typeof value !== 'object') {
    return null
  }
  return {
    workspace_path: String(value.workspace_path || ''),
    host_workspace_path: String(value.host_workspace_path || ''),
    runtime_workspace_path: String(value.runtime_workspace_path || ''),
    cwd: String(value.cwd || ''),
    worktree_mode: String(value.worktree_mode || ''),
  }
}

function summarizeAgentDetail(value) {
  if (!value || typeof value !== 'object') {
    return null
  }
  return {
    name: String(value.name || ''),
    mode: String(value.mode || ''),
    enabled: Boolean(value.enabled),
  }
}

function flowIDFromDetail(detail) {
  return String(detail?.definition?.flow_id || '').trim()
}

function flowIDFromCreatePayload(payload) {
  return flowIDFromDetail(payload?.flow)
}

function assertDeliverResultAccepted(payload, label) {
  const result = payload?.result
  if (!result || typeof result !== 'object') {
    return
  }
  const ackStatus = String(result.ack?.status || result.assignment_state?.status || '').trim()
  const pendingSync = Boolean(result.pending_sync || result.assignment_state?.pending_sync)
  const reason = String(result.ack?.reason || result.assignment_state?.reason || result.outbox?.last_error || '').trim()
  if (pendingSync || (ackStatus && !['accepted', 'duplicate'].includes(ackStatus))) {
    fail(`${label} was not accepted by target: status=${ackStatus || '<unknown>'} pending_sync=${pendingSync} reason=${reason || '<none>'}`)
  }
}

async function pageJSON(page, endpoint, init = {}, summary) {
  const started = performance.now()
  const result = await page.evaluate(async ({ endpoint: innerEndpoint, init: innerInit }) => {
    const bootstrap = await fetch('/v1/auth/desktop/session', {
      cache: 'no-store',
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    }).then(async (response) => ({ ok: response.ok, status: response.status, text: await response.text() })).catch((error) => ({ ok: false, status: 0, text: error instanceof Error ? error.message : String(error) }))

    const headers = { Accept: 'application/json', ...(innerInit.headers || {}) }
    const response = await fetch(innerEndpoint, {
      ...innerInit,
      credentials: 'same-origin',
      headers,
    })
    const text = await response.text()
    let payload = null
    try {
      payload = text ? JSON.parse(text) : null
    } catch {
      payload = null
    }
    return {
      bootstrap,
      ok: response.ok,
      status: response.status,
      text,
      payload,
    }
  }, { endpoint, init })
  const duration = Math.round(performance.now() - started)
  const event = {
    method: init.method || 'GET',
    path: endpoint,
    status: result.status,
    ok: result.ok,
    duration_ms: duration,
    bootstrap_status: result.bootstrap?.status,
    summary: compactPayload(result.payload),
  }
  summary.api_calls.push(event)
  console.log(`API ${event.method} ${endpoint} status=${event.status} duration_ms=${duration}`)
  if (!result.ok) {
    const message = result.payload?.error || result.text || `HTTP ${result.status}`
    const error = new Error(`${event.method} ${endpoint} failed: ${message}`)
    error.apiEvent = event
    throw error
  }
  return result.payload
}

async function waitForPageReady(page, url, opts, summary) {
  await page.goto(`${url}/flow`, { waitUntil: 'domcontentloaded' })
  await unlockVaultIfNeeded(page, opts, summary)
  await failIfOnboardingVisible(page)
  await page.locator(selectors.page).waitFor({ state: 'visible', timeout: 60000 })
}

async function unlockVaultIfNeeded(page, opts, summary) {
  const passwordBox = page.locator(selectors.vaultPassword).first()
  if (!(await passwordBox.isVisible({ timeout: 1500 }).catch(() => false))) {
    return
  }
  const envName = String(opts.desktopVaultPasswordEnv || '').trim()
  if (!envName) {
    fail('desktop vault is locked; rerun with --desktop-vault-password-env <env>')
  }
  const password = process.env[envName]
  if (!password) {
    fail(`desktop vault is locked and ${envName} is empty`)
  }
  await passwordBox.fill(password)
  await page.locator(selectors.vaultUnlock).first().click()
  summary.notes.push('desktop vault was unlocked by env-provided password')
}

async function failIfOnboardingVisible(page) {
  const onboardingText = page.getByText(/Finish setup|Connect a provider|Create your swarm identity/i).first()
  if (await onboardingText.isVisible({ timeout: 1000 }).catch(() => false)) {
    fail('desktop onboarding is visible; finish onboarding before running the Flow smoke harness')
  }
}

async function selectOptionIfPresent(locator, value, label) {
  try {
    await locator.selectOption(value)
  } catch (error) {
    throw new Error(`unable to select ${label}=${value}: ${error instanceof Error ? error.message : String(error)}`)
  }
}

async function createHostFlowViaUI(page, opts, summary, recorder) {
  const flowName = `Flow smoke host ${timestamp()}`
  const finish = recorder.start('host.ui.create_flow', { flow_name: flowName })
  await page.locator(selectors.addOpen).click()
  await page.locator(selectors.addModal).waitFor({ state: 'visible', timeout: 15000 })
  await page.locator(selectors.addName).fill(flowName)
  await selectOptionIfPresent(page.locator(selectors.addAgent), opts.agent, 'agent')
  await selectOptionIfPresent(page.locator(selectors.addTarget), 'local', 'target')
  await selectOptionIfPresent(page.locator(selectors.addWorkspace), '.', 'workspace')
  await selectOptionIfPresent(page.locator(selectors.addCadence), 'On demand', 'cadence')
  await page.locator(selectors.addTask).fill(opts.prompt)

  const createResponsePromise = page.waitForResponse((response) => requestPath(response.url()) === '/v3/flows' && response.request().method() === 'POST', { timeout: 60000 })
  await page.locator(selectors.addSubmit).click()
  const createResponse = await createResponsePromise
  const createPayload = await createResponse.json().catch(() => null)
  summary.observations.host_create_response = compactPayload(createPayload)
  if (!createResponse.ok()) {
    fail(`UI create Flow failed with status ${createResponse.status()}`)
  }
  const flowID = flowIDFromCreatePayload(createPayload)
  if (!flowID) {
    fail('UI create Flow response did not include flow_id')
  }
  summary.created_flows.push(flowID)
  await page.locator(selectors.detail).waitFor({ state: 'visible', timeout: 30000 })
  await page.getByRole('heading', { name: flowName }).waitFor({ state: 'visible', timeout: 30000 })
  finish({ flow_id: flowID })
  return { flowID, flowName }
}

async function runFlowNowFromDetail(page, flowID, flowName, opts, summary, recorder) {
  const finish = recorder.start('host.ui.run_now', { flow_id: flowID })
  const responsePromise = page.waitForResponse((response) => requestPath(response.url()) === `/v3/flows/${encodeURIComponent(flowID)}/run-now` && response.request().method() === 'POST', { timeout: opts.runTimeoutMs })
  await page.locator(selectors.runNow).click()
  const response = await responsePromise
  const payload = await response.json().catch(() => null)
  summary.observations.host_run_now_response = compactPayload(payload)
  if (!response.ok()) {
    const reason = payload?.error || `HTTP ${response.status()}`
    fail(`Run now failed before verification: ${reason}`)
  }
  assertDeliverResultAccepted(payload, 'host UI run-now')
  finish({ status: response.status(), run: payload?.run || null })
  const expectedRunID = String(payload?.run?.reason || payload?.result?.ack?.reason || '').match(/run_now started\s+(\S+)/)?.[1] || ''
  await verifyFlowRunVisible(page, flowID, flowName, opts.runTimeoutMs, summary, 'host.run_now', { expectedRunID })
}

async function verifyFlowRunVisible(page, flowID, flowName, timeoutMs, summary, label, options = {}) {
  const requireControllerSession = options.requireControllerSession !== false
  const expectedRunID = String(options.expectedRunID || '').trim()
  const deadline = Date.now() + timeoutMs
  let last = null
  while (Date.now() < deadline) {
    const [statusPayload, historyPayload, sessionsPayload] = await Promise.all([
      pageJSON(page, `/v3/flows/${encodeURIComponent(flowID)}/status?limit=50`, {}, summary).catch((error) => ({ error: error.message })),
      pageJSON(page, `/v3/flows/${encodeURIComponent(flowID)}/history?limit=20`, {}, summary).catch((error) => ({ error: error.message })),
      pageJSON(page, '/v1/sessions?limit=100', {}, summary).catch((error) => ({ error: error.message })),
    ])
    const history = Array.isArray(historyPayload.history) ? historyPayload.history : []
    const sessions = Array.isArray(sessionsPayload.sessions) ? sessionsPayload.sessions : []
    const flowRuns = history.filter((run) => String(run.flow_id || '').trim() === flowID)
    const matchingSessions = sessions.filter((session) => {
      const metadata = session?.metadata && typeof session.metadata === 'object' ? session.metadata : {}
      return String(metadata.flow_id || '').trim() === flowID || String(session?.title || '').includes(flowName)
    })
    last = {
      status: compactPayload(statusPayload),
      history: history.map(summarizeRun),
      matching_sessions: matchingSessions.map(summarizeSession),
    }
    summary.observations[`${label}.last_poll`] = last
    const latestRun = expectedRunID
      ? flowRuns.find((run) => String(run.run_id || '').trim() === expectedRunID)
      : flowRuns[0] || history[0]
    if (latestRun && String(latestRun.session_id || '').trim() && (!requireControllerSession || matchingSessions.length > 0)) {
      const duplicateCompletedRuns = flowRuns.filter((run) => String(run.run_id || '').trim() !== String(latestRun.run_id || '').trim())
      if (duplicateCompletedRuns.length > 0) {
        fail(`${label} produced duplicate history entries for one run-now command: ${JSON.stringify(duplicateCompletedRuns.map(summarizeRun))}`)
      }
      const latestSessionID = String(latestRun.session_id || '').trim()
      const latestSession = matchingSessions.find((session) => String(session?.id || '').trim() === latestSessionID) || matchingSessions[0] || null
      const latestSessionActive = Boolean(latestSession?.lifecycle?.active)
      summary.observations[`${label}.verified`] = {
        run: summarizeRun(latestRun),
        session: latestSession ? summarizeSession(latestSession) : null,
        controller_session_required: requireControllerSession,
      }
      const recentRuns = page.locator(selectors.recentRuns).first()
      if (await recentRuns.isVisible({ timeout: 1000 }).catch(() => false)) {
        const text = await recentRuns.innerText().catch(() => '')
        summary.observations[`${label}.recent_runs_text`] = text.slice(0, 500)
      }
      if (latestSessionActive) {
        await captureSidebarVerification(page, flowID, flowName, summary, label)
      } else {
        summary.notes.push(`${label} sidebar active-placement verification skipped: run completed before sidebar capture`)
      }
      return latestRun
    }
    await delay(3000)
  }
  const expectation = requireControllerSession ? 'mirrored history and session list' : 'mirrored history'
  fail(`${label} did not appear in ${expectation} before timeout; last=${JSON.stringify(last)}`)
}

async function captureSidebarVerification(page, flowID, flowName, summary, label) {
  const session = summary.observations[`${label}.verified`]?.session
  const workspacePath = String(session?.workspace_path || '').trim()
  if (workspacePath) {
    await navigateToWorkspaceSidebar(page, workspacePath, summary, label)
  }
  const sidebar = page.locator(selectors.desktopSidebar).first()
  if (!(await sidebar.isVisible({ timeout: 3000 }).catch(() => false))) {
    fail(`${label} sidebar was not visible for verification`)
  }
  const text = await sidebar.innerText().catch(() => '')
  const hasFlowTitle = text.includes(flowName) || text.includes('Flow smoke') || text.includes('flow smoke ok') || text.includes('Run smoke prompt')
  const hasBackgroundBadge = /background|flow/i.test(text)
  const hasElapsed = /\b\d+(?:s|m|h)\b/.test(text)
  summary.observations[`${label}.sidebar`] = {
    text: text.slice(0, 1000),
    has_flow_title: hasFlowTitle,
    has_background_badge: hasBackgroundBadge,
    has_elapsed_label: hasElapsed,
  }
  if (!hasFlowTitle || !hasBackgroundBadge) {
    fail(`${label} sidebar did not show expected Flow title/background badge for ${flowID}`)
  }
}

async function navigateToWorkspaceSidebar(page, workspacePath, summary, label) {
  const name = workspacePath.replace(/[\\/]+$/, '').split(/[\\/]/).filter(Boolean).pop() || 'workspace'
  const base = name.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'workspace'
  await page.goto(new URL(`/${encodeURIComponent(base)}`, page.url()).toString(), { waitUntil: 'domcontentloaded' })
  await page.waitForLoadState('networkidle', { timeout: 5000 }).catch(() => {})
  summary.observations[`${label}.sidebar_route`] = { workspace_path: workspacePath, slug: base }
}

function targetDefaultsForPhase(opts) {
  if (opts.phase === 'host') {
    return { kind: 'self', relationship: 'self' }
  }
  if (opts.phase === 'container') {
    return { kind: 'local', relationship: 'child' }
  }
  if (opts.phase === 'ssh') {
    return { kind: 'remote', relationship: 'child' }
  }
  return { kind: opts.targetKind || '', relationship: '' }
}

async function resolveHarnessTarget(page, opts, summary) {
  const payload = await pageJSON(page, '/v1/swarm/targets', {}, summary)
  const targets = Array.isArray(payload.targets) ? payload.targets : []
  const defaults = targetDefaultsForPhase(opts)
  const wantedKind = opts.targetKind || defaults.kind
  const wantedName = opts.targetName
  const wantedSwarmID = opts.targetSwarmID
  const wantedDeploymentID = opts.targetDeploymentID
  const candidates = targets.filter((target) => {
    if (wantedSwarmID && String(target.swarm_id || '').trim() !== wantedSwarmID) {
      return false
    }
    if (wantedDeploymentID && String(target.deployment_id || '').trim() !== wantedDeploymentID) {
      return false
    }
    if (wantedKind && String(target.kind || '').trim().toLowerCase() !== wantedKind.toLowerCase()) {
      return false
    }
    if (wantedName && String(target.name || '').trim().toLowerCase() !== wantedName.toLowerCase()) {
      return false
    }
    if (opts.phase !== 'target' && defaults.relationship && String(target.relationship || '').trim().toLowerCase() !== defaults.relationship) {
      return false
    }
    return true
  })
  summary.observations.available_targets = targets.map(summarizeTarget)
  if (candidates.length === 0) {
    fail(`no target matched phase=${opts.phase} kind=${wantedKind || '<any>'} name=${wantedName || '<any>'} swarm_id=${wantedSwarmID || '<any>'}`)
  }
  const online = candidates.find((target) => target.online && target.selectable) || candidates[0]
  if (!online.online || !online.selectable) {
    fail(`matched target is not online/selectable: ${JSON.stringify(summarizeTarget(online))}`)
  }
  summary.observations.selected_target = summarizeTarget(online)
  return online
}

function flowTargetSelection(target) {
  return {
    swarm_id: String(target.swarm_id || '').trim() || undefined,
    kind: String(target.kind || '').trim() || undefined,
    deployment_id: String(target.deployment_id || '').trim() || undefined,
    name: String(target.name || '').trim() || undefined,
  }
}

function nextScheduleTime(delayMinutes) {
  const now = new Date()
  let minutes = Math.max(1, Math.ceil(delayMinutes))
  if (now.getUTCSeconds() > 45) {
    minutes += 1
  }
  const due = new Date(now.getTime() + minutes * 60_000)
  due.setUTCSeconds(0, 0)
  const hh = String(due.getUTCHours()).padStart(2, '0')
  const mm = String(due.getUTCMinutes()).padStart(2, '0')
  return { due, hhmm: `${hh}:${mm}`, delay_minutes: minutes }
}

async function createFlowViaAPI(page, input, summary, label) {
  const payload = await pageJSON(page, '/v3/flows', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  }, summary)
  const flowID = flowIDFromCreatePayload(payload)
  if (!flowID) {
    fail(`${label} create response did not include flow_id`)
  }
  summary.created_flows.push(flowID)
  summary.observations[`${label}.create_response`] = compactPayload(payload)
  assertDeliverResultAccepted(payload, `${label} create`)
  return flowID
}

async function runFlowNowViaAPI(page, flowID, summary, label) {
  const payload = await pageJSON(page, `/v3/flows/${encodeURIComponent(flowID)}/run-now`, { method: 'POST' }, summary)
  summary.observations[`${label}.run_now_response`] = compactPayload(payload)
  assertDeliverResultAccepted(payload, `${label} run-now`)
  return payload
}

function normalizeProfileMode(agentKind) {
  const value = String(agentKind || '').trim().toLowerCase().replace(/-/g, '_')
  if (value === 'agent') {
    return 'primary'
  }
  return value || 'background'
}

function baseCreateInput({ name, target, agent, agentKind, workspace, prompt, cadence, scheduleTime }) {
  const scheduled = cadence !== 'on_demand'
  return {
    name,
    enabled: scheduled,
    target: flowTargetSelection(target),
    agent: { profile_name: agent, profile_mode: normalizeProfileMode(agentKind) },
    workspace: { workspace_path: workspace, host_workspace_path: workspace },
    schedule: scheduled
      ? { cadence, time: scheduleTime, timezone: 'UTC' }
      : { cadence: 'on_demand', timezone: 'UTC' },
    catch_up_policy: { mode: 'once' },
    intent: {
      prompt,
      mode: 'flow smoke harness',
      tasks: [
        { id: 'smoke', title: 'Run smoke prompt', detail: prompt, action: 'propose' },
      ],
    },
  }
}

async function runAPITargetSmoke(page, opts, summary, recorder) {
  const target = await resolveHarnessTarget(page, opts, summary)
  if (opts.runNow) {
    const name = `Flow smoke ${opts.phase} run-now ${timestamp()}`
    const finishCreate = recorder.start(`${opts.phase}.api.create_run_now_flow`, { target: summarizeTarget(target) })
    const flowID = await createFlowViaAPI(page, baseCreateInput({
      name,
      target,
      agent: opts.agent,
      agentKind: opts.agentKind,
      workspace: opts.workspace,
      prompt: opts.prompt,
      cadence: 'on_demand',
    }), summary, `${opts.phase}.run_now`)
    finishCreate({ flow_id: flowID })
    const finishRun = recorder.start(`${opts.phase}.api.run_now`, { flow_id: flowID })
    const runNowPayload = await runFlowNowViaAPI(page, flowID, summary, `${opts.phase}.run_now`)
    finishRun()
    const expectedRunID = String(runNowPayload?.run?.reason || runNowPayload?.result?.ack?.reason || '').match(/run_now started\s+(\S+)/)?.[1] || ''
    await verifyFlowRunVisible(page, flowID, name, opts.runTimeoutMs, summary, `${opts.phase}.run_now`, { expectedRunID })
  }

  if (opts.schedule) {
    const schedule = nextScheduleTime(opts.scheduleDelayMinutes)
    const name = `Flow smoke ${opts.phase} scheduled ${timestamp()}`
    const finishCreate = recorder.start(`${opts.phase}.api.create_scheduled_flow`, { target: summarizeTarget(target), schedule_time: schedule.hhmm })
    const flowID = await createFlowViaAPI(page, baseCreateInput({
      name,
      target,
      agent: opts.agent,
      agentKind: opts.agentKind,
      workspace: opts.workspace,
      prompt: opts.schedulePrompt,
      cadence: 'daily',
      scheduleTime: schedule.hhmm,
    }), summary, `${opts.phase}.scheduled`)
    finishCreate({ flow_id: flowID, due_utc: schedule.due.toISOString() })
    await waitForScheduledRun(page, flowID, name, schedule.due, opts.scheduleTimeoutMs, summary, `${opts.phase}.scheduled`)
  }
}

async function waitForScheduledRun(page, flowID, flowName, due, timeoutMs, summary, label) {
  const now = Date.now()
  const waitBeforeDue = Math.max(0, due.getTime() - now)
  if (waitBeforeDue > 0) {
    console.log(`WAIT ${label} due_at=${due.toISOString()} wait_ms=${waitBeforeDue}`)
    await delay(Math.min(waitBeforeDue, timeoutMs))
  }
  const remaining = Math.max(15000, timeoutMs - Math.max(0, Date.now() - now))
  return verifyFlowRunVisible(page, flowID, flowName, remaining, summary, label)
}

async function cleanupCreatedFlows(page, summary) {
  for (const flowID of [...summary.created_flows].reverse()) {
    try {
      await pageJSON(page, `/v3/flows/${encodeURIComponent(flowID)}`, { method: 'DELETE' }, summary)
      summary.cleaned_flows.push(flowID)
    } catch (error) {
      summary.cleanup_errors.push({ flow_id: flowID, error: error instanceof Error ? error.message : String(error) })
    }
  }
}

async function main() {
  const opts = parseArgs(process.argv.slice(2))
  if (opts.help) {
    usage()
    return
  }

  const url = await resolveDesktopURL(opts)
  const artifactDir = path.resolve(opts.artifactDir || defaultArtifactDir())
  await fs.mkdir(artifactDir, { recursive: true })

  const summary = {
    ok: false,
    phase: opts.phase,
    url,
    artifact_dir: path.relative(ROOT_DIR, artifactDir),
    started_at: new Date().toISOString(),
    finished_at: '',
    timings_ms: {},
    steps: [],
    api_events: [],
    api_calls: [],
    observations: {},
    created_flows: [],
    cleaned_flows: [],
    cleanup_errors: [],
    artifacts: [],
    notes: [],
    error: '',
  }
  const recorder = createRecorder(summary)
  const playwright = loadPlaywright()
  let browser
  let page

  const overallTimer = setTimeout(() => {
    console.error(`overall timeout after ${opts.timeoutMs}ms`)
    process.exitCode = 1
  }, opts.timeoutMs)

  try {
    const launchOptions = { headless: opts.headless }
    if (opts.browserExecutable.trim()) {
      launchOptions.executablePath = opts.browserExecutable.trim()
    }
    browser = await playwright.chromium.launch(launchOptions)
    const context = await browser.newContext()
    page = await context.newPage()

    page.on('request', (request) => {
      if (!isInterestingURL(request.url())) {
        return
      }
      summary.api_events.push({
        type: 'request',
        method: request.method(),
        path: requestPath(request.url()),
        at: new Date().toISOString(),
      })
    })
    page.on('response', (response) => {
      if (!isInterestingURL(response.url())) {
        return
      }
      summary.api_events.push({
        type: 'response',
        method: response.request().method(),
        path: requestPath(response.url()),
        status: response.status(),
        ok: response.ok(),
        at: new Date().toISOString(),
      })
    })

    const finishReady = recorder.start('open_flows_page')
    await waitForPageReady(page, url, opts, summary)
    finishReady()
    await maybeScreenshot(page, artifactDir, '01-flows-page', summary)

    if (opts.phase === 'host') {
      const { flowID, flowName } = await createHostFlowViaUI(page, opts, summary, recorder)
      await maybeScreenshot(page, artifactDir, '02-host-flow-detail', summary)
      if (opts.runNow) {
        await runFlowNowFromDetail(page, flowID, flowName, opts, summary, recorder)
        await maybeScreenshot(page, artifactDir, '03-host-run-now-result', summary)
      }
      if (opts.schedule) {
        await runAPITargetSmoke(page, { ...opts, runNow: false, schedule: true }, summary, recorder)
      }
    } else {
      await runAPITargetSmoke(page, opts, summary, recorder)
    }

    summary.ok = true
  } catch (error) {
    summary.error = error instanceof Error ? error.message : String(error)
    console.error(`FLOW_SMOKE_FAILED ${summary.error}`)
    if (page) {
      await maybeScreenshot(page, artifactDir, 'failure', summary)
    }
    process.exitCode = 1
  } finally {
    clearTimeout(overallTimer)
    if (page && !opts.keepFlows) {
      await cleanupCreatedFlows(page, summary)
    }
    if (browser) {
      await browser.close().catch(() => {})
    }
    summary.finished_at = new Date().toISOString()
    const summaryPath = path.join(artifactDir, 'summary.json')
    await writeJSON(summaryPath, summary)
    console.log(`SUMMARY ${path.relative(ROOT_DIR, summaryPath)}`)
    if (summary.ok) {
      console.log('FLOW_SMOKE_OK')
    }
  }
}

main().catch((error) => {
  console.error(error instanceof Error ? error.stack || error.message : String(error))
  process.exit(1)
})
