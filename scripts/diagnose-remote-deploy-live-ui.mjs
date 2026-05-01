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
  dashboardAdd: '[data-testid="swarm-dashboard-add-swarm"]',
  dashboardError: '[data-testid="swarm-dashboard-error"]',
  dashboardStatus: '[data-testid="swarm-dashboard-status"]',
  desktopVaultPassword: '[data-testid="desktop-vault-password"]',
  desktopVaultUnlock: '[data-testid="desktop-vault-unlock"]',
  modal: '[data-testid="add-swarm-modal"]',
  modalError: '[data-testid="add-swarm-error"]',
  targetRemote: '[data-testid="add-swarm-target-remote"]',
  sshTarget: '[data-testid="add-swarm-ssh-target"]',
  remoteRuntime: '[data-testid="add-swarm-remote-runtime"]',
  methodTailscale: '[data-testid="add-swarm-method-tailscale"]',
  methodLAN: '[data-testid="add-swarm-method-lan"]',
  loginMode: '[data-testid="add-swarm-tailscale-login-mode"]',
  authKey: '[data-testid="add-swarm-tailscale-auth-key"]',
  remoteReachableHost: '[data-testid="add-swarm-remote-reachable-host"]',
  workspaceCheckbox: '[data-testid="add-swarm-workspace-checkbox"]',
  vaultPassword: '[data-testid="add-swarm-sync-vault-password"]',
  childName: '[data-testid="add-swarm-child-name"]',
  runPreflight: '[data-testid="add-swarm-run-preflight"]',
  preflightSuccess: '[data-testid="add-swarm-preflight-success"]',
  launch: '[data-testid="add-swarm-launch"]',
}

function usage() {
  console.log(`Usage: ./scripts/diagnose-remote-deploy-live-ui.mjs --ssh-target <target> [options]

Drive the real live desktop UI with Playwright and time the actual Add Swarm
remote deploy flow. This script does not start swarmd, does not rebuild the
host, does not touch Tailscale Serve, and does not use the isolated shell E2E
harness.

Required:
  --ssh-target <target>          SSH alias or user@host entered in Add Swarm.

Live instance:
  --url <url>                    Desktop URL. Default: SWARM_DESKTOP_URL or http://127.0.0.1:<desktop_port>.
  --config <path>                swarm.conf for desktop_port discovery. Default: XDG config.

Flow:
  --transport <tailscale|lan>    Remote deploy method. Default: tailscale.
  --runtime <docker|podman>      Requested remote runtime. Default: docker.
  --remote-host <host>           Remote reachable host for LAN / WireGuard mode.
  --swarm-name <name>            Child swarm name. Default: timestamped diagnostic name.
  --workspace <path|first|none>  Workspace selection. Default: current working directory, fallback to first.
  --strict-workspace             Fail if --workspace path is not listed by the live UI.
  --tailscale-auth-key-env <env> Use this env var as the launch-only Tailscale auth key.
  --desktop-vault-password-env <env>
                                 Unlock the live desktop vault first when needed.
  --sync-vault-password-env <env>
                                 Use this env var as the Swarm Sync vault password.
  --configure-only               Stop after the UI is configured and workspace selection is made.
  --preflight-only               Stop after preflight succeeds.
  --wait-for-manual-auth         Keep waiting after a manual Tailscale auth URL is produced.
                                 Default: stop there and report auth_required.

Browser/artifacts:
  --artifact-dir <path>          Default: tmp/remote-deploy-ui-diagnostics/<timestamp>.
  --browser-executable <path>    Use a system browser executable.
  --headful                      Show the browser window.
  --timeout-ms <ms>              Overall timeout. Default: 900000.
  --preflight-timeout-ms <ms>    Preflight timeout. Default: 300000.
  --launch-timeout-ms <ms>       Launch/enrollment/approval timeout. Default: 600000.
  --help                         Show this help.

No raw auth keys are accepted on the command line.`)
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
    url: process.env.SWARM_DESKTOP_URL || '',
    configPath: process.env.SWARM_CONFIG || '',
    sshTarget: process.env.SWARM_REMOTE_SSH_TARGET || '',
    transport: process.env.SWARM_REMOTE_TRANSPORT || 'tailscale',
    runtime: process.env.SWARM_REMOTE_RUNTIME || 'docker',
    remoteHost: process.env.SWARM_REMOTE_HOST || '',
    swarmName: process.env.SWARM_REMOTE_SWARM_NAME || '',
    workspace: process.env.SWARM_REMOTE_WORKSPACE || process.cwd(),
    strictWorkspace: false,
    tailscaleAuthKeyEnv: process.env.SWARM_TAILSCALE_AUTH_KEY_ENV || '',
    desktopVaultPasswordEnv: process.env.SWARM_DESKTOP_VAULT_PASSWORD_ENV || '',
    syncVaultPasswordEnv: process.env.SWARM_SYNC_VAULT_PASSWORD_ENV || '',
    configureOnly: false,
    preflightOnly: false,
    waitForManualAuth: false,
    artifactDir: '',
    browserExecutable: process.env.PLAYWRIGHT_BROWSER_EXECUTABLE || '',
    headless: true,
    timeoutMs: Number(process.env.SWARM_REMOTE_UI_TIMEOUT_MS || '') || 900000,
    preflightTimeoutMs: Number(process.env.SWARM_REMOTE_UI_PREFLIGHT_TIMEOUT_MS || '') || 300000,
    launchTimeoutMs: Number(process.env.SWARM_REMOTE_UI_LAUNCH_TIMEOUT_MS || '') || 600000,
  }

  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index]
    switch (arg) {
      case '--help':
      case '-h':
        opts.help = true
        break
      case '--url':
        opts.url = requireValue(argv, index, arg)
        index += 1
        break
      case '--config':
        opts.configPath = requireValue(argv, index, arg)
        index += 1
        break
      case '--ssh-target':
        opts.sshTarget = requireValue(argv, index, arg)
        index += 1
        break
      case '--transport':
        opts.transport = requireValue(argv, index, arg)
        index += 1
        break
      case '--runtime':
        opts.runtime = requireValue(argv, index, arg)
        index += 1
        break
      case '--remote-host':
        opts.remoteHost = requireValue(argv, index, arg)
        index += 1
        break
      case '--swarm-name':
        opts.swarmName = requireValue(argv, index, arg)
        index += 1
        break
      case '--workspace':
        opts.workspace = requireValue(argv, index, arg)
        index += 1
        break
      case '--strict-workspace':
        opts.strictWorkspace = true
        break
      case '--tailscale-auth-key-env':
        opts.tailscaleAuthKeyEnv = requireValue(argv, index, arg)
        index += 1
        break
      case '--desktop-vault-password-env':
        opts.desktopVaultPasswordEnv = requireValue(argv, index, arg)
        index += 1
        break
      case '--sync-vault-password-env':
        opts.syncVaultPasswordEnv = requireValue(argv, index, arg)
        index += 1
        break
      case '--configure-only':
        opts.configureOnly = true
        break
      case '--preflight-only':
        opts.preflightOnly = true
        break
      case '--wait-for-manual-auth':
        opts.waitForManualAuth = true
        break
      case '--artifact-dir':
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
        opts.timeoutMs = parseNumber(requireValue(argv, index, arg), arg)
        index += 1
        break
      case '--preflight-timeout-ms':
        opts.preflightTimeoutMs = parseNumber(requireValue(argv, index, arg), arg)
        index += 1
        break
      case '--launch-timeout-ms':
        opts.launchTimeoutMs = parseNumber(requireValue(argv, index, arg), arg)
        index += 1
        break
      default:
        fail(`unknown argument: ${arg}`)
    }
  }

  opts.transport = opts.transport.trim().toLowerCase()
  opts.runtime = opts.runtime.trim().toLowerCase()
  if (!['tailscale', 'lan'].includes(opts.transport)) {
    fail('--transport must be tailscale or lan')
  }
  if (!['docker', 'podman'].includes(opts.runtime)) {
    fail('--runtime must be docker or podman')
  }
  if (!opts.help && !opts.sshTarget.trim()) {
    fail('--ssh-target is required')
  }
  if (opts.transport === 'lan' && !opts.remoteHost.trim()) {
    fail('--remote-host is required when --transport lan')
  }
  if (opts.tailscaleAuthKeyEnv.trim() && opts.transport !== 'tailscale') {
    fail('--tailscale-auth-key-env only applies to --transport tailscale')
  }
  return opts
}

function timestamp() {
  return new Date().toISOString().replace(/[-:]/g, '').replace(/\.\d{3}Z$/, 'Z')
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
    // Missing config is allowed here; the live desktop URL default remains explicit.
  }
  return `http://127.0.0.1:${desktopPort}`
}

function defaultArtifactDir() {
  return path.join(ROOT_DIR, 'tmp', 'remote-deploy-ui-diagnostics', timestamp())
}

function loadPlaywright() {
  try {
    const requireFromWeb = createRequire(WEB_PACKAGE_JSON)
    return requireFromWeb('playwright')
  } catch (error) {
    fail(`Playwright is not installed for the web package: ${error instanceof Error ? error.message : String(error)}`)
  }
}

function redactSensitive(value) {
  return String(value ?? '')
    .replace(/https:\/\/login\.tailscale\.com\/a\/[A-Za-z0-9_-]+/g, 'https://login.tailscale.com/a/<redacted>')
    .replace(/tskey-[A-Za-z0-9_-]+/g, 'tskey-<redacted>')
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
  return pathAndQuery.startsWith('/v1/')
    || pathAndQuery === '/readyz'
    || pathAndQuery === '/healthz'
}

function remoteDeployAPIKind(url) {
  const pathAndQuery = requestPath(url)
  const pathname = pathAndQuery.split('?')[0]
  if (pathname === '/v1/deploy/remote/session/create') {
    return 'remote_session_create'
  }
  if (pathname === '/v1/deploy/remote/session/start') {
    return 'remote_session_start'
  }
  if (pathname === '/v1/deploy/remote/session') {
    return 'remote_session_list'
  }
  if (/^\/v1\/deploy\/remote\/session\/[^/]+\/approve$/.test(pathname)) {
    return 'remote_session_approve'
  }
  return ''
}

function parseRemoteTimers(output) {
  const timers = []
  const text = String(output ?? '')
  for (const line of text.split(/\r?\n/)) {
    const match = line.match(/\bSWARM_REMOTE_TIMER\s+step=([A-Za-z0-9_.:-]+)\s+elapsed_ms=([0-9]+)/)
    if (!match) {
      continue
    }
    timers.push({
      step: match[1],
      elapsed_ms: Number(match[2]),
    })
  }
  return timers
}

function summarizeRemoteSession(session) {
  if (!session || typeof session !== 'object') {
    return null
  }
  const payloads = Array.isArray(session.preflight?.payloads) ? session.preflight.payloads : []
  const remoteTimers = parseRemoteTimers(session.last_remote_output)
  const startTimings = Array.isArray(session.start_timings)
    ? session.start_timings.map((timing) => ({
        step: String(timing.step ?? ''),
        elapsed_ms: Number(timing.elapsed_ms || 0),
        status: String(timing.status ?? ''),
        fields: timing.fields && typeof timing.fields === 'object' ? Object.fromEntries(
          Object.entries(timing.fields).map(([key, value]) => [String(key), redactSensitive(value)]),
        ) : undefined,
      })).filter((timing) => timing.step)
    : []
  return {
    id: String(session.id ?? ''),
    name: String(session.name ?? ''),
    status: String(session.status ?? ''),
    transport_mode: String(session.transport_mode ?? ''),
    remote_runtime: String(session.remote_runtime ?? ''),
    enrollment_status: String(session.enrollment_status ?? ''),
    enrollment_id_present: Boolean(String(session.enrollment_id ?? '').trim()),
    child_swarm_id_present: Boolean(String(session.child_swarm_id ?? '').trim()),
    remote_auth_url_present: Boolean(String(session.remote_auth_url ?? '').trim()),
    remote_endpoint_present: Boolean(String(session.remote_endpoint ?? '').trim()),
    image_archive_bytes: Number(session.image_archive_bytes || 0),
    last_progress: redactSensitive(session.last_progress || ''),
    last_error: redactSensitive(session.last_error || ''),
    remote_timers: remoteTimers,
    remote_timer_total_ms: remoteTimers.reduce((sum, item) => sum + (Number(item.elapsed_ms) || 0), 0),
    start_timings: startTimings,
    start_timing_total_ms: startTimings.reduce((sum, item) => sum + (Number(item.elapsed_ms) || 0), 0),
    preflight: {
      files_to_copy_count: Array.isArray(session.preflight?.files_to_copy) ? session.preflight.files_to_copy.length : 0,
      payload_count: payloads.length,
      payload_archive_count: payloads.reduce((sum, payload) => sum + 1 + (Array.isArray(payload.directories) ? payload.directories.length : 0), 0),
      payload_included_files: payloads.reduce((sum, payload) => sum + (Number(payload.included_files) || 0), 0),
      payload_included_bytes: payloads.reduce((sum, payload) => sum + (Number(payload.included_bytes) || 0), 0),
      disk_required_bytes: Number(session.preflight?.remote_disk?.required_bytes || 0),
      disk_available_bytes: Number(session.preflight?.remote_disk?.available_bytes || 0),
      summary: redactSensitive(session.preflight?.summary || ''),
    },
  }
}

async function summarizeAPIResponse(response, requestRecords) {
  const record = requestRecords.get(response.request()) || null
  const summary = {
    kind: remoteDeployAPIKind(response.url()),
    method: response.request().method(),
    path: redactSensitive(requestPath(response.url())),
    status: response.status(),
    started_ms: record?.started_ms,
    finished_ms: record?.finished_ms,
    duration_ms: record?.duration_ms,
    ok: response.ok(),
    path_id: '',
    error: '',
    session: null,
  }
  let text = ''
  try {
    text = await response.text()
  } catch (error) {
    summary.error = redactSensitive(error instanceof Error ? error.message : String(error))
    return summary
  }
  if (!text.trim()) {
    return summary
  }
  let payload
  try {
    payload = JSON.parse(text)
  } catch {
    summary.error = redactSensitive(text.trim())
    return summary
  }
  summary.ok = Boolean(payload.ok ?? response.ok())
  summary.path_id = String(payload.path_id ?? '')
  summary.error = redactSensitive(payload.error || '')
  if (payload.session) {
    summary.session = summarizeRemoteSession(payload.session)
  } else if (Array.isArray(payload.sessions) && payload.sessions.length === 1) {
    summary.session = summarizeRemoteSession(payload.sessions[0])
  }
  if (summary.kind === 'remote_session_start' && summary.session?.remote_timer_total_ms && Number.isFinite(summary.duration_ms)) {
    summary.start_request_unattributed_ms = Math.max(0, summary.duration_ms - summary.session.remote_timer_total_ms)
    if (summary.session?.start_timing_total_ms) {
      summary.start_request_unattributed_ms = Math.max(0, summary.duration_ms - summary.session.start_timing_total_ms)
    }
  }
  return summary
}

async function captureAPIResponse(responsePromise, label, requestRecords, summary) {
  try {
    const response = await responsePromise
    const apiSummary = await summarizeAPIResponse(response, requestRecords)
    summary.api_responses.push(apiSummary)
    return apiSummary
  } catch (error) {
    summary.notes.push(`${label} response was not captured: ${error instanceof Error ? error.message : String(error)}`)
    return null
  }
}

function delay(ms) {
  return new Promise((resolve) => {
    setTimeout(resolve, ms)
  })
}

async function captureAPIResponseWithSessionProgress(responsePromise, label, requestRecords, summary, page, sessionID) {
  let stopped = false
  let lastProgressKey = ''
  const progressLoop = (async () => {
    while (!stopped) {
      await delay(5000)
      if (stopped || !String(sessionID || '').trim()) {
        continue
      }
      try {
        const session = await fetchRemoteSessionFromPage(page, sessionID)
        if (!session) {
          continue
        }
        summary.final_remote_session = session
        const progressKey = compactSessionProgress(session)
        if (progressKey !== lastProgressKey) {
          logRemoteSessionProgress(label, session)
          lastProgressKey = progressKey
        }
      } catch (error) {
        const message = redactSensitive(error instanceof Error ? error.message : String(error))
        if (message && message !== lastProgressKey) {
          console.log(`PROGRESS ${label}_poll_error ${message}`)
          lastProgressKey = message
        }
      }
    }
  })()
  try {
    return await captureAPIResponse(responsePromise, label, requestRecords, summary)
  } finally {
    stopped = true
    await progressLoop.catch(() => {})
  }
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
    summary.screenshot_errors.push({
      name,
      error: error instanceof Error ? error.message : String(error),
    })
  }
}

function createRecorder(summary) {
  const startedAt = performance.now()
  return {
    elapsed() {
      return Math.round(performance.now() - startedAt)
    },
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

function buildBottlenecks(summary, apiEvents) {
  const bottlenecks = []
  const create = summary.api_responses.find((item) => item.kind === 'remote_session_create')
  if (create?.duration_ms) {
    bottlenecks.push({
      name: 'remote_preflight_create',
      duration_ms: create.duration_ms,
      detail: create.session?.preflight?.summary || '',
    })
  }
  const start = summary.api_responses.find((item) => item.kind === 'remote_session_start')
  if (start?.duration_ms) {
    bottlenecks.push({
      name: 'remote_session_start',
      duration_ms: start.duration_ms,
      image_archive_bytes: start.session?.image_archive_bytes || 0,
      remote_timer_total_ms: start.session?.remote_timer_total_ms || 0,
      start_request_unattributed_ms: start.start_request_unattributed_ms || 0,
    })
    for (const timer of start.session?.remote_timers || []) {
      bottlenecks.push({
        name: `remote_timer.${timer.step}`,
        duration_ms: timer.elapsed_ms,
      })
    }
    for (const timing of start.session?.start_timings || []) {
      bottlenecks.push({
        name: timing.step,
        duration_ms: timing.elapsed_ms,
        status: timing.status,
        fields: timing.fields,
      })
    }
  }
  for (const event of apiEvents) {
    if (!Number.isFinite(event.duration_ms) || event.duration_ms < 1000) {
      continue
    }
    bottlenecks.push({
      name: `api.${event.method} ${event.path}`,
      duration_ms: event.duration_ms,
      status: event.status,
    })
  }
  return bottlenecks
    .filter((item) => Number.isFinite(item.duration_ms))
    .sort((left, right) => right.duration_ms - left.duration_ms)
    .slice(0, 20)
}

function compactSessionProgress(session) {
  if (!session) {
    return 'session=<missing>'
  }
  const parts = [
    `id=${session.id || '<unknown>'}`,
    `status=${session.status || '<unknown>'}`,
  ]
  if (session.enrollment_status) {
    parts.push(`enrollment=${session.enrollment_status}`)
  }
  if (session.remote_auth_url_present) {
    parts.push('auth_url=yes')
  }
  if (session.remote_endpoint_present) {
    parts.push('remote_endpoint=yes')
  }
  if (session.last_progress) {
    parts.push(`progress=${JSON.stringify(session.last_progress)}`)
  }
  if (session.last_error) {
    parts.push(`error=${JSON.stringify(session.last_error)}`)
  }
  const slowStart = [...(session.start_timings || [])]
    .sort((left, right) => (right.elapsed_ms || 0) - (left.elapsed_ms || 0))
    .slice(0, 3)
    .map((item) => `${item.step}=${item.elapsed_ms}ms`)
  if (slowStart.length > 0) {
    parts.push(`slow_start=${slowStart.join(',')}`)
  }
  const remoteTimers = [...(session.remote_timers || [])]
    .sort((left, right) => (right.elapsed_ms || 0) - (left.elapsed_ms || 0))
    .slice(0, 3)
    .map((item) => `${item.step}=${item.elapsed_ms}ms`)
  if (remoteTimers.length > 0) {
    parts.push(`remote_timers=${remoteTimers.join(',')}`)
  }
  return parts.join(' ')
}

function logRemoteSessionProgress(label, session) {
  console.log(`PROGRESS ${label} ${compactSessionProgress(session)}`)
}

function fieldByLabel(page, label) {
  return page.locator('label')
    .filter({ hasText: new RegExp(`^${label}$`, 'i') })
    .locator('xpath=..')
}

function candidates(page, name) {
  switch (name) {
    case 'dashboardAdd':
      return [
        page.locator(selectors.dashboardAdd).first(),
        page.getByRole('button', { name: /^\+?\s*Add swarm$/i }).first(),
      ]
    case 'desktopVaultPassword':
      return [
        page.locator(selectors.desktopVaultPassword).first(),
        page.getByPlaceholder('Enter password to unlock').first(),
        page.locator('#desktop-vault-password').first(),
      ]
    case 'desktopVaultUnlock':
      return [
        page.locator(selectors.desktopVaultUnlock).first(),
        page.getByRole('button', { name: /^Unlock Vault$/i }).first(),
      ]
    case 'modal':
      return [
        page.locator(selectors.modal).first(),
        page.getByRole('heading', { name: /^Add swarm$/i }).first(),
      ]
    case 'targetRemote':
      return [
        page.locator(selectors.targetRemote).first(),
        page.getByRole('button', { name: /Remote over SSH/i }).first(),
      ]
    case 'sshTarget':
      return [
        page.locator(selectors.sshTarget).first(),
        page.getByPlaceholder('user@host or ssh-config alias').first(),
      ]
    case 'remoteRuntime':
      return [
        page.locator(selectors.remoteRuntime).first(),
        fieldByLabel(page, 'Remote runtime').locator('select').first(),
      ]
    case 'methodTailscale':
      return [
        page.locator(selectors.methodTailscale).first(),
        page.getByRole('button', { name: /SSH \+ Tailscale/i }).first(),
      ]
    case 'methodLAN':
      return [
        page.locator(selectors.methodLAN).first(),
        page.getByRole('button', { name: /SSH \+ LAN \/ WireGuard/i }).first(),
      ]
    case 'loginMode':
      return [
        page.locator(selectors.loginMode).first(),
        fieldByLabel(page, 'Login mode').locator('select').first(),
      ]
    case 'authKey':
      return [
        page.locator(selectors.authKey).first(),
        page.getByPlaceholder('tskey-...').first(),
      ]
    case 'remoteReachableHost':
      return [
        page.locator(selectors.remoteReachableHost).first(),
        fieldByLabel(page, 'Remote reachable host').locator('input').first(),
      ]
    case 'vaultPassword':
      return [
        page.locator(selectors.vaultPassword).first(),
        page.getByPlaceholder('Vault password').first(),
      ]
    case 'childName':
      return [
        page.locator(selectors.childName).first(),
        fieldByLabel(page, 'Child swarm name').locator('input').first(),
      ]
    case 'runPreflight':
      return [
        page.locator(selectors.runPreflight).first(),
        page.getByRole('button', { name: /Run preflight|Use detected address and run preflight/i }).first(),
      ]
    case 'launch':
      return [
        page.locator(selectors.launch).first(),
        page.getByRole('button', { name: /Launch and add/i }).first(),
      ]
    default:
      return []
  }
}

async function findUsableLocator(page, locatorCandidates, label, timeoutMs, requireEnabled = true) {
  const deadline = Date.now() + timeoutMs
  let lastError = ''
  while (Date.now() < deadline) {
    for (const locator of locatorCandidates) {
      try {
        if ((await locator.count()) === 0) {
          continue
        }
        const first = locator.first()
        if (!(await first.isVisible().catch(() => false))) {
          continue
        }
        if (requireEnabled && !(await first.isEnabled().catch(() => false))) {
          continue
        }
        return first
      } catch (error) {
        lastError = error instanceof Error ? error.message : String(error)
      }
    }
    await page.waitForTimeout(100)
  }
  fail(`timed out waiting for ${label}${lastError ? `: ${lastError}` : ''}`)
}

function normalizeWorkspacePath(value) {
  const trimmed = String(value ?? '').trim()
  if (!trimmed || trimmed === 'first' || trimmed === 'none') {
    return trimmed
  }
  return path.resolve(trimmed)
}

async function selectWorkspace(page, opts, summary) {
  const requested = String(opts.workspace ?? '').trim()
  if (requested === 'none') {
    summary.workspace = { requested, selected: '', mode: 'none' }
    return
  }
  let boxes = page.locator(selectors.workspaceCheckbox)
  let hasWorkspaceMetadata = true
  const deadline = Date.now() + 30000
  while (Date.now() < deadline) {
    boxes = page.locator(selectors.workspaceCheckbox)
    hasWorkspaceMetadata = true
    if ((await boxes.count()) > 0) {
      break
    }
    boxes = page.locator('input[type="checkbox"]')
    hasWorkspaceMetadata = false
    if ((await boxes.count()) > 0) {
      break
    }
    await page.waitForTimeout(100)
  }
  const count = await boxes.count()
  if (count === 0) {
    if (opts.strictWorkspace) {
      fail('live Add Swarm UI reported no selectable workspaces')
    }
    summary.notes.push('live Add Swarm UI reported no selectable workspaces; continuing with zero workspace payloads')
    summary.workspace = { requested, selected: '', mode: 'none-live-empty' }
    return
  }

  let selectedIndex = -1
  let selectedPath = ''
  if (requested === 'first') {
    selectedIndex = 0
  } else if (hasWorkspaceMetadata) {
    const normalizedRequested = normalizeWorkspacePath(requested)
    for (let index = 0; index < count; index += 1) {
      const candidatePath = (await boxes.nth(index).getAttribute('data-workspace-path')) || ''
      if (normalizeWorkspacePath(candidatePath) === normalizedRequested) {
        selectedIndex = index
        break
      }
    }
  } else {
    const normalizedRequested = normalizeWorkspacePath(requested)
    for (let index = 0; index < count; index += 1) {
      const labelText = await boxes.nth(index).locator('xpath=ancestor::label[1]').innerText().catch(() => '')
      if (labelText.includes(requested) || labelText.includes(normalizedRequested)) {
        selectedIndex = index
        selectedPath = normalizedRequested
        break
      }
    }
  }

  if (selectedIndex < 0) {
    if (opts.strictWorkspace) {
      fail(`workspace is not listed by the live UI: ${requested}`)
    }
    selectedIndex = 0
    summary.notes.push(hasWorkspaceMetadata
      ? 'requested workspace was not listed by the live UI; selected the first workspace instead'
      : 'live UI has no workspace metadata selectors; selected the first workspace checkbox')
  }

  const box = boxes.nth(selectedIndex)
  if (hasWorkspaceMetadata) {
    selectedPath = (await box.getAttribute('data-workspace-path')) || ''
  } else if (!selectedPath) {
    const labelText = await box.locator('xpath=ancestor::label[1]').innerText().catch(() => '')
    selectedPath = labelText.match(/\/[^\s]+/)?.[0] || ''
  }
  await box.check({ timeout: 30000 })
  summary.workspace = {
    requested,
    selected: selectedPath,
    mode: requested === 'first'
      ? 'first'
      : normalizeWorkspacePath(selectedPath) === normalizeWorkspacePath(requested)
        ? 'exact'
        : 'fallback-first',
  }
}

async function fillOptionalSecret(page, locatorCandidates, envName, label) {
  if (!envName.trim()) {
    return false
  }
  const value = process.env[envName]
  if (!value) {
    fail(`${label} env var is empty or missing: ${envName}`)
  }
  const locator = await findUsableLocator(page, locatorCandidates, `${label} input`, 10000)
  await locator.fill(value)
  return true
}

async function unlockDesktopVaultIfNeeded(page, opts, summary) {
  let passwordInput
  try {
    passwordInput = await findUsableLocator(page, candidates(page, 'desktopVaultPassword'), 'desktop vault password input', 1000)
  } catch {
    return
  }
  const envName = opts.desktopVaultPasswordEnv.trim()
  if (!envName) {
    fail('live desktop vault is locked; pass --desktop-vault-password-env <env>')
  }
  const password = process.env[envName]
  if (!password) {
    fail(`desktop vault password env var is empty or missing: ${envName}`)
  }
  await passwordInput.fill(password)
  const unlock = await findUsableLocator(page, candidates(page, 'desktopVaultUnlock'), 'desktop vault unlock button', 10000)
  await unlock.click()
  summary.notes.push(`desktop vault unlock attempted from env var ${envName}`)
}

async function waitForPreflightOutcome(page, timeoutMs) {
  const result = await page.waitForFunction(
    (sel) => {
      const success = document.querySelector(sel.success)?.textContent?.trim()
      if (success) {
        return { ok: true, text: success }
      }
      const error = document.querySelector(sel.error)?.textContent?.trim()
      if (error) {
        return { ok: false, text: error }
      }
      const bodyText = document.body?.innerText || ''
      if (bodyText.includes('Preflight passed')) {
        return { ok: true, text: 'Preflight passed' }
      }
      const knownFailures = ['Remote preflight failed']
      for (const failure of knownFailures) {
        if (bodyText.includes(failure)) {
          return { ok: false, text: failure }
        }
      }
      return null
    },
    { success: selectors.preflightSuccess, error: selectors.modalError },
    { timeout: timeoutMs },
  )
  return result.jsonValue()
}

async function readLaunchOutcome(page) {
  return page.evaluate((sel) => {
    const bodyText = document.body?.innerText || ''
    const modal = document.querySelector(sel.modal)
    const modalOpen = Boolean(modal)
      || (
        bodyText.includes('Add swarm')
        && (
          bodyText.includes('Launch and add')
          || bodyText.includes('Working')
          || bodyText.includes('Remote over SSH')
        )
      )
    const modalError = document.querySelector(sel.modalError)?.textContent?.trim()
    if (modalError) {
      return { ok: false, text: modalError, source: 'modal' }
    }
    const dashboardError = document.querySelector(sel.dashboardError)?.textContent?.trim()
    if (dashboardError) {
      return { ok: false, text: dashboardError, source: 'dashboard' }
    }
    const dashboardStatus = document.querySelector(sel.dashboardStatus)?.textContent?.trim()
    if (!modalOpen && dashboardStatus) {
      return { ok: true, text: dashboardStatus, source: 'dashboard' }
    }
    if (bodyText.includes('Added remote child')) {
      return { ok: true, text: 'Added remote child', source: 'body' }
    }
    const knownFailures = [
      'Remote deploy failed',
      'Remote child did not enroll',
      'Remote launch failed',
      'Run the remote preflight check',
      'Tailscale auth key is required',
    ]
    for (const failure of knownFailures) {
      if (bodyText.includes(failure)) {
        return { ok: false, text: failure, source: 'body' }
      }
    }
    return null
  }, {
    modal: selectors.modal,
    modalError: selectors.modalError,
    dashboardError: selectors.dashboardError,
    dashboardStatus: selectors.dashboardStatus,
  })
}

async function fetchRemoteSessionFromPage(page, sessionID) {
  if (!String(sessionID || '').trim()) {
    return null
  }
  const payload = await page.evaluate(async (id) => {
    const response = await fetch(`/v1/deploy/remote/session?refresh=1&id=${encodeURIComponent(id)}`)
    return response.json()
  }, String(sessionID).trim())
  if (payload?.session) {
    return summarizeRemoteSession(payload.session)
  }
  if (Array.isArray(payload?.sessions) && payload.sessions.length === 1) {
    return summarizeRemoteSession(payload.sessions[0])
  }
  return null
}

async function waitForLaunchOutcome(page, timeoutMs, sessionID, summary) {
  const deadline = Date.now() + timeoutMs
  let lastProgressLogMS = 0
  let lastProgressKey = ''
  while (Date.now() < deadline) {
    const launch = await readLaunchOutcome(page)
    if (launch) {
      return launch
    }
    if (String(sessionID || '').trim()) {
      const now = Date.now()
      if (now - lastProgressLogMS >= 5000) {
        try {
          const session = await fetchRemoteSessionFromPage(page, sessionID)
          if (session) {
            summary.final_remote_session = session
            const progressKey = compactSessionProgress(session)
            if (progressKey !== lastProgressKey || now - lastProgressLogMS >= 30000) {
              logRemoteSessionProgress('remote_session', session)
              lastProgressKey = progressKey
              lastProgressLogMS = now
            }
            if (session.status === 'failed') {
              return { ok: false, text: session.last_error || 'remote deploy session failed', source: 'api' }
            }
            if (session.status === 'attached' || session.child_swarm_id_present || session.remote_endpoint_present) {
              return { ok: true, text: session.last_progress || 'remote child attached', source: 'api' }
            }
          }
        } catch (error) {
          const message = redactSensitive(error instanceof Error ? error.message : String(error))
          if (message && message !== lastProgressKey) {
            console.log(`PROGRESS remote_session_poll_error ${message}`)
            lastProgressKey = message
            lastProgressLogMS = now
          }
        }
      }
    }
    await page.waitForTimeout(1000)
  }
  return { ok: false, text: `remote child did not complete before ${timeoutMs}ms timeout`, source: 'timeout' }
}

async function main() {
  const opts = parseArgs(process.argv.slice(2))
  if (opts.help) {
    usage()
    return
  }

  if (!opts.swarmName.trim()) {
    opts.swarmName = `remote-ui-${timestamp()}`
  }

  const desktopURL = await resolveDesktopURL(opts)
  const artifactDir = path.resolve(opts.artifactDir.trim() || defaultArtifactDir())
  await fs.mkdir(artifactDir, { recursive: true })

  const summary = {
    ok: false,
    desktop_url: desktopURL,
    settings_url: '',
    ssh_target: opts.sshTarget,
    transport: opts.transport,
    runtime: opts.runtime,
    swarm_name: opts.swarmName,
    configure_only: opts.configureOnly,
    preflight_only: opts.preflightOnly,
    wait_for_manual_auth: opts.waitForManualAuth,
    started_at: new Date().toISOString(),
    finished_at: '',
    total_ms: 0,
    timings_ms: {},
    steps: [],
    api_responses: [],
    remote_preflight: null,
    remote_start: null,
    remote_approve: null,
    final_remote_session: null,
    bottlenecks: [],
    workspace: null,
    notes: [],
    artifacts: [],
    screenshot_errors: [],
    result: 'failed',
    error: '',
  }

  const recorder = createRecorder(summary)
  const playwright = loadPlaywright()
  const apiEvents = []
  const consoleEvents = []
  const requestRecords = new Map()
  const settingsURL = new URL('/settings', desktopURL)
  settingsURL.searchParams.set('tab', 'swarm')
  summary.settings_url = settingsURL.toString()

  let browser
  let page
  let overallTimer
  try {
    browser = await playwright.chromium.launch({
      headless: opts.headless,
      executablePath: opts.browserExecutable.trim() || undefined,
    })
    const context = await browser.newContext({
      ignoreHTTPSErrors: true,
      viewport: { width: 1440, height: 1000 },
    })
    page = await context.newPage()

    page.on('request', (request) => {
      if (!isInterestingURL(request.url())) {
        return
      }
      const record = {
        method: request.method(),
        path: redactSensitive(requestPath(request.url())),
        started_ms: recorder.elapsed(),
      }
      requestRecords.set(request, record)
      apiEvents.push(record)
    })
    page.on('response', (response) => {
      const record = requestRecords.get(response.request())
      if (!record) {
        return
      }
      record.status = response.status()
      record.finished_ms = recorder.elapsed()
      record.duration_ms = record.finished_ms - record.started_ms
    })
    page.on('requestfailed', (request) => {
      const record = requestRecords.get(request)
      if (!record) {
        return
      }
      record.failed_ms = recorder.elapsed()
      record.duration_ms = record.failed_ms - record.started_ms
      record.failure = redactSensitive(request.failure()?.errorText || 'request failed')
    })
    page.on('console', (message) => {
      consoleEvents.push({
        type: message.type(),
        elapsed_ms: recorder.elapsed(),
        text: redactSensitive(message.text()),
      })
    })
    page.on('dialog', async (dialog) => {
      summary.notes.push(`browser dialog accepted: ${dialog.type()}`)
      await dialog.accept()
    })

    overallTimer = setTimeout(() => {
      summary.error = `overall timeout exceeded after ${opts.timeoutMs}ms`
      void page.close().catch(() => {})
    }, opts.timeoutMs)

    console.log(`ARTIFACT_DIR=${path.relative(ROOT_DIR, artifactDir)}`)
    console.log(`DESKTOP_URL=${desktopURL}`)

    let finish = recorder.start('open_live_swarm_dashboard')
    await page.goto(settingsURL.toString(), { waitUntil: 'domcontentloaded', timeout: 60000 })
    await unlockDesktopVaultIfNeeded(page, opts, summary)
    const dashboardAdd = await findUsableLocator(page, candidates(page, 'dashboardAdd'), 'Add swarm dashboard button', 60000)
    finish()
    await maybeScreenshot(page, artifactDir, '01-dashboard', summary)

    finish = recorder.start('open_add_swarm_modal')
    await dashboardAdd.click()
    await findUsableLocator(page, candidates(page, 'modal'), 'Add Swarm modal', 60000, false)
    const targetRemote = await findUsableLocator(page, candidates(page, 'targetRemote'), 'Remote over SSH target option', 60000)
    finish()

    finish = recorder.start('configure_remote_flow')
    await targetRemote.click()
    const sshTarget = await findUsableLocator(page, candidates(page, 'sshTarget'), 'SSH target input', 60000)
    await sshTarget.fill(opts.sshTarget.trim())
    const remoteRuntime = await findUsableLocator(page, candidates(page, 'remoteRuntime'), 'remote runtime select', 60000)
    await remoteRuntime.selectOption(opts.runtime)
    const method = await findUsableLocator(
      page,
      candidates(page, opts.transport === 'tailscale' ? 'methodTailscale' : 'methodLAN'),
      `${opts.transport} deploy method`,
      60000,
    )
    await method.click()
    if (opts.transport === 'lan') {
      const remoteHost = await findUsableLocator(page, candidates(page, 'remoteReachableHost'), 'remote reachable host input', 60000)
      await remoteHost.fill(opts.remoteHost.trim())
    }
    if (opts.transport === 'tailscale' && opts.tailscaleAuthKeyEnv.trim()) {
      const loginMode = await findUsableLocator(page, candidates(page, 'loginMode'), 'Tailscale login mode select', 60000)
      await loginMode.selectOption('key')
      await fillOptionalSecret(page, candidates(page, 'authKey'), opts.tailscaleAuthKeyEnv, 'Tailscale auth key')
      summary.notes.push(`tailscale auth key supplied from env var ${opts.tailscaleAuthKeyEnv}`)
    }
    const childName = await findUsableLocator(page, candidates(page, 'childName'), 'child swarm name input', 60000)
    await childName.fill(opts.swarmName.trim())
    await selectWorkspace(page, opts, summary)
    await fillOptionalSecret(page, candidates(page, 'vaultPassword'), opts.syncVaultPasswordEnv, 'sync vault password')
    const runPreflight = await findUsableLocator(page, candidates(page, 'runPreflight'), 'Run preflight button', 60000)
    finish()
    await maybeScreenshot(page, artifactDir, '02-configured', summary)

      if (opts.configureOnly) {
        summary.ok = true
        summary.result = 'configured'
      } else {
        finish = recorder.start('remote_preflight')
        const createResponsePromise = page.waitForResponse(
          (response) => remoteDeployAPIKind(response.url()) === 'remote_session_create',
          { timeout: opts.preflightTimeoutMs },
        )
        await runPreflight.click()
        const preflight = await waitForPreflightOutcome(page, opts.preflightTimeoutMs)
        const createSummary = await captureAPIResponse(createResponsePromise, 'remote preflight create', requestRecords, summary)
        summary.remote_preflight = createSummary?.session || null
        finish({ ok: Boolean(preflight?.ok) })
        await maybeScreenshot(page, artifactDir, preflight?.ok ? '03-preflight-ok' : '03-preflight-failed', summary)
        if (!preflight?.ok) {
          fail(preflight?.text || 'remote preflight failed')
        }
        summary.preflight_text = redactSensitive(preflight.text || '')

        if (opts.preflightOnly) {
          summary.ok = true
          summary.result = 'preflight_ok'
        } else {
          finish = recorder.start('launch_and_approve_remote_child')
          const launchButton = await findUsableLocator(page, candidates(page, 'launch'), 'Launch and add button', 60000)
          const startResponsePromise = page.waitForResponse(
            (response) => remoteDeployAPIKind(response.url()) === 'remote_session_start',
            { timeout: opts.launchTimeoutMs },
          )
          await launchButton.click()
          const startSummary = await captureAPIResponseWithSessionProgress(
            startResponsePromise,
            'remote_deploy_start',
            requestRecords,
            summary,
            page,
            summary.remote_preflight?.id || '',
          )
          summary.remote_start = startSummary?.session || null
          summary.final_remote_session = startSummary?.session || summary.final_remote_session
          if (startSummary?.session) {
            logRemoteSessionProgress('remote_start', startSummary.session)
          }
          if (startSummary?.session?.status === 'auth_required' && !opts.waitForManualAuth && !opts.tailscaleAuthKeyEnv.trim()) {
            finish({ ok: false, source: 'api', status: 'auth_required' })
            await maybeScreenshot(page, artifactDir, '04-auth-required', summary)
            summary.result = 'auth_required'
            fail(startSummary.session.last_progress || 'remote deploy requires Tailscale approval')
          }
          const launch = await waitForLaunchOutcome(page, opts.launchTimeoutMs, startSummary?.session?.id || '', summary)
          finish({ ok: Boolean(launch?.ok), source: launch?.source || '' })
          await maybeScreenshot(page, artifactDir, launch?.ok ? '04-launch-ok' : '04-launch-failed', summary)
          if (!launch?.ok) {
            fail(launch?.text || 'remote launch failed')
          }
          summary.launch_text = redactSensitive(launch.text || '')
          summary.ok = true
          summary.result = 'launch_ok'
        }
      }
  } catch (error) {
    summary.error = redactSensitive(error instanceof Error ? error.message : String(error))
    if (page) {
      await maybeScreenshot(page, artifactDir, '99-failure', summary)
    }
  } finally {
    if (overallTimer) {
      clearTimeout(overallTimer)
    }
    summary.finished_at = new Date().toISOString()
    summary.total_ms = recorder.elapsed()
    summary.bottlenecks = buildBottlenecks(summary, apiEvents)
    await writeJSON(path.join(artifactDir, 'summary.json'), summary)
    await writeJSON(path.join(artifactDir, 'network-events.json'), apiEvents)
    await writeJSON(path.join(artifactDir, 'console-events.json'), consoleEvents)
    if (browser) {
      await browser.close().catch(() => {})
    }
  }

  console.log(`TIMING total=${summary.total_ms}ms`)
  console.log(`SUMMARY=${path.relative(ROOT_DIR, path.join(artifactDir, 'summary.json'))}`)
  if (!summary.ok) {
    if (summary.result === 'auth_required') {
      console.error(`RESULT auth_required: ${summary.error}`)
      process.exitCode = 2
      return
    }
    console.error(`RESULT failed: ${summary.error}`)
    process.exitCode = 1
    return
  }
  console.log(`RESULT ${summary.result || 'ok'}`)
}

main().catch((error) => {
  console.error(`error: ${error instanceof Error ? error.message : String(error)}`)
  process.exitCode = 1
})
