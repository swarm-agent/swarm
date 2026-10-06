// Purpose: OrchestrateView.handleDeployModalSubmit -> handleProjects /
// CreateProjectTask / deployProjectTaskExecution -> canonical V3 store and sync.
// A real Chromium and isolated real daemon are the narrowest layer proving the
// actual New Task form, explicit source, duplicate-click guard and reload card.
// Pending big Swarm tasks must have zero durable run intents. This is NOT agent
// completion, provider qualification, daemon restart, or a benchmark. No mocks.
import test from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { chromium } from 'playwright'
import { parseArgs, validateFixtureLocation, readModelSettings } from '../../scripts/run-new-task-smoke.mjs'

let currentStep = 'prerequisites'
const diagnostic = (error, status) => process.send?.({ kind: 'smoke-diagnostic', step: currentStep, error, status })
const stage = name => { currentStep = name; diagnostic('none') }
const requireThat = (condition, message) => {
  if (!condition) {
    const error = new Error(message)
    error.safeID = message.toLowerCase().replace(/[^a-z0-9]+/g, '_').slice(0, 60)
    throw error
  }
}
const id = value => { requireThat(typeof value === 'string' && /^[a-zA-Z0-9_-]+$/.test(value), 'invalid durable identity'); return value }

// Deliberately outside src/**/*.spec.ts: a live test must never silently skip or
// enter the hermetic critical tiers. Missing explicit opt-in is a failure.
test('browser New Task persists pending approval through reload', { timeout: 170000 }, async () => {
  requireThat(process.env.SWARM_NEW_TASK_SMOKE_OPTIONS, 'use the explicit bounded runner')
  const input = JSON.parse(process.env.SWARM_NEW_TASK_SMOKE_OPTIONS)
  const options = parseArgs(['--desktop-url', `${input.origin}/`, '--fixture-repo', input.fixture, '--timeout-ms', String(input.timeoutMs), '--model-settings-file', input.settingsFile, '--owner', input.owner, '--isolated-no-provider-egress'])
  const fixture = validateFixtureLocation(options.fixture, process.env.TMPDIR)
  const git = args => execFileSync('git', ['-C', fixture, ...args], { encoding: 'utf8', timeout: 5000, maxBuffer: 65536, stdio: ['ignore', 'pipe', 'pipe'] }).trim()
  requireThat(git(['rev-parse', '--show-toplevel']) === fixture, 'fixture must be its own Git root')
  requireThat(/^[a-f0-9]{40,64}$/.test(git(['rev-parse', '--verify', 'HEAD'])), 'fixture needs a committed HEAD')
  requireThat(git(['status', '--porcelain', '--untracked-files=all']) === '', 'fixture must be clean including untracked files')
  const modelSettings = readModelSettings(options.settingsFile)
  const initialHead = git(['rev-parse', 'HEAD'])
  const owner = options.owner
  let token = '', project, workspaceId, conversationId, taskId, browser, browserServer, context, primaryFailure = false
  const sessionIds = new Set()
  // This independent client only authorizes setup plus exact returned owned IDs.
  // It does NOT widen AttachClient's shared route authority or create tasks by API.
  async function api(method, route, body, expected = 200) {
    const projectRoot = project && `/v3/projects/${id(project.id)}`
    const allowed =
      method === 'GET' && ['/v1/onboarding', '/v1/auth/desktop/session', '/v1/providers', '/v1/workspace/list?limit=1', '/v3/projects?limit=1', '/v1/agent-model-settings'].includes(route) ||
      method === 'POST' && route === '/v1/onboarding' && body?.desktop_onboarding_complete === true && Object.keys(body).length === 1 ||
      method === 'POST' && route === '/v1/model' && body === modelSettings.swarm.action ||
      method === 'POST' && route === '/v1/workspace/add' && body?.path === fixture && body?.make_current === false ||
      method === 'POST' && route === '/v3/projects' && body?.client_request_id === owner ||
      projectRoot && method === 'POST' && route === `${projectRoot}/sessions` && body?.title === owner ||
      projectRoot && method === 'GET' && [projectRoot, `${projectRoot}/tasks?limit=10`, ...(taskId ? [`${projectRoot}/tasks/${id(taskId)}`] : [])].includes(route) ||
      method === 'POST' && route === '/v3/sync/hydrate' && body?.session_ids?.length === 1 && sessionIds.has(body.session_ids[0])
    requireThat(allowed, 'unowned smoke API operation rejected')
    const headers = { Accept: 'application/json', Origin: options.origin, Referer: `${options.origin}/`, 'Sec-Fetch-Site': 'same-origin' }
    if (token) headers['X-Swarm-Token'] = token
    if (body !== undefined) headers['Content-Type'] = 'application/json'
    const operation = route.includes('/tasks/') ? 'task_read' : route.includes('/tasks?') ? 'task_list' : route.endsWith('/sessions') ? 'conversation_create' : route.startsWith('/v3/projects/') ? 'project_read' : ({ '/v1/onboarding': 'onboarding', '/v1/auth/desktop/session': 'desktop_auth', '/v1/providers': 'providers', '/v1/workspace/list?limit=1': 'workspace_empty', '/v3/projects?limit=1': 'project_empty', '/v1/agent-model-settings': 'model_assignments', '/v1/model': 'model_preference', '/v1/workspace/add': 'workspace_register', '/v3/projects': 'project_create', '/v3/sync/hydrate': 'run_intents' })[route]
    currentStep = `${method.toLowerCase()}_${operation}`
    let response
    try { response = await fetch(`${options.origin}${route}`, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), redirect: 'error', signal: AbortSignal.timeout(10000) }) }
    catch { diagnostic('request_unavailable'); throw new Error('request_unavailable') }
    diagnostic(response.status === expected ? 'none' : 'http_status', response.status)
    if (response.status !== expected) { await response.body?.cancel(); const error = new Error('http_status'); error.safeID = 'http_status'; error.status = response.status; throw error }
    if (expected === 404) { await response.body?.cancel(); return null }
    let size = 0, chunks = []
    for await (const chunk of response.body) { size += chunk.length; requireThat(size <= 1048576, 'API response bound exceeded'); chunks.push(chunk) }
    try { return JSON.parse(Buffer.concat(chunks).toString('utf8')) } catch { throw new Error('API returned invalid JSON') }
  }
  async function assertPending() {
    const durable = (await api('GET', `/v3/projects/${project.id}/tasks/${taskId}`)).task
    requireThat(durable?.id === taskId && durable.project_id === project.id && durable.session_id && sessionIds.has(durable.session_id), 'durable task/session identity mismatch')
    requireThat(durable.status === 'pending_approval' && durable.agent === 'swarm' && durable.feature_size === 'big' && (durable.auto_approve ?? false) === false, 'task must remain unapproved big Swarm')
    requireThat(durable.source_workspace?.workspace_id === workspaceId && durable.source_workspace?.path === fixture, 'durable task lost explicit source identity')
    const snapshot = await api('POST', '/v3/sync/hydrate', { surface: 'desktop', session_ids: [durable.session_id], history: { mode: 'none' }, resources: { run_intents: true }, include_active: true })
    requireThat(Object.hasOwn(snapshot.run_intents_by_session ?? {}, durable.session_id), 'sync omitted authoritative run-intent key')
    requireThat(Array.isArray(snapshot.run_intents_by_session[durable.session_id]) && snapshot.run_intents_by_session[durable.session_id].length === 0, 'unapproved execution admitted')
    return durable
  }
  try {
    // The wrapper exclusively created this database offline using maintained
    // identity/settings stores. Exact generated username and empty collections
    // reject a source/user daemon; no PATCH initializer or auth bypass exists.
    stage('readiness')
    const onboarding = await api('GET', '/v1/onboarding')
    requireThat(onboarding.identity?.bootstrapped === true && onboarding.identity?.username === owner, 'requires exact offline fixture owner')
    const auth = await api('GET', '/v1/auth/desktop/session')
    requireThat(typeof auth.token === 'string' && auth.token && !/[\r\n;]/.test(auth.token), 'Desktop bootstrap credential unavailable')
    token = auth.token
    requireThat(auth.username === owner, 'Desktop fixture owner mismatch')
    await api('POST', '/v1/onboarding', { desktop_onboarding_complete: true })
    const providers = (await api('GET', '/v1/providers')).providers
    requireThat(Array.isArray(providers) && providers.every(p => p.ready === false && p.runnable === false), 'credential-free daemon required; ready providers rejected')
    requireThat((await api('GET', '/v3/projects?limit=1')).projects?.length === 0, 'existing projects rejected')
    requireThat((await api('GET', '/v1/workspace/list?limit=1')).workspaces?.length === 0, 'existing workspaces rejected')
    const settings = (await api('GET', '/v1/agent-model-settings')).agent_model_settings
    for (const group of ['swarm', 'system_agents']) {
      for (const [slot, expected] of Object.entries(modelSettings[group])) {
        const actual = settings?.[group]?.[slot]
        for (const key of ['provider', 'model', 'thinking', 'service_tier', 'context_mode']) requireThat((actual?.[key] ?? '') === (expected[key] ?? ''), 'offline canonical assignment mismatch')
      }
    }
    await api('POST', '/v1/model', modelSettings.swarm.action)
    stage('fixture-setup')
    const registered = await api('POST', '/v1/workspace/add', { path: fixture, name: owner, make_current: false })
    workspaceId = id(registered.workspace_id)
    requireThat(registered.workspace?.workspace_path === fixture, 'registered fixture path mismatch')
    project = (await api('POST', '/v3/projects', { name: owner, description: 'Disposable browser persistence smoke; never approve execution.', client_request_id: owner, workspaces: [{ workspace_id: workspaceId, path: fixture, label: owner }] }, 201)).project
    id(project?.id)
    requireThat(project.name === owner && project.workspaces?.length === 1 && project.workspaces[0].workspace_id === workspaceId, 'project fixture ownership mismatch')
    // Project creation attempts real Router context generation. No credentials and
    // operator-denied provider egress prevent spending; do not fake ready context.
    // An empty real conversation gives the Tasks UI a route without retrying AI.
    const conversation = await api('POST', `/v3/projects/${project.id}/sessions`, { title: owner, client_request_id: `${owner}-conversation` })
    conversationId = id(conversation.session?.id)
    sessionIds.add(conversationId)
    stage('browser-form')
    requireThat(typeof process.send === 'function', 'bounded runner IPC required for Chromium cleanup')
    browserServer = await chromium.launchServer({ headless: true, host: '127.0.0.1', timeout: 15000, executablePath: process.env.CHROMIUM_EXECUTABLE_PATH || undefined })
    const chromiumPid = browserServer.process()?.pid
    requireThat(Number.isSafeInteger(chromiumPid), 'owned Chromium PID unavailable')
    process.send({ kind: 'owned-chromium', pid: chromiumPid })
    browser = await chromium.connect(browserServer.wsEndpoint(), { timeout: 10000 })
    context = await browser.newContext({ viewport: { width: 1600, height: 1000 }, serviceWorkers: 'block' })
    let unsafeRequest = false
    // Pure deny/continue safety filter, never fulfill or replace real endpoints.
    // Approval and provider dispatch must not occur even on an accidental click.
    await context.route('**/*', route => {
      const url = new URL(route.request().url())
      const request = route.request()
      if (request.method() === 'POST' && url.pathname === `/v3/projects/${project.id}/tasks`) {
        const body = request.postDataJSON()
        if (body?.intent !== 'code' || body?.feature_size !== 'big' || body?.agent !== 'swarm' || body?.auto_approve !== false || body?.workspace_id !== workspaceId || body?.workspace_path !== fixture) {
          unsafeRequest = true
          return route.abort('blockedbyclient')
        }
      }
      if (url.origin !== options.origin || /\/(approve|deploy|run|messages)(\/|$|:)/.test(url.pathname) && request.method() !== 'GET') {
        unsafeRequest = true
        return route.abort('blockedbyclient')
      }
      return route.continue()
    })
    const page = await context.newPage()
    page.setDefaultTimeout(15000)
    page.setDefaultNavigationTimeout(20000)
    const projectURL = `${options.origin}/projects/${project.id}/sessions/${conversationId}`
    stage('browser_navigation')
    await page.goto(projectURL, { waitUntil: 'domcontentloaded' })
    stage('new_task_button')
    await page.getByRole('button', { name: 'New Task', exact: true }).click()
    const dialog = page.getByRole('dialog', { name: 'Deploy Autonomous Task' })
    stage('big_feature_form')
    await dialog.getByRole('button', { name: 'Big Feature', exact: true }).click()
    const workspaceSelect = dialog.locator('select').filter({ has: page.getByRole('option', { name: `${owner} — ${fixture}`, exact: true }) })
    await workspaceSelect.selectOption(fixture)
    await dialog.locator('#auto-approve-toggle').setChecked(false)
    await dialog.locator('textarea').fill(`Persistence smoke ${owner}. Do not execute or approve this task.`)
    const endpoint = `/v3/projects/${project.id}/tasks`
    let submissions = 0
    page.on('request', req => { if (new URL(req.url()).pathname === endpoint && req.method() === 'POST') submissions++ })
    const returned = page.waitForResponse(res => new URL(res.url()).pathname === endpoint && res.request().method() === 'POST')
    // Two browser DOM clicks in one turn hit the real React handler/ref guard;
    // no network delay injection or endpoint mocking is needed.
    stage('duplicate_submit')
    await dialog.getByRole('button', { name: 'Create Pending Task', exact: true }).evaluate(button => { button.click(); button.click() })
    stage('task-response')
    const response = await returned
    diagnostic(response.status() === 201 ? 'none' : 'http_status', response.status())
    requireThat(response.status() === 201, 'browser task POST must return HTTP 201')
    const payload = response.request().postDataJSON()
    requireThat(payload.intent === 'code' && payload.feature_size === 'big' && payload.agent === 'swarm' && payload.auto_approve === false && payload.deploy_session === true && payload.workspace_path === fixture && payload.workspace_id === workspaceId, 'actual browser request did not honor pending explicit-source contract')
    taskId = id(payload.id)
    requireThat(response.request().headers()['x-request-id'] === taskId, 'stable request header/body identity mismatch')
    const created = (await response.json()).task
    requireThat(created?.id === taskId && created.project_id === project.id, 'real task response identity mismatch')
    sessionIds.add(id(created.session_id))
    await dialog.waitFor({ state: 'hidden' })
    stage('pending_task_card')
    const card = page.locator(`[data-task-id="${taskId}"]`)
    await card.waitFor({ state: 'visible' })
    await card.getByText('pending approval', { exact: true }).waitFor({ state: 'visible' })
    requireThat(submissions === 1, 'duplicate UI click produced multiple submissions')
    await assertPending()
    const list = (await api('GET', `/v3/projects/${project.id}/tasks?limit=10`)).tasks
    requireThat(Array.isArray(list) && list.length === 1 && list[0].id === taskId, 'duplicate durable task or missing task')
    stage('reload')
    await page.reload({ waitUntil: 'domcontentloaded' })
    await card.waitFor({ state: 'visible' })
    await card.getByText('pending approval', { exact: true }).waitFor({ state: 'visible' })
    requireThat(await card.count() === 1, 'reload must show exactly the same durable card')
    requireThat((await card.innerText()).includes('Persistence smoke'), 'reload card lost task prompt/title')
    await assertPending()
    requireThat(submissions === 1, 'reload repeated task submission')
    requireThat(!unsafeRequest, 'browser attempted a forbidden execution or foreign-origin request')
    assert.equal(git(['rev-parse', 'HEAD']), initialHead)
    assert.equal(git(['status', '--porcelain', '--untracked-files=all']), '')
  } catch (error) {
    primaryFailure = true
    diagnostic(error.safeID ?? 'browser_or_fixture_operation_failed', error.status)
    throw error
  } finally {
    // Do not DELETE retained task/session work. The wrapper stops/reaps the
    // entire exclusively owned daemon and disposes its exact state directory.
    // Browser-close failures are secondary and never replace an assertion.
    let closeFailed = false
    for (const resource of [context, browser, browserServer]) {
      try { await resource?.close() } catch { closeFailed = true; diagnostic('browser_close_failed') }
    }
    token = ''
    if (closeFailed && !primaryFailure) throw new Error('browser_close_failed')
  }
  process.send({ kind: 'smoke-success' })
})
